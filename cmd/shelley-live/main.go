package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

type config struct {
	addr             string
	openAIBaseURL    string
	openAIAPIKey     string
	shelleyURL       string
	shelleyCWD       string
	shelleyPublicURL string
	userEmail        string
}

type app struct {
	cfg           config
	openAIClient  *http.Client
	shelleyClient *http.Client
	shelleyBase   string
}

type jobRequest struct {
	Kind               string `json:"kind"`
	Goal               string `json:"goal"`
	Details            string `json:"details"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	ConversationID     string `json:"conversation_id"`
}

func main() {
	cfg := config{
		addr:             envOr("LISTEN_ADDR", ":8765"),
		openAIBaseURL:    strings.TrimRight(envOr("OPENAI_BASE_URL", "https://openai.int.exe.xyz"), "/"),
		openAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		shelleyURL:       envOr("SHELLEY_URL", "unix:///home/exedev/.config/shelley/shelley.sock"),
		shelleyCWD:       envOr("SHELLEY_LIVE_CWD", "/home/exedev/shelley"),
		shelleyPublicURL: strings.TrimRight(envOr("SHELLEY_PUBLIC_URL", "https://exedevwork.exe.xyz"), "/"),
		userEmail:        envOr("SHELLEY_USER_EMAIL", "adam@poyo.co"),
	}
	a, err := newApp(cfg)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              cfg.addr,
		Handler:           a.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	log.Printf("Shelley Live listening on %s", cfg.addr)
	log.Fatal(server.ListenAndServe())
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func newApp(cfg config) (*app, error) {
	client, base, err := newShelleyClient(cfg.shelleyURL)
	if err != nil {
		return nil, err
	}
	return &app{
		cfg:           cfg,
		openAIClient:  &http.Client{Timeout: 30 * time.Second},
		shelleyClient: client,
		shelleyBase:   base,
	}, nil
}

func newShelleyClient(rawURL string) (*http.Client, string, error) {
	if socket, ok := strings.CutPrefix(rawURL, "unix://"); ok {
		if socket == "" {
			return nil, "", errors.New("SHELLEY_URL unix socket path is empty")
		}
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}
		return &http.Client{Transport: transport}, "http://shelley", nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, "", fmt.Errorf("invalid SHELLEY_URL %q", rawURL)
	}
	return &http.Client{}, strings.TrimRight(rawURL, "/"), nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", a.handleStatic)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/session", a.handleSession)
	mux.HandleFunc("POST /api/jobs", a.handleJob)
	mux.HandleFunc("GET /api/jobs/{id}/events", a.handleJobEvents)
	return mux
}

func (a *app) handleStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	data, err := staticFiles.ReadFile("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Write(data)
}

func (a *app) handleSession(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"session": map[string]any{
			"type":  "realtime",
			"model": "gpt-realtime",
			"instructions": `You are the conversational front door to Shelley, a coding agent.
Be concise and practical. Help the user talk through ideas, narrow scope, and define acceptance criteria.
Use plan_with_shelley when the user wants repository-aware investigation or a written plan.
Use build_with_shelley only after the user clearly asks to implement, build, fix, or change something.
Builds run asynchronously. After starting one, tell the user it is running and continue the conversation.
Use continue_shelley_job for follow-up instructions on the active task.
Never claim a Shelley task is complete until a task update says it completed.`,
			"audio": map[string]any{
				"input": map[string]any{
					"transcription":  map[string]any{"model": "gpt-4o-mini-transcribe"},
					"turn_detection": map[string]any{"type": "semantic_vad"},
				},
				"output": map[string]any{"voice": "marin"},
			},
			"tools": []any{
				realtimeTool("plan_with_shelley", "Ask Shelley to inspect the repository and create a plan without editing files.", map[string]any{
					"goal":                stringProperty("What the user wants to understand or plan"),
					"details":             stringProperty("Relevant context and constraints"),
					"acceptance_criteria": stringProperty("How the user will judge the plan"),
				}, []string{"goal"}),
				realtimeTool("build_with_shelley", "Start an asynchronous Shelley implementation task after the user explicitly asks to build or change something.", map[string]any{
					"goal":                stringProperty("The concrete change to implement"),
					"details":             stringProperty("Relevant context and constraints"),
					"acceptance_criteria": stringProperty("Objective completion checks"),
				}, []string{"goal"}),
				realtimeTool("continue_shelley_job", "Send a follow-up instruction to the active Shelley task.", map[string]any{
					"message": stringProperty("The follow-up instruction"),
				}, []string{"message"}),
			},
			"tool_choice": "auto",
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.cfg.openAIBaseURL+"/v1/realtime/client_secrets", bytes.NewReader(encoded))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if a.cfg.openAIAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.cfg.openAIAPIKey)
	}
	resp, err := a.openAIClient.Do(req)
	if err != nil {
		http.Error(w, "OpenAI session request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp, 2<<20)
}

func realtimeTool(name, description string, properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":        "function",
		"name":        name,
		"description": description,
		"parameters": map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             required,
			"additionalProperties": false,
		},
	}
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func (a *app) handleJob(w http.ResponseWriter, r *http.Request) {
	var input jobRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Kind = strings.TrimSpace(input.Kind)
	input.Goal = strings.TrimSpace(input.Goal)
	if input.Kind != "plan" && input.Kind != "build" && input.Kind != "followup" {
		http.Error(w, "kind must be plan, build, or followup", http.StatusBadRequest)
		return
	}
	if input.Goal == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}
	if input.Kind == "followup" && !validConversationID(input.ConversationID) {
		http.Error(w, "a valid conversation_id is required for followup", http.StatusBadRequest)
		return
	}

	payload := map[string]any{
		"message": buildPrompt(input),
	}
	endpoint := a.shelleyBase + "/api/conversations/new"
	if input.ConversationID == "" {
		payload["cwd"] = a.cfg.shelleyCWD
		payload["conversation_options"] = map[string]any{"disable_notifications": false}
	} else {
		endpoint = a.shelleyBase + "/api/conversation/" + input.ConversationID + "/chat"
		payload["queue"] = true
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Shelley-Request", "1")
	req.Header.Set("X-ExeDev-Email", a.cfg.userEmail)
	resp, err := a.shelleyClient.Do(req)
	if err != nil {
		http.Error(w, "Shelley request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyResponse(w, resp, 1<<20)
		return
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		http.Error(w, "invalid Shelley response", http.StatusBadGateway)
		return
	}
	conversationID := input.ConversationID
	if id, ok := result["conversation_id"].(string); ok && id != "" {
		conversationID = id
	}
	result["conversation_id"] = conversationID
	result["kind"] = input.Kind
	result["shelley_url"] = a.cfg.shelleyPublicURL + "/c/" + conversationID
	writeJSON(w, http.StatusAccepted, result)
}

func buildPrompt(input jobRequest) string {
	var intro string
	switch input.Kind {
	case "plan":
		intro = "PLAN ONLY. Inspect the repository and produce a concrete implementation plan. Do not edit files, run destructive commands, or commit."
	case "build":
		intro = "Implement this request asynchronously. Keep the patch focused, validate it, and commit the completed change before reporting back."
	default:
		return input.Goal
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nGoal:\n%s", intro, input.Goal)
	if strings.TrimSpace(input.Details) != "" {
		fmt.Fprintf(&b, "\n\nContext and constraints:\n%s", strings.TrimSpace(input.Details))
	}
	if strings.TrimSpace(input.AcceptanceCriteria) != "" {
		fmt.Fprintf(&b, "\n\nAcceptance criteria:\n%s", strings.TrimSpace(input.AcceptanceCriteria))
	}
	b.WriteString("\n\nReport objective results, validation performed, and any remaining blocker.")
	return b.String()
}

func validConversationID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (a *app) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validConversationID(id) {
		http.Error(w, "invalid conversation id", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.shelleyBase+"/api/conversation/"+id+"/stream", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.shelleyClient.Do(req)
	if err != nil {
		http.Error(w, "Shelley stream failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		copyResponse(w, resp, 1<<20)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			http.NewResponseController(w).Flush()
		}
		if readErr != nil {
			return
		}
	}
}

func copyResponse(w http.ResponseWriter, resp *http.Response, limit int64) {
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.LimitReader(resp.Body, limit))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
