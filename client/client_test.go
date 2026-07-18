package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamUntilEndUsesCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("last_sequence_id"); got != "42" {
			t.Errorf("last_sequence_id = %q, want 42", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"conversation_id":"conversation","stream_delta":{"type":"text","text":"do","index":0,"seq":1}}`)
		fmt.Fprintln(w, `data: {"conversation_id":"conversation","tool_progress":{"tool_use_id":"tool-1","tool_name":"bash","output":"running"}}`)
		fmt.Fprintln(w, `data: {"conversation_id":"conversation","conversation_state":{"working":true}}`)
		fmt.Fprintln(w, `data: {"snapshot_complete":true}`)
		fmt.Fprintln(w, `data: {"conversation_id":"conversation","messages":[{"message_id":"message-1","conversation_id":"conversation","sequence_id":43,"type":"agent","llm_data":"{\"Role\":1,\"Content\":[{\"Type\":2,\"Text\":\"done\"}],\"EndOfTurn\":true}","usage_data":"{\"input_tokens\":10}","display_data":"{\"kind\":\"rich\"}","end_of_turn":true}]}`)
	}))
	defer server.Close()

	cc := &clientConfig{serverURL: server.URL, headers: map[string]string{}}
	httpClient, baseURL, err := cc.newHTTPClient()
	if err != nil {
		t.Fatal(err)
	}

	var events []streamEvent
	err = streamUntilEnd(cc, httpClient, baseURL, "conversation", 42, func(event streamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Event != "stream_delta" || events[0].StreamDelta.Text != "do" {
		t.Fatalf("events = %+v", events)
	}
	if events[1].Event != "tool_progress" || events[1].ToolProgress.Output != "running" {
		t.Fatalf("events = %+v", events)
	}
	message := events[2].Message
	if events[2].Event != "message" || message == nil || message.MessageID != "message-1" || message.Text != "done" || len(message.UsageData) == 0 || len(message.DisplayData) == 0 {
		t.Fatalf("events = %+v", events)
	}
	encoded, err := json.Marshal(events[2])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"llm"`) || strings.Contains(string(encoded), `"generation"`) {
		t.Fatalf("message is not lean: %s", encoded)
	}
	if events[3].Event != "done" || events[3].SequenceID != 43 {
		t.Fatalf("events = %+v", events)
	}
}

func TestStreamUntilEndEmitsTerminalErrorAndFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"conversation_id":"conversation","messages":[{"message_id":"message-1","sequence_id":43,"type":"error","llm_data":"{\"Role\":1,\"Content\":[{\"Type\":2,\"Text\":\"failed\"}],\"EndOfTurn\":true}","end_of_turn":true}]}`)
	}))
	defer server.Close()

	cc := &clientConfig{serverURL: server.URL, headers: map[string]string{}}
	httpClient, baseURL, err := cc.newHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	var events []streamEvent
	err = streamUntilEnd(cc, httpClient, baseURL, "conversation", 42, func(event streamEvent) error {
		events = append(events, event)
		return nil
	})
	if err == nil {
		t.Fatal("expected terminal error")
	}
	if len(events) != 2 || events[0].Event != "message" || events[0].Message.Text != "failed" || events[1].Event != "error" {
		t.Fatalf("events = %+v", events)
	}
}

func TestStreamUntilEndRejectsPrematureEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"messages":[{"sequence_id":43,"type":"agent","end_of_turn":false}]}`)
	}))
	defer server.Close()

	cc := &clientConfig{serverURL: server.URL, headers: map[string]string{}}
	httpClient, baseURL, err := cc.newHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	if err := streamUntilEnd(cc, httpClient, baseURL, "conversation", 42, nil); err == nil {
		t.Fatal("expected premature EOF error")
	}
}
