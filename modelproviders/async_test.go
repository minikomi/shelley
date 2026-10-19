package modelproviders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/models"
)

// gatedServer serves a switchable listing. A response is chosen when a request
// arrives; a gated key's authenticated requests then wait for release, so tests
// order completions with channels. Unauthenticated key probes get 401.
type gatedServer struct {
	server *httptest.Server
	mu     sync.Mutex
	status int
	body   string
	gates  map[string]*gate
}

// gate holds authenticated requests for one key ("*" matches every key).
type gate struct {
	entered chan struct{}
	release chan struct{}
}

func newGatedServer(t *testing.T, body string) *gatedServer {
	t.Helper()
	g := &gatedServer{body: body, gates: map[string]*gate{}}
	g.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		g.mu.Lock()
		status, body := g.status, g.body
		held := cmpGate(g.gates[key], g.gates["*"])
		g.mu.Unlock()
		if held != nil {
			held.entered <- struct{}{}
			<-held.release
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(g.server.Close)
	return g
}

func cmpGate(a, b *gate) *gate {
	if a != nil {
		return a
	}
	return b
}

// respond sets the response of requests that arrive from now on.
func (g *gatedServer) respond(status int, body string) {
	g.mu.Lock()
	g.status, g.body = status, body
	g.mu.Unlock()
}

// hold gates the key until the caller closes the returned gate's release.
func (g *gatedServer) hold(key string) *gate {
	h := &gate{entered: make(chan struct{}, 8), release: make(chan struct{})}
	g.mu.Lock()
	g.gates[key] = h
	g.mu.Unlock()
	return h
}

// unhold stops gating new requests; requests already waiting stay held.
func (g *gatedServer) unhold(key string) {
	g.mu.Lock()
	delete(g.gates, key)
	g.mu.Unlock()
}

func (g *gatedServer) input(key, name string) Input {
	return Input{ProviderType: "openai", Endpoint: g.server.URL + "/v1", APIKey: key, DisplayName: name}
}

func (g *gatedServer) manager(t *testing.T) (*Manager, *models.Manager, *db.DB) {
	t.Helper()
	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	registry, err := models.NewManager(&models.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return New(database, registry, g.server.Client()), registry, database
}

const asyncTimeout = 10 * time.Second

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(asyncTimeout):
		t.Fatal("timed out waiting; an operation is blocked")
		panic("unreachable")
	}
}

// promptly runs fn and fails if it does not return while discovery is held.
func promptly[T any](t *testing.T, fn func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- fn() }()
	return recv(t, done)
}

type refreshResult struct {
	conn Connection
	err  error
}

func goRefresh(m *Manager, id string) <-chan refreshResult {
	done := make(chan refreshResult, 1)
	go func() {
		conn, err := m.Refresh(context.Background(), id)
		done <- refreshResult{conn, err}
	}()
	return done
}

