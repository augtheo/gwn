# gwn

A terminal workspace navigator. Scans configured project directories, detects git worktrees, and manages tmux sessions — each with a neovim window and an AI assistant window — so switching between workspaces is a single keypress.

```
  gwn

 ╭──────────────────────────────────────────╮
 │ search workspaces...                     │
 ╰──────────────────────────────────────────╯

  myapp ▾  main ●
   feat/auth ●
   fix/typo ○
  example-sdk  main ○
  scripts  ○

enter: open  tab: expand worktrees  ↑↓: navigate  q: quit
```

## Features

- Fuzzy search across all projects in configured paths
- Git worktree detection — expands inline with `Tab`
- MRU ordering — most recently opened workspaces float to the top
- Tmux session management — creates sessions on demand with two default windows:
  - `editor` — `nvim .`
  - `ai` — `claude` (or `opencode`, configurable)
- Live session indicator — green dot means a tmux session is already running
- `gwn open <path>` subcommand for scripting and tmux bindings

## Requirements

- Nix (for building and the dev shell)
- tmux 3.3+
- A Nerd Font (for icons — optional, configurable)
- `git` in PATH (for worktree listing)

## Installation

### From source with Nix

```bash
git clone <repo>
cd gwn
nix build
```

This produces `./result/bin/gwn`. To install it persistently, add it to your Home Manager config:

```nix
# flake.nix input
inputs.gwn.url = "path:/path/to/gwn";

# home.nix
home.packages = [ inputs.gwn.packages.${system}.default ];
```

Or add to your system profile directly:

```bash
nix profile install .
```

### Development shell

```bash
nix develop
# now you have: go, gopls, golangci-lint, gotools
go build -o gwn .
```

## Configuration

On first launch, gwn writes a default config to `~/.config/gwn/config.toml` (respects `$XDG_CONFIG_HOME`).

```toml
scan_paths = [
  "~/projects/work",
  "~/projects/personal",
]

scan_depth = 1       # how many levels deep to look for projects
editor    = "nvim ."                          # full command — no args are appended
assistant = "claude"                          # or "opencode"

session_prefix    = ""    # prefix for tmux session names, e.g. "w" → "w-myapp"
auto_attach_single = true # skip TUI and attach directly when only one match
nerd_font_icons   = true  # set false if your terminal doesn't have Nerd Fonts

[appearance]
theme = "mocha" # catppuccin flavor (only mocha is currently built-in)
```

Edit the file and relaunch — no restart required.

## Tmux integration

Add the following to `~/.config/tmux/tmux.conf` (or `~/.tmux.conf`):

```tmux
bind -n M-p display-popup \
  -E \
  -w 80% \
  -h 70% \
  -b rounded \
  -T " workspaces " \
  "gwn"
```

`M-p` (Alt+P) opens gwn in a floating popup. Selecting a workspace switches your current tmux client to that session and closes the popup.

The full snippet is in [`docs/tmux.conf`](docs/tmux.conf).

## Keybindings

| Key | Action |
|-----|--------|
| `↑` / `↓` or `Ctrl+k` / `Ctrl+j` | Navigate list |
| `Enter` | Open workspace / switch tmux session |
| `Tab` | Expand or collapse worktrees for a git repo |
| Type anything | Fuzzy filter |
| `q` / `Esc` / `Ctrl+C` | Quit |

## How sessions work

When you open a workspace:

1. If a tmux session for that workspace already exists → switch to it.
2. If not → create a new session with two windows:
   - Window 0 `editor`: runs `nvim .` in the workspace directory
   - Window 1 `ai`: runs `claude` (or your configured assistant)

Session names are derived from the directory name (or `reponame-branchname` for worktrees), with non-alphanumeric characters replaced by `-`.

State (session names, last-accessed times) is persisted at `~/.local/share/gwn/state.json` (respects `$XDG_DATA_HOME`).

## Scripting

To open a specific workspace without the TUI (useful in shell scripts or other tmux bindings):

```bash
gwn open ~/projects/augtheo/myapp
```

This creates the session if it doesn't exist and switches to it, then exits.
