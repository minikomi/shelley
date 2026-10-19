package modelproviders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/models"
)

const (
	lunaOnly    = `{"data":[{"id":"gpt-6-luna"},{"id":"vendor/unknown-chat"}]}`
	lunaAndSol  = `{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6-astra"},{"id":"vendor/unknown-chat"}]}`
	noEligibles = `{"data":[{"id":"text-embedding-3-small"},{"id":"vendor/unknown-chat"}]}`
)

// durableFixture serves a switchable listing. Every request, including key
// probes, is counted. status != 0 makes the listing fail.
type durableFixture struct {
	server   *httptest.Server
	listing  atomic.Value
	status   atomic.Int32
	requests atomic.Int32
}

func newDurableFixture(t *testing.T, listing string) *durableFixture {
	t.Helper()
	f := &durableFixture{}
	f.listing.Store(listing)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if status := int(f.status.Load()); status != 0 {
			w.WriteHeader(status)
			fmt.Fprint(w, "private upstream body")
			return
		}
		fmt.Fprint(w, f.listing.Load().(string))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *durableFixture) input(key, name string) Input {
	return Input{ProviderType: "openai", Endpoint: f.server.URL + "/v1", APIKey: key, DisplayName: name}
}

type durableState struct {
	provider generated.ModelProvider
	models   []string
	revision uint64
}

func captureDurable(t *testing.T, database *db.DB, registry *models.Manager, providerID string) durableState {
	t.Helper()
	provider, err := database.GetModelProvider(context.Background(), providerID)
	if err != nil {
		t.Fatal(err)
	}
	return durableState{provider: *provider, models: registry.GetAvailableModels(), revision: registry.ProviderCatalogRevision(providerID)}
}

func (s durableState) requireUnchanged(t *testing.T, database *db.DB, registry *models.Manager) {
	t.Helper()
	now := captureDurable(t, database, registry, s.provider.ProviderID)
	p, q := s.provider, now.provider
	if p.ProviderType != q.ProviderType || p.Endpoint != q.Endpoint || p.ApiKey != q.ApiKey ||
		p.DisplayName != q.DisplayName || !p.UpdatedAt.Equal(q.UpdatedAt) {
		t.Fatalf("provider row changed: %+v -> %+v", p, q)
	}
	if !slices.Equal(s.models, now.models) || s.revision != now.revision {
		t.Fatalf("runtime catalog changed: %v rev %d -> %v rev %d", s.models, s.revision, now.models, now.revision)
	}
}

func failProviderWrites(t *testing.T, database *db.DB, providerID string) (restore func()) {
	t.Helper()
	ctx := context.Background()
	if err := database.Pool().Exec(ctx, fmt.Sprintf(`CREATE TRIGGER fail_routes BEFORE UPDATE ON model_providers
		WHEN NEW.provider_id = '%s' BEGIN SELECT RAISE(ABORT, 'synthetic storage failure'); END`, providerID)); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := database.Pool().Exec(ctx, "DROP TRIGGER fail_routes"); err != nil {
			t.Fatal(err)
		}
	}
}

