package server

import (
	"strings"
	"testing"
	"unicode/utf8"

	"shelley.exe.dev/llm"
)

func TestBuildTranscriptionPromptUsesSafeRecentContext(t *testing.T) {
	messages := []llm.Message{
		llm.UserStringMessage("Work on Shelley and exe.dev.\n```sh\nexport API_KEY=hidden\n```\n[/tmp/screenshot.png]"),
		{
			Role: llm.MessageRoleAssistant,
			Content: []llm.Content{
				{Type: llm.ContentTypeText, Text: "Update RecordingPanel.vue on minikomi-faster-recording."},
				{Type: llm.ContentTypeToolUse, ToolName: "bash"},
			},
		},
		llm.UserStringMessage("Authorization: Bearer hidden"),
		llm.UserStringMessage("The current feature is transcription context."),
	}

	prompt := buildTranscriptionPrompt(
		"minikomi-faster-recording",
		"/home/exedev/shelley",
		"transcription-testing",
		messages,
	)

	for _, expected := range []string{
		"dictating a request or instruction to Shelley",
		"Application: Shelley",
		"Platform: exe.dev",
		"VM: minikomi-faster-recording",
		"Project: shelley",
		"Working directory: /home/exedev/shelley",
		"Conversation: transcription-testing",
		"User: Work on Shelley and exe.dev.",
		"Assistant: Update RecordingPanel.vue on minikomi-faster-recording.",
		"User: The current feature is transcription context.",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt missing %q:\n%s", expected, prompt)
		}
	}
	for _, excluded := range []string{
		"API_KEY",
		"screenshot.png",
		"Authorization",
		"Bearer hidden",
		"ToolName",
	} {
		if strings.Contains(prompt, excluded) {
			t.Errorf("prompt contains %q:\n%s", excluded, prompt)
		}
	}
}

func TestBuildTranscriptionPromptIsCapped(t *testing.T) {
	messages := make([]llm.Message, 10)
	for index := range messages {
		messages[index] = llm.UserStringMessage(strings.Repeat("context ", 200))
	}
	prompt := buildTranscriptionPrompt(
		strings.Repeat("vm", 200),
		"/home/exedev/"+strings.Repeat("project", 100),
		strings.Repeat("conversation", 100),
		messages,
	)
	if got := utf8.RuneCountInString(prompt); got > transcriptionPromptMaxRunes {
		t.Fatalf("prompt runes = %d", got)
	}
}
