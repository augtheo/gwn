# Agents guide for gwn

Context for AI agents (Claude Code, OpenCode, etc.) working in this repo.

## What this is

`gwn` is a Go TUI application. It is a single binary with no server component. The source is small and self-contained. Dependencies are vendored under `vendor/`.

## Project layout

```
main.go                        entry point — calls cmd.Execute()
cmd/
  root.go                      default command: scan → TUI
  open.go                      `gwn open <path>` subcommand
internal/
  config/config.go             TOML config struct, XDG path resolution, default writer
  scanner/git.go               Workspace type, git repo detection, worktree listing
  scanner/scanner.go           Parallel directory scan over configured paths
  state/state.go               JSON state persistence, atomic write, flock
  tmux/client.go               Thin os/exec wrappers for tmux commands
  tmux/session.go              Session naming, OpenWorkspace, switch/attach logic
  ui/app.go                    bubbletea Model — all TUI logic lives here
  ui/styles.go                 lipgloss styles (catppuccin mocha palette) and icons
docs/tmux.conf                 tmux keybinding snippet for the user to copy
flake.nix                      Nix package + devShell
vendor/                        Vendored Go dependencies (do not edit manually)
```

## How to build

```bash
nix develop          # enter the dev shell (provides go, gopls, golangci-lint)
go build ./...       # compile
go vet ./...         # lint
```

Or build the Nix package directly:

```bash
nix build
./result/bin/gwn --help
```

## Dependency management

Dependencies are vendored. After any `go get` or `go.mod` change:

```bash
go mod tidy
go mod vendor
```

The flake uses `vendorHash = null` because the `vendor/` directory is committed. Do not change this to a hash.

## Key design decisions

**No network calls except explicit user actions.** All git introspection (worktree listing, branch detection) is local, via `git worktree list --porcelain` and `git show-ref`. The `go-git` library is imported but only used for `PlainOpen` to detect if a directory is a git repo. The deliberate exceptions are `Ctrl+G` (`scanner.CloneBare`, shells out to `git clone`/`fetch`/`ls-remote`), `Ctrl+F` (`scanner.Fetch`, shells out to `git fetch`), and `Ctrl+R` (`scanner.ListOpenPRs`/`scanner.FetchPR`, shell out to `gh pr list` and `git fetch origin pull/<n>/head`) — all explicitly triggered by the user, never automatic. In particular, `AddWorktree` never fetches on its own; it only looks at already-fetched `refs/remotes/origin/*` and, after `Ctrl+R`, `refs/heads/pr-<n>`.

**Bare repo + worktree layout.** A workspace directory can optionally be `<repo>/.bare` (a bare git dir) plus one subdirectory per worktree (`<repo>/<branch>`). `scanner.bareGitDir` detects this. Because `scanDir` only reads one level per scan root and skips dotfiles, worktree subdirectories nested this way are never independently rescanned as duplicate top-level entries — this is why the layout exists (see `AddWorktree`/`CloneBare` in `scanner/git.go`). Plain (non-bare) repos still get worktrees created as sibling directories (`<repo>-<branch>`), which *can* show up as duplicate top-level entries since they live at the same scan depth as the original repo.

**Tmux interaction is purely via the CLI.** No tmux socket library. `os/exec` wrapping `tmux` subcommands is sufficient and more stable across tmux versions.

**State is keyed by absolute path.** When updating state, always use `filepath.Abs` first. The state file is written atomically (write to `.tmp`, then `os.Rename`).

**Expansion state in the TUI is keyed by workspace path**, not by list index. Indices change when the fuzzy filter query changes; paths do not. The `Model.expanded` field is `map[string]bool`.

**Session naming.** `tmux.SessionName(prefix, path)` uses `filepath.Base(path)` and sanitizes to `[a-zA-Z0-9-]`. For worktrees, `tmux.WorktreeSessionName(prefix, repoName, branch)` concatenates and sanitizes — it does not use `filepath.Base` because branch names can contain `/`.

## Adding a new config field

1. Add the field to `Config` in `internal/config/config.go` with a `toml` tag.
2. Set a sensible default in `defaults()`.
3. Use it wherever needed. The config is passed through from `cmd/root.go` into the scanner/UI/tmux layers.

## Adding a new tmux command

Add a thin wrapper in `internal/tmux/client.go` following the existing pattern (`exec.Command("tmux", ...)` + error return). Keep business logic in `session.go`, not `client.go`.

## Changing the TUI

All bubbletea logic is in `internal/ui/app.go`. The model fields are:

- `all []scanner.Workspace` — full unfiltered list from the scanner
- `filtered []listItem` — current view after fuzzy filter + worktree expansion
- `expanded map[string]bool` — which repo paths have their worktrees shown
- `cursor int` — index into `filtered`

`refilter()` rebuilds `filtered` from `all` + current query + `expanded`. It is called on every keystroke that changes the search input, and after every `toggleExpand()`. Keep it fast — it runs on the hot path.

Styles are all in `internal/ui/styles.go`. The palette is catppuccin mocha hardcoded as lipgloss colors. To change a color, edit the `col*` vars there.
