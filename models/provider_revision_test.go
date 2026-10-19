package models

import (
	"testing"

	"shelley.exe.dev/models/modelsdev"
)

func TestProviderServiceRevisionTracksOnlyItsRoute(t *testing.T) {
	build := func(id, endpoint, key string, metadata *modelsdev.Metadata) Built {
		t.Helper()
		model, err := BuildProviderModel("account", id, "provider:account:"+id, id, "openai", endpoint, key, nil, metadata)
		if err != nil {
			t.Fatal(err)
		}
		return model
	}
	const endpoint = "https://api.openai.com/v1"
	current := build("gpt-6-luna", endpoint, "key", nil)
	manager, err := NewManager(&Config{})
	if err != nil {
		t.Fatal(err)
	}
	manager.SetProviderResolver(func(string) (Built, error) { return current, nil })
	revision := func() uint64 {
		t.Helper()
		_, source, revision, err := manager.GetServiceSnapshot(current.ID)
		if err != nil || source != "account" {
			t.Fatalf("snapshot source=%q err=%v", source, err)
		}
		return revision
	}
	beforeDiscovery := revision()
	manager.ReplaceProviderModels("account", []Built{current})
	if revision() != beforeDiscovery {
		t.Fatal("first catalog publication changed an identical resolved route")
	}
	for _, routes := range [][]Built{
		{build("gpt-6-luna", endpoint, "key", nil), build("gpt-6-astra", endpoint, "key", nil)},
		{build("gpt-6-luna", endpoint, "key", nil)},
	} {
		manager.ReplaceProviderModels("account", routes)
		if revision() != beforeDiscovery {
			t.Fatal("an unrelated model's addition/removal invalidated the current route")
		}
	}
	if err := manager.RefreshBuiltModels(nil); err != nil {
		t.Fatal(err)
	}
	if revision() != beforeDiscovery {
		t.Fatal("built-in refresh lost the saved route's service revision")
	}
	for _, changed := range []Built{
		build("gpt-6-luna", endpoint, "rotated-key", nil),
		build("gpt-6-luna", "https://proxy.example/v1", "key", nil),
		build("gpt-6-luna", endpoint, "key", &modelsdev.Metadata{Match: "exact", OutputLimit: 4096}),
	} {
		manager.ReplaceProviderModels("account", []Built{changed})
		if revision() == beforeDiscovery {
			t.Fatal("a changed route retained its service revision")
		}
	}
}
