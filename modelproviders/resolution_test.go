package modelproviders

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/models"
)

// Only the connection is saved. An existing conversation's route is rebuilt
// from its ID and the connection's current key, so neither a discovery
// outage after restart nor the provider no longer listing the model moves
// it to another account; deleting the connection makes it fail explicitly.
func TestExistingRouteResolvesFromConnectionWithoutDiscovery(t *testing.T) {
	var listingFails atomic.Bool
	var listing atomic.Value
	listing.Store(`{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6-astra"},{"id":"text-embedding-3-small"}]}`)
	var listings atomic.Int32
	var inferenceKey, inferenceModel atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			listings.Add(1)
			if listingFails.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			fmt.Fprint(w, listing.Load().(string))
			return
		}
		inferenceKey.Store(r.Header.Get("Authorization"))
		var body strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			body.Write(buf[:n])
			if err != nil {
				break
			}
		}
		inferenceModel.Store(body.String())
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"x\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"x\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	ctx := context.Background()
	input := Input{ProviderType: "openai", Endpoint: server.URL + "/v1", APIKey: "first-key", DisplayName: "acct"}
	infer := func(registry *models.Manager, id string) error {
		t.Helper()
		service, err := registry.GetService(id)
		if err != nil {
			return err
		}
		_, err = service.Do(ctx, &llm.Request{Messages: []llm.Message{{
			Role: llm.MessageRoleUser, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "hi"}},
		}}, OnStream: func(llm.StreamDelta) {}})
		return err
	}

	registry, _ := models.NewManager(&models.Config{})
	first := New(database, registry, server.Client())
	checked, err := first.Test(ctx, "", input)
	if err != nil {
		t.Fatal(err)
	}
	created, err := first.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	ids := registry.GetAvailableModels()
	if len(ids) != 2 || len(checked.Chat) != 2 || created.ModelCount != 2 || created.LastSuccess == nil {
		t.Fatalf("check=%v created=%+v published=%v", checked.Chat, created, ids)
	}
	luna := routeID(created.ProviderID, "gpt-6-luna")
	if !slices.Contains(ids, luna) {
		t.Fatalf("published %v, want %s", ids, luna)
	}
	if _, err := first.Update(ctx, created.ProviderID, Input{ProviderType: "openai", Endpoint: input.Endpoint, APIKey: "second-key", DisplayName: "acct"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(registry.GetAvailableModels(), ids) {
		t.Fatalf("rotation changed route IDs: %v", registry.GetAvailableModels())
	}

	// Restart while listing is down: no routes are published, but the
	// existing route resolves through the rotated key and its wire ID.
	listingFails.Store(true)
	restarted, _ := models.NewManager(&models.Config{})
	second := New(database, restarted, server.Client())
	sources, err := second.RefreshAll(ctx)
	if err != nil || len(sources) != 1 || sources[0].State != "error" || sources[0].ModelCount != 0 || sources[0].LastSuccess != nil {
		t.Fatalf("startup during outage = %+v, %v", sources, err)
	}
	if len(restarted.GetAvailableModels()) != 0 {
		t.Fatalf("outage published %v", restarted.GetAvailableModels())
	}
	before := listings.Load()
	info := restarted.GetModelInfo(luna)
	if info == nil || info.SourceID != created.ProviderID || info.APIModelName != "gpt-6-luna" || info.Source != "acct" || info.DisplayName != "GPT-6 Luna" {
		t.Fatalf("resolved info = %+v", info)
	}
	if err := infer(restarted, luna); err != nil {
		t.Fatalf("inference during outage: %v", err)
	}
	if listings.Load() != before {
		t.Fatal("resolving a route made a listing request")
	}
	if inferenceKey.Load() != "Bearer second-key" || !strings.Contains(inferenceModel.Load().(string), `"gpt-6-luna"`) {
		t.Fatalf("inference used %v for %v", inferenceKey.Load(), inferenceModel.Load())
	}

	// A model the provider stops listing leaves the picker but its existing
	// route still reaches the same account.
	listingFails.Store(false)
	listing.Store(`{"data":[{"id":"gpt-6-astra"}]}`)
	refreshed, err := second.Refresh(ctx, created.ProviderID)
	if err != nil || refreshed.ModelCount != 1 || slices.Contains(restarted.GetAvailableModels(), luna) {
		t.Fatalf("refresh = %+v, %v, published %v", refreshed, err, restarted.GetAvailableModels())
	}
	if err := infer(restarted, luna); err != nil || inferenceKey.Load() != "Bearer second-key" {
		t.Fatalf("unlisted route: %v via %v", err, inferenceKey.Load())
	}

	// Deleting the connection makes its routes fail rather than fall back.
	if err := second.Delete(ctx, created.ProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.GetService(luna); err == nil || !strings.Contains(err.Error(), "removed provider connection") {
		t.Fatalf("deleted route error = %v", err)
	}
	if restarted.HasModel(luna) || restarted.GetModelInfo(luna) != nil {
		t.Fatal("deleted route still known")
	}
}

func TestParseRouteIDRoundTripsAndRejectsOtherIDs(t *testing.T) {
	provider, upstream, ok := parseRouteID(routeID("p:1", "vendor/model:x"))
	if !ok || provider != "p:1" || upstream != "vendor/model:x" {
		t.Fatalf("round trip = %q %q %v", provider, upstream, ok)
	}
	for _, id := range []string{"gpt-6-luna", "provider:", "provider:abc", "provider::YQ", "provider:YQ:", "provider:!!:YQ"} {
		if _, _, ok := parseRouteID(id); ok {
			t.Fatalf("parsed %q", id)
		}
	}
}

// Start returns before discovery answers, reporting the source as
// discovering; a source that fails at startup is retried once.
func TestStartDiscoversInBackgroundAndRetriesFailures(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	var listings atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch listings.Add(1) {
		case 1:
			close(arrived)
			<-release
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			fmt.Fprint(w, `{"data":[{"id":"gpt-6-luna"}]}`)
		}
	}))
	defer server.Close()
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	row, err := database.CreateModelProvider(ctx, generated.CreateModelProviderParams{
		ProviderID: "acct", ProviderType: "openai", Endpoint: server.URL + "/v1", ApiKey: "k", DisplayName: "acct",
	})
	if err != nil {
		t.Fatal(err)
	}

	registry, _ := models.NewManager(&models.Config{})
	manager := New(database, registry, server.Client())
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-arrived
	if got := manager.connection(*row); got.State != "discovering" || got.Error != "" {
		t.Fatalf("before discovery answers: %+v, want discovering", got)
	}
	close(release)

	// The same startup path, run synchronously with no retry delay: its
	// discovery fails again and the retry succeeds.
	manager.discoverAtStartup(ctx, 0)
	if got := manager.connection(*row); got.State != "ready" || got.ModelCount != 1 || listings.Load() != 3 {
		t.Fatalf("after retry: %+v after %d listings, want ready with one model after 3", got, listings.Load())
	}
}
