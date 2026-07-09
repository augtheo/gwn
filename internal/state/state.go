package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const version = 1

type State struct {
	Version    int                  `json:"version"`
	Workspaces map[string]Workspace `json:"workspaces"`

	path string
	lock *os.File
}

type Workspace struct {
	Type         string     `json:"type"` // "plain" | "git_repo" | "worktree"
	TmuxSession  string     `json:"tmux_session"`
	LastAccessed time.Time  `json:"last_accessed"`
	Worktrees    []Worktree `json:"worktrees,omitempty"`
}

type Worktree struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	TmuxSession string `json:"tmux_session"`
}

func Load() (*State, error) {
	path := statePath()
	s := &State{
		Version:    version,
		Workspaces: make(map[string]Workspace),
		path:       path,
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, s); err != nil {
		return s, nil
	}
	s.path = path
	return s, nil
}

func (s *State) Save() error {
	tmp := s.path + ".tmp"
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *State) Lock() error {
	f, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	s.lock = f
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

func (s *State) Unlock() {
	if s.lock != nil {
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		s.lock = nil
	}
}

func (s *State) Touch(path, session string) {
	ws := s.Workspaces[path]
	ws.TmuxSession = session
	ws.LastAccessed = time.Now()
	s.Workspaces[path] = ws
}

// Remove drops path's tracked state, e.g. after its worktree is deleted.
func (s *State) Remove(path string) {
	delete(s.Workspaces, path)
}

func statePath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "gwn", "state.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "gwn", "state.json")
}
