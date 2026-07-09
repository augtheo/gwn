package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ScanPaths        []string           `toml:"scan_paths"`
	ScanDepth        int                `toml:"scan_depth"`
	Editor           string             `toml:"editor"`
	Assistant        string             `toml:"assistant"`
	SessionPrefix    string             `toml:"session_prefix"`
	AutoAttachSingle bool               `toml:"auto_attach_single"`
	NerdFontIcons    bool               `toml:"nerd_font_icons"`
	VimMode          bool               `toml:"vim_mode"`
	DefaultGitHost   string             `toml:"default_git_host"`
	CloneProtocol    string             `toml:"clone_protocol"`
	ReviewCommand    string             `toml:"review_command"`
	BranchPrefixes   []BranchPrefixRule `toml:"branch_prefixes"`
	Appearance       Appearance         `toml:"appearance"`
}

// BranchPrefixRule auto-prefixes new branch names (in the Ctrl+W prompt) for
// repos under Path, e.g. so work repos get "augtheo/" but personal ones don't.
type BranchPrefixRule struct {
	Path   string `toml:"path"`
	Prefix string `toml:"prefix"`
}

type Appearance struct {
	Theme string `toml:"theme"`
}

// BranchPrefixFor returns the configured branch prefix for repoPath, using
// the longest matching BranchPrefixes entry, or "" if none match.
func (c *Config) BranchPrefixFor(repoPath string) string {
	repoPath = filepath.Clean(repoPath)

	best := ""
	bestLen := -1
	for _, rule := range c.BranchPrefixes {
		root := filepath.Clean(rule.Path)
		if repoPath != root && !strings.HasPrefix(repoPath, root+string(filepath.Separator)) {
			continue
		}
		if len(root) > bestLen {
			bestLen = len(root)
			best = rule.Prefix
		}
	}
	return best
}

func Load() (*Config, error) {
	cfg := defaults()
	path := configPath()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return cfg, nil
		}
		if err := writeDefault(path, cfg); err != nil {
			return cfg, nil
		}
		return cfg, nil
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}

	expanded := make([]string, 0, len(cfg.ScanPaths))
	for _, p := range cfg.ScanPaths {
		expanded = append(expanded, expandHome(p))
	}
	cfg.ScanPaths = expanded

	for i := range cfg.BranchPrefixes {
		cfg.BranchPrefixes[i].Path = expandHome(cfg.BranchPrefixes[i].Path)
	}

	return cfg, nil
}

func configPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "gwn", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gwn", "config.toml")
}

func expandHome(path string) string {
	if len(path) > 1 && path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func defaults() *Config {
	return &Config{
		ScanPaths:        []string{"~/projects"},
		ScanDepth:        1,
		Editor:           "nvim .",
		Assistant:        "claude",
		SessionPrefix:    "",
		AutoAttachSingle: true,
		NerdFontIcons:    true,
		VimMode:          true,
		DefaultGitHost:   "github.com",
		CloneProtocol:    "https",
		ReviewCommand:    "gh pr diff {pr} | delta --side-by-side --paging=always",
		Appearance:       Appearance{Theme: "mocha"},
	}
}

func writeDefault(path string, cfg *Config) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}
