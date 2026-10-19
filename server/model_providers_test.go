package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
	"shelley.exe.dev/modelproviders"
	"shelley.exe.dev/models"
	"shelley.exe.dev/models/modelsdev"
)

type sourceNamedService struct {
	llm.Service
	source string
}

func (s sourceNamedService) Provider() string { return s.source }

func TestModelProviderRoutesPublishAndDeleteCredentialSafeSource(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	var failDiscovery atomic.Bool
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/v1/models" {
			t.Errorf("path = %q, want prefixed /custom/v1/models", r.URL.Path)
		}
		if r.Header.Get("Authorization") == "" {
			// Unauthenticated key probe: listing requires a key.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Errorf("authorization = %q, want explicit key", r.Header.Get("Authorization"))
		}
		if failDiscovery.Load() {
			http.Error(w, "upstream-secret-response", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"unknown-chat"}]}`)
	}))
	defer fixture.Close()

	llmManager, err := models.NewManager(&models.Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	providerManager := modelproviders.New(database, llmManager, fixture.Client())
	s := &Server{db: database, llmManager: llmManager}
	s.SetModelProviderManager(providerManager)
	s.refreshBuiltModels = func(context.Context) ([]models.Built, error) { return nil, nil }

	body := fmt.Sprintf(`{"provider_type":"openai","endpoint":%q,"api_key":"provider-secret","display_name":"Fixture"}`,
		fixture.URL+"/custom/v1")
	create := httptest.NewRecorder()
	s.handleCreateModelProvider(create, httptest.NewRequest(http.MethodPost, "/api/model-providers", strings.NewReader(body)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %q", create.Code, create.Body.String())
	}
	if strings.Contains(create.Body.String(), "provider-secret") {
		t.Fatalf("create response leaked credential: %s", create.Body.String())
	}
	var connection modelproviders.Connection
	if err := json.NewDecoder(create.Body).Decode(&connection); err != nil {
		t.Fatal(err)
	}
	if connection.State != "ready" || connection.ModelCount != 1 || connection.ProviderID == "" {
		t.Fatalf("connection = %+v, want ready one-model source", connection)
	}

	modelList := httptest.NewRecorder()
	s.handleModels(modelList, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var available []ModelInfo
	if err := json.NewDecoder(modelList.Body).Decode(&available); err != nil {
		t.Fatal(err)
	}
	var providerModel *ModelInfo
	for i := range available {
		if available[i].SourceID == connection.ProviderID {
			if providerModel != nil {
				t.Fatalf("models = %+v, want exactly one admitted provider model", available)
			}
			providerModel = &available[i]
		}
	}
	if providerModel == nil || !providerModel.Ready || providerModel.Tier != models.Tier1 ||
		providerModel.APIModelName != "gpt-6-luna" || !strings.HasPrefix(providerModel.ID, "provider:") {
		t.Fatalf("models = %+v, want one ready recognized provider model; unknown IDs must be excluded", available)
	}

	list := httptest.NewRecorder()
	s.handleListModelProviders(list, httptest.NewRequest(http.MethodGet, "/api/model-providers", nil))
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "provider-secret") {
		t.Fatalf("list status/body = %d %q; credentials must not be returned", list.Code, list.Body.String())
	}

	failDiscovery.Store(true)
	refresh := httptest.NewRecorder()
	s.handleRefreshModelProviders(refresh, httptest.NewRequest(http.MethodPost, "/api/model-providers/refresh", nil))
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, want per-source results; body %q", refresh.Code, refresh.Body.String())
	}
	var refreshResponse []modelproviders.Connection
	if err := json.NewDecoder(refresh.Body).Decode(&refreshResponse); err != nil {
		t.Fatal(err)
	}
	if len(refreshResponse) != 1 || refreshResponse[0].Error == "" ||
		refreshResponse[0].State != "error" ||
		strings.Contains(refresh.Body.String(), "upstream-secret-response") {
		t.Fatalf("refresh response = %+v, want sanitized failed source", refreshResponse)
	}
	if !llmManager.HasModel(providerModel.ID) {
		t.Fatalf("failed source refresh removed previously published route %q", providerModel.ID)
	}

	// A restart while discovery is still failing publishes nothing and
	// reports the error, but the existing route still resolves to its account.
	restartedLLM, err := models.NewManager(&models.Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	restartedProviders := modelproviders.New(database, restartedLLM, fixture.Client())
	if _, err := restartedProviders.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := &Server{db: database, llmManager: restartedLLM}
	restarted.SetModelProviderManager(restartedProviders)
	if info := restartedLLM.GetModelInfo(providerModel.ID); info == nil || info.SourceID != connection.ProviderID || slices.Contains(restartedLLM.GetAvailableModels(), providerModel.ID) {
		t.Fatalf("restart: route %q info %+v, available %v; want resolvable but unlisted", providerModel.ID, info, restartedLLM.GetAvailableModels())
	}
	list = httptest.NewRecorder()
	restarted.handleListModelProviders(list, httptest.NewRequest(http.MethodGet, "/api/model-providers", nil))
	var restartedSources []modelproviders.Connection
	if err := json.NewDecoder(list.Body).Decode(&restartedSources); err != nil {
		t.Fatal(err)
	}
	if len(restartedSources) != 1 || restartedSources[0].State != "error" || restartedSources[0].ModelCount != 0 || restartedSources[0].LastSuccess != nil {
		t.Fatalf("restarted sources = %+v", restartedSources)
	}
	failDiscovery.Store(false)

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/model-providers/"+connection.ProviderID, nil)
	deleteReq.SetPathValue("id", connection.ProviderID)
	revision := llmManager.ProviderCatalogRevision(connection.ProviderID)
	deleted := httptest.NewRecorder()
	s.handleDeleteModelProvider(deleted, deleteReq)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body %q", deleted.Code, deleted.Body.String())
	}
	if llmManager.HasModel(providerModel.ID) {
		t.Fatalf("deleted provider route %q remains registered", providerModel.ID)
	}
	if got := llmManager.ProviderCatalogRevision(connection.ProviderID); got <= revision {
		t.Fatalf("provider revision after deletion = %d, want greater than %d", got, revision)
	}

}

func TestIdleProviderLoopRebuildsAfterKeyRotation(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	conversation, err := database.CreateConversation(t.Context(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	const modelID = "provider:cHJvdmlkZXItMQ:bW9kZWwtMQ"
	build := func(key string) models.Built {
		t.Helper()
		built, err := models.BuildProviderModel("provider-1", "gpt-luna-custom", modelID, "Work", "openai", "https://api.openai.com/v1", key, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		built.Service = sourceNamedService{predictable.NewService(), key}
		return built
	}
	providerModel := build("old-key")
	llmManager, err := models.NewManager(&models.Config{Models: []models.Built{providerModel}})
	if err != nil {
		t.Fatal(err)
	}

	cm := NewConversationManager(
		conversation.ConversationID, database, slog.Default(),
		claudetool.ToolSetConfig{EnableBrowser: false}, nil, nil, nil, nil, nil,
	)
	cm.resolveModelService = llmManager.GetServiceSnapshot
	var resolvedProvider string
	cm.decorateService = func(service llm.Service) (llm.Service, error) {
		resolvedProvider = service.Provider()
		return service, nil
	}
	cm.hydrated = true
	cm.modelID = modelID
	oldService, _, oldRevision, err := llmManager.GetServiceSnapshot(modelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := cm.ensureLoop(oldService, modelID); err != nil {
		t.Fatalf("initial ensureLoop: %v", err)
	}
	oldLoop := cm.loop
	t.Cleanup(cm.ResetLoop)

	providerModel = build("new-key")
	llmManager.ReplaceProviderModels("provider-1", []models.Built{providerModel})
	if err := cm.ensureLoop(oldService, modelID); err != nil {
		t.Fatalf("ensureLoop after source revision: %v", err)
	}
	if cm.loop == oldLoop {
		t.Fatal("idle conversation retained loop captured from old provider catalog")
	}
	_, _, revision, err := llmManager.GetServiceSnapshot(modelID)
	if err != nil || revision == oldRevision || cm.loopSourceRevision != revision {
		t.Fatalf("loop revision = %d, current = %d, old = %d, err=%v", cm.loopSourceRevision, revision, oldRevision, err)
	}
	if resolvedProvider != "new-key" {
		t.Fatalf("loop service = %q; stale service must not receive the new revision", resolvedProvider)
	}
	llmManager.ReplaceProviderModels("provider-1", nil)
	if err := cm.ensureLoop(oldService, modelID); err == nil {
		t.Fatal("deleted route accepted a previously resolved service")
	}
}

func TestModelsExposeKnownIncompatibleProviderRouteUnready(t *testing.T) {
	toolCall := false
	manager, err := models.NewManager(&models.Config{Models: []models.Built{{
		ID: "provider:cHJvdmlkZXItMQ:aW1hZ2Utb25seQ", Provider: models.ProviderOpenAI,
		SourceID: "provider-1", APIModelName: "image-only",
		Capabilities: &modelsdev.Metadata{Match: "exact", ToolCall: &toolCall},
		Service:      predictable.NewService(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{llmManager: manager, logger: slog.Default()}
	rec := httptest.NewRecorder()
	s.handleModels(rec, httptest.NewRequest(http.MethodGet, "/api/models", nil))
	var available []ModelInfo
	if err := json.NewDecoder(rec.Body).Decode(&available); err != nil {
		t.Fatal(err)
	}
	if len(available) != 1 || available[0].Ready || available[0].SourceID != "provider-1" ||
		available[0].Capabilities == nil || available[0].Capabilities.ToolCall == nil ||
		*available[0].Capabilities.ToolCall {
		t.Fatalf("models = %+v, want incompatible route present but unready with metadata", available)
	}
}

func TestProviderModelTiersUseWireIDsWithinEachAccount(t *testing.T) {
	list := []ModelInfo{
		{ID: "account-a/new", SourceID: "a", APIModelName: "claude-opus-5.5", Ready: true},
		{ID: "account-a/old", SourceID: "a", APIModelName: "claude-opus-4.8", Ready: true},
		{ID: "account-b/old", SourceID: "b", APIModelName: "claude-opus-4.8", Ready: true},
		{ID: "account-b/unknown", SourceID: "b", APIModelName: "unfamiliar", Ready: true},
	}
	assignModelTiers(list)
	for i, want := range []int{models.Tier1, models.Tier2, models.Tier1, models.Tier2} {
		if list[i].Tier != want {
			t.Errorf("%s tier = %d, want %d", list[i].ID, list[i].Tier, want)
		}
	}
}

func TestProviderModelsExposeTechnicalContextWithoutAdvisoryFallback(t *testing.T) {
	manager, err := models.NewManager(&models.Config{Models: []models.Built{
		{ID: "reported", SourceID: "a", APIModelName: "unknown", Service: predictable.NewService(),
			Capabilities: &modelsdev.Metadata{Match: "unknown", ContextLimit: 32100, Provenance: map[string]string{"context_limit": "provider"}}},
		{ID: "advisory", SourceID: "b", APIModelName: "gpt-5", Service: predictable.NewService(),
			Capabilities: &modelsdev.Metadata{Match: "advisory", ContextLimit: 400000}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{llmManager: manager}
	for _, model := range s.getModelList() {
		want := 0
		if model.ID == "reported" {
			want = 32100
		}
		if model.MaxContextTokens != want {
			t.Errorf("%s context = %d, want %d", model.ID, model.MaxContextTokens, want)
		}
	}
}

func TestProviderWorkhorseStaysOnSameSourceAndUsesWireName(t *testing.T) {
	manager, err := models.NewManager(&models.Config{Models: []models.Built{
		{
			ID: "provider:cA:cHJpbWFyeQ", Provider: models.ProviderOpenAI, SourceID: "p",
			APIModelName: "gpt-5.6-sol", Service: sourceNamedService{predictable.NewService(), "primary"},
		},
		{
			ID: "provider:cA:d29ya2hvcnNl", Provider: models.ProviderOpenAI, SourceID: "p",
			APIModelName: "gpt-6-luna-fast", ReleaseDate: "2025-01-01",
			Service: sourceNamedService{predictable.NewService(), "same-account"},
		},
		{
			ID: "provider:cQ:d29ya2hvcnNl", Provider: models.ProviderOpenAI, SourceID: "q",
			APIModelName: "gpt-6-luna-latest", ReleaseDate: "2026-01-01",
			Service: sourceNamedService{predictable.NewService(), "other-account"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	service, err := manager.GetWorkhorseService("provider:cA:cHJpbWFyeQ")
	if err != nil {
		t.Fatal(err)
	}
	if service.Provider() != "same-account" {
		t.Fatalf("workhorse provider = %q, want same-account candidate selected by wire name", service.Provider())
	}
}

type providerTestFixture struct {
	server   *Server
	manager  *modelproviders.Manager
	llm      *models.Manager
	requests *atomic.Int32
	url      string
}

// newProviderTestFixture serves a model listing that requires a credential
// (so the unauthenticated key probe gets 401) and records every request.
func newProviderTestFixture(t *testing.T, listing string) providerTestFixture {
	t.Helper()
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	var requests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") == "" && r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, listing)
	}))
	t.Cleanup(fixture.Close)
	llmManager, err := models.NewManager(&models.Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	manager := modelproviders.New(database, llmManager, fixture.Client())
	s := &Server{db: database, llmManager: llmManager}
	s.SetModelProviderManager(manager)
	return providerTestFixture{server: s, manager: manager, llm: llmManager, requests: &requests, url: fixture.URL}
}

func (f providerTestFixture) post(handler func(http.ResponseWriter, *http.Request), target, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, target, strings.NewReader(body)))
	return rec
}

func TestModelProviderTestAndConnectPublishSameRecognizedListWithKeyProbe(t *testing.T) {
	f := newProviderTestFixture(t, `{"data":[{"id":"gpt-6-luna"},{"id":"vendor/unknown-chat"},{"id":"claude-proxy-x"},{"id":"gpt-6-astra"}]}`)
	body := fmt.Sprintf(`{"provider_type":"openai","endpoint":%q,"api_key":"secret","display_name":"Fixture"}`, f.url+"/v1")

	rec := f.post(f.server.handleTestModelProvider, "/api/model-providers/test", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("test status = %d, body %q", rec.Code, rec.Body.String())
	}
	// The response shape is exactly chat + key_checked.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 2 || raw["chat"] == nil || raw["key_checked"] == nil {
		t.Fatalf("test response keys = %v, want chat and key_checked only: %s", raw, rec.Body.String())
	}
	var tested modelproviders.TestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &tested); err != nil {
		t.Fatal(err)
	}
	if !tested.KeyChecked || len(tested.Chat) != 2 {
		t.Fatalf("test result = %+v, want two recognized models and a checked key (401 on unauthenticated listing)", tested)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("test response leaked key: %s", rec.Body.String())
	}
	if saved, _ := f.manager.List(t.Context()); len(saved) != 0 || len(f.llm.GetAvailableModels()) != 0 {
		t.Fatalf("Test persisted or published: saved=%v models=%v", saved, f.llm.GetAvailableModels())
	}

	create := f.post(f.server.handleCreateModelProvider, "/api/model-providers", body)
	if create.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %q", create.Code, create.Body.String())
	}
	var connection modelproviders.Connection
	if err := json.Unmarshal(create.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	var wire []string
	for _, id := range f.llm.GetAvailableModels() {
		wire = append(wire, f.llm.GetModelInfo(id).APIModelName)
	}
	if connection.ModelCount != len(tested.Chat) || strings.Join(wire, ",") != "gpt-6-luna,gpt-6-astra" {
		t.Fatalf("Connect published %v (count %d), Check listed %v", wire, connection.ModelCount, tested.Chat)
	}
}

func TestModelProviderDuplicateCredentialsReturnConflictForConnectAndTest(t *testing.T) {
	f := newProviderTestFixture(t, `{"data":[{"id":"gpt-6-luna"}]}`)
	body := func(endpoint, key string) string {
		return fmt.Sprintf(`{"provider_type":"openai","endpoint":%q,"api_key":%q}`, endpoint, key)
	}
	first := f.post(f.server.handleCreateModelProvider, "/api/model-providers", body(f.url+"/v1", "same"))
	if first.Code != http.StatusCreated {
		t.Fatalf("first create = %d %q", first.Code, first.Body.String())
	}
	var connection modelproviders.Connection
	if err := json.Unmarshal(first.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	// Canonical variants of the same endpoint (scheme case, trailing slash).
	variant := "HTTP" + strings.TrimPrefix(f.url, "http") + "/v1/"
	f.requests.Store(0)
	for name, handler := range map[string]func(http.ResponseWriter, *http.Request){
		"create": f.server.handleCreateModelProvider, "test": f.server.handleTestModelProvider,
	} {
		rec := f.post(handler, "/api/model-providers", body(variant, "same"))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s duplicate status = %d, body %q, want 409", name, rec.Code, rec.Body.String())
		}
	}
	if f.requests.Load() != 0 {
		t.Fatalf("duplicate rejection made %d upstream requests", f.requests.Load())
	}
	// A different account key is not a duplicate, and an edit never conflicts with itself.
	if rec := f.post(f.server.handleTestModelProvider, "/api/model-providers/test", body(variant, "another")); rec.Code != http.StatusOK {
		t.Fatalf("different key test = %d %q", rec.Code, rec.Body.String())
	}
	self := httptest.NewRecorder()
	selfReq := httptest.NewRequest(http.MethodPost, "/api/model-providers/test?provider_id="+connection.ProviderID, strings.NewReader(body(variant, "same")))
	f.server.handleTestModelProvider(self, selfReq)
	if self.Code != http.StatusOK {
		t.Fatalf("self test = %d %q", self.Code, self.Body.String())
	}
}

func TestModelProviderOriginChangeWithoutKeyIsRejectedBeforeAnyRequest(t *testing.T) {
	f := newProviderTestFixture(t, `{"data":[{"id":"gpt-6-luna"}]}`)
	var otherRequests atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherRequests.Add(1)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("saved credential forwarded to a new origin")
		}
	}))
	defer other.Close()
	create := f.post(f.server.handleCreateModelProvider, "/api/model-providers",
		fmt.Sprintf(`{"provider_type":"openai","endpoint":%q,"api_key":"secret"}`, f.url+"/v1"))
	if create.Code != http.StatusCreated {
		t.Fatalf("create = %d %q", create.Code, create.Body.String())
	}
	var connection modelproviders.Connection
	if err := json.Unmarshal(create.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	f.requests.Store(0)
	body := fmt.Sprintf(`{"provider_type":"openai","endpoint":%q}`, other.URL+"/v1")

	update := httptest.NewRecorder()
	updateReq := httptest.NewRequest(http.MethodPut, "/api/model-providers/"+connection.ProviderID, strings.NewReader(body))
	updateReq.SetPathValue("id", connection.ProviderID)
	f.server.handleUpdateModelProvider(update, updateReq)
	test := httptest.NewRecorder()
	f.server.handleTestModelProvider(test, httptest.NewRequest(http.MethodPost, "/api/model-providers/test?provider_id="+connection.ProviderID, strings.NewReader(body)))
	for name, rec := range map[string]*httptest.ResponseRecorder{"update": update, "test": test} {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, body %q, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if otherRequests.Load() != 0 || f.requests.Load() != 0 {
		t.Fatalf("requests after rejection: new origin=%d saved origin=%d, want none", otherRequests.Load(), f.requests.Load())
	}
	list := httptest.NewRecorder()
	f.server.handleListModelProviders(list, httptest.NewRequest(http.MethodGet, "/api/model-providers", nil))
	var sources []modelproviders.Connection
	if err := json.Unmarshal(list.Body.Bytes(), &sources); err != nil || len(sources) != 1 ||
		sources[0].State != "ready" || sources[0].Endpoint != f.url+"/v1" {
		t.Fatalf("saved source after rejected edits = %+v, %v", sources, err)
	}
}

func TestModelProviderAnthropicPublicListingReportsKeyUnchecked(t *testing.T) {
	listing := `{"data":[{"id":"claude-opus-5.5","display_name":"Opus"},{"id":"claude-fixture-unknown"}],"has_more":false}`
	for name, requireKey := range map[string]bool{"public custom endpoint": false, "custom endpoint requiring a key": true} {
		t.Run(name, func(t *testing.T) {
			database, cleanup := setupTestDB(t)
			t.Cleanup(cleanup)
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/proxy/v1/models" || r.Header.Get("anthropic-version") == "" {
					t.Errorf("request = %s version=%q", r.URL.Path, r.Header.Get("anthropic-version"))
				}
				if requireKey && r.Header.Get("x-api-key") == "" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				fmt.Fprint(w, listing)
			}))
			defer fixture.Close()
			s := &Server{db: database}
			s.SetModelProviderManager(modelproviders.New(database, nil, fixture.Client()))
			rec := httptest.NewRecorder()
			s.handleTestModelProvider(rec, httptest.NewRequest(http.MethodPost, "/api/model-providers/test", strings.NewReader(
				fmt.Sprintf(`{"provider_type":"anthropic","endpoint":%q,"api_key":"unverified"}`, fixture.URL+"/proxy/v1"))))
			var result struct {
				Chat       []string `json:"chat"`
				KeyChecked bool     `json:"key_checked"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil ||
				len(result.Chat) != 1 || result.Chat[0] != "Opus" || result.KeyChecked != requireKey {
				t.Fatalf("status=%d body=%q, want one recognized model and key_checked=%v", rec.Code, rec.Body.String(), requireKey)
			}
		})
	}
}

