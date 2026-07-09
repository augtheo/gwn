package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// SessionName derives a tmux session name from a directory path.
func SessionName(prefix, path string) string {
	return sanitize(prefix, filepath.Base(path))
}

// WorktreeSessionName derives a session name for a specific worktree branch.
func WorktreeSessionName(prefix, repoName, branch string) string {
	return sanitize(prefix, repoName+"-"+branch)
}

func sanitize(prefix, s string) string {
	name := nonAlnum.ReplaceAllString(s, "-")
	name = strings.Trim(name, "-")
	if len(name) > 50 {
		name = name[:50]
	}
	if prefix != "" {
		return prefix + "-" + name
	}
	return name
}

// OpenWorkspace sets up (or reattaches) a tmux session and blocks until the
// attached client detaches. Only safe to call when nothing else is reading
// os.Stdin — for callers running inside a bubbletea Program, use PrepareOpen
// instead and run the returned command via tea.ExecProcess.
func OpenWorkspace(session, dir, editor, assistant, extraWindowName, extraWindowCmd string) error {
	cmd, err := PrepareOpen(session, dir, editor, assistant, extraWindowName, extraWindowCmd)
	if err != nil {
		return err
	}
	if cmd == nil {
		return nil
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// PrepareOpen creates/configures the tmux session and windows, then returns
// an attach-session command for the caller to run against the controlling
// terminal. It returns a nil command when no attach is needed, i.e. the
// target session was already reached via switch-client from inside tmux.
//
// extraWindowName/extraWindowCmd optionally add a fourth window (e.g. "diff"
// for PR review sessions) alongside "editor"/"ai"; pass "", "" to skip it.
//
// Setup here never touches stdio, so it's safe to call while another process
// (like a bubbletea Program) is reading os.Stdin. Only the returned attach
// command needs exclusive access to the terminal.
func PrepareOpen(session, dir, editor, assistant, extraWindowName, extraWindowCmd string) (*exec.Cmd, error) {
	if HasSession(session) {
		if InsideTmux() {
			return nil, SwitchClient(session)
		}
		return AttachSessionCmd(session), nil
	}

	if err := NewSession(session, dir); err != nil {
		return nil, fmt.Errorf("new-session: %w", err)
	}
	// Rename by targeting the session (not session:index) — works regardless of base-index.
	if err := RenameCurrentWindow(session, "shell"); err != nil {
		return nil, err
	}
	if err := NewWindow(session, "editor", dir); err != nil {
		return nil, err
	}
	if err := NewWindow(session, "ai", dir); err != nil {
		return nil, err
	}
	if extraWindowName != "" {
		if err := NewWindow(session, extraWindowName, dir); err != nil {
			return nil, err
		}
	}
	if err := SelectWindow(session, "shell"); err != nil {
		return nil, err
	}

	if InsideTmux() {
		// Switch client first so the session inherits the real terminal size,
		// then start programs — they launch at the correct dimensions.
		if err := SwitchClient(session); err != nil {
			return nil, err
		}
		if err := SendKeysToWindow(session, "editor", editor); err != nil {
			return nil, err
		}
		if err := SendKeysToWindow(session, "ai", assistant); err != nil {
			return nil, err
		}
		if extraWindowName != "" {
			return nil, SendKeysToWindow(session, extraWindowName, extraWindowCmd)
		}
		return nil, nil
	}

	// Outside tmux: send keys before attaching (attach blocks; programs will
	// receive SIGWINCH and redraw once the terminal resizes on attach).
	if err := SendKeysToWindow(session, "editor", editor); err != nil {
		return nil, err
	}
	if err := SendKeysToWindow(session, "ai", assistant); err != nil {
		return nil, err
	}
	if extraWindowName != "" {
		if err := SendKeysToWindow(session, extraWindowName, extraWindowCmd); err != nil {
			return nil, err
		}
	}
	return AttachSessionCmd(session), nil
}
