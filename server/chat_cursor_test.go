package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

func TestChatResponseIncludesMessageCursor(t *testing.T) {
	server, database, _ := newTestServer(t)
	conversation, err := database.CreateConversation(context.Background(), nil, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/conversation/"+conversation.ConversationID+"/chat", strings.NewReader(`{"message":"hello","model":"predictable"}`))
	w := httptest.NewRecorder()
	server.handleChatConversation(w, req, conversation.ConversationID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		ConversationID string `json:"conversation_id"`
		MessageID      string `json:"message_id"`
		SequenceID     int64  `json:"sequence_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ConversationID != conversation.ConversationID || response.MessageID == "" || response.SequenceID == 0 {
		t.Fatalf("response = %+v", response)
	}

	message, err := database.GetMessageByID(context.Background(), response.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if message.ConversationID != response.ConversationID || message.SequenceID != response.SequenceID || message.Type != string(db.MessageTypeUser) {
		t.Fatalf("message = %+v", message)
	}
}