func TestModelProviderTestAcceptsTopLevelArrayListing(t *testing.T) {
	f := newProviderTestFixture(t, `[{"id":"gpt-6-sol","type":"chat","pricing":{"input":1}},{"id":"vendor/image","type":"image"}]`)
	rec := f.post(f.server.handleTestModelProvider, "/api/model-providers/test",
		fmt.Sprintf(`{"provider_type":"openai","endpoint":%q,"api_key":"k"}`, f.url+"/v1"))
	var result modelproviders.TestResult
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &result) != nil || len(result.Chat) != 1 || !result.KeyChecked {
		t.Fatalf("status=%d body=%q, want one recognized model from a top-level array", rec.Code, rec.Body.String())
	}
}

// A server without a provider manager (predictable-only) has no saved
// connections: listing is an empty array, not a management error, while every
// mutation stays disabled.
func TestPredictableOnlyListsNoProvidersAndKeepsMutationsDisabled(t *testing.T) {
	s := &Server{}
	list := httptest.NewRecorder()
	s.handleListModelProviders(list, httptest.NewRequest(http.MethodGet, "/api/model-providers", nil))
	if list.Code != http.StatusOK || strings.TrimSpace(list.Body.String()) != "[]" {
		t.Fatalf("list = %d %q, want 200 []", list.Code, list.Body.String())
	}
	for name, call := range map[string]func(http.ResponseWriter, *http.Request){
		"create":  s.handleCreateModelProvider,
		"test":    s.handleTestModelProvider,
		"update":  s.handleUpdateModelProvider,
		"delete":  s.handleDeleteModelProvider,
		"refresh": s.handleRefreshModelProvider,
	} {
		rec := httptest.NewRecorder()
		call(rec, httptest.NewRequest(http.MethodPost, "/api/model-providers", strings.NewReader(`{}`)))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", name, rec.Code)
		}
	}
	// Refreshing all with no manager has nothing to refresh: an empty array.
	rec := httptest.NewRecorder()
	s.handleRefreshModelProviders(rec, httptest.NewRequest(http.MethodPost, "/api/model-providers/refresh", nil))
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("refresh all = %d %q, want 200 []", rec.Code, rec.Body.String())
	}
}

