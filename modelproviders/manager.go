// Package modelproviders manages saved, authenticated connections to OpenAI-
// compatible and Anthropic Messages model APIs.
package modelproviders

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
	"shelley.exe.dev/models/modelsdev"
)

var (
	ErrNotFound  = errors.New("model provider not found")
	ErrInvalid   = errors.New("invalid model provider")
	ErrDuplicate = errors.New("a provider with this endpoint and API key is already saved")
	// ErrConflict reports an edit based on a saved provider that changed while
	// the edit was being checked.
	ErrConflict = errors.New("model provider changed while it was being edited")
)

const (
	discoveryTimeout    = 30 * time.Second
	discoveryBodyLimit  = 4 << 20 // OpenRouter's public listing is already ~0.8 MB
	discoveryPageLimit  = 50
	discoveryModelLimit = 10000
)

// Publisher replaces a provider's complete runtime catalog. Implementations
// must perform no fallible work after a provider row has been committed.
type Publisher interface {
	ReplaceProviderModels(providerID string, built []models.Built)
	// RelabelProviderModels changes only the source label of a provider's
	// already-published routes.
	RelabelProviderModels(providerID, source string)
	// SetProviderResolver supplies routes that the published catalog does
	// not list.
	SetProviderResolver(resolve func(modelID string) (models.Built, error))
}

// Input is the user-editable connection configuration. An empty api_key on
// Update retains the existing key.
type Input struct {
	ProviderType string `json:"provider_type"`
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"api_key"`
	DisplayName  string `json:"display_name"`
}

// Connection is safe to return to clients: it intentionally contains no key.
type Connection struct {
	ProviderID   string     `json:"provider_id"`
	ProviderType string     `json:"provider_type"`
	Endpoint     string     `json:"endpoint"`
	DisplayName  string     `json:"display_name"`
	HasAPIKey    bool       `json:"has_api_key"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	State        string     `json:"state"`
	ModelCount   int        `json:"model_count"`
	LastSuccess  *time.Time `json:"last_success,omitempty"`
	Error        string     `json:"error,omitempty"`
}

type sourceState struct {
	modelCount  int
	lastSuccess *time.Time
	err         string
}

// publishedState is what this manager last published for a provider: its
// configuration and accepted routes. It lets an unchanged successful refresh
// skip republication (which would invalidate idle loops).
type publishedState struct {
	providerType, endpoint, apiKey, displayName string
	routes                                      []route
}

func (p *publishedState) sameAs(row generated.ModelProvider, routes []route) bool {
	return p != nil && p.providerType == row.ProviderType && p.endpoint == row.Endpoint &&
		p.apiKey == row.ApiKey && p.displayName == row.DisplayName && reflect.DeepEqual(p.routes, routes)
}

// route is one accepted model. Services are built from a route plus the
// provider's current configuration.
type route struct {
	UpstreamID  string
	DisplayName string
	Metadata    *modelsdev.Metadata
}

// Manager makes saved-source mutations atomic with opMu, which is held only
// to commit: network requests never run under it. A discovery result is
// committed only if its source still has the configuration it was discovered
// with and no newer refresh of that source has started. The database holds
// connection configuration only; discovered routes and their status live in
// memory.
type Manager struct {
	db        *db.DB
	publisher Publisher
	httpc     *http.Client
	opMu      sync.Mutex
	mu        sync.RWMutex
	states    map[string]sourceState
	published map[string]*publishedState
	// refreshes counts started refreshes per source (guarded by mu).
	refreshes      map[string]uint64
	edits          map[string]uint64 // guarded by opMu
	cbMu           sync.RWMutex
	onChange       func()
	catalogChanged bool
}

// SetOnChange registers the single optional callback invoked after every
// visible catalog, status or configuration change, never for Test or for
// discarded stale results. It may run while the manager's operation lock is
// held, so it must not block or call back into the Manager.
func (m *Manager) SetOnChange(fn func()) {
	m.cbMu.Lock()
	m.onChange = fn
	m.cbMu.Unlock()
}

// HasCatalogState includes a deleted last source: reconnecting clients may
// still have its catalog cached.
func (m *Manager) HasCatalogState() bool {
	m.cbMu.RLock()
	defer m.cbMu.RUnlock()
	return m.catalogChanged
}

// notify must not be called with mu held.
func (m *Manager) notify() {
	m.cbMu.Lock()
	m.catalogChanged = true
	fn := m.onChange
	m.cbMu.Unlock()
	if fn != nil {
		fn()
	}
}

// New constructs a provider connection manager and registers it as the
// publisher's resolver for routes its catalog does not list. Redirects are
// disabled even when the supplied client allows them, so credentials are
// never forwarded to a redirect target.
func New(database *db.DB, publisher Publisher, httpc *http.Client) *Manager {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	safeClient := *httpc
	safeClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	m := &Manager{db: database, publisher: publisher, httpc: &safeClient, states: map[string]sourceState{}, published: map[string]*publishedState{}, refreshes: map[string]uint64{}, edits: map[string]uint64{}}
	if publisher != nil {
		publisher.SetProviderResolver(m.Resolve)
	}
	return m
}

// loadConcurrency bounds simultaneous discovery. Each source keeps its own
// discoveryTimeout, so one unreachable endpoint delays only its own slot.
const loadConcurrency = 8

