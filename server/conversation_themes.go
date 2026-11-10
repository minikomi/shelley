package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
)

func (cm *ConversationManager) chapterContext(ctx context.Context) (string, error) {
	themes, err := cm.db.GetConversationThemes(ctx, cm.conversationID)
	if err != nil {
		return "", err
	}
	if len(themes.Chapters) == 0 {
		return "There is no conversation chapter yet. Use chapter to record the user's first substantive goal; do not announce this bookkeeping.", nil
	}
	current := themes.Chapters[len(themes.Chapters)-1].Title
	return fmt.Sprintf("Current conversation chapter: %q.\nCompare the latest user's topic with this heading. Emit chapter for a meaningful new subtopic, question, decision, goal, or approach worth navigating to, even within the same overall task. Do not wait for a major goal change or for the user to ask. Skip routine implementation, progress updates, and tiny corrections. The three-user-message spacing brake still applies.", current), nil
}

func (cm *ConversationManager) chapterRecorder() func(context.Context, string) (string, error) {
	if cm.role != roleTopLevel {
		return nil
	}
	return func(ctx context.Context, title string) (string, error) {
		return cm.db.AppendConversationChapter(ctx, cm.conversationID, title)
	}
}

func (s *Server) handleConversationThemes(w http.ResponseWriter, r *http.Request) {
	themes, err := s.db.GetConversationThemes(r.Context(), r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		s.logger.Error("failed to load conversation themes", "error", err)
		http.Error(w, "Failed to load conversation themes", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, themes)
}
