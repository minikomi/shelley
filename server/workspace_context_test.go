package server

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"shelley.exe.dev/db"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm"
)

func TestPartitionMessagesWorkspace(t *testing.T) {
	for name, role := range map[string]conversationRole{"parent": roleTopLevel, "subagent": roleSubagent} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cm := &ConversationManager{role: role, logger: slog.Default()}
			var rows []generated.Message
			for _, item := range []struct {
				kind db.MessageType
				msg  llm.Message
			}{
				{db.MessageTypeSystem, llm.UserStringMessage("stable system prompt")},
				{db.MessageTypeAgent, llm.Message{Role: llm.MessageRoleAssistant, Content: []llm.Content{{Type: llm.ContentTypeText, Text: "existing history"}}}},
			} {
				data, err := json.Marshal(item.msg)
				if err != nil {
					t.Fatal(err)
				}
				text := string(data)
				rows = append(rows, generated.Message{Type: string(item.kind), LlmData: &text})
			}
			for _, cwd := range []string{"", t.TempDir(), t.TempDir()} {
				cm.mu.Lock()
				cm.cwd = cwd
				cm.mu.Unlock()
				history, system, err := cm.partitionMessages(rows)
				if err != nil {
					t.Fatal(err)
				}
				if len(history) != 2 || history[0].Role != llm.MessageRoleUser {
					t.Fatalf("history = %+v, want workspace followed by existing history", history)
				}
				want := "<workspace>\nWorkspace: not supplied. Use explicit paths.\n</workspace>"
				if cwd != "" {
					want = "<workspace>\nWorkspace: " + cwd + "\nDefault for relative paths and newly created helpers.\n</workspace>"
				}
				if got := messageText(history[0]); got != want {
					t.Fatalf("workspace = %q, want %q", got, want)
				}
				if got := messageText(history[1]); got != "existing history" {
					t.Fatalf("existing history changed: %q", got)
				}
				if len(system) != 1 || system[0].Text != "stable system prompt" {
					t.Fatalf("system prompt changed: %+v", system)
				}
				if len(rows) != 2 || strings.Contains(*rows[0].LlmData, "<workspace>") {
					t.Fatal("workspace context was persisted into the input rows")
				}
			}
		})
	}
}
