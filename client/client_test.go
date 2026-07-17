package client

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamUntilEndUsesCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("last_sequence_id"); got != "42" {
			t.Errorf("last_sequence_id = %q, want 42", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"messages":[{"sequence_id":43,"type":"agent","llm_data":"{\"Content\":[{\"Type\":2,\"Text\":\"done\"}]}","end_of_turn":true}]}`)
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
	if len(events) != 1 || events[0].SequenceID != 43 || events[0].Text != "done" || !events[0].EndOfTurn {
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
