package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/augtheo/gwn/internal/config"
	"github.com/augtheo/gwn/internal/scanner"
	"github.com/augtheo/gwn/internal/state"
	"github.com/augtheo/gwn/internal/tmux"
	"github.com/augtheo/gwn/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gwn",
	Short: "workspace navigator",
	RunE:  runTUI,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runTUI(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}

	if pruneOrphaned(st) {
		_ = st.Save()
	}

	workspaces := scanner.Scan(cfg.ScanPaths, cfg.ScanDepth)
	reconcile(workspaces, st)
	sortByMRU(workspaces, st)
	workspaces = moveCurrentFirst(workspaces)

	if cfg.AutoAttachSingle && len(workspaces) == 1 {
		ws := workspaces[0]
		session := tmux.SessionName(cfg.SessionPrefix, ws.Path)
		return tmux.OpenWorkspace(session, ws.Path, cfg.Editor, cfg.AssistantFor(ws.Path), "", "")
	}

	model := ui.New(cfg, st, workspaces)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// pruneOrphaned reaps state entries whose path no longer exists on disk —
// e.g. a worktree deleted through lazygit or a plain `git worktree remove`,
// which gwn's own UI never sees. Any live tmux session tied to that path is
// killed before the entry is dropped. Reports whether state was changed.
func pruneOrphaned(st *state.State) bool {
	sessions, _ := tmux.ListSessions()
	active := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		active[s] = true
	}

	changed := false
	for path, ws := range st.Workspaces {
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if ws.TmuxSession != "" && active[ws.TmuxSession] {
			_ = tmux.KillSession(ws.TmuxSession)
		}
		st.Remove(path)
		changed = true
	}
	return changed
}

func reconcile(workspaces []scanner.Workspace, st *state.State) {
	activeSessions := make(map[string]bool)
	sessions, _ := tmux.ListSessions()
	for _, s := range sessions {
		activeSessions[s] = true
	}

	for i := range workspaces {
		ws := st.Workspaces[workspaces[i].Path]
		if ws.TmuxSession != "" && activeSessions[ws.TmuxSession] {
			workspaces[i].HasSession = true
			workspaces[i].TmuxSession = ws.TmuxSession
		}
		for j := range workspaces[i].Worktrees {
			wt := workspaces[i].Worktrees[j]
			// find session for this worktree path in state
			if stWs, ok := st.Workspaces[wt.Path]; ok {
				if stWs.TmuxSession != "" && activeSessions[stWs.TmuxSession] {
					workspaces[i].Worktrees[j].HasSession = true
					workspaces[i].Worktrees[j].TmuxSession = stWs.TmuxSession
				}
			}
		}
	}
}

// moveCurrentFirst reorders workspaces so the one gwn was launched from (e.g.
// the current tmux session's worktree) is listed first, regardless of MRU
// order. It matches by cwd: first against top-level workspace paths, then —
// for bare-repo layouts where worktrees live in nested subdirectories — the
// parent workspace containing the current worktree.
func moveCurrentFirst(workspaces []scanner.Workspace) []scanner.Workspace {
	cwd, err := os.Getwd()
	if err != nil {
		return workspaces
	}
	cwd = resolveSymlinks(cwd)

	idx := -1
	for i, ws := range workspaces {
		if resolveSymlinks(ws.Path) == cwd {
			idx = i
			break
		}
	}
	if idx == -1 {
		for i, ws := range workspaces {
			for _, wt := range ws.Worktrees {
				if resolveSymlinks(wt.Path) == cwd {
					idx = i
					break
				}
			}
			if idx != -1 {
				break
			}
		}
	}

	if idx <= 0 {
		return workspaces
	}

	reordered := make([]scanner.Workspace, 0, len(workspaces))
	reordered = append(reordered, workspaces[idx])
	reordered = append(reordered, workspaces[:idx]...)
	reordered = append(reordered, workspaces[idx+1:]...)
	return reordered
}

func resolveSymlinks(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

func sortByMRU(workspaces []scanner.Workspace, st *state.State) {
	sort.SliceStable(workspaces, func(i, j int) bool {
		wi := st.Workspaces[workspaces[i].Path]
		wj := st.Workspaces[workspaces[j].Path]
		return wi.LastAccessed.After(wj.LastAccessed)
	})
}