// startupRetryDelay is how long Start waits before retrying sources whose
// startup discovery failed, typically because the network was not ready yet.
const startupRetryDelay = 30 * time.Second

// Start discovers saved sources in the background so startup never waits on
// providers; existing conversations resolve their routes meanwhile. Only a
// database failure is returned.
func (m *Manager) Start(ctx context.Context) error {
	if _, err := m.getRows(ctx); err != nil {
		return err
	}
	go m.discoverAtStartup(ctx, startupRetryDelay)
	return nil
}

// discoverAtStartup runs Refresh All, then retries once each source that is
// still failing after retryDelay.
func (m *Manager) discoverAtStartup(ctx context.Context, retryDelay time.Duration) {
	sources, err := m.RefreshAll(ctx)
	if err != nil {
		slog.Error("saved provider discovery failed", "error", err)
		return
	}
	var failed []string
	for _, source := range sources {
		if source.State == "error" {
			failed = append(failed, source.ProviderID)
		}
	}
	if len(failed) == 0 {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(retryDelay):
	}
	for _, id := range failed {
		if ctx.Err() != nil {
			return
		}
		m.mu.RLock()
		state, ok := m.states[id]
		m.mu.RUnlock()
		if ok && state.err == "" {
			continue
		}
		if _, err := m.Refresh(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
			slog.Warn("saved provider discovery retry failed", "provider_id", id, "error", err)
		}
	}
}

