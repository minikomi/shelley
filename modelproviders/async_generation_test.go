package modelproviders

import (
	"context"
	"errors"
	"testing"
)

func TestConfigurationRoundTripInvalidatesPendingOperations(t *testing.T) {
	for _, operation := range []string{"refresh", "edit"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			g := newGatedServer(t, lunaOnly)
			m, registry, _ := g.manager(t)
			created, err := m.Create(ctx, g.input("original", "account"))
			if err != nil {
				t.Fatal(err)
			}
			key := "original"
			if operation == "edit" {
				key = "pending"
			}
			h := g.hold(key)
			defer close(h.release)
			done := make(chan refreshResult, 1)
			go func() {
				var res refreshResult
				if operation == "refresh" {
					res.conn, res.err = m.Refresh(ctx, created.ProviderID)
				} else {
					res.conn, res.err = m.Update(ctx, created.ProviderID, g.input(key, "account"))
				}
				done <- res
			}()
			recv(t, h.entered)
			g.unhold(key)
			g.respond(0, lunaAndSol)
			for _, key := range []string{"temporary", "original"} {
				res := promptly(t, func() refreshResult {
					conn, err := m.Update(ctx, created.ProviderID, g.input(key, "account"))
					return refreshResult{conn, err}
				})
				if res.err != nil {
					t.Fatal(res.err)
				}
			}
			revision := registry.ProviderCatalogRevision(created.ProviderID)
			// Release without double-closing the deferred cleanup channel.
			h.release <- struct{}{}
			res := recv(t, done)
			if operation == "edit" && !errors.Is(res.err, ErrConflict) {
				t.Fatalf("pending edit after configuration round trip: %v", res.err)
			}
			if operation == "refresh" && res.err != nil {
				t.Fatal(res.err)
			}
			if got := registry.ProviderCatalogRevision(created.ProviderID); got != revision {
				t.Fatalf("stale %s republished catalog: revision %d, want %d", operation, got, revision)
			}
		})
	}
}
