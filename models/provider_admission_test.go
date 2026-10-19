package models

import (
	"reflect"
	"slices"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/models/modelsdev"
)

func TestLegacyMissingModelsDoNotConsultProviderResolver(t *testing.T) {
	manager, err := NewManager(&Config{})
	if err != nil {
		t.Fatal(err)
	}
	const savedID = "provider:YWNjb3VudA:bW9kZWw"
	manager.SetProviderResolver(func(id string) (Built, error) {
		if id != savedID {
			t.Fatalf("provider resolver received legacy ID %q", id)
		}
		return Built{ID: id, SourceID: "account", Service: predictable.NewService()}, nil
	})
	if _, err := manager.GetService("missing-legacy"); err == nil {
		t.Fatal("missing legacy model must remain unsupported")
	}
	if manager.HasModel("missing-legacy") || manager.GetModelInfo("missing-legacy") != nil {
		t.Fatal("missing legacy model acquired provider metadata")
	}
	if _, err := manager.GetService(savedID); err != nil {
		t.Fatalf("saved route must still resolve: %v", err)
	}
}

func TestProviderModelEligibleIgnoresRankingButKeepsExclusions(t *testing.T) {
	yes, no := true, false
	chat := &modelsdev.Metadata{
		Match: "exact", ToolCall: &yes,
		InputModalities: []string{"text"}, OutputModalities: []string{"text"},
	}
	embedding := &modelsdev.Metadata{Match: "exact", ToolCall: &yes, InputModalities: []string{"text"}, OutputModalities: []string{"embedding"}}
	noTools := &modelsdev.Metadata{Match: "exact", ToolCall: &no, InputModalities: []string{"text"}, OutputModalities: []string{"text"}}
	advisory := &modelsdev.Metadata{Match: "advisory", ToolCall: &yes, InputModalities: []string{"text"}, OutputModalities: []string{"text"}}

	// Adding a preferred sibling demotes gpt-5.5 for display but never admission.
	if tiers := ProviderTiers(map[string]*modelsdev.Metadata{"gpt-5.5": nil, "gpt-6.1-sol": nil}); tiers["gpt-5.5"] != Tier2 || tiers["gpt-6.1-sol"] != Tier1 {
		t.Fatalf("ranking changed: %v", tiers)
	}
	for _, tc := range []struct {
		name string
		id   string
		md   *modelsdev.Metadata
		want bool
	}{
		{"older catalog model while newer exists", "gpt-5.5", nil, true},
		{"newer catalog model", "gpt-6.1-sol", nil, true},
		{"recognised chat outside catalog", "vendor-chat", chat, true},
		{"unknown without evidence", "vendor-unknown", nil, false},
		{"advisory-only evidence", "vendor-advisory", advisory, false},
		{"known non-chat output", "vendor-embedding", embedding, false},
		{"known no tool calls", "vendor-notools", noTools, false},
		{"catalog ID but known incompatible", "gpt-5.5", embedding, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProviderModelEligible(tc.id, tc.md); got != tc.want {
				t.Fatalf("eligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRelabelProviderModelsChangesOnlyLabelsWithoutInvalidating(t *testing.T) {
	route := func(source, name string) Built {
		t.Helper()
		built, err := BuildProviderModel(source, name, "provider:"+source+":"+name, name, "openai", "https://api.openai.com/v1", "key", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		built.Source = "old " + source
		built.Service = predictable.NewService()
		return built
	}
	mgr, err := NewManager(&Config{Models: []Built{predictableBuilt()}})
	if err != nil {
		t.Fatal(err)
	}
	a1, a2, b1 := route("A", "a1"), route("A", "a2"), route("B", "b1")
	mgr.ReplaceProviderModels("A", []Built{a1, a2})
	mgr.ReplaceProviderModels("B", []Built{b1})
	order := mgr.GetAvailableModels()
	catalogRevisionA := mgr.ProviderCatalogRevision("A")
	_, _, revisionA, err := mgr.GetServiceSnapshot(a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	mgr.mu.RLock()
	service := mgr.services[a1.ID].service
	mgr.mu.RUnlock()

	mgr.RelabelProviderModels("A", "renamed")

	for _, id := range []string{a1.ID, a2.ID} {
		if got := mgr.GetModelInfo(id).Source; got != "renamed" {
			t.Fatalf("%s source = %q", id, got)
		}
	}
	if got := mgr.GetModelInfo(b1.ID).Source; got != "old B" {
		t.Fatalf("other provider relabelled: %q", got)
	}
	if got := mgr.GetAvailableModels(); !reflect.DeepEqual(got, order) {
		t.Fatalf("order = %v, want %v", got, order)
	}
	if got := mgr.ProviderCatalogRevision("A"); got != catalogRevisionA {
		t.Fatalf("catalog revision = %d, want %d", got, catalogRevisionA)
	}
	_, _, revision, err := mgr.GetServiceSnapshot(a1.ID)
	if err != nil || revision != revisionA {
		t.Fatalf("service revision = %d, want %d, err=%v", revision, revisionA, err)
	}
	mgr.mu.RLock()
	same := mgr.services[a1.ID].service == service
	mgr.mu.RUnlock()
	if !same {
		t.Fatal("relabel replaced the service")
	}
	mgr.RelabelProviderModels("missing", "x") // no routes: harmless
}

func TestRefreshBuiltModelsPreservesMixedSourceOrder(t *testing.T) {
	custom := func(t *testing.T, testDB *db.DB, id string) {
		t.Helper()
		if _, err := testDB.CreateModel(t.Context(), generated.CreateModelParams{
			ModelID: id, DisplayName: id, ProviderType: "openai", Endpoint: "https://api.example.com/v1", ApiKey: "k", ModelName: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	saved := Built{ID: "provider:A:a", Provider: ProviderOpenAI, SourceID: "A", Source: "A", Service: predictable.NewService()}
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *db.DB, *Manager)
		want  []string
	}{
		{"custom created before saved", func(t *testing.T, testDB *db.DB, m *Manager) {
			custom(t, testDB, "C")
			if err := m.RefreshCustomModels(); err != nil {
				t.Fatal(err)
			}
			m.ReplaceProviderModels("A", []Built{saved})
		}, []string{"new", "C", saved.ID}},
		{"saved created before custom", func(t *testing.T, testDB *db.DB, m *Manager) {
			m.ReplaceProviderModels("A", []Built{saved})
			custom(t, testDB, "C")
			if err := m.RefreshCustomModels(); err != nil {
				t.Fatal(err)
			}
		}, []string{"new", saved.ID, "C"}},
		{"new custom follows surviving routes", func(t *testing.T, testDB *db.DB, m *Manager) {
			custom(t, testDB, "C")
			if err := m.RefreshCustomModels(); err != nil {
				t.Fatal(err)
			}
			m.ReplaceProviderModels("A", []Built{saved})
			custom(t, testDB, "D") // in the DB, not yet loaded
		}, []string{"new", "C", saved.ID, "D"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
			if err != nil {
				t.Fatal(err)
			}
			defer testDB.Close()
			if err := testDB.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			mgr, err := NewManager(&Config{Models: []Built{{ID: "old", Provider: ProviderBuiltIn, Source: "x", Service: predictable.NewService()}}, DB: testDB})
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, testDB, mgr)
			if err := mgr.RefreshBuiltModels([]Built{{ID: "new", Provider: ProviderBuiltIn, Source: "x", Service: predictable.NewService()}}); err != nil {
				t.Fatal(err)
			}
			if got := mgr.GetAvailableModels(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order = %v, want %v", got, tc.want)
			}
			if err := mgr.RefreshCustomModels(); err != nil {
				t.Fatal(err)
			}
			if got := mgr.GetAvailableModels(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("order after reapplying individual models = %v, want %v", got, tc.want)
			}
		})
	}
}

// Without saved routes, refreshes order custom routes exactly as the
// database lists them; order preservation is only for saved-route catalogs.
func TestLegacyRefreshKeepsBaselineOrderWithoutSavedRoutes(t *testing.T) {
	testDB, err := db.New(db.Config{DSN: t.TempDir() + "/test.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer testDB.Close()
	if err := testDB.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B", "C"} {
		if _, err := testDB.CreateModel(t.Context(), generated.CreateModelParams{
			ModelID: id, DisplayName: id, ProviderType: "openai", Endpoint: "https://api.example.com/v1", ApiKey: "k", ModelName: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := testDB.GetModels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new"}
	for _, row := range rows {
		want = append(want, row.ModelID)
	}
	mgr, err := NewManager(&Config{Models: []Built{{ID: "old", Provider: ProviderBuiltIn, Source: "x", Service: predictable.NewService()}}, DB: testDB})
	if err != nil {
		t.Fatal(err)
	}
	reverseCustom := func() {
		mgr.mu.Lock()
		defer mgr.mu.Unlock()
		slices.Reverse(mgr.modelOrder[1:])
	}
	reverseCustom()
	if err := mgr.RefreshBuiltModels([]Built{{ID: "new", Provider: ProviderBuiltIn, Source: "x", Service: predictable.NewService()}}); err != nil {
		t.Fatal(err)
	}
	if got := mgr.GetAvailableModels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("order after RefreshBuiltModels = %v, want %v", got, want)
	}
	reverseCustom()
	if err := mgr.RefreshCustomModels(); err != nil {
		t.Fatal(err)
	}
	if got := mgr.GetAvailableModels(); !reflect.DeepEqual(got, want) {
		t.Fatalf("order after RefreshCustomModels = %v, want %v", got, want)
	}
}
