package modelproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
)

func discoveredIDs(descriptors []discoveredModel) []string {
	ids := make([]string, len(descriptors))
	for i, descriptor := range descriptors {
		ids[i] = descriptor.ID
	}
	return ids
}

func TestDuplicateCheckRejectsInvalidSavedEndpoint(t *testing.T) {
	err := checkDuplicate(
		[]generated.ModelProvider{{ProviderID: "broken", Endpoint: "not-a-url", ApiKey: "fixture"}},
		&generated.ModelProvider{Endpoint: "https://example.test/v1", ApiKey: "fixture"},
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid saved endpoint error = %v", err)
	}
}

func TestDiscoverOpenAIMixedIDsAndAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/v1/models" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer explicit-secret" {
			t.Errorf("authorization = %q", got)
		}
		fmt.Fprint(w, `{"data":[{"id":"vendor/unfamiliar"},{"id":"claude-proxy-x"},{"id":"vendor/unfamiliar"}]}`)
	}))
	defer server.Close()
	ids, err := discover(context.Background(), server.Client(), "openai", server.URL+"/prefix/v1", "explicit-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(discoveredIDs(ids), ","), "vendor/unfamiliar,claude-proxy-x"; got != want {
		t.Fatalf("ids = %q, want %q", got, want)
	}
}

func TestDiscoverAnthropicFollowsEveryPage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "anthropic-secret" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("Anthropic authentication/version headers missing")
		}
		switch requests.Add(1) {
		case 1:
			if r.URL.Path != "/v1/models" || r.URL.Query().Get("after_id") != "" || r.URL.Query().Get("before_id") != "" {
				t.Errorf("first request = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"data":[{"id":"claude-a"}],"has_more":true,"last_id":"claude-a"}`)
		case 2:
			if got := r.URL.Query().Get("after_id"); got != "claude-a" || r.URL.Query().Get("before_id") != "" {
				t.Errorf("next page query = %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"data":[{"id":"claude-b"}],"has_more":false,"last_id":"claude-b"}`)
		default:
			t.Errorf("unexpected page %d", requests.Load())
		}
	}))
	defer server.Close()
	ids, err := discover(context.Background(), server.Client(), "anthropic", server.URL+"/v1", "anthropic-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(discoveredIDs(ids), ","); got != "claude-a,claude-b" {
		t.Fatalf("ids = %q", got)
	}
	if requests.Load() != 2 {
		t.Fatalf("pages = %d, want 2", requests.Load())
	}
}

func TestDiscoverRejectsMalformedEmptyAndLoopingResponses(t *testing.T) {
	for name, body := range map[string]string{
		"empty body":   "",
		"missing data": `{"object":"list"}`,
		"not array":    `{"data":{"id":"x"}}`,
		"invalid row":  `{"data":[{"name":"no-id"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			_, err := discover(context.Background(), server.Client(), "openai", server.URL+"/v1", "secret")
			if err == nil {
				t.Fatal("expected malformed response error")
			}
		})
	}
	t.Run("pagination loop", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"data":[{"id":"x"}],"has_more":true,"last_id":"same"}`)
		}))
		defer server.Close()
		_, err := discover(context.Background(), server.Client(), "anthropic", server.URL+"/v1", "secret")
		if err == nil || !strings.Contains(err.Error(), "loop") {
			t.Fatalf("error = %v, want pagination loop", err)
		}
	})
}

func TestDiscoveryErrorsNeverIncludeBodyOrCredentialsAndRedirectsStop(t *testing.T) {
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected.Store(true)
		if r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("credential forwarded across redirect")
		}
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/models", http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	_, err := discover(context.Background(), source.Client(), "openai", source.URL+"/v1", "sensitive-key")
	if err == nil || redirected.Load() {
		t.Fatalf("error = %v, redirected = %v", err, redirected.Load())
	}

	body := "response-secret"
	authFailure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, body)
	}))
	defer authFailure.Close()
	_, err = discover(context.Background(), authFailure.Client(), "openai", authFailure.URL+"/v1", "sensitive-key")
	if err == nil || strings.Contains(err.Error(), body) || strings.Contains(err.Error(), "sensitive-key") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestDiscoveryBodyLimitAndCancellation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, strings.Repeat("x", discoveryBodyLimit+1))
	}))
	defer server.Close()
	_, err := discover(context.Background(), server.Client(), "openai", server.URL+"/v1", "secret")
	if err == nil || !strings.Contains(err.Error(), "body limit") {
		t.Fatalf("oversized response error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = discover(ctx, server.Client(), "openai", server.URL+"/v1", "secret")
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled request error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("server requests = %d, want only oversized request", requests.Load())
	}
}

