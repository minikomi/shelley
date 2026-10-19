package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/modelproviders"
	"shelley.exe.dev/models"
)

type nopPublisher struct{}

func (nopPublisher) ReplaceProviderModels(string, []models.Built)           {}
func (nopPublisher) RelabelProviderModels(string, string)                   {}
func (nopPublisher) SetProviderResolver(func(string) (models.Built, error)) {}

// readModelsChanged consumes frames until one announces a catalog change.
func readModelsChanged(t *testing.T, scanner *bufio.Scanner) {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var frame StreamResponse
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame.ModelsChanged {
			if frame.ConversationID != "" || len(frame.Messages) != 0 {
				t.Fatalf("models_changed is not a bare global event: %+v", frame)
			}
			return
		}
	}
	t.Fatalf("missing models_changed frame: %v", scanner.Err())
}

func TestStreamAnnouncesCatalogOnConnectAndProviderChange(t *testing.T) {
	t.Parallel()
	svr, database, _ := newTestServer(t)
	var listing atomic.Value
	listing.Store(`{"data":[{"id":"fixture-model"}]}`)
	discovery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, listing.Load().(string))
	}))
	defer discovery.Close()
	if _, err := database.CreateModelProvider(context.Background(), generated.CreateModelProviderParams{
		ProviderID: "fixture", ProviderType: "openai", Endpoint: discovery.URL + "/v1", ApiKey: "fixture-key", DisplayName: "Fixture",
	}); err != nil {
		t.Fatal(err)
	}
	manager := modelproviders.New(database, nopPublisher{}, discovery.Client())
	svr.SetModelProviderManager(manager)
	// Connections announce the catalog once the manager has visible state.
	if _, err := manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	svr.RegisterRoutes(mux)
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)

	// Every connection of an install with provider state starts with an
	// invalidation, closing the gap between
	// the client's catalog fetch and its subscription.
	first, _ := openDiskSpaceStream(t, httpServer.URL+"/api/stream2")
	readModelsChanged(t, first)
	second, _ := openDiskSpaceStream(t, httpServer.URL+"/api/stream2")
	readModelsChanged(t, second)

	// A background mutation reaches every open stream.
	listing.Store(`{"data":[{"id":"fixture-model"},{"id":"fixture-model-2"}]}`)
	if _, err := manager.RefreshAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	readModelsChanged(t, first)
	readModelsChanged(t, second)
}

func TestModelProviderConflictMapsTo409(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeModelProviderError(rec, fmt.Errorf("edit: %w", modelproviders.ErrConflict))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
}
