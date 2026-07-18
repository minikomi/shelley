// Package client implements the experimental Shelley CLI client.
// It communicates with a running Shelley server over a Unix socket or HTTP.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"shelley.exe.dev/llm"
)

// DefaultSocketPath returns the default Unix socket path (~/.config/shelley/shelley.sock).
func DefaultSocketPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "shelley", "shelley.sock")
}

func defaultClientURL() string {
	return "unix://" + DefaultSocketPath()
}

func parseClientURL(rawURL string) (scheme, address string, err error) {
	if sockPath, ok := strings.CutPrefix(rawURL, "unix://"); ok {
		if sockPath == "" {
			return "", "", fmt.Errorf("unix:// URL must include a socket path")
		}
		return "unix", sockPath, nil
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return strings.SplitN(rawURL, "://", 2)[0], rawURL, nil
	}
	return "", "", fmt.Errorf("unsupported URL scheme: %s (use unix://, http://, or https://)", rawURL)
}

type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ", ") }

func (f *multiFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type clientConfig struct {
	serverURL string
	headers   map[string]string
}

func (cc *clientConfig) newHTTPClient() (*http.Client, string, error) {
	scheme, address, err := parseClientURL(cc.serverURL)
	if err != nil {
		return nil, "", err
	}

	switch scheme {
	case "unix":
		transport := &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", address)
			},
		}
		return &http.Client{Transport: transport}, "http://localhost", nil
	case "http", "https":
		return &http.Client{}, address, nil
	default:
		return nil, "", fmt.Errorf("unsupported scheme: %s", scheme)
	}
}

func (cc *clientConfig) newRequest(method, url string, body *strings.Reader) (*http.Request, error) {
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, url, body)
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		req.Header.Set("X-Shelley-Request", "1")
	}
	for k, v := range cc.headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

// Run is the entry point for "shelley client [args...]".
func Run(args []string) {
	fs := flag.NewFlagSet("client", flag.ExitOnError)
	urlFlag := fs.String("url", defaultClientURL(), "Server URL (unix:///path, http://host:port, https://host:port)")
	var headerFlags multiFlag
	fs.Var(&headerFlags, "H", `Extra HTTP header ("Name: Value", can be repeated)`)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "EXPERIMENTAL: Shelley CLI client\n\n")
		fmt.Fprintf(fs.Output(), "Usage: shelley client [flags] <subcommand> [args...]\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), "\nSubcommands:\n")
		fmt.Fprintf(fs.Output(), "  chat     Send a message (new or existing conversation)\n")
		fmt.Fprintf(fs.Output(), "  read     Read conversation messages\n")
		fmt.Fprintf(fs.Output(), "  list     List conversations\n")
		fmt.Fprintf(fs.Output(), "  search   Search conversations by content\n")
		fmt.Fprintf(fs.Output(), "  archive  Archive a conversation\n")
		fmt.Fprintf(fs.Output(), "  help     Print detailed help\n")
	}
	fs.Parse(args)

	headers := make(map[string]string)
	for _, h := range headerFlags {
		parts := strings.SplitN(h, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Error: invalid header %q (expected \"Name: Value\")\n", h)
			os.Exit(1)
		}
		headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	cc := &clientConfig{serverURL: *urlFlag, headers: headers}

	subArgs := fs.Args()
	if len(subArgs) == 0 {
		fs.Usage()
		os.Exit(1)
	}

	switch subArgs[0] {
	case "chat":
		cmdChat(cc, subArgs[1:])
	case "read":
		cmdRead(cc, subArgs[1:])
	case "list":
		cmdList(cc, subArgs[1:])
	case "search":
		cmdSearch(cc, subArgs[1:])
	case "archive":
		cmdArchive(cc, subArgs[1:])
	case "help":
		cmdHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n", subArgs[0])
		fs.Usage()
		os.Exit(1)
	}
}