func TestCanonicalEndpointRequiresAbsoluteAPIBase(t *testing.T) {
	for _, endpoint := range []string{
		"", "/v1", "https://user:secret@example.com/v1",
		"https://example.com/v1?key=secret", "https://example.com/v1#fragment",
		"https://example.com/v1/chat/completions", "https://example.com/a/%2e%2e/v1",
	} {
		if _, err := canonicalEndpoint(endpoint); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalEndpoint(%q) error = %v, want ErrInvalid", endpoint, err)
		}
	}
	if got, err := canonicalEndpoint("https://example.com/proxy/v1/"); err != nil || got != "https://example.com/proxy/v1" {
		t.Fatalf("canonical endpoint = %q, %v", got, err)
	}
}

func TestProviderLifecycleRotationFailedRefreshAndDelete(t *testing.T) {
	type discoveryResponse struct {
		body   string
		status int
	}
	var response atomic.Value
	response.Store(discoveryResponse{body: `{"data":[{"id":"gpt-6-luna"},{"id":"unfamiliar-one"}]}`})
	var currentKey atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got != "Bearer "+currentKey.Load().(string) {
			t.Errorf("Authorization = %q", got)
		}
		result := response.Load().(discoveryResponse)
		if result.status != 0 {
			w.WriteHeader(result.status)
			fmt.Fprint(w, "private upstream body")
			return
		}
		fmt.Fprint(w, result.body)
	}))
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	registry, err := models.NewManager(&models.Config{})
	if err != nil {
		t.Fatal(err)
	}
	manager := New(database, registry, server.Client())
	currentKey.Store("first-key")
	created, err := manager.Create(context.Background(), Input{
		ProviderType: "openai", Endpoint: server.URL + "/prefix/v1",
		APIKey: "first-key", DisplayName: "Test source",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.State != "ready" || created.ModelCount != 1 || created.LastSuccess == nil {
		t.Fatalf("created = %+v", created)
	}
	listed, err := manager.List(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %v, %v", listed, err)
	}
	raw, _ := json.Marshal(listed)
	if strings.Contains(string(raw), "first-key") {
		t.Fatalf("connection leaked key: %s", raw)
	}
	if got := publishedWireIDs(registry); !slices.Equal(got, []string{"gpt-6-luna"}) {
		t.Fatalf("published wire IDs = %v, want only the recognized model", got)
	}
	originalID := registry.GetAvailableModels()[0]
	if !strings.HasPrefix(originalID, "provider:") || registry.GetModelInfo(originalID).SourceID != created.ProviderID {
		t.Fatalf("unexpected provider runtime model: %q", originalID)
	}

	retained, err := manager.Update(context.Background(), created.ProviderID, Input{
		ProviderType: "openai", Endpoint: server.URL + "/prefix/v1",
		DisplayName: "Retained key",
	})
	if err != nil || !retained.HasAPIKey {
		t.Fatalf("empty edit did not retain key: connection=%+v err=%v", retained, err)
	}

	currentKey.Store("rotated-key")
	response.Store(discoveryResponse{body: `{"data":[{"id":"gpt-6-astra"},{"id":"unfamiliar-three"},{"id":"gpt-6-luna"}]}`})
	updated, err := manager.Update(context.Background(), created.ProviderID, Input{
		ProviderType: "openai", Endpoint: server.URL + "/prefix/v1",
		APIKey: "rotated-key", DisplayName: "Renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ModelCount != 2 || updated.LastSuccess == nil || !slices.Equal(publishedWireIDs(registry), []string{"gpt-6-luna", "gpt-6-astra"}) {
		t.Fatalf("updated=%+v models=%v", updated, registry.GetAvailableModels())
	}
	publishedBeforeFailure := registry.GetAvailableModels()
	lastSuccessBeforeFailure := *updated.LastSuccess

	response.Store(discoveryResponse{status: http.StatusUnauthorized})
	failed, err := manager.Refresh(context.Background(), created.ProviderID)
	if err == nil || failed.State != "error" || failed.ModelCount != 2 || failed.LastSuccess == nil ||
		!failed.LastSuccess.Equal(lastSuccessBeforeFailure) {
		t.Fatalf("failed refresh = %+v, err=%v", failed, err)
	}
	if strings.Contains(failed.Error, "private upstream body") || strings.Contains(failed.Error, "rotated-key") {
		t.Fatalf("unsafe status error: %q", failed.Error)
	}
	if !slices.Equal(registry.GetAvailableModels(), publishedBeforeFailure) {
		t.Fatalf("failed refresh replaced the published catalog: %v", registry.GetAvailableModels())
	}

	if err := manager.Delete(context.Background(), created.ProviderID); err != nil {
		t.Fatal(err)
	}
	if len(registry.GetAvailableModels()) != 0 {
		t.Fatalf("models after delete = %v", registry.GetAvailableModels())
	}
	if _, err := manager.Refresh(context.Background(), created.ProviderID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refresh after delete error = %v, want ErrNotFound", err)
	}
}

func TestRefreshAllKeepsIndependentSourceErrors(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"good-unknown"}]}`)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, "secret")
	}))
	defer bad.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	for _, row := range []generated.CreateModelProviderParams{
		{ProviderID: "good", ProviderType: "openai", Endpoint: good.URL + "/v1", ApiKey: "a", DisplayName: "good"},
		{ProviderID: "bad", ProviderType: "openai", Endpoint: bad.URL + "/v1", ApiKey: "b", DisplayName: "bad"},
	} {
		if _, err := database.CreateModelProvider(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, nil)
	if _, err := manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	sources, err := manager.List(context.Background())
	if err != nil || len(sources) != 2 {
		t.Fatalf("List = %v, %v", sources, err)
	}
	states := map[string]string{}
	counts := map[string]int{}
	for _, source := range sources {
		states[source.ProviderID] = source.State
		counts[source.ProviderID] = source.ModelCount
	}
	if states["good"] != "ready" || states["bad"] != "error" || counts["good"] != 1 || counts["bad"] != 0 {
		t.Fatalf("states = %v counts = %v", states, counts)
	}
	if got := publishedWireIDs(registry); !slices.Equal(got, []string{"gpt-6-luna"}) {
		t.Fatalf("published = %v, want only the good source's recognized model", got)
	}
}

func TestConcurrentRefreshUpdateAndDeleteSerialize(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var block atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() {
			once.Do(func() {
				close(started)
				<-release
			})
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, server.Client())
	created, err := manager.Create(context.Background(), Input{
		ProviderType: "openai", Endpoint: server.URL + "/v1", APIKey: "key", DisplayName: "source",
	})
	if err != nil {
		t.Fatal(err)
	}
	// A real admitted route exists, so an empty registry at the end proves
	// deletion won over the queued refresh and update rather than that nothing
	// was ever published.
	if got := publishedWireIDs(registry); !slices.Equal(got, []string{"gpt-6-luna"}) {
		t.Fatalf("published before concurrency = %v, want the admitted route", got)
	}
	block.Store(true)
	refreshDone := make(chan error, 1)
	go func() {
		_, err := manager.Refresh(context.Background(), created.ProviderID)
		refreshDone <- err
	}()
	<-started
	type result struct {
		name string
		err  error
	}
	results := make(chan result, 2)
	launched := make(chan struct{}, 2)
	go func() {
		launched <- struct{}{}
		// A rotated key (not a label-only edit) so the update discovers.
		_, err := manager.Update(context.Background(), created.ProviderID, Input{
			ProviderType: "openai", Endpoint: server.URL + "/v1", APIKey: "rotated", DisplayName: "edited",
		})
		results <- result{name: "update", err: err}
	}()
	go func() {
		launched <- struct{}{}
		results <- result{name: "delete", err: manager.Delete(context.Background(), created.ProviderID)}
	}()
	<-launched
	<-launched
	close(release)
	// Delete is no longer queued behind the refresh's network call, so it may win.
	if err := <-refreshDone; err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got := []result{<-results, <-results}
	deleted := false
	for _, result := range got {
		if result.name == "delete" && result.err == nil {
			deleted = true
		}
		if result.name == "update" && result.err != nil && !errors.Is(result.err, ErrNotFound) {
			t.Fatalf("update error = %v", result.err)
		}
	}
	if !deleted {
		t.Fatalf("delete did not succeed: %+v", got)
	}
	sources, err := manager.List(context.Background())
	if err != nil || len(sources) != 0 || len(registry.GetAvailableModels()) != 0 {
		t.Fatalf("final sources=%v models=%v err=%v", sources, registry.GetAvailableModels(), err)
	}
}

func TestSavedProviderInferenceUsesExplicitEndpointKeysAndWireIDs(t *testing.T) {
	for _, protocol := range []struct {
		name, path string
	}{
		{"anthropic", "/prefix/v1/messages"},
		{"openai", "/prefix/v1/chat/completions"},
		{"openai-responses", "/prefix/v1/responses"},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			const wireID = "gpt-6-luna"
			var inference atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if r.URL.Path != "/prefix/v1/models" {
						t.Errorf("discovery path = %q", r.URL.Path)
					}
					fmt.Fprintf(w, `{"data":[{"id":%q},{"id":"vendor/unfamiliar-model"}]}`, wireID)
					return
				}
				inference.Store(true)
				if r.URL.Path != protocol.path {
					t.Errorf("inference path = %q, want %q", r.URL.Path, protocol.path)
				}
				if protocol.name == "anthropic" {
					if got := r.Header.Get("x-api-key"); got != "explicit-inference-key" {
						t.Errorf("x-api-key = %q", got)
					}
				} else if got := r.Header.Get("Authorization"); got != "Bearer explicit-inference-key" {
					t.Errorf("Authorization = %q", got)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if payload["model"] != wireID {
					t.Errorf("wire model = %v, want %q", payload["model"], wireID)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				switch protocol.name {
				case "anthropic":
					fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"unfamiliar\",\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
					fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
					fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				case "openai":
					fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"unfamiliar\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
					fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"unfamiliar\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
					fmt.Fprint(w, "data: [DONE]\n\n")
				case "openai-responses":
					fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]}}\n\n")
				}
			}))
			defer server.Close()
			database, cleanup := db.NewTestDB(t)
			defer cleanup()
			registry, _ := models.NewManager(&models.Config{})
			manager := New(database, registry, server.Client())
			connection, err := manager.Create(context.Background(), Input{
				ProviderType: protocol.name, Endpoint: server.URL + "/prefix/v1",
				APIKey: "explicit-inference-key", DisplayName: "fixture",
			})
			if err != nil {
				t.Fatal(err)
			}
			modelID := registry.GetAvailableModels()[0]
			service, err := registry.GetService(modelID)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Do(context.Background(), &llm.Request{Messages: []llm.Message{{
				Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hello"}},
			}}, OnStream: func(llm.StreamDelta) {}})
			if err != nil {
				t.Fatalf("inference: %v", err)
			}
			if !inference.Load() || connection.ModelCount != 1 {
				t.Fatalf("inference=%v connection=%+v", inference.Load(), connection)
			}
			info := registry.GetModelInfo(modelID)
			if info == nil || info.APIModelName != wireID || info.SourceID != connection.ProviderID {
				t.Fatalf("model info = %+v", info)
			}
		})
	}
}

type rewriteTransport struct{ target string }

func (t rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = "http", t.target
	return http.DefaultTransport.RoundTrip(r)
}

func TestCheckModelsChecksKeyWhenListingIsPublic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"vendor/model"}]}`)
		case "/api/v1/key":
			if r.Header.Get("Authorization") != "Bearer good" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"data":{}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	manager := New(nil, nil, &http.Client{Transport: rewriteTransport{strings.TrimPrefix(server.URL, "http://")}})
	check := func(endpoint, key string) (TestResult, error) {
		return manager.Test(context.Background(), "", Input{ProviderType: "openai", Endpoint: endpoint, APIKey: key})
	}
	if result, err := check("https://openrouter.ai/api/v1", "good"); err != nil || !result.KeyChecked || len(result.Chat) != 1 {
		t.Fatalf("good OpenRouter key: %+v, %v", result, err)
	}
	if _, err := check("https://openrouter.ai/api/v1", "bad"); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("bad OpenRouter key error = %v", err)
	}
	if result, err := check("https://public.example/api/v1", "anything"); err != nil || result.KeyChecked {
		t.Fatalf("public listing elsewhere: %+v, %v", result, err)
	}
}