func TestBlockedRefreshDoesNotBlockRenameOrOtherAccounts(t *testing.T) {
	ctx := context.Background()
	g := newGatedServer(t, lunaOnly)
	m, registry, _ := g.manager(t)
	a, err := m.Create(ctx, g.input("key-a", "a"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create(ctx, g.input("key-b", "b"))
	if err != nil {
		t.Fatal(err)
	}
	revision := registry.ProviderCatalogRevision(a.ProviderID)
	h := g.hold("key-a")
	refresh := goRefresh(m, a.ProviderID)
	recv(t, h.entered)
	g.unhold("key-a")

	type ops struct {
		renamed, created, refreshed Connection
		errs                        []error
	}
	got := promptly(t, func() ops {
		var o ops
		var err error
		o.renamed, err = m.Update(ctx, a.ProviderID, g.input("", "renamed"))
		o.errs = append(o.errs, err)
		o.created, err = m.Create(ctx, g.input("key-c", "c"))
		o.errs = append(o.errs, err)
		o.refreshed, err = m.Refresh(ctx, b.ProviderID)
		o.errs = append(o.errs, err)
		_, err = m.List(ctx)
		o.errs = append(o.errs, err)
		return o
	})
	for _, err := range got.errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got.renamed.DisplayName != "renamed" || got.created.State != "ready" || got.refreshed.State != "ready" {
		t.Fatalf("operations while discovery was blocked: %+v", got)
	}
	close(h.release)
	res := recv(t, refresh)
	if res.err != nil || res.conn.DisplayName != "renamed" || res.conn.State != "ready" {
		t.Fatalf("released refresh = %+v", res)
	}
	// Neither the rename nor the unchanged refresh republishes services.
	if got := registry.ProviderCatalogRevision(a.ProviderID); got != revision {
		t.Fatalf("revision = %d, want %d unchanged by the label-only rename", got, revision)
	}
	for _, id := range registry.GetAvailableModels() {
		if info := registry.GetModelInfo(id); info.SourceID == a.ProviderID && info.Source != "renamed" {
			t.Fatalf("source label = %q, want renamed", info.Source)
		}
	}
}

func TestStaleDiscoveryAfterEditOrDeleteCannotOverwriteOrResurrect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int // the delayed old-config discovery's response
		edit   bool
	}{
		{"success after edit", 0, true},
		{"error after edit", http.StatusBadGateway, true},
		{"success after delete", 0, false},
		{"error after delete", http.StatusBadGateway, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := newGatedServer(t, lunaOnly)
			m, registry, _ := g.manager(t)
			created, err := m.Create(ctx, g.input("old", "acct"))
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			m.SetOnChange(func() { calls.Add(1) })

			g.respond(tc.status, lunaAndSol)
			h := g.hold("old")
			refresh := goRefresh(m, created.ProviderID)
			recv(t, h.entered)
			g.unhold("old")
			g.respond(0, noEligibles)

			if tc.edit {
				if res := promptly(t, func() refreshResult {
					c, err := m.Update(ctx, created.ProviderID, g.input("new", "acct"))
					return refreshResult{c, err}
				}); res.err != nil {
					t.Fatal(res.err)
				}
			} else if err := promptly(t, func() error { return m.Delete(ctx, created.ProviderID) }); err != nil {
				t.Fatal(err)
			}
			before := calls.Load()
			close(h.release)
			res := recv(t, refresh)
			if calls.Load() != before {
				t.Fatal("discarded stale result notified")
			}
			if tc.edit {
				if res.err != nil || res.conn.State != "ready" || res.conn.Error != "" || res.conn.ModelCount != 0 {
					t.Fatalf("stale refresh = %+v, want the edited source's own state", res)
				}
				if got := publishedWireIDs(registry); len(got) != 0 {
					t.Fatalf("stale success overwrote the edited catalog: %v", got)
				}
				return
			}
			if !errors.Is(res.err, ErrNotFound) {
				t.Fatalf("stale refresh error = %v, want ErrNotFound", res.err)
			}
			sources, err := m.List(ctx)
			m.mu.RLock()
			leaked := len(m.states) + len(m.published) + len(m.refreshes)
			m.mu.RUnlock()
			if err != nil || len(sources) != 0 || len(registry.GetAvailableModels()) != 0 || leaked != 0 {
				t.Fatalf("deleted source resurrected: %v %v leaked=%d", sources, registry.GetAvailableModels(), leaked)
			}
		})
	}
}

func TestDeleteIsNotBlockedByDiscoveryOfSameSource(t *testing.T) {
	ctx := context.Background()
	g := newGatedServer(t, lunaOnly)
	m, _, _ := g.manager(t)
	created, err := m.Create(ctx, g.input("key", "acct"))
	if err != nil {
		t.Fatal(err)
	}
	h := g.hold("key")
	refresh := goRefresh(m, created.ProviderID)
	recv(t, h.entered)
	if err := promptly(t, func() error { return m.Delete(ctx, created.ProviderID) }); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	if res := recv(t, refresh); !errors.Is(res.err, ErrNotFound) {
		t.Fatalf("refresh of deleted source = %+v", res)
	}
}

