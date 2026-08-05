package server

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"shelley.exe.dev/dtach"
)

// TerminalSession is the on-disk + in-memory record of a persistent terminal.
// The owning process is a `shelley dtach new` child detached via setsid so
// it survives the parent shelley exiting; it terminates when the user's
// command exits or the session is killed.
type TerminalSession struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	Socket  string `json:"socket"`
	LogFile string `json:"log_file"`
	PID     int    `json:"pid"`
	// ConversationID is the conversation that owns this terminal. Empty means
	// global: the terminal is visible in every conversation. Records written
	// before scoping existed unmarshal with an empty value and so read as
	// global. On the wire this is represented as null (see terminalDTO); the
	// "" <-> null conversion happens only in terminalDTO and in the scope
	// handler.
	ConversationID string    `json:"conversation_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// SpawnerFunc starts a dtach server hosting `cmd` on the given socket. The
// implementation must not return until the socket is ready to accept
// connections. The default spawns an out-of-process `shelley dtach serve`
// child so sessions outlive the parent shelley. Tests can replace it to run
// in-process.
type SpawnerFunc func(socket, logFile, cwd, command string, cols, rows uint16, extraEnv []string) (pid int, err error)

// TerminalSessions tracks persistent dtach sessions on disk.
type TerminalSessions struct {
	dir      string // root directory holding per-session files
	exe      string // path to the shelley executable (for re-exec)
	logger   *slog.Logger
	spawner  SpawnerFunc
	mu       sync.Mutex
	sessions map[string]*TerminalSession
	// attachMu serializes attachOrSpawn for the duration of socket-stat /
	// spawn so concurrent reconnects for the same id don't double-spawn.
	attachMu sync.Mutex
}

// NewTerminalSessions opens (or creates) a sessions directory and reaps any
// stale records left over from previous runs.
func NewTerminalSessions(dir string, logger *slog.Logger) (*TerminalSessions, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("terminals: mkdir %s: %w", dir, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("terminals: locate shelley executable: %w", err)
	}
	ts := &TerminalSessions{
		dir:      dir,
		exe:      exe,
		logger:   logger,
		sessions: make(map[string]*TerminalSession),
	}
	ts.spawner = ts.spawnSubprocess
	ts.scan()
	return ts, nil
}

// SetSpawner overrides the spawn strategy (intended for tests).
func (t *TerminalSessions) SetSpawner(s SpawnerFunc) { t.spawner = s }

// scan loads sessions from disk, dropping any whose dtach socket is dead.
func (t *TerminalSessions) scan() {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := name[:len(name)-len(".json")]
		data, err := os.ReadFile(filepath.Join(t.dir, name))
		if err != nil {
			continue
		}
		var s TerminalSession
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}
		if !t.socketAlive(s.Socket) {
			t.removeFiles(id)
			continue
		}
		t.sessions[id] = &s
	}
}

func (t *TerminalSessions) socketAlive(path string) bool {
	if _, err := os.Stat(path); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (t *TerminalSessions) removeFiles(id string) {
	os.Remove(filepath.Join(t.dir, id+".json"))
	os.Remove(filepath.Join(t.dir, id+".sock"))
	os.Remove(filepath.Join(t.dir, id+".log"))
}

// List returns a snapshot of known live sessions, oldest first.
func (t *TerminalSessions) List() []*TerminalSession {
	t.mu.Lock()
	out := make([]*TerminalSession, 0, len(t.sessions))
	for _, s := range t.sessions {
		out = append(out, s)
	}
	t.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Get returns a session by ID, or nil.
func (t *TerminalSessions) Get(id string) *TerminalSession {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sessions[id]
}

// Spawn launches a new dtach-backed session, immediately attaches to it, and
// returns the session record together with the attached client. Doing the
// attach inline closes the race where a fast-exiting command tears down the
// socket before any external attach can succeed.
func (t *TerminalSessions) Spawn(command, cwd, conversationID string, cols, rows uint16, extraEnv []string) (*TerminalSession, *dtach.Client, error) {
	if command == "" {
		return nil, nil, errors.New("terminals: empty command")
	}
	if cwd == "" {
		if wd, err := os.Getwd(); err == nil {
			cwd = wd
		} else {
			cwd = "/"
		}
	}
	id, err := newTerminalID()
	if err != nil {
		return nil, nil, err
	}
	socket := filepath.Join(t.dir, id+".sock")
	logFile := filepath.Join(t.dir, id+".log")

	// SHELLEY_TERMINAL_ID identifies this dtach session. It's stable across
	// reattaches because the id is the on-disk session id.
	env := append([]string(nil), extraEnv...)
	env = append(env, "SHELLEY_TERMINAL_ID="+id)

	pid, err := t.spawner(socket, logFile, cwd, command, cols, rows, env)
	if err != nil {
		return nil, nil, err
	}

	// Attach immediately to pin the session open: while at least one client
	// is connected, Serve will not tear down even if the command exits quickly.
	dc, attachErr := attachWithRetry(socket, 3*time.Second)
	if attachErr != nil {
		return nil, nil, fmt.Errorf("terminals: attach freshly spawned session: %w", attachErr)
	}

	sess := &TerminalSession{
		ID:             id,
		Command:        command,
		Cwd:            cwd,
		Socket:         socket,
		LogFile:        logFile,
		PID:            pid,
		ConversationID: conversationID,
		CreatedAt:      time.Now().UTC(),
	}

	if err := t.writeSession(sess); err != nil {
		dc.Close()
		return nil, nil, err
	}

	t.logger.Info("spawned persistent terminal", "id", id, "command", command, "cwd", cwd, "pid", sess.PID, "conversation_id", conversationID)
	return sess, dc, nil
}

// writeSession persists a session record and then publishes it in memory. The
// JSON is written to a temp file in the sessions dir and renamed over the
// record so a crash mid-write can never leave a truncated file, and the
// in-memory map is only updated once the durable write succeeded.
func (t *TerminalSessions) writeSession(sess *TerminalSession) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(t.dir, sess.ID+".json.*")
	if err != nil {
		return fmt.Errorf("terminals: create temp record: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("terminals: write temp record: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("terminals: chmod temp record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("terminals: close temp record: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(t.dir, sess.ID+".json")); err != nil {
		return fmt.Errorf("terminals: rename record: %w", err)
	}

	t.mu.Lock()
	t.sessions[sess.ID] = sess
	t.mu.Unlock()
	return nil
}

// ErrNoSuchTerminal reports that a terminal id is not known.
var ErrNoSuchTerminal = errors.New("terminals: unknown terminal id")

// SetConversationID re-scopes a terminal. An empty conversationID makes the
// terminal global. Returns the updated record.
func (t *TerminalSessions) SetConversationID(id, conversationID string) (*TerminalSession, error) {
	t.mu.Lock()
	cur := t.sessions[id]
	t.mu.Unlock()
	if cur == nil {
		return nil, ErrNoSuchTerminal
	}
	// Copy so a failed write leaves the in-memory record untouched.
	updated := *cur
	updated.ConversationID = conversationID
	if err := t.writeSession(&updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// GlobalizeConversation re-scopes every terminal owned by conversationID to
// global. Used when a conversation is permanently deleted so its terminals
// remain reachable rather than pointing at a conversation that no longer
// exists.
func (t *TerminalSessions) GlobalizeConversation(conversationID string) error {
	if conversationID == "" {
		return nil
	}
	for _, s := range t.List() {
		if s.ConversationID != conversationID {
			continue
		}
		if _, err := t.SetConversationID(s.ID, ""); err != nil && !errors.Is(err, ErrNoSuchTerminal) {
			return err
		}
	}
	return nil
}

// attachWithRetry repeatedly tries to dial the dtach socket for up to the
// given deadline. Sub-process spawns can race ahead of accept(), so a brief
// retry loop avoids spurious failures.
func attachWithRetry(socket string, max time.Duration) (*dtach.Client, error) {
	deadline := time.Now().Add(max)
	var lastErr error
	for {
		dc, err := dtach.Attach(socket)
		if err == nil {
			return dc, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Kill terminates the session (SIGTERM the dtach server group) and removes
// its on-disk files.
func (t *TerminalSessions) Kill(id string) error {
	t.mu.Lock()
	s := t.sessions[id]
	if s != nil {
		delete(t.sessions, id)
	}
	t.mu.Unlock()
	if s == nil {
		return nil
	}
	if s.PID > 0 {
		// Signal the process group (dtach + child shell + descendants).
		_ = syscall.Kill(-s.PID, syscall.SIGTERM)
	}
	t.removeFiles(id)
	return nil
}

// Forget drops a session from memory and disk without signalling it; intended
// for cleanup after the underlying socket is observed dead.
func (t *TerminalSessions) Forget(id string) {
	t.mu.Lock()
	delete(t.sessions, id)
	t.mu.Unlock()
	t.removeFiles(id)
}

func newTerminalID() (string, error) {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return "t" + string(out), nil
}

// spawnSubprocess starts `shelley dtach new` as an out-of-process child so
// it survives shelley restarts (Setsid keeps it detached in its own session).
func (t *TerminalSessions) spawnSubprocess(socket, logFile, cwd, command string, cols, rows uint16, extraEnv []string) (int, error) {
	logF, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("terminals: open log: %w", err)
	}
	defer logF.Close()

	args := []string{
		"dtach", "new",
		"-s", socket,
		"-cwd", cwd,
		"-cols", fmt.Sprintf("%d", cols),
		"-rows", fmt.Sprintf("%d", rows),
		"--",
		"bash", "--login", "-c", command,
	}
	cmd := exec.Command(t.exe, args...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.Stdin = nil
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("terminals: start dtach: %w", err)
	}
	// Waiting for the listener to come up is the caller's job (attachWithRetry).
	// Reap the child in the background so it does not become a zombie when it
	// exits. The PID is captured first so spawning stays non-blocking. If
	// shelley exits, this goroutine dies and the orphaned child reparents to
	// init, which reaps it.
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

// InProcessSpawner runs the dtach server in a goroutine inside the current
// process. Sessions die when this process exits. Intended for tests; blocks
// until the listener is ready.
func InProcessSpawner(socket, logFile, cwd, command string, cols, rows uint16, extraEnv []string) (int, error) {
	ready := make(chan struct{})
	var env []string
	if len(extraEnv) > 0 {
		env = append(os.Environ(), extraEnv...)
	}
	go func() {
		_ = dtach.Serve(dtach.ServerOptions{
			SocketPath: socket,
			Command:    "bash",
			Args:       []string{"--login", "-c", command},
			Dir:        cwd,
			Cols:       cols,
			Rows:       rows,
			Env:        env,
			Ready:      ready,
		})
	}()
	<-ready
	return os.Getpid(), nil
}

// LockAttach returns a function that releases the attach mutex. Callers use
// it to serialize the lookup-or-spawn path.
func (t *TerminalSessions) LockAttach() func() {
	t.attachMu.Lock()
	return t.attachMu.Unlock
}