// RefreshAll discovers every saved source independently. Discovery runs
// concurrently, but results are published in saved-source order so registry
// order is deterministic. Per-source failures are reflected in each
// Connection; only database failures are returned.
func (m *Manager) RefreshAll(ctx context.Context) ([]Connection, error) {
	rows, seqs, err := m.snapshotAll(ctx)
	if err != nil {
		return []Connection{}, err
	}
	type result struct {
		routes []route
		err    error
	}
	results := make([]chan result, len(rows))
	for i := range results {
		results[i] = make(chan result, 1)
	}
	go func() {
		slots := make(chan struct{}, loadConcurrency)
		for i, row := range rows {
			slots <- struct{}{}
			go func(i int, row generated.ModelProvider) {
				defer func() { <-slots }()
				routes, err := m.discoverRoutes(ctx, row)
				results[i] <- result{routes: routes, err: err}
			}(i, row)
		}
	}()
	sources := make([]Connection, 0, len(rows))
	for i, row := range rows {
		r := <-results[i]
		source, _, err := m.commitRefresh(ctx, row, seqs[i], r.routes, r.err)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return []Connection{}, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// snapshotAll reads every saved source and starts a refresh of each.
func (m *Manager) snapshotAll(ctx context.Context) ([]generated.ModelProvider, []uint64, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	rows, err := m.getRows(ctx)
	if err != nil {
		return nil, nil, err
	}
	m.unpublishRemoved(rows)
	seqs := make([]uint64, len(rows))
	for i, row := range rows {
		seqs[i] = m.beginRefresh(row.ProviderID)
	}
	return rows, seqs, nil
}

// snapshotOne reads a saved source and starts a refresh of it.
func (m *Manager) snapshotOne(ctx context.Context, providerID string) (*generated.ModelProvider, uint64, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	row, err := m.getRow(ctx, providerID)
	if err != nil {
		return nil, 0, err
	}
	return row, m.beginRefresh(providerID), nil
}

func (m *Manager) beginRefresh(providerID string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshes[providerID]++
	return m.refreshes[providerID]
}

// commitRefresh records a finished discovery of row, which was started as
// refresh seq. A result is discarded, leaving current state untouched, when
// the source was deleted (ErrNotFound), edited, or refreshed again since.
// It returns the source's discovery error separately from a database error.
func (m *Manager) commitRefresh(ctx context.Context, row generated.ModelProvider, seq uint64, routes []route, err error) (Connection, error, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	cur, getErr := m.getRow(ctx, row.ProviderID)
	if getErr != nil {
		return Connection{}, nil, getErr
	}
	m.mu.RLock()
	latest := m.refreshes[row.ProviderID] == seq
	m.mu.RUnlock()
	if !latest || !sameConfig(cur, &row) {
		return m.connection(*cur), nil, nil
	}
	if err == nil {
		err = m.publishRoutes(*cur, routes)
	}
	if err != nil {
		m.recordError(cur.ProviderID, err)
	}
	return m.connection(*cur), err, nil
}

// sameConfig compares connection configuration, excluding the label.
func sameConfig(a, b *generated.ModelProvider) bool {
	return a.ProviderType == b.ProviderType && a.Endpoint == b.Endpoint && a.ApiKey == b.ApiKey
}

// unpublishRemoved drops runtime routes of sources whose rows no longer exist.
func (m *Manager) unpublishRemoved(rows []generated.ModelProvider) {
	active := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		active[row.ProviderID] = struct{}{}
	}
	m.mu.RLock()
	var removed []string
	for id := range m.states {
		if _, ok := active[id]; !ok {
			removed = append(removed, id)
		}
	}
	m.mu.RUnlock()
	slices.Sort(removed)
	for _, id := range removed {
		if m.publisher != nil {
			m.publisher.ReplaceProviderModels(id, nil)
		}
		m.forget(id)
	}
}

// forget drops a source's runtime state and notifies; the caller holds opMu.
func (m *Manager) forget(providerID string) {
	delete(m.edits, providerID)
	m.mu.Lock()
	delete(m.states, providerID)
	delete(m.published, providerID)
	delete(m.refreshes, providerID)
	m.mu.Unlock()
	m.notify()
}

// List returns saved connections without exposing API keys.
func (m *Manager) List(ctx context.Context) ([]Connection, error) {
	rows, err := m.getRows(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Connection, 0, len(rows))
	for _, row := range rows {
		result = append(result, m.connection(row))
	}
	return result, nil
}

// Create discovers and materializes the complete catalog before persisting
// the source, then performs a non-fallible publication. Duplicates and
// automatic labels are rechecked when committing.
func (m *Manager) Create(ctx context.Context, input Input) (Connection, error) {
	candidate, err := validateInput(input, nil)
	if err != nil {
		return Connection{}, err
	}
	if strings.TrimSpace(candidate.ApiKey) == "" {
		return Connection{}, fmt.Errorf("%w: api_key is required", ErrInvalid)
	}
	name := candidate.DisplayName
	if err := m.prepareCandidate(ctx, candidate); err != nil {
		return Connection{}, err
	}
	id, err := newProviderID()
	if err != nil {
		return Connection{}, fmt.Errorf("generate provider id: %w", err)
	}
	candidate.ProviderID = id
	routes, err := m.discoverRoutes(ctx, *candidate)
	if err != nil {
		return Connection{}, err
	}
	if _, err := m.checkKey(ctx, *candidate); err != nil {
		return Connection{}, err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	candidate.DisplayName = name
	if err := m.prepareCandidate(ctx, candidate); err != nil {
		return Connection{}, err
	}
	built, err := buildRoutes(*candidate, routes, m.httpc)
	if err != nil {
		return Connection{}, err
	}
	saved, err := m.db.CreateModelProvider(ctx, generated.CreateModelProviderParams{
		ProviderID: candidate.ProviderID, ProviderType: candidate.ProviderType,
		Endpoint: candidate.Endpoint, ApiKey: candidate.ApiKey, DisplayName: candidate.DisplayName,
	})
	if err != nil {
		return Connection{}, err
	}
	m.publish(*saved, routes, built)
	return m.connection(*saved), nil
}

// Update validates and discovers the candidate configuration before changing
// persistence. An empty api_key retains the saved key. If the saved provider
// changed meanwhile, nothing is saved and ErrConflict is returned.
func (m *Manager) Update(ctx context.Context, providerID string, input Input) (Connection, error) {
	m.opMu.Lock()
	old, err := m.getRow(ctx, providerID)
	edit := m.edits[providerID]
	m.opMu.Unlock()
	if err != nil {
		return Connection{}, err
	}
	candidate, err := validateInput(input, old)
	if err != nil {
		return m.connection(*old), err
	}
	candidate.ProviderID = providerID
	name := candidate.DisplayName
	if err := m.prepareCandidate(ctx, candidate); err != nil {
		return m.connection(*old), err
	}
	var routes []route
	if !labelOnly(old, candidate) {
		routes, err = m.discoverRoutes(ctx, *candidate)
		if err == nil {
			_, err = m.checkKey(ctx, *candidate)
		}
		if err != nil {
			// The candidate was never persisted or published, so its failure says
			// nothing about the working saved source and must not alter its status.
			return m.connection(*old), err
		}
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	cur, err := m.getRow(ctx, providerID)
	if err != nil {
		return Connection{}, err
	}
	if m.edits[providerID] != edit || !sameConfig(cur, old) || cur.DisplayName != old.DisplayName {
		return m.connection(*cur), ErrConflict
	}
	candidate.DisplayName = name
	if err := m.prepareCandidate(ctx, candidate); err != nil {
		return m.connection(*cur), err
	}
	if labelOnly(old, candidate) {
		return m.relabel(ctx, old, candidate.DisplayName)
	}
	built, err := buildRoutes(*candidate, routes, m.httpc)
	if err != nil {
		return m.connection(*old), err
	}
	saved, err := m.db.UpdateModelProvider(ctx, generated.UpdateModelProviderParams{
		ProviderID: providerID, ProviderType: candidate.ProviderType,
		Endpoint: candidate.Endpoint, ApiKey: candidate.ApiKey, DisplayName: candidate.DisplayName,
	})
	if err != nil {
		return m.connection(*old), translateDBError(err)
	}
	m.edits[providerID]++
	m.beginRefresh(providerID)
	m.publishIfChanged(*saved, routes, built)
	return m.connection(*saved), nil
}

// labelOnly reports whether candidate differs from old at most in its display
// name: same protocol, canonical endpoint and credential.
func labelOnly(old, candidate *generated.ModelProvider) bool {
	oldEndpoint, err := canonicalEndpoint(old.Endpoint)
	return err == nil && old.ProviderType == candidate.ProviderType &&
		oldEndpoint == candidate.Endpoint && old.ApiKey == candidate.ApiKey
}

// relabel changes only the account label: no discovery or key probe, and
// routes and status are untouched.
func (m *Manager) relabel(ctx context.Context, old *generated.ModelProvider, name string) (Connection, error) {
	if name == old.DisplayName {
		return m.connection(*old), nil
	}
	saved, err := m.db.UpdateModelProvider(ctx, generated.UpdateModelProviderParams{
		ProviderID: old.ProviderID, ProviderType: old.ProviderType,
		Endpoint: old.Endpoint, ApiKey: old.ApiKey, DisplayName: name,
	})
	if err != nil {
		return m.connection(*old), translateDBError(err)
	}
	m.edits[old.ProviderID]++
	if m.publisher != nil {
		m.publisher.RelabelProviderModels(saved.ProviderID, sourceName(*saved))
	}
	m.mu.Lock()
	if p := m.published[saved.ProviderID]; p != nil {
		p.displayName = saved.DisplayName
	}
	m.mu.Unlock()
	m.notify()
	return m.connection(*saved), nil
}

// TestResult lists exactly the models that connecting would publish.
type TestResult struct {
	Chat []string `json:"chat"`
	// KeyChecked is false when the endpoint lists models without
	// authentication and offers no way to check the key without inference.
	KeyChecked bool `json:"key_checked"`
}

// Test discovers a candidate configuration without saving or publishing it.
// A blank api_key with a providerID uses that provider's saved key.
func (m *Manager) Test(ctx context.Context, providerID string, input Input) (TestResult, error) {
	var saved *generated.ModelProvider
	if providerID != "" {
		old, err := m.getRow(ctx, providerID)
		if err != nil {
			return TestResult{}, err
		}
		saved = old
	}
	candidate, err := validateInput(input, saved)
	if err != nil {
		return TestResult{}, err
	}
	if strings.TrimSpace(candidate.ApiKey) == "" {
		return TestResult{}, fmt.Errorf("%w: api_key is required", ErrInvalid)
	}
	// Match Connect/Update's duplicate rule without writing anything. The
	// provider being edited is excluded; a database-less manager has no rows.
	if m.db != nil {
		rows, err := m.getRows(ctx)
		if err != nil {
			return TestResult{}, err
		}
		candidate.ProviderID = providerID
		if err := checkDuplicate(rows, candidate); err != nil {
			return TestResult{}, err
		}
	}
	candidate.ProviderID = "test"
	routes, err := m.discoverRoutes(ctx, *candidate)
	if err != nil {
		return TestResult{}, err
	}
	keyChecked, err := m.checkKey(ctx, *candidate)
	if err != nil {
		return TestResult{}, err
	}
	result := TestResult{Chat: []string{}, KeyChecked: keyChecked}
	for _, r := range routes {
		result.Chat = append(result.Chat, r.DisplayName)
	}
	return result, nil
}

// Delete removes the saved row and then removes its runtime catalog.
func (m *Manager) Delete(ctx context.Context, providerID string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.db == nil {
		return errors.New("model provider database is unavailable")
	}
	if err := m.db.DeleteModelProvider(ctx, providerID); err != nil {
		return translateDBError(err)
	}
	if m.publisher != nil {
		m.publisher.ReplaceProviderModels(providerID, nil)
	}
	m.forget(providerID)
	return nil
}

// Refresh rediscovers one saved source. On failure, the published catalog and
// last-success time remain while the error is recorded.
func (m *Manager) Refresh(ctx context.Context, providerID string) (Connection, error) {
	row, seq, err := m.snapshotOne(ctx, providerID)
	if err != nil {
		return Connection{}, err
	}
	routes, err := m.discoverRoutes(ctx, *row)
	conn, refreshErr, err := m.commitRefresh(ctx, *row, seq, routes, err)
	return conn, cmp.Or(err, refreshErr)
}

// Resolve builds a saved provider route that the published catalog does not
// list, from the route ID and the provider's saved connection, without any
// network request. Existing conversations therefore keep their account when
// discovery fails or the provider stops listing the model; the provider
// itself reports whether the model still works.
func (m *Manager) Resolve(modelID string) (models.Built, error) {
	providerID, upstreamID, ok := parseRouteID(modelID)
	if !ok {
		return models.Built{}, fmt.Errorf("unsupported model: %s", modelID)
	}
	row, err := m.getRow(context.Background(), providerID)
	if errors.Is(err, ErrNotFound) {
		return models.Built{}, fmt.Errorf("model %s belongs to a removed provider connection", upstreamID)
	}
	if err != nil {
		return models.Built{}, err
	}
	endpoint, err := canonicalEndpoint(row.Endpoint)
	if err != nil {
		return models.Built{}, err
	}
	built, err := buildRoutes(*row, []route{newRoute(endpoint, discoveredModel{ID: upstreamID})}, m.httpc)
	if err != nil {
		return models.Built{}, err
	}
	return built[0], nil
}

// publishRoutes builds and publishes a successful discovery.
func (m *Manager) publishRoutes(row generated.ModelProvider, routes []route) error {
	built, err := buildRoutes(row, routes, m.httpc)
	if err != nil {
		return err
	}
	m.publishIfChanged(row, routes, built)
	return nil
}

// publishIfChanged publishes built unless it reproduces exactly what is
// already published; either way the discovery counts as a success.
func (m *Manager) publishIfChanged(row generated.ModelProvider, routes []route, built []models.Built) {
	m.mu.RLock()
	unchanged := m.published[row.ProviderID].sameAs(row, routes)
	m.mu.RUnlock()
	if !unchanged {
		m.publish(row, routes, built)
		return
	}
	now := time.Now().UTC()
	m.mu.Lock()
	m.states[row.ProviderID] = sourceState{modelCount: len(routes), lastSuccess: &now}
	m.mu.Unlock()
	m.notify()
}

// discoverRoutes lists the provider's models and keeps exactly those Connect
// would publish: enriched with catalog metadata, then only models that are
// compatible by models.ProviderModelEligible, newest first. Picker ranking
// plays no part.
func (m *Manager) discoverRoutes(ctx context.Context, row generated.ModelProvider) ([]route, error) {
	endpoint, err := canonicalEndpoint(row.Endpoint)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	descriptors, err := discover(ctx, m.httpc, row.ProviderType, endpoint, row.ApiKey)
	if err != nil {
		return nil, err
	}
	routes := make([]route, 0, len(descriptors))
	for _, descriptor := range descriptors {
		r := newRoute(endpoint, descriptor)
		if models.ProviderModelEligible(r.UpstreamID, r.Metadata) {
			routes = append(routes, r)
		}
	}
	slices.SortStableFunc(routes, func(a, b route) int {
		return strings.Compare(b.Metadata.ReleaseDate, a.Metadata.ReleaseDate)
	})
	return routes, nil
}

// newRoute enriches a provider descriptor with catalog metadata.
func newRoute(endpoint string, descriptor discoveredModel) route {
	var found *modelsdev.Metadata
	if value, ok := modelsdev.LookupMetadata(endpoint, descriptor.ID); ok {
		found = &value
	}
	metadata := descriptor.enrich(found)
	return route{UpstreamID: descriptor.ID, DisplayName: cmp.Or(metadata.Name, descriptor.ID), Metadata: metadata}
}

const routePrefix = "provider:"

func routeID(providerID, upstreamID string) string {
	return routePrefix + base64.RawURLEncoding.EncodeToString([]byte(providerID)) + ":" + base64.RawURLEncoding.EncodeToString([]byte(upstreamID))
}

func parseRouteID(modelID string) (providerID, upstreamID string, ok bool) {
	rest, ok := strings.CutPrefix(modelID, routePrefix)
	if !ok {
		return "", "", false
	}
	encodedProvider, encodedUpstream, ok := strings.Cut(rest, ":")
	if !ok {
		return "", "", false
	}
	p, err := base64.RawURLEncoding.DecodeString(encodedProvider)
	if err != nil || len(p) == 0 {
		return "", "", false
	}
	u, err := base64.RawURLEncoding.DecodeString(encodedUpstream)
	if err != nil || len(u) == 0 {
		return "", "", false
	}
	return string(p), string(u), true
}

// buildRoutes constructs runtime services from routes and the provider's
// current configuration.
func buildRoutes(row generated.ModelProvider, routes []route, httpc *http.Client) ([]models.Built, error) {
	endpoint, err := canonicalEndpoint(row.Endpoint)
	if err != nil {
		return nil, err
	}
	built := make([]models.Built, 0, len(routes))
	for _, r := range routes {
		model, err := models.BuildProviderModel(row.ProviderID, r.UpstreamID, routeID(row.ProviderID, r.UpstreamID), r.DisplayName, row.ProviderType, endpoint, row.ApiKey, httpc, r.Metadata)
		if err != nil {
			return nil, fmt.Errorf("%w: unsupported provider type", ErrInvalid)
		}
		model.Source = sourceName(row)
		built = append(built, model)
	}
	return built, nil
}

// sourceName is the runtime label of a provider's routes.
func sourceName(row generated.ModelProvider) string {
	return cmp.Or(row.DisplayName, sourceLabel(row.Endpoint))
}

// publish replaces the runtime catalog after a successful discovery.
func (m *Manager) publish(row generated.ModelProvider, routes []route, built []models.Built) {
	if m.publisher != nil {
		m.publisher.ReplaceProviderModels(row.ProviderID, built)
	}
	now := time.Now().UTC()
	m.mu.Lock()
	m.states[row.ProviderID] = sourceState{modelCount: len(built), lastSuccess: &now}
	m.published[row.ProviderID] = &publishedState{
		providerType: row.ProviderType, endpoint: row.Endpoint, apiKey: row.ApiKey, displayName: row.DisplayName,
		routes: routes,
	}
	m.mu.Unlock()
	m.notify()
}

func (m *Manager) recordError(providerID string, err error) {
	m.mu.Lock()
	state := m.states[providerID]
	state.err = err.Error()
	m.states[providerID] = state
	m.mu.Unlock()
	m.notify()
}

func (m *Manager) connection(row generated.ModelProvider) Connection {
	m.mu.RLock()
	state, ok := m.states[row.ProviderID]
	m.mu.RUnlock()
	result := Connection{
		ProviderID: row.ProviderID, ProviderType: row.ProviderType,
		Endpoint: row.Endpoint, DisplayName: row.DisplayName,
		HasAPIKey: strings.TrimSpace(row.ApiKey) != "",
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		State: "ready", ModelCount: state.modelCount, LastSuccess: state.lastSuccess,
	}
	switch {
	case !ok:
		result.State = "discovering"
	case state.err != "":
		result.State = "error"
		result.Error = state.err
	}
	return result
}

func (m *Manager) prepareCandidate(ctx context.Context, candidate *generated.ModelProvider) error {
	rows, err := m.getRows(ctx)
	if err != nil {
		return err
	}
	if err := checkDuplicate(rows, candidate); err != nil {
		return err
	}
	names := make(map[string]bool)
	for _, row := range rows {
		if row.ProviderID == candidate.ProviderID {
			continue
		}
		name := row.DisplayName
		if name == "" {
			name = sourceLabel(row.Endpoint)
		}
		names[name] = true
	}
	if candidate.DisplayName == "" {
		base := sourceLabel(candidate.Endpoint)
		candidate.DisplayName = base
		for n := 2; names[candidate.DisplayName]; n++ {
			candidate.DisplayName = fmt.Sprintf("%s (%d)", base, n)
		}
	}
	return nil
}

// checkDuplicate rejects a candidate whose canonical endpoint and key match
// another saved row. Saved rows are canonicalized too, so legacy spellings
// (host case, default ports) still collide. A row with the candidate's own
// non-empty ProviderID is ignored.
func checkDuplicate(rows []generated.ModelProvider, candidate *generated.ModelProvider) error {
	endpoint, err := canonicalEndpoint(candidate.Endpoint)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if candidate.ProviderID != "" && row.ProviderID == candidate.ProviderID {
			continue
		}
		savedEndpoint, err := canonicalEndpoint(row.Endpoint)
		if err != nil {
			return fmt.Errorf("saved provider %s: %w", row.ProviderID, err)
		}
		if savedEndpoint == endpoint && row.ApiKey == candidate.ApiKey {
			return ErrDuplicate
		}
	}
	return nil
}

func (m *Manager) getRows(ctx context.Context) ([]generated.ModelProvider, error) {
	if m.db == nil {
		return nil, errors.New("model provider database is unavailable")
	}
	return m.db.GetModelProviders(ctx)
}

func (m *Manager) getRow(ctx context.Context, providerID string) (*generated.ModelProvider, error) {
	if m.db == nil {
		return nil, errors.New("model provider database is unavailable")
	}
	row, err := m.db.GetModelProvider(ctx, providerID)
	if err != nil {
		return nil, translateDBError(err)
	}
	return row, nil
}

func translateDBError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// validateInput validates user input. A blank api_key retains old's key only
// when the canonical origin is unchanged; otherwise the saved credential would
// be sent to a different recipient, so it is rejected before any request.
func validateInput(input Input, old *generated.ModelProvider) (*generated.ModelProvider, error) {
	if input.ProviderType != "anthropic" && input.ProviderType != "openai" && input.ProviderType != "openai-responses" {
		return nil, fmt.Errorf("%w: unsupported provider_type", ErrInvalid)
	}
	endpoint, err := canonicalEndpoint(input.Endpoint)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(input.DisplayName)
	key := input.APIKey
	if strings.TrimSpace(key) == "" {
		key = ""
		if old != nil {
			if !sameOrigin(old.Endpoint, endpoint) {
				return nil, fmt.Errorf("%w: api_key must be entered again when the endpoint origin changes", ErrInvalid)
			}
			key = old.ApiKey
		}
	}
	return &generated.ModelProvider{
		ProviderType: input.ProviderType, Endpoint: endpoint,
		ApiKey: key, DisplayName: name,
	}, nil
}

// sameOrigin compares canonical scheme, host and port. An uncanonicalizable
// endpoint never matches.
func sameOrigin(a, b string) bool {
	oa, ok := endpointOrigin(a)
	if !ok {
		return false
	}
	ob, ok := endpointOrigin(b)
	return ok && oa == ob
}

func endpointOrigin(raw string) (string, bool) {
	endpoint, err := canonicalEndpoint(raw)
	if err != nil {
		return "", false
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func canonicalEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: endpoint must be an absolute HTTP(S) API base without credentials, query, or fragment", ErrInvalid)
	}
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		if segment == "." || segment == ".." || strings.Contains(strings.ToLower(segment), "%2f") || strings.Contains(strings.ToLower(segment), "%5c") {
			return "", fmt.Errorf("%w: endpoint path is not canonical", ErrInvalid)
		}
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\\x00") {
			return "", fmt.Errorf("%w: endpoint path is not canonical", ErrInvalid)
		}
	}
	path := strings.TrimRight(u.Path, "/")
	lower := strings.ToLower(path)
	for _, suffix := range []string{"/models", "/messages", "/chat/completions", "/responses"} {
		if strings.HasSuffix(lower, suffix) {
			return "", fmt.Errorf("%w: endpoint must be an API base, not an operation URL", ErrInvalid)
		}
	}
	// Equivalent origin spellings must compare equal: hostnames are
	// case-insensitive and default ports are implicit. Path case is preserved.
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host += ":" + port
	}
	u.Host = host
	u.Path = path
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func newProviderID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

