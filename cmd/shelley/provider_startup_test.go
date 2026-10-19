package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/modelproviders"
	"shelley.exe.dev/models"
	"shelley.exe.dev/server"
)

type nopPublisher struct{}

func (nopPublisher) ReplaceProviderModels(string, []models.Built)           {}
func (nopPublisher) RelabelProviderModels(string, string)                   {}
func (nopPublisher) SetProviderResolver(func(string) (models.Built, error)) {}

// Startup discovery must announce its result to a stream opened before the
// result exists, and must stop when the serve context ends.
func TestStartSavedProvidersAnnouncesBackgroundDiscoveryAndHonorsCancellation(t *testing.T) {
	database, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	release := make(chan struct{})
	requested := make(chan struct{}, 2)
	var requests atomic.Int32
	canceled := make(chan struct{})
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- struct{}{}
		if requests.Add(1) == 1 {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"fixture-model"}]}`)
			return
		}
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(discovery.Close)
	if _, err := database.CreateModelProvider(t.Context(), generated.CreateModelProviderParams{
		ProviderID: "fixture", ProviderType: "openai", Endpoint: discovery.URL + "/v1", ApiKey: "fixture-key", DisplayName: "Fixture",
	}); err != nil {
		t.Fatal(err)
	}
	svr := server.NewServer(database, server.NewLLMServiceManager(&server.LLMConfig{Logger: slog.Default(), DB: database}),
		claudetool.ToolSetConfig{}, slog.Default(), false, "", "")
	manager := modelproviders.New(database, nopPublisher{}, discovery.Client())
	if err := startSavedProviders(ctx, svr, manager); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/stream2", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	nextModelsChanged := func() {
		t.Helper()
		for scanner.Scan() {
			if line, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				var frame server.StreamResponse
				if err := json.Unmarshal([]byte(line), &frame); err != nil {
					t.Fatal(err)
				}
				if frame.ModelsChanged {
					return
				}
			}
		}
		t.Fatalf("stream ended without models_changed: %v", scanner.Err())
	}
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("startup discovery never reached the fixture")
	}
	close(release)
	nextModelsChanged() // startup discovery result

	// A later refresh blocks on the fixture; ending the serve context cancels it.
	go manager.Refresh(ctx, "fixture")
	select {
	case <-requested:
	case <-ctx.Done():
		t.Fatal("refresh never reached the fixture")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("ending serve context did not cancel discovery")
	}
}
