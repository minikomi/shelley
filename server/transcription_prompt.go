package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) transcriptionPrompt(ctx context.Context, conversationID string) (string, error) {
	conversation, err := s.db.GetConversationByID(ctx, conversationID)
	if err != nil {
		return "", err
	}
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return buildTranscriptionPrompt(hostname, derefString(conversation.Cwd), derefString(conversation.Slug)), nil
}

func buildTranscriptionPrompt(hostname, cwd, slug string) string {
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
		if project := filepath.Base(cwd); project != "." && project != string(filepath.Separator) {
			lines = append(lines, "Project: "+project)
		}
		lines = append(lines, "Working directory: "+cwd)
	}
	if slug = cleanTranscriptionMetadata(slug); slug != "" {
		lines = append(lines, "Conversation: "+slug)
	}
	return strings.Join(lines, "\n")
}

func cleanTranscriptionMetadata(value string) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > 200 {
		runes = runes[:200]
	}
	return string(runes)
}
