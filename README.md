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
- Create new worktrees on the fly with `Ctrl+W`
- Clone a remote as a new bare repo with `Ctrl+G`, ready for worktrees from the start
- Fetch a repo from `origin` with `Ctrl+F`, with a spinner on its row while it runs
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
- `git` in PATH (for worktree listing, and `git clone`/`fetch` if you use `Ctrl+G`/`Ctrl+F`)

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
default_git_host  = "github.com" # host assumed for "owner/repo" shorthand with Ctrl+G
clone_protocol    = "https"      # "https" or "ssh" — used to build the clone URL for shorthand forms

# Auto-prefix new branch names (Ctrl+W) for repos under a given path.
# Matched by longest path prefix; repos outside all of these get no prefix.
[[branch_prefixes]]
path   = "~/projects/work"
prefix = "augtheo"

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
| `Ctrl+W` | Create a new worktree for the selected repo (prompts for branch name) |
| `Ctrl+G` | Clone a remote repo as a new bare repo (prompts for owner/repo or a URL) |
| `Ctrl+F` | Fetch the selected repo from `origin` (shows a spinner on its row while running) |
| Type anything | Fuzzy filter |
| `q` / `Esc` / `Ctrl+C` | Quit |

## Creating worktrees

Select any git repo (or one of its worktrees) and press `Ctrl+W` to create a new worktree. Type a branch name and hit `Enter`:

- If the branch already exists locally, it's checked out into the new worktree.
- Else if it exists on the `origin` remote — already fetched, e.g. a bot-opened branch you just `git fetch`ed — a local branch is created tracking it (`git worktree add --track -b <branch> ... origin/<branch>`), not a fresh branch off `HEAD`.
- Otherwise a new branch is created off the current `HEAD`.

If a `branch_prefixes` rule (see Configuration) matches the repo, the prompt is pre-filled with `<prefix>/` — keep typing the rest, e.g. `feature/412-add-login`.

The full branch name (including any `/`) is stored as a normal git ref — a branch like `augtheo/feature/412-add-login` is valid and unaffected. Only the *filesystem path* gets flattened: slashes and other non-alphanumeric characters become `-`.

Where the new worktree lands depends on the repo's layout:

- **Bare repos** (see below) get worktrees nested inside, at `<repo>/<flattened-branch>`.
- **Plain repos** get a sibling directory named `<repo>-<flattened-branch>`. Since this sibling lives at the same scan depth as the repo itself, it'll show up as its own top-level entry in addition to being nested under the repo when expanded.

The repo entry expands automatically to show the new worktree. Press `Esc` to cancel the prompt.

To pick up new branches from the remote (e.g. one a bot just opened), select the repo and press `Ctrl+F` to fetch `origin` — a spinner shows on its row while it runs. There's no *automatic* fetch on `Ctrl+W` itself, since that'd be a silent network call; fetching is always this explicit, separate step. Equivalent by hand:

```bash
git -C <repo>/.bare fetch origin   # or just `git fetch` inside a plain repo
```

## Cloning repos (bare + worktrees)

Press `Ctrl+G` from anywhere in the list and type a repo to clone:

- `owner/repo` — uses `default_git_host` from config (defaults to `github.com`)
- `host/owner/repo` — any host
- a full `git@...` or `https://...` URL — used as-is

Shorthand forms (`owner/repo`, `host/owner/repo`) are built using `clone_protocol` from config — `https` by default, so cloning works out of the box via your credential helper / `gh` auth with no SSH key setup. Set `clone_protocol = "ssh"` if you'd rather use `git@host:owner/repo.git`. Either way, typing a full URL always overrides this.

`gwn` clones it as a **bare** repo into `<first scan_path>/<repo-name>/.bare`, and fixes up the origin fetch refspec (plain `git clone --bare` doesn't wire this up, so `git fetch` wouldn't otherwise update remote-tracking branches). It then detects the remote's default branch and prompts you to create the first worktree, pre-filled with that branch name — hit `Enter` to accept it or type a different one.

This bare + worktree layout is the recommended way to work with a repo in `gwn`: because `gwn` only scans one directory level deep and skips dotfiles, worktrees nested inside `<repo>/` (as `<repo>/<branch>`) never show up as duplicate top-level entries the way sibling-directory worktrees can for plain repos.

`gwn` doesn't migrate existing plain repos into this layout automatically — if you want an existing repo to use it, convert it manually (move its `.git` to `.bare`, set `core.bare = true`, then `git worktree add`).

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
