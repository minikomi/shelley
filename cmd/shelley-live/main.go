package main

import (
	"bufio"
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
	"os/exec"
	"path"
	"path/filepath"
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
	workspaceRoot    string
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
	Cwd                string `json:"cwd"`
}

type repositorySearchRequest struct {
	Terms []string `json:"terms"`
	Cwd   string   `json:"cwd"`
}

type repositoryReadRequest struct {
	Path      string `json:"path"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Cwd       string `json:"cwd"`
}

type repositorySelectRequest struct {
	Path string `json:"path"`
}

type sessionRequest struct {
	Cwd string `json:"cwd"`
}

func main() {
	cfg := config{
		addr:             envOr("LISTEN_ADDR", ":8765"),
		openAIBaseURL:    strings.TrimRight(envOr("OPENAI_BASE_URL", "https://openai.int.exe.xyz"), "/"),
		openAIAPIKey:     os.Getenv("OPENAI_API_KEY"),
		shelleyURL:       envOr("SHELLEY_URL", "unix:///home/exedev/.config/shelley/shelley.sock"),
		shelleyCWD:       envOr("SHELLEY_LIVE_CWD", "/home/exedev/shelley"),
		workspaceRoot:    envOr("SHELLEY_LIVE_ROOT", "/home/exedev"),
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
	mux.HandleFunc("GET /api/config", a.handleConfig)
	mux.HandleFunc("POST /api/session", a.handleSession)
	mux.HandleFunc("POST /api/repository/select", a.handleRepositorySelect)
	mux.HandleFunc("POST /api/repository/search", a.handleRepositorySearch)
	mux.HandleFunc("POST /api/repository/read", a.handleRepositoryRead)
	mux.HandleFunc("GET /api/conversations", a.handleConversations)
	mux.HandleFunc("GET /api/conversations/{id}", a.handleConversation)
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
	var input sessionRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	cwd := a.cfg.shelleyCWD
	if strings.TrimSpace(input.Cwd) != "" {
		var err error
		cwd, err = a.validateWorkspacePath(input.Cwd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	instructions := fmt.Sprintf(`You coordinate a live conversation with Shelley, an asynchronous coding agent.
The active working directory is %q. Never ask which project or directory to use unless the user explicitly wants to change it.
Be concise, practical, and decisive. Do not repeatedly paraphrase the request.
For any substantive codebase-specific request, start plan_with_shelley promptly so an asynchronous Shelley agent can inspect the repository and prior work while the conversation continues.
Use search_repository and read_repository_file only for quick follow-up facts while the Shelley agent runs.
Never merely promise to inspect, explore, or check the code. Perform a tool call.
Use list_shelley_conversations and read_shelley_conversation when earlier work or a current task may contain relevant decisions.
Ask a clarifying question only for a genuine product decision that cannot be inferred from the request, repository, or prior conversations.
When reasonable defaults exist, state the assumptions briefly and proceed.
When the user says "go for it", "build it", "implement it", "do it", or otherwise explicitly approves implementation, call build_with_shelley immediately. Do not ask another setup question.
Planning and builds run asynchronously. Tell the user when one starts, continue the conversation, and incorporate its findings when the task update arrives.
Use continue_shelley_job for follow-up instructions on the active task.
Never claim a Shelley task is complete until a task update says it completed.`, cwd)
	body := map[string]any{
		"session": map[string]any{
			"type":         "realtime",
			"model":        "gpt-realtime",
			"instructions": instructions,
			"audio": map[string]any{
				"input": map[string]any{
					"transcription":  map[string]any{"model": "gpt-4o-mini-transcribe"},
					"turn_detection": map[string]any{"type": "semantic_vad", "eagerness": "low"},
				},
				"output": map[string]any{"voice": "marin"},
			},
			"tools": []any{
				realtimeTool("set_working_directory", "Change the active project directory for repository inspection and future Shelley plan/build tasks.", map[string]any{
					"path": stringProperty("Absolute project directory under /home/exedev"),
				}, []string{"path"}),
				realtimeTool("search_repository", "Search the active repository for relevant symbols, phrases, routes, tests, or concepts. Use this proactively instead of asking the user where code lives.", map[string]any{
					"terms": map[string]any{
						"type":        "array",
						"description": "One to five short literal search terms, ordered from most important to least important",
						"items":       map[string]any{"type": "string"},
						"minItems":    1,
						"maxItems":    5,
					},
				}, []string{"terms"}),
				realtimeTool("read_repository_file", "Read a bounded line range from a repository file found with search_repository.", map[string]any{
					"path":       stringProperty("Repository-relative file path"),
					"start_line": map[string]any{"type": "integer", "description": "First line to read, starting at 1"},
					"end_line":   map[string]any{"type": "integer", "description": "Last line to read, inclusive; at most 200 lines"},
				}, []string{"path"}),
				realtimeTool("list_shelley_conversations", "List recent Shelley conversations with their directory, working state, and preview. Use this to find relevant prior work or current tasks.", map[string]any{}, []string{}),
				realtimeTool("read_shelley_conversation", "Read the recent user and agent messages from a Shelley conversation.", map[string]any{
					"conversation_id": stringProperty("Conversation ID returned by list_shelley_conversations"),
				}, []string{"conversation_id"}),
				realtimeTool("plan_with_shelley", "Start an asynchronous Shelley research and planning agent for a substantive codebase request. Call this early so it can inspect while the live conversation continues.", map[string]any{
					"goal":                stringProperty("What the user wants to understand or plan"),
					"details":             stringProperty("Relevant context and constraints"),
					"acceptance_criteria": stringProperty("How the user will judge the plan"),
				}, []string{"goal"}),
				realtimeTool("build_with_shelley", "Start or promote the active asynchronous Shelley task into implementation after explicit user approval.", map[string]any{
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

func (a *app) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"cwd":            a.cfg.shelleyCWD,
		"workspace_root": a.cfg.workspaceRoot,
	})
}

func (a *app) handleRepositorySelect(w http.ResponseWriter, r *http.Request) {
	var input repositorySelectRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	cwd, err := a.validateWorkspacePath(input.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cwd": cwd})
}

func (a *app) validateWorkspacePath(rawPath string) (string, error) {
	requested := strings.TrimSpace(rawPath)
	if requested == "" {
		requested = a.cfg.shelleyCWD
	}
	if !filepath.IsAbs(requested) {
		return "", errors.New("working directory must be an absolute path")
	}
	root, err := filepath.EvalSymlinks(a.cfg.workspaceRoot)
	if err != nil {
		return "", errors.New("workspace root is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return "", errors.New("working directory does not exist")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("working directory is not a directory")
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working directory must be under %s", root)
	}
	return resolved, nil
}

func (a *app) handleRepositorySearch(w http.ResponseWriter, r *http.Request) {
	var input repositorySearchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(input.Terms) == 0 || len(input.Terms) > 5 {
		http.Error(w, "terms must contain one to five values", http.StatusBadRequest)
		return
	}
	cwd, err := a.validateWorkspacePath(input.Cwd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var output strings.Builder
	for _, rawTerm := range input.Terms {
		term := strings.TrimSpace(rawTerm)
		if term == "" || len(term) > 120 {
			http.Error(w, "search terms must be between 1 and 120 characters", http.StatusBadRequest)
			return
		}
		cmd := exec.CommandContext(ctx, "rg",
			"--line-number",
			"--ignore-case",
			"--fixed-strings",
			"--max-count", "12",
			"--glob", "!ui/node_modules/**",
			"--glob", "!ui/dist/**",
			"--glob", "!.git/**",
			"--", term, cwd,
		)
		found, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				http.Error(w, "repository search failed: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
		fmt.Fprintf(&output, "## %s\n", term)
		if len(found) == 0 {
			output.WriteString("No matches.\n")
		} else {
			text := strings.ReplaceAll(string(found), cwd+string(filepath.Separator), "")
			output.WriteString(text)
			if !strings.HasSuffix(text, "\n") {
				output.WriteByte('\n')
			}
		}
		if output.Len() > 24<<10 {
			output.WriteString("\n[results truncated]\n")
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": output.String()})
}

func (a *app) handleRepositoryRead(w http.ResponseWriter, r *http.Request) {
	var input repositoryReadRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	relative := filepath.Clean(strings.TrimSpace(input.Path))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		http.Error(w, "path must be a repository-relative file", http.StatusBadRequest)
		return
	}
	cwd, err := a.validateWorkspacePath(input.Cwd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fullPath := filepath.Join(cwd, relative)
	resolvedRoot, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		http.Error(w, "working directory is unavailable", http.StatusInternalServerError)
		return
	}
	resolvedPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		http.Error(w, "path escapes the repository", http.StatusBadRequest)
		return
	}

	start := input.StartLine
	if start < 1 {
		start = 1
	}
	end := input.EndLine
	if end < start {
		end = start + 119
	}
	if end-start >= 200 {
		end = start + 199
	}
	file, err := os.Open(resolvedPath)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	var output strings.Builder
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	line := 0
	for scanner.Scan() {
		line++
		if line < start {
			continue
		}
		if line > end {
			break
		}
		fmt.Fprintf(&output, "%d: %s\n", line, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		http.Error(w, "file read failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       relative,
		"start_line": start,
		"end_line":   min(line, end),
		"content":    output.String(),
	})
}

func (a *app) handleConversations(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.shelleyBase+"/api/conversations/snapshot", nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := a.shelleyClient.Do(req)
	if err != nil {
		http.Error(w, "Shelley request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		copyResponse(w, resp, 1<<20)
		return
	}
	var snapshot struct {
		Conversations []map[string]any `json:"conversations"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&snapshot); err != nil {
		http.Error(w, "invalid Shelley response", http.StatusBadGateway)
		return
	}
	limit := min(20, len(snapshot.Conversations))
	compact := make([]map[string]any, 0, limit)
	for _, conversation := range snapshot.Conversations[:limit] {
		compact = append(compact, map[string]any{
			"conversation_id": conversation["conversation_id"],
			"slug":            conversation["slug"],
			"cwd":             conversation["cwd"],
			"updated_at":      conversation["updated_at"],
			"working":         conversation["agent_working"],
			"preview":         conversation["preview"],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": compact})
}

func (a *app) handleConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validConversationID(id) {
		http.Error(w, "invalid conversation id", http.StatusBadRequest)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.shelleyBase+"/api/conversation/"+id, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	resp, err := a.shelleyClient.Do(req)
	if err != nil {
		http.Error(w, "Shelley request failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		copyResponse(w, resp, 2<<20)
		return
	}
	var payload struct {
		Conversation map[string]any `json:"conversation"`
		Messages     []struct {
			Type        string  `json:"type"`
			DisplayData *string `json:"display_data"`
			LLMData     *string `json:"llm_data"`
			EndOfTurn   *bool   `json:"end_of_turn"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		http.Error(w, "invalid Shelley response", http.StatusBadGateway)
		return
	}
	start := max(0, len(payload.Messages)-24)
	messages := make([]map[string]any, 0, len(payload.Messages)-start)
	total := 0
	for _, message := range payload.Messages[start:] {
		if message.Type != "user" && message.Type != "agent" {
			continue
		}
		text := extractMessageText(message.DisplayData)
		if text == "" {
			text = extractMessageText(message.LLMData)
		}
		if text == "" {
			continue
		}
		if len(text) > 4000 {
			text = text[:4000] + "\n[message truncated]"
		}
		total += len(text)
		if total > 20<<10 {
			break
		}
		messages = append(messages, map[string]any{
			"role":        message.Type,
			"text":        text,
			"end_of_turn": message.EndOfTurn,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"conversation_id": id,
		"slug":            payload.Conversation["slug"],
		"cwd":             payload.Conversation["cwd"],
		"working":         payload.Conversation["agent_working"],
		"messages":        messages,
	})
}

func extractMessageText(raw *string) string {
	if raw == nil || *raw == "" {
		return ""
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return *raw
	}
	var found []string
	var visit func(any)
	visit = func(node any) {
		switch typed := node.(type) {
		case string:
			found = append(found, typed)
		case []any:
			for _, child := range typed {
				visit(child)
			}
		case map[string]any:
			if text, ok := typed["text"].(string); ok {
				found = append(found, text)
				return
			}
			if content, ok := typed["content"].(string); ok {
				found = append(found, content)
				return
			}
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
	return strings.Join(found, "\n")
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
		cwd, err := a.validateWorkspacePath(input.Cwd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		payload["cwd"] = cwd
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
