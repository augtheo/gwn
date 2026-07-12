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

// hasClaudeHistory reports whether Claude Code has a recorded conversation
// for dir, by checking for its project transcript directory under
// ~/.claude/projects. Claude names that directory by replacing every
// non-alphanumeric character in the absolute path with '-'.
func hasClaudeHistory(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	name := nonAlnum.ReplaceAllString(abs, "-")
	entries, err := os.ReadDir(filepath.Join(home, ".claude", "projects", name))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			return true
		}
	}
	return false
}

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

// assistantCmd adjusts the configured assistant command for the "ai"
// window. For Claude Code, it appends -c ("continue the most recent
// conversation in the current directory") so that a freshly created tmux
// session — e.g. after a reboot killed the tmux server and gwn recreates the
// session from scratch — resumes that worktree's last conversation instead
// of starting blank. Claude errors out with "No conversation found to
// continue" if -c is passed with no prior history, so it's only appended
// when dir already has a recorded conversation.
func assistantCmd(assistant, dir string) string {
	fields := strings.Fields(assistant)
	if len(fields) == 0 || fields[0] != "claude" {
		return assistant
	}
	for _, f := range fields[1:] {
		if f == "-c" || f == "--continue" || f == "-r" || f == "--resume" {
			return assistant
		}
	}
	if !hasClaudeHistory(dir) {
		return assistant
	}
	return assistant + " -c"
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
	assistant = assistantCmd(assistant, dir)

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