type providerSupport struct {
	Supported *bool `json:"supported"`
}

type discoveredModel struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	Lifecycle      string `json:"lifecycle"`
	MaxInputTokens *int   `json:"max_input_tokens"`
	MaxTokens      *int   `json:"max_tokens"`
	Capabilities   *struct {
		ImageInput *providerSupport `json:"image_input"`
		Thinking   *struct {
			Supported *bool `json:"supported"`
			Types     *struct {
				Adaptive *providerSupport `json:"adaptive"`
				Enabled  *providerSupport `json:"enabled"`
			} `json:"types"`
		} `json:"thinking"`
		Effort *struct {
			Low    *providerSupport `json:"low"`
			Medium *providerSupport `json:"medium"`
			High   *providerSupport `json:"high"`
			Max    *providerSupport `json:"max"`
		} `json:"effort"`
	} `json:"capabilities"`
}

func (d discoveredModel) enrich(metadata *modelsdev.Metadata) *modelsdev.Metadata {
	if metadata == nil {
		metadata = &modelsdev.Metadata{Match: "unknown", ModelID: d.ID}
	}
	metadata.Provenance = make(map[string]string)
	mark := func(field string) { metadata.Provenance[field] = "provider" }
	if d.DisplayName != "" {
		metadata.Name = d.DisplayName
		mark("name")
	}
	if d.Lifecycle != "" {
		metadata.Lifecycle = d.Lifecycle
		mark("lifecycle")
	}
	if d.MaxInputTokens != nil {
		metadata.ContextLimit = *d.MaxInputTokens
		mark("context_limit")
	}
	if d.MaxTokens != nil {
		metadata.OutputLimit = *d.MaxTokens
		mark("output_limit")
	}
	if d.Capabilities != nil {
		c := d.Capabilities
		if c.ImageInput != nil && c.ImageInput.Supported != nil {
			metadata.Images = c.ImageInput.Supported
			mark("images")
		}
		if c.Thinking != nil {
			if c.Thinking.Supported != nil {
				metadata.Reasoning = c.Thinking.Supported
				mark("reasoning")
			}
			if c.Thinking.Types != nil {
				metadata.ThinkingTypes = []string{}
				if c.Thinking.Types.Adaptive != nil && c.Thinking.Types.Adaptive.Supported != nil && *c.Thinking.Types.Adaptive.Supported {
					metadata.ThinkingTypes = append(metadata.ThinkingTypes, "adaptive")
				}
				if c.Thinking.Types.Enabled != nil && c.Thinking.Types.Enabled.Supported != nil && *c.Thinking.Types.Enabled.Supported {
					metadata.ThinkingTypes = append(metadata.ThinkingTypes, "enabled")
				}
				mark("thinking_types")
			}
		}
		if c.Effort != nil {
			levels := make(map[llm.ThinkingLevel]bool)
			if metadata.Authoritative("reasoning_levels") {
				for _, level := range metadata.ReasoningLevels {
					levels[level] = true
				}
			}
			reported := false
			for _, entry := range []struct {
				level   llm.ThinkingLevel
				support *providerSupport
			}{
				{llm.ThinkingLevelLow, c.Effort.Low}, {llm.ThinkingLevelMedium, c.Effort.Medium},
				{llm.ThinkingLevelHigh, c.Effort.High}, {llm.ThinkingLevelMax, c.Effort.Max},
			} {
				if entry.support != nil && entry.support.Supported != nil {
					levels[entry.level] = *entry.support.Supported
					reported = true
				}
			}
			if reported {
				metadata.ReasoningLevels = []llm.ThinkingLevel{}
				for _, level := range []llm.ThinkingLevel{llm.ThinkingLevelOff, llm.ThinkingLevelLow, llm.ThinkingLevelMedium, llm.ThinkingLevelHigh, llm.ThinkingLevelXHigh, llm.ThinkingLevelMax} {
					if levels[level] {
						metadata.ReasoningLevels = append(metadata.ReasoningLevels, level)
					}
				}
				mark("reasoning_levels")
			}
		}
	}
	return metadata
}

