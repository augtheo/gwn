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

 NORMAL  i//: search  j/k gg/G ^d/^u: move  enter/l: open  h: collapse  tab: expand  ^w/^g/^f/^r: worktree/clone/fetch/review  q: quit
```

## Features

- Vim-style modal navigation — starts in Normal mode; press `i` or `/` to search
- Fuzzy search across all projects in configured paths
- Git worktree detection — expands inline with `Tab`
- Create new worktrees on the fly with `Ctrl+W`
- Clone a remote as a new bare repo with `Ctrl+G`, ready for worktrees from the start
- Fetch a repo from `origin` with `Ctrl+F`, with a spinner on its row while it runs
- Review a PR with `Ctrl+R` on a bare repo — pick from its open PRs, and gwn fetches it, creates a worktree, and opens a session with an extra `diff` window
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
- `gh` (GitHub CLI, authenticated) in PATH if you use `Ctrl+R` to review PRs
- [`delta`](https://github.com/dandavison/delta) in PATH for the default `review_command`'s syntax-highlighted, side-by-side diffs (optional — swap `review_command` for `gh pr diff {pr} | less -R` if you'd rather not install it)

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
vim_mode          = true  # start in modal Normal mode; false = classic always-typing search
default_git_host  = "github.com" # host assumed for "owner/repo" shorthand with Ctrl+G
clone_protocol    = "https"      # "https" or "ssh" — used to build the clone URL for shorthand forms
review_command    = "gh pr diff {pr} | delta --side-by-side --paging=always" # run in the "diff" window after Ctrl+R checkout; {pr} is the PR number

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

gwn starts in vim-style **Normal** mode (single keys act, nothing is typed into
the search box). Press `i` or `/` to enter **Insert** mode and filter by
typing; `Esc` returns to Normal mode. Set `vim_mode = false` in the config to
disable modes entirely and always type directly into the search box, as
before.

### Normal mode

| Key | Action |
|-----|--------|
| `j` / `↓` or `k` / `↑` | Move down / up (also `Ctrl+j`/`Ctrl+n`, `Ctrl+k`/`Ctrl+p`) |
| `gg` / `G` | Jump to top / bottom of the list |
| `Ctrl+D` / `Ctrl+U` | Half-page down / up |
| A number before a motion, e.g. `5j` or `5G` | Repeat the motion, or jump to that absolute row for `G`/`gg` |
| `Enter` / `l` | Open workspace / switch tmux session |
| `h` | Collapse the selected repo's worktrees, or jump to the parent repo from one of its worktrees |
| `Tab` | Expand or collapse worktrees for a git repo |
| `i` / `a` | Enter Insert mode (cursor moves to end of the current filter) |
| `/` | Clear the filter and enter Insert mode |
| `Ctrl+W` | Create a new worktree for the selected repo (prompts for branch name) |
| `Ctrl+G` | Clone a remote repo as a new bare repo (prompts for owner/repo or a URL) |
| `Ctrl+F` | Fetch the selected repo from `origin` (shows a spinner on its row while running) |
| `Ctrl+R` | On a bare repo, open a picker of its open PRs; confirming checks one out and opens its session |
| `q` / `Esc` / `Ctrl+C` | Quit |

### Insert mode

| Key | Action |
|-----|--------|
| Type anything | Fuzzy filter |
| `↑` / `↓` or `Ctrl+k`/`Ctrl+j`/`Ctrl+p`/`Ctrl+n` | Navigate list without leaving Insert mode |
| `Enter` | Open workspace / switch tmux session |
| `Esc` | Return to Normal mode |
| `Ctrl+C` | Quit |

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

## Reviewing a PR

`gwn` doesn't help you find which PRs to review — use `gh pr list --search "review-requested:@me"` or the [`gh dash`](https://github.com/dlvhdr/gh-dash) TUI for that. Once you know which repo, select its bare-repo row and press `Ctrl+R`:

1. `gwn` fetches the repo's open PRs via `gh pr list` and shows a picker (number, title, author). Type to fuzzy-filter, `↑`/`↓` to move.
2. `Enter` on a PR fetches its head ref (`git fetch origin pull/<n>/head:pr-<n>`) and creates a worktree for it — same underlying mechanism as `Ctrl+W`, just skipping the branch-name prompt.
3. The tmux session opens automatically with a fourth window, `diff`, running `review_command` (default `gh pr diff <n> | delta --side-by-side --paging=always`) for a syntax-highlighted, side-by-side view of the diff.

From there:
- Use the `ai` window's assistant to review — e.g. Claude Code's `review` skill.
- For leaving inline comments, use a tool with real PR support in the terminal, like [`octo.nvim`](https://github.com/pwntester/octo.nvim) in the `editor` window, or `gh pr review`/`gh pr comment` from a shell.

## How sessions work

When you open a workspace:

1. If a tmux session for that workspace already exists → switch to it.
2. If not → create a new session with two windows (three if opened via `Ctrl+R`):
   - Window 0 `editor`: runs `nvim .` in the workspace directory
   - Window 1 `ai`: runs `claude` (or your configured assistant)
   - Window 2 `diff` (PR worktrees only): runs `review_command` for that PR

Session names are derived from the directory name (or `reponame-branchname` for worktrees), with non-alphanumeric characters replaced by `-`.

State (session names, last-accessed times) is persisted at `~/.local/share/gwn/state.json` (respects `$XDG_DATA_HOME`).

## Scripting

To open a specific workspace without the TUI (useful in shell scripts or other tmux bindings):

```bash
gwn open ~/projects/augtheo/myapp
```

This creates the session if it doesn't exist and switches to it, then exits.
