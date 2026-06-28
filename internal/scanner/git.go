package scanner

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
)

type WorkspaceType string

const (
	TypePlain   WorkspaceType = "plain"
	TypeGitRepo WorkspaceType = "git_repo"
	TypeWorktree WorkspaceType = "worktree"
)

type Workspace struct {
	Path      string
	Name      string
	Type      WorkspaceType
	Branch    string
	Worktrees []WorktreeInfo
	// set after reconcile with state
	TmuxSession string
	HasSession  bool
}

type WorktreeInfo struct {
	Path        string
	Branch      string
	TmuxSession string
	HasSession  bool
}

func detectWorkspace(path string) Workspace {
	ws := Workspace{
		Path: path,
		Name: filepath.Base(path),
		Type: TypePlain,
	}

	repo, err := git.PlainOpen(path)
	if err != nil {
		// Try detecting if it's inside a worktree by walking up
		return ws
	}

	ws.Type = TypeGitRepo

	// Get current branch
	if head, err := repo.Head(); err == nil {
		if head.Name().IsBranch() {
			ws.Branch = head.Name().Short()
		} else {
			ws.Branch = head.Hash().String()[:7]
		}
	}

	// Use git CLI for worktree listing (more reliable than go-git for linked worktrees)
	ws.Worktrees = listWorktrees(path)

	return ws
}

func listWorktrees(repoPath string) []WorktreeInfo {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var worktrees []WorktreeInfo
	var current WorktreeInfo

	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			current.Branch = "HEAD (detached)"
		case line == "":
			// blank line separates entries
		}
	}
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	return worktrees
}