func cmdChat(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client chat", flag.ExitOnError)
	prompt := fs.String("p", "", "Message to send (required)")
	convID := fs.String("c", "", "Conversation ID to continue (creates new if omitted)")
	model := fs.String("model", "", "Model to use (server default if empty)")
	cwd := fs.String("cwd", "", "Working directory for the conversation")
	wait := fs.Bool("wait", false, "Wait for the submitted turn to finish")
	ephemeral := fs.Bool("ephemeral", false, "Wait for end of turn, then archive the conversation (for cron-style cleanup)")
	noNotify := fs.Bool("disable-notifications", false, "Disable end-of-turn notifications for this conversation (new conversations only)")
	fs.Parse(args)

	if *prompt == "" {
		fmt.Fprintf(os.Stderr, "Error: -p PROMPT is required\n")
		os.Exit(1)
	}

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Default cwd to the caller's working directory for new conversations,
	// so the server doesn't fall back to its own cwd (which may be unrelated
	// and cause expensive filesystem walks).
	effectiveCwd := *cwd
	if effectiveCwd == "" && *convID == "" {
		if wd, err := os.Getwd(); err == nil {
			effectiveCwd = wd
		}
	}

	reqBody := map[string]any{"message": *prompt}
	if *model != "" {
		reqBody["model"] = *model
	}
	if effectiveCwd != "" {
		reqBody["cwd"] = effectiveCwd
	}
	// Conversation options are applied only at creation time, so
	// -disable-notifications is meaningful only for new conversations (no -c).
	if *noNotify {
		if *convID != "" {
			fmt.Fprintf(os.Stderr, "Error: -disable-notifications only applies to new conversations (omit -c)\n")
			os.Exit(1)
		}
		reqBody["conversation_options"] = map[string]any{"disable_notifications": true}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	var apiURL string
	if *convID != "" {
		apiURL = baseURL + "/api/conversation/" + *convID + "/chat"
	} else {
		apiURL = baseURL + "/api/conversations/new"
	}

	req, err := cc.newRequest("POST", apiURL, strings.NewReader(string(bodyBytes)))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var errBody map[string]any
		if json.NewDecoder(resp.Body).Decode(&errBody) == nil {
			fmt.Fprintf(os.Stderr, "Error (HTTP %d): %v\n", resp.StatusCode, errBody)
		} else {
			fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		}
		os.Exit(1)
	}

	var respBody struct {
		ConversationID string  `json:"conversation_id"`
		MessageID      string  `json:"message_id"`
		SequenceID     int64   `json:"sequence_id"`
		Slug           *string `json:"slug"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	cid := respBody.ConversationID
	if cid == "" {
		cid = *convID
	}
	output := map[string]any{
		"event":           "accepted",
		"conversation_id": cid,
	}
	if respBody.MessageID != "" {
		output["message_id"] = respBody.MessageID
		output["sequence_id"] = respBody.SequenceID
	}
	if respBody.Slug != nil {
		output["slug"] = *respBody.Slug
	}

	json.NewEncoder(os.Stdout).Encode(output)

	if *wait || *ephemeral {
		if cid == "" || respBody.SequenceID == 0 {
			fmt.Fprintf(os.Stderr, "Error: chat response did not include a message cursor\n")
			os.Exit(1)
		}
		var waitErr error
		if *wait {
			waitErr = readStream(cc, client, baseURL, cid, respBody.SequenceID)
		} else {
			waitErr = waitForEndOfTurn(cc, client, baseURL, cid, respBody.SequenceID)
		}
		if *ephemeral {
			if err := archiveConversation(cc, client, baseURL, cid); err != nil {
				fmt.Fprintf(os.Stderr, "Error archiving: %v\n", err)
				os.Exit(1)
			}
		}
		if waitErr != nil {
			fmt.Fprintf(os.Stderr, "Error reading stream: %v\n", waitErr)
			os.Exit(1)
		}
	}
}

// waitForEndOfTurn streams the conversation until the agent's turn ends.
// Stream events are discarded; only end-of-turn detection is performed.
func waitForEndOfTurn(cc *clientConfig, client *http.Client, baseURL, conversationID string, after int64) error {
	return streamUntilEnd(cc, client, baseURL, conversationID, after, nil)
}

func archiveConversation(cc *clientConfig, client *http.Client, baseURL, conversationID string) error {
	req, err := cc.newRequest("POST", baseURL+"/api/conversation/"+conversationID+"/archive", nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// streamEvent is one typed JSONL record emitted by chat or read.
type streamEvent struct {
	Event          string            `json:"event"`
	ConversationID string            `json:"conversation_id"`
	Message        *messageEvent     `json:"message,omitempty"`
	StreamDelta    *llm.StreamDelta  `json:"stream_delta,omitempty"`
	ToolProgress   *llm.ToolProgress `json:"tool_progress,omitempty"`
	SequenceID     int64             `json:"sequence_id,omitempty"`
}

type messageEvent struct {
	MessageID   string          `json:"message_id"`
	SequenceID  int64           `json:"sequence_id"`
	Type        string          `json:"type"`
	Text        string          `json:"text,omitempty"`
	ToolName    string          `json:"tool_name,omitempty"`
	UsageData   json.RawMessage `json:"usage_data,omitempty"`
	DisplayData json.RawMessage `json:"display_data,omitempty"`
	CreatedAt   string          `json:"created_at,omitempty"`
	EndOfTurn   bool            `json:"end_of_turn"`
}

func cmdRead(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client read", flag.ExitOnError)
	wait := fs.Bool("wait", false, "Wait for agent turn to finish (stream new messages)")
	after := fs.Int64("after", -1, "Only stream messages after this sequence ID (required with -wait)")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Usage: shelley client read [-wait -after SEQUENCE_ID] CONVERSATION_ID\n")
		os.Exit(1)
	}
	if *wait && *after < 0 {
		fmt.Fprintf(os.Stderr, "Error: -after SEQUENCE_ID is required with -wait\n")
		os.Exit(1)
	}
	conversationID := fs.Arg(0)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if *wait {
		if err := readStream(cc, client, baseURL, conversationID, *after); err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stream: %v\n", err)
			os.Exit(1)
		}
	} else {
		readSnapshot(cc, client, baseURL, conversationID)
	}
}

func readSnapshot(cc *clientConfig, client *http.Client, baseURL, conversationID string) {
	req, err := cc.newRequest("GET", baseURL+"/api/conversation/"+conversationID, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var sr streamResponseWire
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	encoder := json.NewEncoder(os.Stdout)
	for _, msg := range sr.Messages {
		event, err := messageStreamEvent(msg, conversationID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing message: %v\n", err)
			os.Exit(1)
		}
		if err := encoder.Encode(event); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing message: %v\n", err)
			os.Exit(1)
		}
	}
}

func readStream(cc *clientConfig, client *http.Client, baseURL, conversationID string, after int64) error {
	encoder := json.NewEncoder(os.Stdout)
	return streamUntilEnd(cc, client, baseURL, conversationID, after, func(event streamEvent) error {
		return encoder.Encode(event)
	})
}

func streamUntilEnd(cc *clientConfig, client *http.Client, baseURL, conversationID string, after int64, emit func(streamEvent) error) error {
	streamURL := fmt.Sprintf("%s/api/conversation/%s/stream?last_sequence_id=%d", baseURL, conversationID, after)
	req, err := cc.newRequest("GET", streamURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	seenSeqIDs := make(map[int64]bool)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var sr streamResponseWire
		if err := json.Unmarshal([]byte(data), &sr); err != nil {
			return fmt.Errorf("decode event: %w", err)
		}

		if sr.ConversationID != "" && sr.ConversationID != conversationID {
			continue
		}
		if emit != nil {
			for _, event := range responseEvents(sr, conversationID) {
				if err := emit(event); err != nil {
					return err
				}
			}
		}

		for _, msg := range sr.Messages {
			if seenSeqIDs[msg.SequenceID] {
				continue
			}
			seenSeqIDs[msg.SequenceID] = true

			event, err := messageStreamEvent(msg, conversationID)
			if err != nil {
				return err
			}
			if emit != nil {
				if err := emit(event); err != nil {
					return err
				}
			}

			if (msg.Type == "agent" || msg.Type == "error") && event.Message.EndOfTurn {
				if emit != nil {
					terminalEvent := "done"
					if msg.Type == "error" {
						terminalEvent = "error"
					}
					if err := emit(streamEvent{Event: terminalEvent, ConversationID: conversationID, SequenceID: msg.SequenceID}); err != nil {
						return err
					}
				}
				if msg.Type == "error" {
					return fmt.Errorf("turn ended with error")
				}
				return nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("stream ended before end of turn")
}

func cmdList(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client list", flag.ExitOnError)
	archived := fs.Bool("archived", false, "List archived conversations instead")
	limit := fs.Int("limit", 50, "Maximum number of conversations to return")
	query := fs.String("q", "", "Search query")
	fs.Parse(args)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	endpoint := "/api/conversations"
	if *archived {
		endpoint = "/api/conversations/archived"
	}

	params := fmt.Sprintf("?limit=%d", *limit)
	if *query != "" {
		params += "&q=" + url.QueryEscape(*query)
	}

	req, err := cc.newRequest("GET", baseURL+endpoint+params, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var conversations []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&conversations); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	for _, conv := range conversations {
		var c struct {
			ConversationID string  `json:"conversation_id"`
			Slug           *string `json:"slug"`
			CreatedAt      string  `json:"created_at"`
			UpdatedAt      string  `json:"updated_at"`
			Working        bool    `json:"working"`
			Model          *string `json:"model"`
		}
		if json.Unmarshal(conv, &c) == nil {
			json.NewEncoder(os.Stdout).Encode(c)
		}
	}
}

func cmdSearch(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client search", flag.ExitOnError)
	limit := fs.Int("limit", 20, "Maximum number of results")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: shelley client search [flags] QUERY\n\n")
		fmt.Fprintf(fs.Output(), "Search conversations by slug and message content.\n\n")
		fmt.Fprintf(fs.Output(), "Flags:\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(1)
	}
	query := strings.Join(fs.Args(), " ")

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	params := fmt.Sprintf("?q=%s&search_content=true&limit=%d", url.QueryEscape(query), *limit)
	req, err := cc.newRequest("GET", baseURL+"/api/conversations"+params, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var conversations []json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&conversations); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing response: %v\n", err)
		os.Exit(1)
	}

	for _, conv := range conversations {
		var c struct {
			ConversationID string  `json:"conversation_id"`
			Slug           *string `json:"slug"`
			CreatedAt      string  `json:"created_at"`
			UpdatedAt      string  `json:"updated_at"`
			Working        bool    `json:"working"`
			Model          *string `json:"model"`
		}
		if json.Unmarshal(conv, &c) == nil {
			json.NewEncoder(os.Stdout).Encode(c)
		}
	}
}

func cmdArchive(cc *clientConfig, args []string) {
	fs := flag.NewFlagSet("client archive", flag.ExitOnError)
	fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "Usage: shelley client archive CONVERSATION_ID\n")
		os.Exit(1)
	}
	conversationID := fs.Arg(0)

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	req, err := cc.newRequest("POST", baseURL+"/api/conversation/"+conversationID+"/archive", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: HTTP %d\n", resp.StatusCode)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "Archived %s\n", conversationID)
}

// --- Wire types for JSON parsing ---

type streamResponseWire struct {
	ConversationID string            `json:"conversation_id,omitempty"`
	Messages       []messageWire     `json:"messages"`
	StreamDelta    *llm.StreamDelta  `json:"stream_delta,omitempty"`
	ToolProgress   *llm.ToolProgress `json:"tool_progress,omitempty"`
}

type messageWire struct {
	MessageID      string  `json:"message_id"`
	ConversationID string  `json:"conversation_id"`
	SequenceID     int64   `json:"sequence_id"`
	Type           string  `json:"type"`
	LlmData        *string `json:"llm_data,omitempty"`
	UsageData      *string `json:"usage_data,omitempty"`
	CreatedAt      string  `json:"created_at"`
	DisplayData    *string `json:"display_data,omitempty"`
	EndOfTurn      *bool   `json:"end_of_turn,omitempty"`
}

func responseEvents(response streamResponseWire, conversationID string) []streamEvent {
	var events []streamEvent
	if response.StreamDelta != nil {
		events = append(events, streamEvent{Event: "stream_delta", ConversationID: conversationID, StreamDelta: response.StreamDelta})
	}
	if response.ToolProgress != nil {
		events = append(events, streamEvent{Event: "tool_progress", ConversationID: conversationID, ToolProgress: response.ToolProgress})
	}
	return events
}

func messageStreamEvent(msg messageWire, fallbackConversationID string) (streamEvent, error) {
	conversationID := msg.ConversationID
	if conversationID == "" {
		conversationID = fallbackConversationID
	}
	message := &messageEvent{
		MessageID:  msg.MessageID,
		SequenceID: msg.SequenceID,
		Type:       msg.Type,
		CreatedAt:  msg.CreatedAt,
	}
	if msg.EndOfTurn != nil {
		message.EndOfTurn = *msg.EndOfTurn
	}
	if msg.LlmData != nil {
		var llmMessage llm.Message
		if err := json.Unmarshal([]byte(*msg.LlmData), &llmMessage); err != nil {
			return streamEvent{}, fmt.Errorf("decode message %d llm_data: %w", msg.SequenceID, err)
		}
		message.Text, message.ToolName = summarizeLLMMessage(&llmMessage)
	}
	var err error
	if message.UsageData, err = rawJSON(msg.UsageData); err != nil {
		return streamEvent{}, fmt.Errorf("decode message %d usage_data: %w", msg.SequenceID, err)
	}
	if message.DisplayData, err = rawJSON(msg.DisplayData); err != nil {
		return streamEvent{}, fmt.Errorf("decode message %d display_data: %w", msg.SequenceID, err)
	}
	return streamEvent{Event: "message", ConversationID: conversationID, Message: message}, nil
}

func summarizeLLMMessage(message *llm.Message) (string, string) {
	var texts []string
	var toolName string
	for _, content := range message.Content {
		if content.Type == llm.ContentTypeText && content.Text != "" {
			texts = append(texts, content.Text)
		}
		if content.Type == llm.ContentTypeToolUse && toolName == "" {
			toolName = content.ToolName
		}
		if content.Type == llm.ContentTypeToolResult {
			for _, result := range content.ToolResult {
				if result.Type == llm.ContentTypeText && result.Text != "" {
					texts = append(texts, result.Text)
				}
			}
		}
	}
	return strings.Join(texts, "\n"), toolName
}

func rawJSON(value *string) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	raw := json.RawMessage(*value)
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON")
	}
	return raw, nil
}

func cmdHelp() {
	fmt.Printf(`EXPERIMENTAL: Shelley CLI Client

Usage:
  shelley client [flags] <subcommand> [args...]

Flags:
  -url URL     Server URL (default: unix://%s)
  -H HEADER    Extra HTTP header "Name: Value" (can be repeated)

Subcommands:
  chat -p PROMPT [-c CONVERSATION_ID] [-model MODEL] [-cwd DIR] [-wait] [-ephemeral] [-disable-notifications]
      Send a message. Creates a new conversation unless -c is given.
      Emits an accepted JSON event with conversation_id, message_id, and
      sequence_id. With -wait, also emits message, stream_delta,
      tool_progress, and terminal done or error events.
      With -ephemeral, waits for the agent turn to end and then archives
      the conversation (useful for cron-style invocations that clean up
      after themselves).
      With -disable-notifications, disables end-of-turn notifications (push,
      email, discord, ntfy) for the conversation. New conversations only.

  read [-wait -after SEQUENCE_ID] CONVERSATION_ID
      Emit messages using the same JSON event shape as chat -wait.
      With -wait, continue streaming until the agent turn ends; -after is
      required.

  list [-archived] [-limit N] [-q QUERY]
      List conversations as JSON lines.

  search [-limit N] QUERY
      Search conversations by slug and message content.
      Prints matching conversations as JSON lines.

  archive CONVERSATION_ID
      Archive a conversation.

  help
      Print this help text.

Connecting over HTTP with auth headers:
  shelley client -url http://localhost:9999 -H "X-Exedev-Userid: user" list

Examples:
  # Start a conversation and stream the agent's response
  shelley client chat -wait -p "list files"

  # Continue a conversation
  shelley client chat -c "$ID" -p "now count them"

  # Read current state
  shelley client read "$ID"

NOTE: This feature is EXPERIMENTAL and may change without notice.
`, DefaultSocketPath())
}
