package server

import (
	"net/http"
	"strings"
	"testing"

	"shelley.exe.dev/db"
)

func (e *liveMessageEnv) progressEnabled(conversationID, session string) bool {
	e.t.Helper()
	c := e.s.liveCalls.get(conversationID, session)
	if c == nil {
		e.t.Fatal("no call")
	}
	var enabled bool
	c.locked(func() { enabled = c.progressEnabled })
	return enabled
}

func TestLivePreferences(t *testing.T) {
	t.Parallel()
	e := newLiveMessageEnv(t)
	e.liveProvider()
	if code, body := e.do("GET", "/api/live-settings", ""); code != 200 || body != `{"voice":"marin","speaking_pace":0,"progress_updates":false}` {
		t.Fatalf("defaults: %d %s", code, body)
	}
	for _, bad := range []string{
		`{}`,
		`{"voice":"robot","speaking_pace":0,"progress_updates":true}`,
		`{"voice":"marin","speaking_pace":3,"progress_updates":true}`,
		`{"voice":"marin","speaking_pace":0,"progress_updates":15}`,
		`{"voice":"marin","speaking_pace":0,"progress_updates":null}`,
	} {
		if code, _ := e.do("PUT", "/api/live-settings", bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, code)
		}
	}
	if _, body := e.do("GET", "/api/live-settings", ""); body != `{"voice":"marin","speaking_pace":0,"progress_updates":false}` {
		t.Fatalf("invalid PUT changed settings: %s", body)
	}
	slug := "voice-settings"
	conv, err := e.database.CreateConversation(t.Context(), &slug, true, nil, nil, db.ConversationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	call := e.startCall(conv.ConversationID, "")
	if every := e.progressEnabled(conv.ConversationID, call.session); every {
		t.Errorf("progress enabled %v before saving", every)
	}
	// Saved settings apply only to the next call.
	if code, body := e.do("PUT", "/api/live-settings", `{"voice":"cinder","speaking_pace":-1,"progress_updates":true}`); code != 200 || body != `{"voice":"cinder","speaking_pace":-1,"progress_updates":true}` {
		t.Fatalf("save: %d %s", code, body)
	}
	if every := e.progressEnabled(conv.ConversationID, call.session); every {
		t.Errorf("progress enabled %v after saving", every)
	}
	if _, body := e.do("GET", "/api/live-settings", ""); body != `{"voice":"cinder","speaking_pace":-1,"progress_updates":true}` {
		t.Errorf("saved: %s", body)
	}
	e.do("DELETE", call.path, "")
	<-call.ended
	next := e.startCall(conv.ConversationID, "")
	if every := e.progressEnabled(conv.ConversationID, next.session); !every {
		t.Errorf("next call progress enabled = %v", every)
	}
	e.do("DELETE", next.path, "")
	<-next.ended
}

func TestLivePreferencesInvalidStoredSettings(t *testing.T) {
	t.Parallel()
	for _, stored := range []string{
		`{"voice":"removed-voice","speaking_pace":0,"progress_updates":true}`,
		`{"voice":"","speaking_pace":0,"progress_updates":true}`,
		`{"voice":"marin","speaking_pace":3,"progress_updates":true}`,
	} {
		t.Run(stored, func(t *testing.T) {
			e := newLiveMessageEnv(t)
			if err := e.database.SetSetting(t.Context(), livePreferencesKey, stored); err != nil {
				t.Fatal(err)
			}
			// Invalid stored values fail explicitly; never silently substitute preferences.
			if code, body := e.do("GET", "/api/live-settings", ""); code != http.StatusInternalServerError {
				t.Fatalf("load invalid settings: %d %s", code, body)
			}
			if code, _ := e.do("GET", "/api/live-settings/options", ""); code != http.StatusOK {
				t.Fatalf("options: %d", code)
			}
			// Call setup must reject invalid values rather than send them to the provider.
			if _, err := e.s.getLivePreferences(t.Context()); err == nil || !strings.Contains(err.Error(), "stored Live settings: invalid Live") {
				t.Fatalf("call settings error: %v", err)
			}
			valid := `{"voice":"cinder","speaking_pace":-1,"progress_updates":true}`
			if code, body := e.do("PUT", "/api/live-settings", valid); code != http.StatusOK || body != valid {
				t.Fatalf("repair: %d %s", code, body)
			}
			if p, err := e.s.getLivePreferences(t.Context()); err != nil || p.Voice != "cinder" || p.SpeakingPace != -1 || !p.ProgressUpdates {
				t.Fatalf("repaired call settings: %+v, %v", p, err)
			}
		})
	}
}