// When a source fails during Refresh All the response is still a 200 Connection
// array carrying the failed source in state=error, and the published route stays.
func TestModelProviderRefreshAllReportsSourceFailureAsConnection(t *testing.T) {
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	var listing atomic.Value
	listing.Store(`{"data":[{"id":"gpt-6-luna"}]}`)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, listing.Load().(string))
	}))
	defer fixture.Close()
	llmManager, err := models.NewManager(&models.Config{DB: database})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{db: database, llmManager: llmManager}
	s.SetModelProviderManager(modelproviders.New(database, llmManager, fixture.Client()))
	created, err := s.modelProviders.Create(t.Context(), modelproviders.Input{
		ProviderType: "openai", Endpoint: fixture.URL + "/v1", APIKey: "secret", DisplayName: "Fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	listing.Store(`not json`)

	rec := httptest.NewRecorder()
	s.handleRefreshModelProviders(rec, httptest.NewRequest(http.MethodPost, "/api/model-providers/refresh", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	var sources []modelproviders.Connection
	if err := json.Unmarshal(rec.Body.Bytes(), &sources); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].ProviderID != created.ProviderID ||
		sources[0].State != "error" || sources[0].ModelCount != 1 || sources[0].Error == "" {
		t.Fatalf("response = %s; want a one-element Connection array in error state", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") || len(llmManager.GetAvailableModels()) != 1 {
		t.Fatalf("leak or route loss: models=%v body=%s", llmManager.GetAvailableModels(), rec.Body.String())
	}
}
