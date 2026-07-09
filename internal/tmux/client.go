package tmux

import (
	"os"
	"os/exec"
	"strings"
)

func InsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

func HasSession(name string) bool {
	return exec.Command("tmux", "has-session", "-t", name).Run() == nil
}

func NewSession(name, dir string) error {
	return exec.Command("tmux", "new-session", "-d", "-s", name, "-c", dir).Run()
}

// RenameCurrentWindow renames the current (first) window of a session.
// Avoids index-based targeting which breaks when base-index != 0.
func RenameCurrentWindow(session, newName string) error {
	return exec.Command("tmux", "rename-window", "-t", session, newName).Run()
}

// NewWindow creates a new named window in the session, appended at the end.
func NewWindow(session, name, dir string) error {
	return exec.Command("tmux", "new-window", "-t", session, "-n", name, "-c", dir).Run()
}

// SendKeysToWindow sends keys to a window identified by name, not index.
func SendKeysToWindow(session, window, keys string) error {
	target := session + ":" + window
	return exec.Command("tmux", "send-keys", "-t", target, keys, "Enter").Run()
}

// SelectWindow selects a window by name.
func SelectWindow(session, window string) error {
	target := session + ":" + window
	return exec.Command("tmux", "select-window", "-t", target).Run()
}

func SwitchClient(session string) error {
	return exec.Command("tmux", "switch-client", "-t", session).Run()
}

func KillSession(name string) error {
	return exec.Command("tmux", "kill-session", "-t", name).Run()
}

func AttachSession(session string) error {
	cmd := AttachSessionCmd(session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// AttachSessionCmd builds an attach-session command without wiring stdio, so
// callers that manage the terminal handoff themselves (e.g. bubbletea's
// ExecProcess) can run it safely.
func AttachSessionCmd(session string) *exec.Cmd {
	return exec.Command("tmux", "attach-session", "-t", session)
}

func ListSessions() ([]string, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil, err
	}
	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}