func discover(ctx context.Context, httpc *http.Client, providerType, endpoint, apiKey string) ([]discoveredModel, error) {
	if providerType != "anthropic" && providerType != "openai" && providerType != "openai-responses" {
		return nil, fmt.Errorf("%w: unsupported provider_type", ErrInvalid)
	}
	client := *httpc
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	httpc = &client
	base, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid endpoint", ErrInvalid)
	}
	path := strings.TrimRight(base.Path, "/") + "/models"
	cursorParam := ""
	if providerType == "anthropic" {
		cursorParam = "after_id"
	} else {
		cursorParam = "after"
	}
	var (
		descriptors []discoveredModel
		seenIDs     = map[string]struct{}{}
		cursors     = map[string]struct{}{}
		cursor      string
		bodyLeft    = int64(discoveryBodyLimit)
	)
	for page := 0; page < discoveryPageLimit; page++ {
		pageURL := *base
		pageURL.Path = path
		pageURL.RawPath = ""
		query := pageURL.Query()
		if cursor != "" && cursorParam != "" {
			query.Set(cursorParam, cursor)
		}
		pageURL.RawQuery = query.Encode()
		data, hasMore, lastID, err := fetchModelPage(ctx, httpc, providerType, pageURL.String(), apiKey, &bodyLeft)
		if err != nil {
			return nil, err
		}
		for _, descriptor := range data {
			if _, exists := seenIDs[descriptor.ID]; exists {
				continue
			}
			seenIDs[descriptor.ID] = struct{}{}
			descriptors = append(descriptors, descriptor)
			if len(descriptors) > discoveryModelLimit {
				return nil, discoveryFailure("model limit exceeded")
			}
		}
		if !hasMore {
			return descriptors, nil
		}
		if lastID == "" {
			return nil, discoveryFailure("pagination cursor missing")
		}
		if _, exists := cursors[lastID]; exists || lastID == cursor {
			return nil, discoveryFailure("pagination loop detected")
		}
		cursors[lastID] = struct{}{}
		cursor = lastID
	}
	return nil, discoveryFailure("page limit exceeded")
}

