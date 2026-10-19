package modelproviders

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// authRequiredListing serves body only to requests carrying a credential, as
// first-party APIs do; unauthenticated probes receive 401.
func authRequiredListing(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		if r.Header.Get("Authorization") == "" && r.Header.Get("x-api-key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, body)
	}))
}

func publishedWireIDs(registry *models.Manager) []string {
	var ids []string
	for _, id := range registry.GetAvailableModels() {
		ids = append(ids, registry.GetModelInfo(id).APIModelName)
	}
	return ids
}

func TestCanonicalEndpointNormalizesOriginSpelling(t *testing.T) {
	for raw, want := range map[string]string{
		"https://OPENROUTER.AI/api/v1":              "https://openrouter.ai/api/v1",
		"https://openrouter.ai:443/api/v1/":         "https://openrouter.ai/api/v1",
		"HTTPS://Api.Example.COM:443/Mixed/Case/v1": "https://api.example.com/Mixed/Case/v1",
		"http://Example.com:80/v1":                  "http://example.com/v1",
		"https://example.com:80/v1":                 "https://example.com:80/v1",
		"http://example.com:443/v1":                 "http://example.com:443/v1",
		"https://example.com:8443/v1":               "https://example.com:8443/v1",
		"http://[::1]:80/v1":                        "http://[::1]/v1",
		"http://[::1]:8080/v1":                      "http://[::1]:8080/v1",
	} {
		got, err := canonicalEndpoint(raw)
		if err != nil || got != want {
			t.Errorf("canonicalEndpoint(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if !sameOrigin("https://OPENROUTER.AI:443/api/v1", "https://openrouter.ai/other") {
		t.Error("equivalent origins must match")
	}
	for _, other := range []string{"http://openrouter.ai/api/v1", "https://openrouter.ai:8443/api/v1", "https://evil.example/api/v1", "not a url"} {
		if sameOrigin("https://openrouter.ai/api/v1", other) {
			t.Errorf("origin %q must differ", other)
		}
	}
}

func TestDiscoveryListingFixturesAcrossProviders(t *testing.T) {
	for _, tc := range []struct {
		file, providerType string
		discovered         []string
		admitted           []string
	}{
		{"openai.json", "openai", []string{"gpt-6-luna", "text-embedding-fixture"}, []string{"gpt-6-luna"}},
		{"openrouter.json", "openai", []string{"vendor/fixture-chat", "gpt-6-astra"}, []string{"gpt-6-astra"}},
		{"together.json", "openai", []string{"gpt-6-sol", "vendor/fixture-image"}, []string{"gpt-6-sol"}},
		{"groq.json", "openai-responses", []string{"gpt-6-luna", "whisper-fixture"}, []string{"gpt-6-luna"}},
		{"mistral.json", "openai", []string{"gpt-6-astra"}, []string{"gpt-6-astra"}},
		{"deepseek.json", "openai", []string{"vendor/fixture-reasoner"}, nil},
		{"anthropic.json", "anthropic", []string{"claude-opus-5.5", "claude-fixture-unknown"}, []string{"claude-opus-5.5"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "listings", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			server := authRequiredListing(t, string(raw), nil)
			defer server.Close()

			descriptors, err := discover(context.Background(), server.Client(), tc.providerType, server.URL+"/v1", "fixture-key")
			if err != nil {
				t.Fatal(err)
			}
			if got := discoveredIDs(descriptors); !slices.Equal(got, tc.discovered) {
				t.Fatalf("discovered = %v, want %v", got, tc.discovered)
			}

			database, cleanup := db.NewTestDB(t)
			defer cleanup()
			registry, _ := models.NewManager(&models.Config{})
			manager := New(database, registry, server.Client())
			input := Input{ProviderType: tc.providerType, Endpoint: server.URL + "/v1", APIKey: "fixture-key"}
			tested, err := manager.Test(context.Background(), "", input)
			if err != nil {
				t.Fatal(err)
			}
			if !tested.KeyChecked || len(tested.Chat) != len(tc.admitted) {
				t.Fatalf("Test = %+v, want %d admitted checked models", tested, len(tc.admitted))
			}
			if tc.providerType == "anthropic" && !slices.Equal(tested.Chat, []string{"Fixture Opus"}) {
				t.Fatalf("provider display name not used: %v", tested.Chat)
			}
			created, err := manager.Create(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			// Check and Connect must agree, and unknown/non-chat IDs never publish.
			if created.ModelCount != len(tested.Chat) || !slices.Equal(publishedWireIDs(registry), tc.admitted) {
				t.Fatalf("published %v (count %d), want %v matching Test %v",
					publishedWireIDs(registry), created.ModelCount, tc.admitted, tested.Chat)
			}
		})
	}
}

func TestDiscoverAcceptsTopLevelArrayAndKeepsStrictMalformedEmptyDistinction(t *testing.T) {
	serve := func(body string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
	}
	for name, tc := range map[string]struct {
		providerType, body string
		wantIDs            []string
		wantErr            bool
	}{
		"together array":                {"openai", `[{"id":"a"},{"id":"b"}]`, []string{"a", "b"}, false},
		"array with whitespace":         {"openai", " \n[{\"id\":\"a\"}]\n", []string{"a"}, false},
		"valid empty array":             {"openai", `[]`, nil, false},
		"valid empty data":              {"openai", `{"data":[]}`, nil, false},
		"array rejected for anthropic":  {"anthropic", `[{"id":"a"}]`, nil, true},
		"empty body":                    {"openai", ``, nil, true},
		"null":                          {"openai", `null`, nil, true},
		"empty object":                  {"openai", `{}`, nil, true},
		"data null":                     {"openai", `{"data":null}`, nil, true},
		"data object":                   {"openai", `{"data":{"id":"a"}}`, nil, true},
		"array of strings":              {"openai", `["a"]`, nil, true},
		"array element without id":      {"openai", `[{"name":"a"}]`, nil, true},
		"array element blank id":        {"openai", `[{"id":" "}]`, nil, true},
		"array element numeric id":      {"openai", `[{"id":7}]`, nil, true},
		"truncated array":               {"openai", `[{"id":"a"}`, nil, true},
		"extra provider fields ignored": {"openai", `{"data":[{"id":"a","capabilities":["chat"],"max_tokens":"n/a","pricing":{"x":1}}]}`, []string{"a"}, false},
		"anthropic invalid token limit": {"anthropic", `{"data":[{"id":"a","max_tokens":0}]}`, nil, true},
	} {
		t.Run(name, func(t *testing.T) {
			server := serve(tc.body)
			defer server.Close()
			got, err := discover(context.Background(), server.Client(), tc.providerType, server.URL+"/v1", "k")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr && !slices.Equal(discoveredIDs(got), tc.wantIDs) {
				t.Fatalf("ids = %v, want %v", discoveredIDs(got), tc.wantIDs)
			}
		})
	}
	t.Run("oversized top-level array", func(t *testing.T) {
		server := serve("[" + strings.Repeat(" ", discoveryBodyLimit) + `{"id":"a"}]`)
		defer server.Close()
		if _, err := discover(context.Background(), server.Client(), "openai", server.URL+"/v1", "k"); err == nil || !strings.Contains(err.Error(), "body limit") {
			t.Fatalf("error = %v, want body limit", err)
		}
	})
}

func TestOriginChangeNeverRetainsSavedKeyInUpdateOrTest(t *testing.T) {
	var original, target atomic.Int32
	originalServer := authRequiredListing(t, `{"data":[{"id":"gpt-6-luna"}]}`, &original)
	defer originalServer.Close()
	targetServer := authRequiredListing(t, `{"data":[{"id":"gpt-6-luna"}]}`, &target)
	defer targetServer.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, originalServer.Client())
	created, err := manager.Create(context.Background(), Input{
		ProviderType: "openai", Endpoint: originalServer.URL + "/v1", APIKey: "saved-key", DisplayName: "saved",
	})
	if err != nil {
		t.Fatal(err)
	}
	original.Store(0)
	statusBefore, _ := manager.List(context.Background())

	for name, endpoint := range map[string]string{
		"other host and port": targetServer.URL + "/v1",
		"other scheme":        "https://" + strings.TrimPrefix(originalServer.URL, "http://") + "/v1",
	} {
		t.Run(name, func(t *testing.T) {
			input := Input{ProviderType: "openai", Endpoint: endpoint}
			if _, err := manager.Update(context.Background(), created.ProviderID, input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Update error = %v, want ErrInvalid", err)
			}
			if _, err := manager.Test(context.Background(), created.ProviderID, input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Test error = %v, want ErrInvalid", err)
			}
			// A whitespace-only key is blank, not a replacement credential.
			input.APIKey = "   "
			if _, err := manager.Update(context.Background(), created.ProviderID, input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Update with blank key error = %v, want ErrInvalid", err)
			}
		})
	}
	if target.Load() != 0 || original.Load() != 0 {
		t.Fatalf("requests after rejected origin changes: target=%d original=%d, want none", target.Load(), original.Load())
	}
	statusAfter, _ := manager.List(context.Background())
	if statusAfter[0].State != "ready" || statusAfter[0].Endpoint != statusBefore[0].Endpoint {
		t.Fatalf("rejected edits changed saved source: %+v", statusAfter[0])
	}

	// A re-entered key may move to the new origin; the old key never does.
	if _, err := manager.Test(context.Background(), created.ProviderID, Input{
		ProviderType: "openai", Endpoint: targetServer.URL + "/v1", APIKey: "new-key",
	}); err != nil {
		t.Fatal(err)
	}
	// Path-only and equivalent-spelling edits keep the saved key.
	for _, endpoint := range []string{originalServer.URL + "/v1/", "HTTP://" + strings.TrimPrefix(originalServer.URL, "http://") + "/v1"} {
		if _, err := manager.Update(context.Background(), created.ProviderID, Input{ProviderType: "openai", Endpoint: endpoint}); err != nil {
			t.Fatalf("same-origin update %q: %v", endpoint, err)
		}
	}
}

func TestFailedCandidateUpdateDoesNotPoisonWorkingSourceStatus(t *testing.T) {
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") == "Bearer bad-candidate" && failing.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, server.Client())
	created, err := manager.Create(context.Background(), Input{ProviderType: "openai", Endpoint: server.URL + "/v1", APIKey: "good", DisplayName: "working"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := manager.List(context.Background())
	failing.Store(true)
	returned, err := manager.Update(context.Background(), created.ProviderID, Input{
		ProviderType: "openai", Endpoint: server.URL + "/v1", APIKey: "bad-candidate", DisplayName: "candidate",
	})
	if err == nil {
		t.Fatal("candidate with rejected key was accepted")
	}
	after, _ := manager.List(context.Background())
	if returned.State != "ready" || after[0].State != "ready" || after[0].Error != "" ||
		after[0].DisplayName != "working" || after[0].ModelCount != 1 ||
		!after[0].LastSuccess.Equal(*before[0].LastSuccess) {
		t.Fatalf("failed candidate changed working source: returned=%+v after=%+v", returned, after[0])
	}
}

func TestTestDetectsDuplicatesLikeConnectWithoutWriting(t *testing.T) {
	var requests atomic.Int32
	server := authRequiredListing(t, `{"data":[{"id":"gpt-6-luna"}]}`, &requests)
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, server.Client())
	first, err := manager.Create(context.Background(), Input{ProviderType: "openai", Endpoint: server.URL + "/v1", APIKey: "same", DisplayName: "first"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.Create(context.Background(), Input{ProviderType: "openai", Endpoint: server.URL + "/v1", APIKey: "other", DisplayName: "second"})
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore, _ := database.GetModelProviders(context.Background())
	requests.Store(0)
	spelling := "HTTP://" + strings.TrimPrefix(server.URL, "http://") + "/v1/"

	if _, err := manager.Test(context.Background(), "", Input{ProviderType: "openai", Endpoint: spelling, APIKey: "same"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("new-connection Test error = %v, want ErrDuplicate", err)
	}
	// Editing the second row to the first row's credentials collides too.
	if _, err := manager.Test(context.Background(), other.ProviderID, Input{ProviderType: "openai", Endpoint: spelling, APIKey: "same"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("edit-to-duplicate Test error = %v, want ErrDuplicate", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("duplicate Test made %d requests, want none", requests.Load())
	}
	// A provider never duplicates itself, whether the key is retained or restated.
	for _, key := range []string{"", "same"} {
		if _, err := manager.Test(context.Background(), first.ProviderID, Input{ProviderType: "openai", Endpoint: spelling, APIKey: key}); err != nil {
			t.Fatalf("self Test with key %q: %v", key, err)
		}
	}
	rowsAfter, _ := database.GetModelProviders(context.Background())
	if len(rowsAfter) != len(rowsBefore) || len(registry.GetAvailableModels()) != 2 {
		t.Fatalf("Test wrote state: rows %d -> %d, models=%v", len(rowsBefore), len(rowsAfter), registry.GetAvailableModels())
	}
}

func TestSavedLegacyEndpointSpellingsCollideWithCanonicalCandidates(t *testing.T) {
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	if _, err := database.CreateModelProvider(context.Background(), generated.CreateModelProviderParams{
		ProviderID: "legacy", ProviderType: "openai", Endpoint: "https://OPENROUTER.AI:443/api/v1/", ApiKey: "k", DisplayName: "legacy",
	}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected request")
	})}
	manager := New(database, nil, client)
	input := Input{ProviderType: "openai", Endpoint: "https://openrouter.ai/api/v1", APIKey: "k"}
	if _, err := manager.Create(context.Background(), input); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Create error = %v, want ErrDuplicate", err)
	}
	if _, err := manager.Test(context.Background(), "", input); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Test error = %v, want ErrDuplicate", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want none", requests.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestKnownOpenRouterKeyCheckRecognizesNormalizedOrigin(t *testing.T) {
	var keyChecks, unauthenticatedListings atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			if r.Header.Get("Authorization") == "" {
				unauthenticatedListings.Add(1)
			}
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
		case "/api/v1/key":
			keyChecks.Add(1)
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
	for _, endpoint := range []string{
		"https://openrouter.ai/api/v1", "https://OPENROUTER.AI/api/v1", "https://openrouter.ai:443/api/v1/",
	} {
		keyChecks.Store(0)
		if result, err := manager.Test(context.Background(), "", Input{ProviderType: "openai", Endpoint: endpoint, APIKey: "good"}); err != nil || !result.KeyChecked || len(result.Chat) != 1 {
			t.Fatalf("%s good key: %+v, %v", endpoint, result, err)
		}
		if keyChecks.Load() != 1 {
			t.Fatalf("%s did not use dedicated key check", endpoint)
		}
		if _, err := manager.Test(context.Background(), "", Input{ProviderType: "openai-responses", Endpoint: endpoint, APIKey: "bad"}); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
			t.Fatalf("%s bad key error = %v", endpoint, err)
		}
	}
	// Different origins are not OpenRouter, even with the same path.
	keyChecks.Store(0)
	for _, endpoint := range []string{"https://openrouter.ai:8443/api/v1", "http://openrouter.ai/api/v1", "https://openrouter.ai.example/api/v1"} {
		if result, err := manager.Test(context.Background(), "", Input{ProviderType: "openai", Endpoint: endpoint, APIKey: "bad"}); err != nil || result.KeyChecked {
			t.Fatalf("%s: %+v, %v; public unknown origin must be unchecked", endpoint, result, err)
		}
	}
	if keyChecks.Load() != 0 {
		t.Fatalf("saved key was sent to non-OpenRouter origin %d times", keyChecks.Load())
	}
}

func TestKeyProbeSemanticsForUnauthorizedPublicAndFirstPartyListings(t *testing.T) {
	probe := func(status int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" && r.Header.Get("x-api-key") == "" {
				if status != http.StatusOK {
					w.WriteHeader(status)
					return
				}
			}
			fmt.Fprint(w, `{"data":[{"id":"claude-opus-5.5"}]}`)
		}))
	}
	for _, tc := range []struct {
		name         string
		providerType string
		unauthStatus int
		wantChecked  bool
	}{
		{"openai unauthorized", "openai", http.StatusUnauthorized, true},
		{"openai forbidden", "openai-responses", http.StatusForbidden, true},
		{"openai public", "openai", http.StatusOK, false},
		{"openai probe not found", "openai", http.StatusNotFound, false},
		{"custom anthropic requiring auth", "anthropic", http.StatusUnauthorized, true},
		{"custom anthropic public", "anthropic", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := probe(tc.unauthStatus)
			defer server.Close()
			manager := New(nil, nil, server.Client())
			result, err := manager.Test(context.Background(), "", Input{ProviderType: tc.providerType, Endpoint: server.URL + "/v1", APIKey: "wrong-but-accepted"})
			if err != nil || result.KeyChecked != tc.wantChecked || len(result.Chat) != 1 {
				t.Fatalf("Test = %+v, %v; want key_checked=%v", result, err, tc.wantChecked)
			}
		})
	}

	t.Run("first-party Anthropic needs no extra request", func(t *testing.T) {
		var requests atomic.Int32
		manager := New(nil, nil, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests.Add(1)
			if r.URL.Host != "api.anthropic.com" || r.Header.Get("x-api-key") != "k" {
				t.Errorf("unexpected request %s key=%q", r.URL, r.Header.Get("x-api-key"))
			}
			rec := httptest.NewRecorder()
			fmt.Fprint(rec, `{"data":[{"id":"claude-opus-5.5","display_name":"Opus"}],"has_more":false}`)
			return rec.Result(), nil
		})})
		for _, endpoint := range []string{"https://api.anthropic.com/v1", "https://API.Anthropic.com:443/v1"} {
			requests.Store(0)
			result, err := manager.Test(context.Background(), "", Input{ProviderType: "anthropic", Endpoint: endpoint, APIKey: "k"})
			if err != nil || !result.KeyChecked || requests.Load() != 1 {
				t.Fatalf("%s: %+v, %v, requests=%d", endpoint, result, err, requests.Load())
			}
		}
	})
}

