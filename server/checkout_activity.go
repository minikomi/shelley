package server

import (
	"context"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"time"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/gitstate"
)

func (cm *ConversationManager) noteCheckout(ctx context.Context, vm claudetool.VM, dir string, edited bool) {
	ctx = context.WithoutCancel(ctx)
	if vm == nil {
		state := gitstate.GetGitState(dir)
		if state.IsRepo {
			cm.recordCheckout(ctx, "", state.Worktree, edited)
		}
		return
	}
	argv := vm.Command(`exec git -C "$1" rev-parse --show-toplevel`, dir)
	if root, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output(); err == nil {
		cm.recordCheckout(ctx, vm.Name(), strings.TrimSpace(string(root)), edited)
	}
}

func (cm *ConversationManager) recordCheckout(ctx context.Context, machine, root string, edited bool) {
	err := cm.db.QueriesTx(ctx, func(q *generated.Queries) error {
		return q.RecordCheckoutActivity(ctx, generated.RecordCheckoutActivityParams{
			ConversationID: cm.conversationID, Machine: machine, Worktree: root, Edited: edited, TouchedAt: time.Now().UnixNano(),
		})
	})
	if err != nil {
		cm.logger.Error("record checkout activity", "error", err, "worktree", root)
	}
}

func (s *Server) handleRecentCheckouts(w http.ResponseWriter, r *http.Request) {
	m, err := s.requestMachine(r)
	if err != nil {
		machineError(w, err)
		return
	}
	params := generated.RecentCheckoutsParams{Chat: r.URL.Query().Get("conversation_id")}
	if m.vm != nil {
		params.Machine = m.vm.Name()
	}
	if dir := m.dir(r.URL.Query().Get("cwd")); dir != "" {
		params.Workspace, _ = m.gitRoot(dir)
	}
	var paths []string
	err = s.db.Queries(r.Context(), func(q *generated.Queries) error {
		var err error
		paths, err = q.RecentCheckouts(r.Context(), params)
		return err
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if params.Workspace != "" && !slices.Contains(paths, params.Workspace) {
		paths = append(paths, params.Workspace)
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspace": params.Workspace, "paths": paths})
}
