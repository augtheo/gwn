package config

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ScanPaths        []string   `toml:"scan_paths"`
	ScanDepth        int        `toml:"scan_depth"`
	Editor           string     `toml:"editor"`
	Assistant        string     `toml:"assistant"`
	SessionPrefix    string     `toml:"session_prefix"`
	AutoAttachSingle bool       `toml:"auto_attach_single"`
	NerdFontIcons    bool       `toml:"nerd_font_icons"`
	Appearance       Appearance `toml:"appearance"`
}

type Appearance struct {
	Theme string `toml:"theme"`
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