func TestUpdateConflictsWhenSourceChangedDuringDiscovery(t *testing.T) {
	ctx := context.Background()
	g := newGatedServer(t, lunaOnly)
	m, registry, database := g.manager(t)
	created, err := m.Create(ctx, g.input("key", "acct"))
	if err != nil {
		t.Fatal(err)
	}
	revision := registry.ProviderCatalogRevision(created.ProviderID)
	h := g.hold("rotated")
	update := make(chan refreshResult, 1)
	go func() {
		c, err := m.Update(ctx, created.ProviderID, g.input("rotated", "acct"))
		update <- refreshResult{c, err}
	}()
	recv(t, h.entered)
	if res := promptly(t, func() refreshResult {
		c, err := m.Update(ctx, created.ProviderID, g.input("", "renamed"))
		return refreshResult{c, err}
	}); res.err != nil {
		t.Fatal(res.err)
	}
	close(h.release)
	res := recv(t, update)
	if !errors.Is(res.err, ErrConflict) {
		t.Fatalf("stale update = %+v, want ErrConflict", res)
	}
	row, err := database.GetModelProvider(ctx, created.ProviderID)
	if err != nil || row.ApiKey != "key" || row.DisplayName != "renamed" {
		t.Fatalf("row after conflict = %+v, %v", row, err)
	}
	if got := registry.ProviderCatalogRevision(created.ProviderID); got != revision {
		t.Fatalf("revision = %d, want %d unchanged by the label-only rename", got, revision)
	}

	// An update whose source was deleted meanwhile is not found, not recreated.
	h = g.hold("again")
	go func() {
		c, err := m.Update(ctx, created.ProviderID, g.input("again", "renamed"))
		update <- refreshResult{c, err}
	}()
	recv(t, h.entered)
	if err := promptly(t, func() error { return m.Delete(ctx, created.ProviderID) }); err != nil {
		t.Fatal(err)
	}
	close(h.release)
	if res := recv(t, update); !errors.Is(res.err, ErrNotFound) {
		t.Fatalf("update after delete = %+v", res)
	}
	if rows, _ := database.GetModelProviders(ctx); len(rows) != 0 {
		t.Fatalf("update resurrected the source: %v", rows)
	}
}

func TestOverlappingRefreshesKeepNewestStartedResult(t *testing.T) {
	type response struct {
		status int
		body   string
	}
	ok := func(body string) response { return response{0, body} }
	failing := response{http.StatusBadGateway, ""}
	for _, tc := range []struct {
		name       string
		old, newer response
		wantCount  int
		wantError  bool
		viaRefresh bool
	}{
		{"old success, new success", ok(noEligibles), ok(lunaAndSol), 2, false, true},
		{"old error, new success", failing, ok(lunaAndSol), 2, false, true},
		{"old success, new error", ok(noEligibles), failing, 1, true, true},
		{"RefreshAll then Refresh", ok(noEligibles), ok(lunaAndSol), 2, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			g := newGatedServer(t, lunaOnly)
			m, registry, _ := g.manager(t)
			created, err := m.Create(ctx, g.input("key", "acct"))
			if err != nil {
				t.Fatal(err)
			}
			g.respond(tc.old.status, tc.old.body)
			h := g.hold("key")
			older := make(chan error, 1)
			if tc.viaRefresh {
				go func() { _, err := m.Refresh(ctx, created.ProviderID); older <- err }()
			} else {
				go func() { _, err := m.RefreshAll(ctx); older <- err }()
			}
			recv(t, h.entered)
			g.unhold("key")
			g.respond(tc.newer.status, tc.newer.body)
			newer := recv(t, goRefresh(m, created.ProviderID))
			if (newer.err != nil) != tc.wantError {
				t.Fatalf("newer refresh error = %v", newer.err)
			}
			var calls atomic.Int32
			m.SetOnChange(func() { calls.Add(1) })
			close(h.release)
			recv(t, older)
			if calls.Load() != 0 {
				t.Fatal("older refresh notified after a newer one committed")
			}
			list, err := m.List(ctx)
			if err != nil || len(list) != 1 {
				t.Fatal(list, err)
			}
			got := list[0]
			if got.ModelCount != tc.wantCount || (got.Error != "") != tc.wantError || len(registry.GetAvailableModels()) != tc.wantCount {
				t.Fatalf("final = %+v with %v models, want count %d error %v", got, registry.GetAvailableModels(), tc.wantCount, tc.wantError)
			}
		})
	}
}