func TestRefreshAllDiscoversConcurrentlyWithIsolatedStatusAndDeterministicPublication(t *testing.T) {
	fastSeen := make(chan struct{})
	var fastOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/slow/"):
			// Returns only after the later source was requested, which a
			// serial RefreshAll could never reach.
			select {
			case <-fastSeen:
			case <-time.After(10 * time.Second):
				t.Error("slow source blocked later sources")
			}
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"},{"id":"vendor/unknown"}]}`)
		case strings.HasPrefix(r.URL.Path, "/fast/"):
			fastOnce.Do(func() { close(fastSeen) })
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-astra"}]}`)
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "private upstream body")
		}
	}))
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	for _, row := range []generated.CreateModelProviderParams{
		{ProviderID: "a-slow", ProviderType: "openai", Endpoint: server.URL + "/slow/v1", ApiKey: "a", DisplayName: "slow"},
		{ProviderID: "b-fast", ProviderType: "openai", Endpoint: server.URL + "/fast/v1", ApiKey: "b", DisplayName: "fast"},
		{ProviderID: "c-bad", ProviderType: "openai", Endpoint: server.URL + "/bad/v1", ApiKey: "c", DisplayName: "bad"},
	} {
		if _, err := database.CreateModelProvider(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, server.Client())
	if _, err := manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	sources, err := manager.List(context.Background())
	if err != nil || len(sources) != 3 {
		t.Fatalf("List = %v, %v", sources, err)
	}
	want := map[string]struct {
		state string
		count int
	}{"a-slow": {"ready", 1}, "b-fast": {"ready", 1}, "c-bad": {"error", 0}}
	for _, source := range sources {
		w := want[source.ProviderID]
		if source.State != w.state || source.ModelCount != w.count ||
			(w.state == "error" && (source.LastSuccess != nil || strings.Contains(source.Error, "private upstream body"))) {
			t.Errorf("%s = %+v, want %+v", source.ProviderID, source, w)
		}
	}
	var order []string
	for _, id := range registry.GetAvailableModels() {
		order = append(order, registry.GetModelInfo(id).SourceID)
	}
	if !slices.Equal(order, []string{"a-slow", "b-fast"}) {
		t.Fatalf("publication order = %v, want saved-source order", order)
	}

	// Removing a source's row and refreshing again unpublishes only that source.
	if err := database.DeleteModelProvider(context.Background(), "a-slow"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := publishedWireIDs(registry); !slices.Equal(got, []string{"gpt-6-astra"}) {
		t.Fatalf("models after removed row = %v", got)
	}
}

func TestRefreshAllBoundsConcurrentDiscovery(t *testing.T) {
	const sources = loadConcurrency + 2
	var started, inFlight, maxInFlight atomic.Int32
	saturated := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := maxInFlight.Load()
			if current <= seen || maxInFlight.CompareAndSwap(seen, current) {
				break
			}
		}
		// Hold the first loadConcurrency requests until all slots are busy.
		// An unbounded RefreshAll would start extra requests while they are held.
		n := started.Add(1)
		if n == loadConcurrency {
			close(saturated)
		}
		if n <= loadConcurrency {
			select {
			case <-saturated:
			case <-time.After(10 * time.Second):
				t.Error("RefreshAll never reached its concurrency bound")
			}
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
	}))
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	for i := 0; i < sources; i++ {
		if _, err := database.CreateModelProvider(context.Background(), generated.CreateModelProviderParams{
			ProviderID: fmt.Sprintf("source-%02d", i), ProviderType: "openai",
			Endpoint: fmt.Sprintf("%s/s%d/v1", server.URL, i), ApiKey: "k", DisplayName: fmt.Sprintf("s%02d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, server.Client())
	if _, err := manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := maxInFlight.Load(); got != loadConcurrency {
		t.Fatalf("max concurrent discoveries = %d, want exactly %d", got, loadConcurrency)
	}
	if got := len(registry.GetAvailableModels()); got != sources {
		t.Fatalf("published models = %d, want %d", got, sources)
	}
}

func TestPresetProvidersAdmitNativeModels(t *testing.T) {
	for _, tc := range []struct{ endpoint, wireID string }{
		{"https://api.groq.com/openai/v1", "llama-3.1-8b-instant"},
		{"https://api.mistral.ai/v1", "pixtral-12b"},
		{"https://api.together.ai/v1", "essentialai/Rnj-1-Instruct"},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			fixture := authRequiredListing(t, fmt.Sprintf(`{"data":[{"id":%q}]}`, tc.wireID), nil)
			defer fixture.Close()
			client := &http.Client{Transport: rewriteTransport{target: strings.TrimPrefix(fixture.URL, "http://")}}
			database, cleanup := db.NewTestDB(t)
			defer cleanup()
			registry, err := models.NewManager(&models.Config{})
			if err != nil {
				t.Fatal(err)
			}
			manager := New(database, registry, client)
			input := Input{ProviderType: "openai", Endpoint: tc.endpoint, APIKey: "fixture-key"}
			checked, err := manager.Test(t.Context(), "", input)
			if err != nil || len(checked.Chat) != 1 {
				t.Fatalf("Check = %+v, %v; want one native chat model", checked, err)
			}
			connected, err := manager.Create(t.Context(), input)
			if err != nil || connected.ModelCount != 1 || !slices.Equal(publishedWireIDs(registry), []string{tc.wireID}) {
				t.Fatalf("Connect = %+v, %v; published = %v", connected, err, publishedWireIDs(registry))
			}
		})
	}
}
