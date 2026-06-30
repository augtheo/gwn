package tmux

import (
	"fmt"
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

func OpenWorkspace(session, dir, editor, assistant string) error {
	if HasSession(session) {
		if InsideTmux() {
			return SwitchClient(session)
		}
		return AttachSession(session)
	}

	if err := NewSession(session, dir); err != nil {
		return fmt.Errorf("new-session: %w", err)
	}
	// Rename by targeting the session (not session:index) — works regardless of base-index.
	if err := RenameCurrentWindow(session, "shell"); err != nil {
		return err
	}
	if err := NewWindow(session, "editor", dir); err != nil {
		return err
	}
	if err := NewWindow(session, "ai", dir); err != nil {
		return err
	}
	if err := SelectWindow(session, "shell"); err != nil {
		return err
	}

	if InsideTmux() {
		// Switch client first so the session inherits the real terminal size,
		// then start programs — they launch at the correct dimensions.
		if err := SwitchClient(session); err != nil {
			return err
		}
		if err := SendKeysToWindow(session, "editor", editor); err != nil {
			return err
		}
		return SendKeysToWindow(session, "ai", assistant)
	}

	// Outside tmux: send keys before attaching (attach blocks; programs will
	// receive SIGWINCH and redraw once the terminal resizes on attach).
	if err := SendKeysToWindow(session, "editor", editor); err != nil {
		return err
	}
	if err := SendKeysToWindow(session, "ai", assistant); err != nil {
		return err
	}
	return AttachSession(session)
}
