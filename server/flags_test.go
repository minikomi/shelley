package server

import (
	"testing"

	"shelley.exe.dev/featureflags"
)

func TestRemovedFlagsNotRegistered(t *testing.T) {
	for _, name := range []string{"tool-pills", "reflection-emoji-favicon", "patch-openai-raw"} {
		if _, ok := featureflags.Lookup(name); ok {
			t.Errorf("removed flag %q is still registered", name)
		}
	}
}

func TestFlagCompactSendThresholdsRegisteredOff(t *testing.T) {
	f, ok := featureflags.Lookup("compact-send-thresholds")
	if !ok {
		t.Fatal("compact-send-thresholds not registered")
	}
	if f.Default != false {
		t.Fatalf("default = %v, want false", f.Default)
	}
}

func TestPatchSimpleRegisteredOff(t *testing.T) {
	flag, ok := featureflags.Lookup("patch-simple")
	if !ok {
		t.Fatal("patch-simple not registered")
	}
	if flag.Default != false {
		t.Fatalf("patch-simple default = %v, want false", flag.Default)
	}
}
