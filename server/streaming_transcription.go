package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"golang.org/x/sync/errgroup"
)

const streamingTranscriptionModel = "gpt-live-transcribe"

// The browser sends mono PCM16 at 24 kHz, followed by a single commit.
// Credentials and session configuration stay on the server.
func (s *Server) handleStreamingTranscription(w http.ResponseWriter, r *http.Request) {
	if s.streamingTranscriptionURL == "" {
		http.Error(w, "Streaming transcription is not configured", http.StatusServiceUnavailable)
		return
	}
	prompt, err := s.transcriptionPrompt(r.Context(), r.PathValue("id"), nil)
	if err != nil {
		http.Error(w, "Cannot load transcription conversation", http.StatusNotFound)
		return
	}
	client, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer client.CloseNow()
	ctx, cancel := context.WithTimeout(r.Context(), transcriptionTimeout)
	defer cancel()
	if err := streamTranscription(ctx, client, s.streamingTranscriptionURL, s.streamingTranscriptionKey, prompt); err != nil {
		s.logger.Error("Streaming transcription failed", "error", err)
		_ = wsjson.Write(ctx, client, map[string]string{"type": "error", "message": err.Error()})
		_ = client.Close(websocket.StatusInternalError, "Transcription failed")
		return
	}
	_ = client.Close(websocket.StatusNormalClosure, "Transcription complete")
}

func streamTranscription(ctx context.Context, client *websocket.Conn, endpoint, key, prompt string) error {
	setupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	header := http.Header{}
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}
	upstream, response, err := websocket.Dial(setupCtx, endpoint, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if response != nil {
			return fmt.Errorf("Realtime transcription connection rejected (HTTP %d)", response.StatusCode)
		}
		return fmt.Errorf("connect realtime transcription: %w", err)
	}
	defer upstream.CloseNow()
	upstream.SetReadLimit(1 << 20)
	client.SetReadLimit(64 << 10)
	if err := wsjson.Write(setupCtx, upstream, map[string]any{
		"type": "session.update", "session": map[string]any{
			"type": "transcription", "audio": map[string]any{"input": map[string]any{
				"format":         map[string]any{"type": "audio/pcm", "rate": 24000},
				"transcription":  map[string]any{"model": streamingTranscriptionModel, "prompt": prompt, "delay": "low"},
				"turn_detection": nil,
			}},
		},
	}); err != nil {
		return err
	}
	for {
		var event transcriptionEvent
		if err := wsjson.Read(setupCtx, upstream, &event); err != nil {
			return fmt.Errorf("configure realtime transcription: %w", err)
		}
		if event.Type == "error" {
			return errors.New(event.Error.Message)
		}
		if event.Type == "session.updated" {
			break
		}
	}
	cancel()
	if err := wsjson.Write(ctx, client, map[string]string{"type": "ready"}); err != nil {
		return err
	}
	group, ctx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		defer func() {
			if err != nil && !errors.Is(err, errTranscriptionComplete) && ctx.Err() == nil {
				_ = wsjson.Write(ctx, client, map[string]string{"type": "error", "message": err.Error()})
			}
		}()
		var samples int
		for {
			kind, data, err := client.Read(ctx)
			if err != nil {
				return err
			}
			if kind == websocket.MessageBinary {
				if len(data) == 0 || len(data)%2 != 0 {
					return errors.New("invalid PCM audio chunk")
				}
				samples += len(data) / 2
				if err := wsjson.Write(ctx, upstream, map[string]string{"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(data)}); err != nil {
					return err
				}
				continue
			}
			var event struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &event) != nil || event.Type != "commit" {
				return errors.New("expected audio or commit")
			}
			if samples < 2400 {
				return errors.New("record at least 100 ms of audio")
			}
			if err := wsjson.Write(ctx, upstream, map[string]string{"type": "input_audio_buffer.commit"}); err != nil {
				return err
			}
			// Keep reading for cancellation while the final transcript is pending.
			_, _, err = client.Read(ctx)
			if err == nil {
				return errors.New("audio received after commit")
			}
			return err
		}
	})
	// Completing the upstream reader cancels the client reader, too.
	var completed bool
	group.Go(func() (err error) {
		defer func() {
			if err != nil && !errors.Is(err, errTranscriptionComplete) && ctx.Err() == nil {
				_ = wsjson.Write(ctx, client, map[string]string{"type": "error", "message": err.Error()})
			}
		}()
		itemID := ""
		for {
			var event transcriptionEvent
			if err := wsjson.Read(ctx, upstream, &event); err != nil {
				return err
			}
			switch event.Type {
			case "error", "conversation.item.input_audio_transcription.failed":
				return fmt.Errorf("transcription failed: %s", event.Error.Message)
			case "input_audio_buffer.committed":
				itemID = event.ItemID
			case "conversation.item.input_audio_transcription.delta":
				if err := wsjson.Write(ctx, client, map[string]string{"type": "delta", "text": event.Delta}); err != nil {
					return err
				}
			case "conversation.item.input_audio_transcription.completed":
				if itemID == "" || event.ItemID != itemID {
					return errors.New("unexpected transcription item")
				}
				text := strings.TrimSpace(event.Transcript)
				if text == "" {
					return errors.New("No speech was detected")
				}
				if err := wsjson.Write(ctx, client, map[string]string{"type": "completed", "text": text}); err != nil {
					return err
				}
				completed = true
				return errTranscriptionComplete
			}
		}
	})
	err = group.Wait()
	if completed {
		return nil
	}
	return err
}

var errTranscriptionComplete = errors.New("transcription complete")

type transcriptionEvent struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Error      struct {
		Message string `json:"message"`
	} `json:"error"`
}
