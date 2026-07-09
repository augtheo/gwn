package cmd

import (
	"fmt"
	"os"
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

	workspaces := scanner.Scan(cfg.ScanPaths, cfg.ScanDepth)
	reconcile(workspaces, st)
	sortByMRU(workspaces, st)

	if cfg.AutoAttachSingle && len(workspaces) == 1 {
		ws := workspaces[0]
		session := tmux.SessionName(cfg.SessionPrefix, ws.Path)
		return tmux.OpenWorkspace(session, ws.Path, cfg.Editor, cfg.Assistant, "", "")
	}

	model := ui.New(cfg, st, workspaces)
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err = p.Run()
	return err
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

func sortByMRU(workspaces []scanner.Workspace, st *state.State) {
	sort.SliceStable(workspaces, func(i, j int) bool {
		wi := st.Workspaces[workspaces[i].Path]
		wj := st.Workspaces[workspaces[j].Path]
		return wi.LastAccessed.After(wj.LastAccessed)
	})
}
