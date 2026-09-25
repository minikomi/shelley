package transcription

import "testing"

func TestTranscriptRoleDoesNotRequirePromptSupport(t *testing.T) {
	model := Model{}
	if !model.Supports(RoleTranscript) || model.Supports(RoleTimecoded) {
		t.Fatal("plain transcription depends on optional capabilities")
	}
	model.SupportsPrompted = true
	if !model.Supports(RoleTranscript) || model.Supports(RoleTimecoded) {
		t.Fatal("prompt support changed timestamp eligibility")
	}
	model.SupportsTimecodes = true
	if !model.Supports(RoleTranscript) || !model.Supports(RoleTimecoded) {
		t.Fatal("timestamp capability was not recognized")
	}
	if err := ValidateRole(RoleTranscript); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRole("prompted"); err == nil {
		t.Fatal("prompt capability was accepted as an output role")
	}
}

func TestModelRevisionPinsProtocolEndpointAndWireModel(t *testing.T) {
	base := Model{
		ID: "model", Protocol: ProtocolOpenAI, Provider: "openai",
		Endpoint: "https://api.example/v1/audio/transcriptions", Model: "gpt-transcribe",
	}
	if base.Ref().Revision == "" {
		t.Fatal("revision is empty")
	}
	for name, changed := range map[string]Model{
		"protocol": func() Model { model := base; model.Protocol = ProtocolDeepgram; return model }(),
		"endpoint": func() Model {
			model := base
			model.Endpoint = "https://other.example/v1/audio/transcriptions"
			return model
		}(),
		"wire model": func() Model { model := base; model.Model = "whisper-1"; return model }(),
	} {
		t.Run(name, func(t *testing.T) {
			if changed.Ref().Revision == base.Ref().Revision {
				t.Fatalf("revision did not change: %q", base.Ref().Revision)
			}
		})
	}
}
