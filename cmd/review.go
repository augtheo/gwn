package cmd

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/augtheo/gwn/internal/config"
	"github.com/augtheo/gwn/internal/scanner"
	"github.com/augtheo/gwn/internal/state"
	"github.com/augtheo/gwn/internal/tmux"
	"github.com/spf13/cobra"
)

var reviewCmd = &cobra.Command{
	Use:   "review <path> <pr-number>",
	Short: "check out a PR as a worktree and open its review session (for scripting / gh-dash bindings)",
	Args:  cobra.ExactArgs(2),
	RunE:  runReview,
}

func init() {
	rootCmd.AddCommand(reviewCmd)
}

func runReview(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	st, err := state.Load()
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}

	abs, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}

	n, err := strconv.Atoi(args[1])
	if err != nil {
		return fmt.Errorf("invalid PR number %q: %w", args[1], err)
	}

	branch, err := scanner.FetchPR(abs, n)
	if err != nil {
		return err
	}
	worktreePath, err := scanner.AddWorktree(abs, branch)
	if err != nil {
		return err
	}

	repoName := filepath.Base(abs)
	sessionName := tmux.WorktreeSessionName(cfg.SessionPrefix, repoName, branch)
	diffCmd := scanner.ReviewWindow(branch, cfg.ReviewCommand)
	if diffCmd == "" {
		diffCmd = cfg.DiffCommand
	}

	if err := tmux.OpenWorkspace(sessionName, worktreePath, cfg.Editor, cfg.Assistant, diffCmd); err != nil {
		return err
	}

	st.Touch(worktreePath, sessionName)
	return st.Save()
}
