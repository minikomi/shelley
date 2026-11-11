package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestStreamingTranscription(t *testing.T) {
	for _, failure := range []string{"", "provider error", "wrong item", "empty", "invalid audio"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("missing provider credential")
				}
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.CloseNow()
				var update struct {
					Type    string `json:"type"`
					Session struct {
						Type  string `json:"type"`
						Audio struct {
							Input struct {
								Format struct {
									Rate int `json:"rate"`
								} `json:"format"`
								Transcription struct {
									Model string `json:"model"`
									Delay string `json:"delay"`
								} `json:"transcription"`
								TurnDetection any `json:"turn_detection"`
							} `json:"input"`
						} `json:"audio"`
					} `json:"session"`
				}
				if err := wsjson.Read(ctx, c, &update); err != nil {
					t.Error(err)
					return
				}
				if update.Type != "session.update" || update.Session.Type != "transcription" || update.Session.Audio.Input.Format.Rate != 24000 || update.Session.Audio.Input.Transcription.Model != streamingTranscriptionModel || update.Session.Audio.Input.Transcription.Delay != "low" || update.Session.Audio.Input.TurnDetection != nil {
					t.Errorf("unexpected update: %+v", update)
				}
				if err := wsjson.Write(ctx, c, map[string]string{"type": "session.updated"}); err != nil {
					t.Error(err)
					return
				}
				var audio struct {
					Type  string `json:"type"`
					Audio string `json:"audio"`
				}
				if err := wsjson.Read(ctx, c, &audio); err != nil {
					if failure != "invalid audio" {
						t.Error(err)
					}
					return
				}
				decoded, err := base64.StdEncoding.DecodeString(audio.Audio)
				if err != nil || len(decoded) != 4800 || audio.Type != "input_audio_buffer.append" {
					t.Errorf("invalid forwarded audio: %d %v", len(decoded), err)
					return
				}
				// Partial text must arrive before the browser sends commit.
				_ = wsjson.Write(ctx, c, map[string]string{"type": "conversation.item.input_audio_transcription.delta", "delta": "hello"})
				var commit map[string]string
				if err := wsjson.Read(ctx, c, &commit); err != nil {
					t.Error(err)
					return
				}
				if commit["type"] != "input_audio_buffer.commit" {
					t.Errorf("commit = %v", commit)
				}
				if failure == "provider error" {
					_ = wsjson.Write(ctx, c, map[string]any{"type": "error", "error": map[string]string{"message": "quota exhausted"}})
				} else {
					_ = wsjson.Write(ctx, c, map[string]string{"type": "input_audio_buffer.committed", "item_id": "turn-1"})
					id, text := "turn-1", "hello world"
					if failure == "wrong item" {
						id = "turn-2"
					}
					if failure == "empty" {
						text = " "
					}
					_ = wsjson.Write(ctx, c, map[string]string{"type": "conversation.item.input_audio_transcription.completed", "item_id": id, "transcript": text})
				}
				_, _, _ = c.Read(ctx)
			}))
			defer provider.Close()
			finished := make(chan error, 1)
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.CloseNow()
				err = streamTranscription(ctx, c, "ws"+strings.TrimPrefix(provider.URL, "http"), "test-key", "Shelley")
				finished <- err
			}))
			defer relay.Close()
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(relay.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			var event map[string]string
			if err := wsjson.Read(ctx, c, &event); err != nil || event["type"] != "ready" {
				t.Fatalf("ready: %v %v", event, err)
			}
			audio := make([]byte, 4800)
			if failure == "invalid audio" {
				audio = audio[:3]
			}
			if err := c.Write(ctx, websocket.MessageBinary, audio); err != nil {
				t.Fatal(err)
			}
			if failure != "invalid audio" {
				if err := wsjson.Read(ctx, c, &event); err != nil || event["text"] != "hello" {
					t.Fatalf("partial before commit: %v %v", event, err)
				}
				if err := wsjson.Write(ctx, c, map[string]string{"type": "commit"}); err != nil {
					t.Fatal(err)
				}
				if failure == "" {
					if err := wsjson.Read(ctx, c, &event); err != nil || event["type"] != "completed" || event["text"] != "hello world" {
						t.Fatalf("final: %v %v", event, err)
					}
				}
			}
			if failure != "" {
				if err := wsjson.Read(ctx, c, &event); err != nil || event["type"] != "error" || event["message"] == "" {
					t.Fatalf("error event: %v %v", event, err)
				}
			}
			select {
			case err := <-finished:
				if (err != nil) != (failure != "") {
					t.Fatalf("result: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestStreamingTranscriptionRejectsProviderHTTPError(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no credentials", http.StatusUnauthorized) }))
	defer provider.Close()
	err := streamTranscription(t.Context(), nil, "ws"+strings.TrimPrefix(provider.URL, "http"), "", "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamingTranscriptionCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	disconnected := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		var config json.RawMessage
		if err := wsjson.Read(ctx, c, &config); err != nil {
			t.Error(err)
			return
		}
		_ = wsjson.Write(ctx, c, map[string]string{"type": "session.updated"})
		_, _, _ = c.Read(ctx)
		close(disconnected)
	}))
	defer provider.Close()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		_ = streamTranscription(ctx, c, "ws"+strings.TrimPrefix(provider.URL, "http"), "", "")
	}))
	defer relay.Close()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(relay.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = c.Read(ctx)
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	c.CloseNow()
	select {
	case <-disconnected:
	case <-ctx.Done():
		t.Fatal("provider stayed connected after cancellation")
	}
}