func TestConcurrentCreatesRetainDuplicateAndNumberedLabelPolicy(t *testing.T) {
	ctx := context.Background()
	g := newGatedServer(t, lunaOnly)
	m, _, _ := g.manager(t)
	h := g.hold("*")
	create := func(key string) <-chan refreshResult {
		done := make(chan refreshResult, 1)
		go func() {
			c, err := m.Create(ctx, g.input(key, ""))
			done <- refreshResult{c, err}
		}()
		return done
	}
	// Two same-account creates race: exactly one wins, the other is a duplicate.
	same := []<-chan refreshResult{create("same"), create("same")}
	recv(t, h.entered)
	recv(t, h.entered)
	close(h.release)
	var wins, dups int
	for _, done := range same {
		switch res := recv(t, done); {
		case res.err == nil:
			wins++
		case errors.Is(res.err, ErrDuplicate):
			dups++
		default:
			t.Fatal(res.err)
		}
	}
	if wins != 1 || dups != 1 {
		t.Fatalf("same-account creates: %d won, %d duplicate", wins, dups)
	}

	// Different accounts at one endpoint race for the automatic label.
	h = g.hold("*")
	distinct := []<-chan refreshResult{create("one"), create("two")}
	recv(t, h.entered)
	recv(t, h.entered)
	close(h.release)
	host := strings.TrimPrefix(g.server.URL, "http://")
	var labels []string
	for _, done := range distinct {
		res := recv(t, done)
		if res.err != nil {
			t.Fatal(res.err)
		}
		labels = append(labels, res.conn.DisplayName)
	}
	slices.Sort(labels)
	// The first same-account winner already took the plain host label.
	if want := []string{host + " (2)", host + " (3)"}; !slices.Equal(labels, want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
}

func TestOnChangeFiresForEveryVisibleMutationButNotTestOrStaleResults(t *testing.T) {
	ctx := context.Background()
	g := newGatedServer(t, lunaOnly)
	m, _, database := g.manager(t)
	var calls atomic.Int32
	m.SetOnChange(func() {
		// Notification must never run under the state lock.
		m.mu.Lock()
		m.mu.Unlock()
		calls.Add(1)
	})
	step := func(name string, want int32, fn func() error) {
		t.Helper()
		before := calls.Load()
		err := fn()
		if got := calls.Load() - before; got != want {
			t.Fatalf("%s: %d callbacks (err %v), want %d", name, got, err, want)
		}
	}
	var id string
	step("create", 1, func() error {
		c, err := m.Create(ctx, g.input("key", "acct"))
		id = c.ProviderID
		return err
	})
	step("unchanged success", 1, func() error { _, err := m.Refresh(ctx, id); return err })
	g.respond(http.StatusBadGateway, "")
	step("error", 1, func() error { m.Refresh(ctx, id); return nil })
	g.respond(0, lunaOnly)
	step("recovery clearing error", 1, func() error { _, err := m.Refresh(ctx, id); return err })
	step("catalog change", 1, func() error {
		g.respond(0, lunaAndSol)
		_, err := m.Refresh(ctx, id)
		return err
	})
	step("test", 0, func() error { _, err := m.Test(ctx, id, g.input("key", "acct")); return err })
	step("same label", 0, func() error { _, err := m.Update(ctx, id, g.input("", "acct")); return err })
	step("rename", 1, func() error { _, err := m.Update(ctx, id, g.input("", "renamed")); return err })
	step("edit", 1, func() error { _, err := m.Update(ctx, id, g.input("rotated", "renamed")); return err })
	step("delete", 1, func() error { return m.Delete(ctx, id) })

	// Startup discovery: error, then the single retry's success.
	if _, err := database.CreateModelProvider(ctx, generated.CreateModelProviderParams{ProviderID: "startup", ProviderType: "openai", Endpoint: g.server.URL + "/v1", ApiKey: "k"}); err != nil {
		t.Fatal(err)
	}
	g.respond(http.StatusBadGateway, "")
	step("startup error and failed retry", 2, func() error { m.discoverAtStartup(ctx, 0); return nil })
	g.respond(0, lunaOnly)
	step("startup success", 1, func() error { m.discoverAtStartup(ctx, 0); return nil })
}