func newDurableManager(t *testing.T, f *durableFixture) (*Manager, *models.Manager, *db.DB) {
	t.Helper()
	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	registry, err := models.NewManager(&models.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return New(database, registry, f.server.Client()), registry, database
}

// A label-only edit must work with the provider offline and change nothing
// but the label: no discovery or key probe, same routes, timestamp, error
// status and runtime catalog revision. Only the runtime source label follows.
func TestLabelOnlyUpdateDoesNotDiscoverOrChangeCatalog(t *testing.T) {
	ctx := context.Background()
	f := newDurableFixture(t, lunaAndSol)
	manager, registry, database := newDurableManager(t, f)
	created, err := manager.Create(ctx, f.input("secret-key", "Before"))
	if err != nil {
		t.Fatal(err)
	}
	// Leave a recorded discovery error, then take the provider offline.
	f.status.Store(http.StatusServiceUnavailable)
	failed, err := manager.Refresh(ctx, created.ProviderID)
	if err == nil || failed.State != "error" {
		t.Fatalf("failed refresh = %+v, %v", failed, err)
	}
	// An online listing that would change the catalog must not matter either.
	f.listing.Store(lunaOnly)
	before := captureDurable(t, database, registry, created.ProviderID)
	requests := f.requests.Load()

	renamed, err := manager.Update(ctx, created.ProviderID, f.input("", "After"))
	if err != nil {
		t.Fatalf("offline label-only update: %v", err)
	}
	if f.requests.Load() != requests {
		t.Fatalf("label-only update made %d network requests", f.requests.Load()-requests)
	}
	if renamed.DisplayName != "After" || !renamed.HasAPIKey || renamed.State != "error" || renamed.Error != failed.Error ||
		renamed.ModelCount != 2 || renamed.LastSuccess == nil || !renamed.LastSuccess.Equal(*created.LastSuccess) {
		t.Fatalf("renamed = %+v, want label change only (was %+v)", renamed, failed)
	}
	now := captureDurable(t, database, registry, created.ProviderID)
	if now.provider.DisplayName != "After" || now.provider.ApiKey != "secret-key" {
		t.Fatalf("provider row = %+v, want new label with retained key", now.provider)
	}
	if !slices.Equal(before.models, now.models) || before.revision != now.revision {
		t.Fatalf("label-only update changed catalog: models %v -> %v, revision %d -> %d",
			before.models, now.models, before.revision, now.revision)
	}
	for _, id := range now.models {
		if got := registry.GetModelInfo(id).Source; got != "After" {
			t.Fatalf("runtime source label for %s = %q, want After", id, got)
		}
	}

	// Re-submitting the same label, or the same explicit key, is also a no-op.
	if _, err := manager.Update(ctx, created.ProviderID, f.input("secret-key", "After")); err != nil {
		t.Fatal(err)
	}
	if f.requests.Load() != requests {
		t.Fatalf("no-op update made network requests")
	}
	// A route resolved while offline carries the new label.
	restarted, _ := models.NewManager(&models.Config{})
	New(database, restarted, f.server.Client())
	if got := restarted.GetModelInfo(now.models[0]); got == nil || got.Source != "After" {
		t.Fatalf("resolved source label = %+v", got)
	}
	// A real change (here: rotated key) still discovers and replaces the catalog.
	f.status.Store(0)
	if _, err := manager.Update(ctx, created.ProviderID, f.input("rotated-key", "After")); err != nil {
		t.Fatal(err)
	}
	if got := publishedWireIDs(registry); !slices.Equal(got, []string{"gpt-6-luna"}) {
		t.Fatalf("catalog after rotation = %v, want refreshed catalog", got)
	}
}

func TestLabelOnlyUpdateOfMissingProviderIsNotFound(t *testing.T) {
	f := newDurableFixture(t, lunaOnly)
	manager, _, _ := newDurableManager(t, f)
	if _, err := manager.Update(context.Background(), "missing", f.input("", "x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing error = %v, want ErrNotFound", err)
	}
}

// An unchanged successful refresh records success and clears the error but
// publishes nothing, so no revision bump invalidates idle loops.
func TestNoOpRefreshDoesNotAdvanceRevisionButRecordsSuccess(t *testing.T) {
	ctx := context.Background()
	f := newDurableFixture(t, lunaAndSol)
	manager, registry, _ := newDurableManager(t, f)
	created, err := manager.Create(ctx, f.input("key", "acct"))
	if err != nil {
		t.Fatal(err)
	}
	revision := registry.ProviderCatalogRevision(created.ProviderID)
	if revision == 0 {
		t.Fatal("Create did not publish a revision")
	}
	f.status.Store(http.StatusBadGateway)
	if _, err := manager.Refresh(ctx, created.ProviderID); err == nil {
		t.Fatal("expected failed refresh")
	}
	f.status.Store(0)

	refreshed, err := manager.Refresh(ctx, created.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.State != "ready" || refreshed.Error != "" || refreshed.ModelCount != 2 ||
		refreshed.LastSuccess == nil || !refreshed.LastSuccess.After(*created.LastSuccess) {
		t.Fatalf("no-op refresh = %+v, want ready with an advanced timestamp (created %+v)", refreshed, created)
	}
	if got := registry.ProviderCatalogRevision(created.ProviderID); got != revision {
		t.Fatalf("no-op refresh advanced revision %d -> %d", revision, got)
	}
	all, err := manager.RefreshAll(ctx)
	if err != nil || len(all) != 1 || registry.ProviderCatalogRevision(created.ProviderID) != revision {
		t.Fatalf("no-op RefreshAll = %+v, %v, revision %d (want %d)", all, err, registry.ProviderCatalogRevision(created.ProviderID), revision)
	}

	// Any real change still publishes: catalog, then key rotation.
	restarted, second, loaded := registry, manager, revision
	f.listing.Store(lunaOnly)
	if _, err := second.Refresh(ctx, created.ProviderID); err != nil {
		t.Fatal(err)
	}
	changed := restarted.ProviderCatalogRevision(created.ProviderID)
	if changed <= loaded || len(restarted.GetAvailableModels()) != 1 {
		t.Fatalf("catalog change revision %d -> %d, models %v", loaded, changed, restarted.GetAvailableModels())
	}
	if _, err := second.Update(ctx, created.ProviderID, f.input("rotated", "acct")); err != nil {
		t.Fatal(err)
	}
	if got := restarted.ProviderCatalogRevision(created.ProviderID); got <= changed {
		t.Fatalf("key rotation with an unchanged catalog did not invalidate the source (revision %d -> %d)", changed, got)
	}
}

// Compatibility, not picker prominence, decides membership: a preferred new
// sibling must not drop a still-listed route, Check and Connect agree, and
// unknown or non-chat models stay excluded.
func TestEligibilityKeepsShadowedRouteAndMatchesCheck(t *testing.T) {
	ctx := context.Background()
	f := newDurableFixture(t, `{"data":[{"id":"gpt-5.5"},{"id":"vendor/unknown-chat"},{"id":"text-embedding-3-small"}]}`)
	manager, registry, _ := newDurableManager(t, f)
	created, err := manager.Create(ctx, f.input("key", "acct"))
	if err != nil {
		t.Fatal(err)
	}
	if got := publishedWireIDs(registry); !slices.Equal(got, []string{"gpt-5.5"}) {
		t.Fatalf("published = %v", got)
	}
	f.listing.Store(`{"data":[{"id":"gpt-6.1-sol"},{"id":"gpt-5.5"},{"id":"vendor/unknown-chat"},{"id":"text-embedding-3-small"}]}`)
	checked, err := manager.Test(ctx, created.ProviderID, f.input("", "acct"))
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := manager.Refresh(ctx, created.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	got := publishedWireIDs(registry)
	if len(got) != 2 || !slices.Contains(got, "gpt-5.5") || !slices.Contains(got, "gpt-6.1-sol") ||
		refreshed.ModelCount != len(checked.Chat) || len(checked.Chat) != 2 {
		t.Fatalf("published %v, refreshed %+v, checked %v: want both compatible routes in Check and saved list", got, refreshed, checked.Chat)
	}
}

func TestProviderDiscoveryOrderMatchesCheckAndConnect(t *testing.T) {
	ctx := context.Background()
	f := newDurableFixture(t, `{"data":[
		{"id":"glm-4.5-air"},{"id":"glm-4.5"},{"id":"glm-5.3"},{"id":"glm-5.3-flash"}
	]}`)
	manager, registry, _ := newDurableManager(t, f)
	manager.httpc = &http.Client{Transport: rewriteTransport{target: strings.TrimPrefix(f.server.URL, "http://")}}
	input := Input{ProviderType: "openai", Endpoint: "https://api.z.ai/api/paas/v4", APIKey: "fixture", DisplayName: "fixture"}
	want := []string{"glm-5.3-flash", "glm-5.3", "glm-4.5-air", "glm-4.5"}
	wantNames := []string{"GLM-5.3-Flash", "GLM-5.3", "GLM-4.5-Air", "GLM-4.5"}

	checked, err := manager.Test(ctx, "", input)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(checked.Chat, wantNames) {
		t.Fatalf("Check order = %v, want %v", checked.Chat, wantNames)
	}
	created, err := manager.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := publishedWireIDs(registry); !slices.Equal(got, want) || created.ModelCount != len(want) {
		t.Fatalf("runtime order = %v, want %v", got, want)
	}
}

// A failed storage write while updating an existing, populated provider
// changes nothing: row, credential, status and runtime catalog.
func TestProviderUpdateWriteFailurePreservesSavedAndRuntimeState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input func(f *durableFixture) Input
	}{
		{"key rotation", func(f *durableFixture) Input { return f.input("rotated-key", "acct") }},
		{"protocol change", func(f *durableFixture) Input {
			in := f.input("", "acct")
			in.ProviderType = "openai-responses"
			return in
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newDurableFixture(t, lunaOnly)
			manager, registry, database := newDurableManager(t, f)
			created, err := manager.Create(ctx, f.input("old-key", "acct"))
			if err != nil {
				t.Fatal(err)
			}
			before := captureDurable(t, database, registry, created.ProviderID)
			f.listing.Store(lunaAndSol)
			restore := failProviderWrites(t, database, created.ProviderID)
			if _, err := manager.Update(ctx, created.ProviderID, tc.input(f)); err == nil {
				t.Fatal("update succeeded despite storage failure")
			}
			before.requireUnchanged(t, database, registry)
			sources, err := manager.List(ctx)
			if err != nil || len(sources) != 1 || sources[0].State != "ready" || sources[0].Error != "" || sources[0].ModelCount != 1 ||
				sources[0].LastSuccess == nil || !sources[0].LastSuccess.Equal(*created.LastSuccess) {
				t.Fatalf("source after failed update = %+v, %v", sources, err)
			}
			restore()
			if _, err := manager.Update(ctx, created.ProviderID, tc.input(f)); err != nil {
				t.Fatalf("update after storage recovered: %v", err)
			}
			if len(registry.GetAvailableModels()) != 2 {
				t.Fatalf("recovered update published %v", registry.GetAvailableModels())
			}
		})
	}
}

// Without a database RefreshAll cannot list sources, but still returns a
// non-nil slice together with the explicit error.
func TestRefreshAllWithoutDatabaseReturnsNonNilSourcesAndError(t *testing.T) {
	sources, err := New(nil, nil, nil).RefreshAll(context.Background())
	if err == nil || sources == nil {
		t.Fatalf("RefreshAll = %#v, %v; want non-nil sources and an error", sources, err)
	}
}

// A successful refresh that drops models down to none publishes an empty,
// ready source rather than an error.
func TestRefreshToEmptyCatalogIsReady(t *testing.T) {
	ctx := context.Background()
	for _, listing := range []string{noEligibles, `{"data":[]}`} {
		f := newDurableFixture(t, lunaAndSol)
		manager, registry, _ := newDurableManager(t, f)
		created, err := manager.Create(ctx, f.input("key", "acct"))
		if err != nil {
			t.Fatal(err)
		}
		f.listing.Store(listing)
		emptied, err := manager.Refresh(ctx, created.ProviderID)
		if err != nil || emptied.State != "ready" || emptied.ModelCount != 0 || emptied.Error != "" || len(registry.GetAvailableModels()) != 0 {
			t.Fatalf("empty refresh of %s = %+v, %v, models %v", listing, emptied, err, registry.GetAvailableModels())
		}
	}
}
