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
		return switchOrAttach(session)
	}

	if err := NewSession(session, dir); err != nil {
		return fmt.Errorf("new-session: %w", err)
	}
	if err := RenameWindow(session, 0, "editor"); err != nil {
		return err
	}
	if err := SendKeys(session, 0, editor); err != nil {
		return err
	}
	if err := NewWindow(session, "ai", dir); err != nil {
		return err
	}
	if err := SendKeys(session, 1, assistant); err != nil {
		return err
	}
	if err := SelectWindow(session, 0); err != nil {
		return err
	}

	return switchOrAttach(session)
}

func switchOrAttach(session string) error {
	if InsideTmux() {
		return SwitchClient(session)
	}
	return AttachSession(session)
}
