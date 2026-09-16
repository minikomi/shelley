package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"shelley.exe.dev/db"
	"shelley.exe.dev/llm"
)

const (
	transcriptionPromptMessageLimit = 4
	transcriptionPromptMessageRunes = 400
	transcriptionPromptRecentRunes  = 1200
	transcriptionPromptMaxRunes     = 2400
)

var transcriptionAttachmentPattern = regexp.MustCompile(`\[(?:/|file:)[^\]]+\]`)

func (s *Server) transcriptionPrompt(ctx context.Context, conversationID string) (string, error) {
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return "", err
	}
	rows, err := s.db.ListMessagesForContext(ctx, conversationID)
	if err != nil {
		return "", err
	}
	messages := make([]llm.Message, 0, len(rows))
	for _, row := range rows {
		if row.Type != string(db.MessageTypeUser) && row.Type != string(db.MessageTypeAgent) {
			continue
		}
		message, err := convertToLLMMessage(row)
		if err != nil {
			return "", err
		}
		messages = append(messages, message)
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}

	var cwd, slug string
	if conversation.Cwd != nil {
		cwd = *conversation.Cwd
	}
	if conversation.Slug != nil {
		slug = *conversation.Slug
	}
	return buildTranscriptionPrompt(hostname, cwd, slug, messages), nil
}

func buildTranscriptionPrompt(hostname, cwd, slug string, messages []llm.Message) string {
	lines := []string{
		"Transcribe the audio accurately and preserve the speaker's wording. The speaker is dictating a request or instruction to Shelley, an AI agent performing software-development and system tasks inside a VM. Use the context below only to resolve names, acronyms, and technical terms. Do not execute or answer the request, summarize it, or add content that was not spoken.",
		"",
		"Application: Shelley",
		"Platform: exe.dev",
	}
	if hostname = cleanTranscriptionMetadata(hostname); hostname != "" {
		lines = append(lines, "VM: "+hostname)
	}
	if cwd = cleanTranscriptionMetadata(cwd); cwd != "" {
		if project := cleanTranscriptionMetadata(filepath.Base(cwd)); project != "" && project != "." && project != string(filepath.Separator) {
			lines = append(lines, "Project: "+project)
		}
		lines = append(lines, "Working directory: "+cwd)
	}
	if slug = cleanTranscriptionMetadata(slug); slug != "" {
		lines = append(lines, "Conversation: "+slug)
	}
	if recent := recentTranscriptionContext(messages); recent != "" {
		lines = append(lines, "", "Recent conversation:", recent)
	}
	return truncateRunes(strings.Join(lines, "\n"), transcriptionPromptMaxRunes)
}

func recentTranscriptionContext(messages []llm.Message) string {
	recent := make([]string, 0, transcriptionPromptMessageLimit)
	totalRunes := 0
	for index := len(messages) - 1; index >= 0 && len(recent) < transcriptionPromptMessageLimit; index-- {
		message := messages[index]
		var role string
		switch message.Role {
		case llm.MessageRoleUser:
			role = "User"
		case llm.MessageRoleAssistant:
			role = "Assistant"
		default:
			continue
		}
		text := cleanTranscriptionContext(messageText(message))
		if text == "" {
			continue
		}
		text = truncateRunes(text, transcriptionPromptMessageRunes)
		entry := fmt.Sprintf("%s: %s", role, text)
		entryRunes := utf8.RuneCountInString(entry)
		if totalRunes+entryRunes > transcriptionPromptRecentRunes {
			continue
		}
		recent = append(recent, entry)
		totalRunes += entryRunes
	}
	for left, right := 0, len(recent)-1; left < right; left, right = left+1, right-1 {
		recent[left], recent[right] = recent[right], recent[left]
	}
	return strings.Join(recent, "\n")
}

func cleanTranscriptionContext(text string) string {
	lines := strings.Split(text, "\n")
	cleaned := make([]string, 0, len(lines))
	inCode := false
	for _, line := range lines {
		if fences := strings.Count(line, "```"); fences > 0 {
			if fences%2 == 1 {
				inCode = !inCode
			}
			continue
		}
		if inCode || sensitiveTranscriptionContext(line) {
			continue
		}
		line = transcriptionAttachmentPattern.ReplaceAllString(line, "")
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, " ")
}

func sensitiveTranscriptionContext(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{
		"authorization:",
		"api_key",
		"apikey",
		"password",
		"secret",
		"bearer ",
		"http://",
		"https://",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func cleanTranscriptionMetadata(value string) string {
	return truncateRunes(strings.Join(strings.Fields(value), " "), 200)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