func fetchModelPage(ctx context.Context, httpc *http.Client, providerType, target, apiKey string, bodyLeft *int64) ([]discoveredModel, bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, false, "", discoveryFailure("request could not be created")
	}
	switch providerType {
	case "anthropic":
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, "", discoveryFailure("discovery canceled or timed out")
		}
		return nil, false, "", discoveryFailure("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, "", discoveryFailure(fmt.Sprintf("provider returned HTTP %d", resp.StatusCode))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, *bodyLeft+1))
	if err != nil {
		return nil, false, "", discoveryFailure("response could not be read")
	}
	if int64(len(body)) > *bodyLeft {
		return nil, false, "", discoveryFailure("response body limit exceeded")
	}
	*bodyLeft -= int64(len(body))
	// Anthropic and most OpenAI-compatible APIs wrap the list as
	// {"data":[...]}. Together AI returns the array at the top level. A
	// top-level array has no pagination envelope.
	var (
		dataRaw json.RawMessage
		hasMore bool
		lastID  string
	)
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		if providerType == "anthropic" {
			return nil, false, "", discoveryFailure("provider response data must be an array")
		}
		dataRaw = trimmed
	} else {
		var envelope struct {
			Data    json.RawMessage `json:"data"`
			HasMore *bool           `json:"has_more"`
			LastID  string          `json:"last_id"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return nil, false, "", discoveryFailure("malformed provider response")
		}
		dataRaw = bytes.TrimSpace(envelope.Data)
		hasMore = envelope.HasMore != nil && *envelope.HasMore
		lastID = envelope.LastID
	}
	if len(dataRaw) == 0 || string(dataRaw) == "null" || dataRaw[0] != '[' {
		return nil, false, "", discoveryFailure("provider response data must be an array")
	}
	var modelsList []json.RawMessage
	if err := json.Unmarshal(dataRaw, &modelsList); err != nil || modelsList == nil {
		return nil, false, "", discoveryFailure("provider response data must be an array")
	}
	descriptors := make([]discoveredModel, 0, len(modelsList))
	for _, raw := range modelsList {
		raw = bytes.TrimSpace(raw)
		var item discoveredModel
		if len(raw) == 0 || raw[0] != '{' {
			return nil, false, "", discoveryFailure("provider response contains an invalid model")
		}
		if providerType == "anthropic" {
			if json.Unmarshal(raw, &item) != nil {
				return nil, false, "", discoveryFailure("provider response contains an invalid model")
			}
		} else {
			// OpenAI-compatible listings vary widely in extra fields (and
			// their types); only the wire ID is a common contract.
			var idOnly struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &idOnly) != nil {
				return nil, false, "", discoveryFailure("provider response contains an invalid model")
			}
			item.ID = idOnly.ID
		}
		if strings.TrimSpace(item.ID) == "" {
			return nil, false, "", discoveryFailure("provider response contains an invalid model")
		}
		if (item.MaxTokens != nil && *item.MaxTokens <= 0) || (item.MaxInputTokens != nil && *item.MaxInputTokens <= 0) {
			return nil, false, "", discoveryFailure("provider response contains invalid token limits")
		}
		descriptors = append(descriptors, item)
	}
	return descriptors, hasMore, lastID, nil
}

func discoveryFailure(message string) error {
	return errors.New("model discovery failed: " + message)
}

// sourceLabel names an unlabelled connection by its endpoint host.
func sourceLabel(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return endpoint
}

// checkKey confirms the key itself where the endpoint's contract allows it,
// without inference. It reports false when nothing could check the key.
//
//   - First-party Anthropic rejects unauthenticated listing, so successful
//     discovery already proved the key.
//   - OpenRouter lists models publicly but offers an authenticated /key check.
//   - Any other endpoint is probed without credentials: a 401/403 shows that
//     listing requires authentication, so successful discovery proved the key.
//     A public listing (any other status) cannot show that a bad key would be
//     rejected, so the key is reported unchecked.
//
// Probes never forward the saved key except to the OpenRouter origin it is
// bound to, and redirects are disabled.
func (m *Manager) checkKey(ctx context.Context, row generated.ModelProvider) (bool, error) {
	endpoint, err := canonicalEndpoint(row.Endpoint)
	if err != nil {
		return false, err
	}
	base, err := url.Parse(endpoint)
	if err != nil {
		return false, fmt.Errorf("%w: invalid endpoint", ErrInvalid)
	}
	if row.ProviderType == "anthropic" && isOrigin(base, "https", "api.anthropic.com") {
		return true, nil
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	if row.ProviderType != "anthropic" && isOrigin(base, "https", "openrouter.ai") {
		status, err := m.keyProbe(ctx, row.ProviderType, endpoint+"/key", row.ApiKey)
		if err != nil {
			return false, err
		}
		if status < 200 || status >= 300 {
			return false, fmt.Errorf("API key check failed: provider returned HTTP %d", status)
		}
		return true, nil
	}
	status, err := m.keyProbe(ctx, row.ProviderType, endpoint+"/models", "")
	if err != nil {
		return false, err
	}
	return status == http.StatusUnauthorized || status == http.StatusForbidden, nil
}

// isOrigin reports whether a canonical endpoint is exactly scheme://hostname
// with the scheme's default port.
func isOrigin(u *url.URL, scheme, hostname string) bool {
	return u.Scheme == scheme && u.Hostname() == hostname && u.Port() == ""
}

func (m *Manager) keyProbe(ctx context.Context, providerType, target, apiKey string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, errors.New("API key check failed: request could not be created")
	}
	if providerType == "anthropic" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := m.httpc.Do(req)
	if err != nil {
		return 0, errors.New("API key check failed: request failed")
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}
