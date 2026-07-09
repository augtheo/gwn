package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/augtheo/gwn/internal/config"
	"github.com/augtheo/gwn/internal/state"
	"github.com/augtheo/gwn/internal/tmux"
	"github.com/spf13/cobra"
)

var openCmd = &cobra.Command{
	Use:   "open <path>",
	Short: "open a workspace directly (for scripting / tmux bindings)",
	Args:  cobra.ExactArgs(1),
	RunE:  runOpen,
}

func init() {
	rootCmd.AddCommand(openCmd)
}

func runOpen(cmd *cobra.Command, args []string) error {
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

	session := tmux.SessionName(cfg.SessionPrefix, abs)
	if err := tmux.OpenWorkspace(session, abs, cfg.Editor, cfg.Assistant, "", ""); err != nil {
		return err
	}

	st.Touch(abs, session)
	return st.Save()
}
