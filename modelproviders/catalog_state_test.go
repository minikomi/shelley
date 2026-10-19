package modelproviders

import (
	"context"
	"testing"
)

func TestProviderCatalogStateTracksPublishedChangesIncludingDeletion(t *testing.T) {
	ctx := context.Background()
	g := newGatedServer(t, lunaOnly)
	m, _, _ := g.manager(t)
	notifications := 0
	m.SetOnChange(func() {
		notifications++
		if !m.HasCatalogState() {
			t.Error("notification must make catalog state visible before callbacks")
		}
	})
	if _, err := m.RefreshAll(ctx); err != nil {
		t.Fatal(err)
	}
	if m.HasCatalogState() || notifications != 0 {
		t.Fatal("no-provider startup must not invalidate legacy clients")
	}
	if _, err := m.Test(ctx, "", g.input("key", "Account")); err != nil {
		t.Fatal(err)
	}
	if m.HasCatalogState() || notifications != 0 {
		t.Fatal("testing a proposed connection must not publish catalog state")
	}
	created, err := m.Create(ctx, g.input("key", "Account"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.HasCatalogState() || notifications != 1 {
		t.Fatal("creating a provider must invalidate catalog state")
	}
	if err := m.Delete(ctx, created.ProviderID); err != nil {
		t.Fatal(err)
	}
	if !m.HasCatalogState() || notifications != 2 {
		t.Fatal("reconnect must invalidate the deleted last provider, too")
	}
}
