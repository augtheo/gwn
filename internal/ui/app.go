package ui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/augtheo/gwn/internal/config"
	"github.com/augtheo/gwn/internal/scanner"
	"github.com/augtheo/gwn/internal/state"
	"github.com/augtheo/gwn/internal/tmux"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

type errMsg error
type openedMsg struct{}
type sessionReadyMsg struct{ attachCmd *exec.Cmd }
type worktreeCreatedMsg struct {
	repoPath string
	err      error
}
type repoClonedMsg struct {
	repoPath      string
	repoName      string
	defaultBranch string
	err           error
}
type repoFetchedMsg struct {
	repoPath string
	err      error
}

// bulkDeletedMsg reports the outcome of executeDelete: which repos need
// their worktree list refreshed (worktree-kind targets), and which
// workspace paths no longer exist on disk at all (workspace-kind targets,
// to be dropped from the list rather than rescanned).
type bulkDeletedMsg struct {
	errs             []error
	refreshRepoPaths []string
	removedPaths     []string
}

// pruneReadyMsg carries the deleteTargets that Ctrl+P collected from merged
// worktrees, remote-missing worktrees, and gh-confirmed PR branches.
type pruneReadyMsg struct {
	token   int
	targets []deleteTarget
}

type prListMsg struct {
	repoPath string
	prs      []scanner.PRInfo
	err      error
}
type prCheckedOutMsg struct {
	repoPath     string
	worktreePath string
	sessionName  string
	branch       string
	err          error
}
type spinnerTickMsg struct{}
type worktreeStatusMsg struct {
	repoPath     string
	worktreePath string
	status       scanner.WorktreeStatus
}

type mode int

const (
	modeNormal mode = iota
	modeInsert
	modeVisual
)

type Model struct {
	cfg      *config.Config
	st       *state.State
	all      []scanner.Workspace
	filtered []listItem
	input    textinput.Model
	cursor   int
	width    int
	height   int
	err      error
	quitting bool
	expanded map[string]bool // path → expanded

	// activeView, toggled by tab, swaps the list to only the workspaces and
	// worktrees that currently have a live tmux session attached — the ones
	// actually being worked on — instead of the full scanned tree.
	activeView bool

	mode         mode
	pendingG     bool // "g" seen, waiting for a second "g" (gg = go to top)
	pendingD     bool // "d" seen, waiting for a second "d" (dd = delete row)
	pendingCount int  // numeric prefix accumulated so far, e.g. "5" in "5j"
	visualAnchor int  // row index where visual mode ("V") was entered

	creatingWorktree bool
	createRepoPath   string

	cloningRepo bool

	confirmingDelete bool
	deleteTargets    []deleteTarget

	fetchingPaths map[string]bool
	spinnerFrame  int

	pickingPR  bool
	prRepoPath string
	prAll      []scanner.PRInfo
	prFiltered []scanner.PRInfo
	prCursor    int
	prLoading   bool
	pruningPRs  int
}

type listItem struct {
	ws        scanner.Workspace
	wtIdx     int // -1 = the repo itself, >=0 = worktree index
	parentIdx int // index in filtered of the parent repo (for worktrees)
}

type deleteKind int

const (
	deleteWorktreeKind deleteKind = iota
	deleteWorkspaceKind
)

// deleteTarget describes one row queued for deletion, whether from a single
// dd/Ctrl+X press or gathered from a visual-mode selection.
type deleteTarget struct {
	kind        deleteKind
	repoPath    string // parent repo path (worktree kind), or the workspace's own path (workspace kind)
	path        string // the worktree path, or the workspace path
	label       string // display label: branch name, or workspace name
	sessionName string
	hasSession  bool
	dirty       bool
	unpushed    bool
	ws          scanner.Workspace // full workspace; only populated/used for workspace kind
}

func New(cfg *config.Config, st *state.State, workspaces []scanner.Workspace) Model {
	ti := textinput.New()
	ti.Placeholder = "search active worktrees..."
	ti.Focus()
	ti.CharLimit = 80
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colBlue)
	ti.TextStyle = lipgloss.NewStyle().Foreground(colText)

	m := Model{
		cfg:           cfg,
		st:            st,
		all:           workspaces,
		input:         ti,
		expanded:      make(map[string]bool),
		mode:          modeInsert,
		fetchingPaths: make(map[string]bool),
		activeView:    true,
	}
	if cfg.VimMode {
		m.mode = modeNormal
		m.input.Blur()
	}
	m.refilter()
	return m
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	for _, ws := range m.all {
		cmds = append(cmds, annotateWorktreesCmd(ws))
	}
	return tea.Batch(cmds...)
}

// annotateWorktreesCmd kicks off one background git-status lookup per
// worktree in ws (dirty/merged/pushed/last-commit), so the tree can render
// immediately and fill in status as each lookup finishes rather than
// blocking the initial scan on every worktree in every repo.
func annotateWorktreesCmd(ws scanner.Workspace) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(ws.Worktrees))
	for _, wt := range ws.Worktrees {
		wt := wt
		cmds = append(cmds, func() tea.Msg {
			return worktreeStatusMsg{
				repoPath:     ws.Path,
				worktreePath: wt.Path,
				status:       scanner.AnnotateWorktree(wt.Path, wt.Branch),
			}
		})
	}
	return tea.Batch(cmds...)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.creatingWorktree {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.cancelCreateWorktree()
				return m, nil
			case "enter":
				branch := strings.TrimSpace(m.input.Value())
				if branch == "" {
					return m, nil
				}
				repoPath := m.createRepoPath
				m.cancelCreateWorktree()
				return m, m.createWorktree(repoPath, branch)
			default:
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				return m, cmd
			}
		}

		if m.cloningRepo {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.cancelCloneRepo()
				return m, nil
			case "enter":
				src := strings.TrimSpace(m.input.Value())
				if src == "" {
					return m, nil
				}
				m.cancelCloneRepo()
				return m, m.cloneRepo(src)
			default:
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				return m, cmd
			}
		}

		if m.confirmingDelete {
			switch msg.String() {
			case "y", "enter":
				targets := m.deleteTargets
				m.cancelDelete()
				return m, m.executeDelete(targets)
			default:
				m.cancelDelete()
				return m, nil
			}
		}

		if m.pickingPR {
			if m.cfg.VimMode && m.mode == modeNormal {
				return m.updatePickPRNormal(msg)
			}
			return m.updatePickPRInsert(msg)
		}

		// Chords available regardless of mode.
		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "tab":
			m.toggleActiveView()
			return m, nil
		case "ctrl+t":
			m.startCreateWorktree()
			return m, nil
		case "ctrl+g":
			m.startCloneRepo()
			return m, nil
		case "ctrl+f":
			if m.mode == modeVisual {
				return m, m.startFetchVisualSelection()
			}
			return m, m.startFetch()
		case "ctrl+x":
			m.startDeleteWorktree()
			return m, nil
		case "ctrl+r":
			return m, m.startPickPR()
		case "ctrl+p":
			if m.mode == modeVisual {
				return m, nil
			}
			return m, m.startPruneMerged()
		}

		if m.cfg.VimMode && m.mode == modeVisual {
			return m.updateVisualMode(msg)
		}
		if m.cfg.VimMode && m.mode == modeNormal {
			return m.updateNormalMode(msg)
		}
		return m.updateInsertMode(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case errMsg:
		m.err = msg
		return m, nil

	case worktreeCreatedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		return m, m.refreshWorkspace(msg.repoPath)

	case repoClonedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		cmd := m.refreshWorkspace(msg.repoPath)
		m.beginWorktreePrompt(msg.repoPath, msg.repoName, msg.defaultBranch)
		return m, cmd

	case repoFetchedMsg:
		delete(m.fetchingPaths, msg.repoPath)
		m.err = msg.err
		return m, nil

	case bulkDeletedMsg:
		m.err = joinErrs(msg.errs)
		for _, p := range msg.removedPaths {
			m.removeWorkspace(p)
		}
		seen := make(map[string]bool)
		var cmds []tea.Cmd
		for _, p := range msg.refreshRepoPaths {
			if seen[p] {
				continue
			}
			seen[p] = true
			cmds = append(cmds, m.refreshWorkspace(p))
		}
		return m, tea.Batch(cmds...)

	case prListMsg:
		if !m.pickingPR || m.prRepoPath != msg.repoPath {
			return m, nil
		}
		m.prLoading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.prAll = msg.prs
		m.refilterPR()
		return m, nil

	case prCheckedOutMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		cmd := m.refreshWorkspace(msg.repoPath)
		return m, tea.Batch(cmd, m.openPath(msg.worktreePath, msg.sessionName, msg.branch))

	case pruneReadyMsg:
		if msg.token != m.pruningPRs {
			return m, nil
		}
		m.pruningPRs = 0
		if len(msg.targets) == 0 {
			return m, nil
		}
		m.confirmingDelete = true
		m.deleteTargets = msg.targets
		return m, nil

	case worktreeStatusMsg:
		m.applyWorktreeStatus(msg)
		return m, nil

	case spinnerTickMsg:
		if len(m.fetchingPaths) == 0 && !m.prLoading {
			return m, nil
		}
		m.spinnerFrame++
		return m, spinnerTick()

	case openedMsg:
		m.quitting = true
		return m, tea.Quit

	case sessionReadyMsg:
		if msg.attachCmd == nil {
			m.quitting = true
			return m, tea.Quit
		}
		return m, tea.ExecProcess(msg.attachCmd, func(err error) tea.Msg {
			if err != nil {
				return errMsg(err)
			}
			return openedMsg{}
		})
	}

	return m, nil
}

// updateInsertMode handles keys while the search box is being typed into —
// either because vim mode is disabled, or vim mode is on and the user
// pressed "i", "a", or "/" to start editing.
func (m Model) updateInsertMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.cfg.VimMode {
			m.mode = modeNormal
			m.input.Blur()
			return m, nil
		}
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.refilter()
			m.cursor = 0
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "enter":
		return m, m.openSelected()
	case "up", "ctrl+p":
		m.moveCursor(-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		m.moveCursor(1)
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refilter()
		m.cursor = 0
		return m, cmd
	}
}

// updateNormalMode handles keys in vim mode's default, non-editing mode:
// single keys navigate and act instead of typing into the filter.
func (m Model) updateNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Accumulate a numeric prefix, e.g. "5j" moves down 5 items. A leading
	// "0" isn't special-cased (there's no "start of line" here) so it's
	// only absorbed once a count has already started.
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		d := int(key[0] - '0')
		if d != 0 || m.pendingCount > 0 {
			m.pendingCount = m.pendingCount*10 + d
			m.pendingG = false
			m.pendingD = false
			return m, nil
		}
	}

	hadCount := m.pendingCount > 0
	count := m.pendingCount
	if count == 0 {
		count = 1
	}
	m.pendingCount = 0

	if m.pendingD {
		m.pendingD = false
		if key == "d" {
			m.startDeleteSelected()
		}
		return m, nil
	}

	if m.pendingG {
		m.pendingG = false
		if key == "g" {
			m.cursor = clampIndex(count-1, len(m.filtered))
		}
		return m, nil
	}

	switch key {
	case "esc":
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.refilter()
			m.cursor = 0
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "q":
		m.quitting = true
		return m, tea.Quit
	case "i", "a":
		m.mode = modeInsert
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "/":
		m.input.SetValue("")
		m.refilter()
		m.cursor = 0
		m.mode = modeInsert
		return m, m.input.Focus()
	case "enter":
		return m, m.openSelected()
	case "l":
		if m.expandSelected() {
			return m, nil
		}
		return m, m.openSelected()
	case "j", "down", "ctrl+n", "ctrl+j":
		m.moveCursor(count)
		return m, nil
	case "k", "up", "ctrl+p", "ctrl+k":
		m.moveCursor(-count)
		return m, nil
	case "g":
		m.pendingG = true
		return m, nil
	case "d":
		m.pendingD = true
		return m, nil
	case "V":
		m.mode = modeVisual
		m.visualAnchor = m.cursor
		return m, nil
	case "G":
		if hadCount {
			m.cursor = clampIndex(count-1, len(m.filtered))
		} else if len(m.filtered) > 0 {
			m.cursor = len(m.filtered) - 1
		}
		return m, nil
	case "ctrl+d":
		m.moveCursor(m.halfPage())
		return m, nil
	case "ctrl+u":
		m.moveCursor(-m.halfPage())
		return m, nil
	case "h":
		m.collapseOrJumpToParent()
		return m, nil
	}
	return m, nil
}

// updateVisualMode handles keys while a row range is selected via "V" (vim
// visual-line mode): motions extend the selection, "d" deletes every
// selected row, ctrl+f (handled earlier, as a global chord) fetches every
// selected repo, and esc/V/q leave visual mode without acting.
func (m Model) updateVisualMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if m.pendingG {
		m.pendingG = false
		if key == "g" {
			m.cursor = clampIndex(0, len(m.filtered))
		}
		return m, nil
	}

	switch key {
	case "esc", "V", "q":
		m.mode = modeNormal
		return m, nil
	case "j", "down", "ctrl+n", "ctrl+j":
		m.moveCursor(1)
		return m, nil
	case "k", "up", "ctrl+p", "ctrl+k":
		m.moveCursor(-1)
		return m, nil
	case "g":
		m.pendingG = true
		return m, nil
	case "G":
		if len(m.filtered) > 0 {
			m.cursor = len(m.filtered) - 1
		}
		return m, nil
	case "ctrl+d":
		m.moveCursor(m.halfPage())
		return m, nil
	case "ctrl+u":
		m.moveCursor(-m.halfPage())
		return m, nil
	case "d":
		m.startDeleteVisualSelection()
		return m, nil
	}
	return m, nil
}

// visualSelectionRange returns the [lo, hi] inclusive row indices currently
// spanned by visual mode, ordered regardless of which end the cursor is on.
func (m Model) visualSelectionRange() (int, int) {
	lo, hi := m.visualAnchor, m.cursor
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi
}

func (m *Model) moveCursor(delta int) {
	if len(m.filtered) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor > len(m.filtered)-1 {
		m.cursor = len(m.filtered) - 1
	}
}

func clampIndex(i, n int) int {
	if n == 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i > n-1 {
		return n - 1
	}
	return i
}

// collapseOrJumpToParent implements vim tree-navigation "h": collapse the
// selected repo if it's expanded, or jump up to the parent repo if the
// selection is one of its worktrees.
func (m *Model) collapseOrJumpToParent() {
	if m.activeView || m.cursor >= len(m.filtered) {
		return
	}
	item := m.filtered[m.cursor]
	if item.wtIdx >= 0 {
		m.cursor = item.parentIdx
		return
	}
	if item.ws.Type == scanner.TypeGitRepo && m.expanded[item.ws.Path] {
		m.expanded[item.ws.Path] = false
		m.refilter()
		m.selectWorkspace(item.ws.Path)
	}
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	b.WriteString(styleTitle.PaddingBottom(0).Render("gwn") + "\n")
	b.WriteString(" " + m.renderViewTabs() + "\n\n")

	inputWidth := m.width - 6
	if inputWidth < 20 {
		inputWidth = 20
	}
	inputBorder := styleInput
	if m.cfg.VimMode && m.mode == modeNormal {
		inputBorder = inputBorder.BorderForeground(colOverlay)
	}
	b.WriteString(inputBorder.Width(inputWidth).Render(m.input.View()) + "\n\n")

	switch {
	case m.pickingPR:
		b.WriteString(m.renderPRList() + "\n")
	case m.activeView && len(m.filtered) == 0:
		b.WriteString(styleHints.Render(" no active worktrees — tab: back to all workspaces") + "\n")
	default:
		start, end := m.visibleRange(m.listHeight())
		for i := start; i < end && i < len(m.filtered); i++ {
			b.WriteString(m.renderItem(i) + "\n")
		}
	}

	b.WriteString("\n")
	switch {
	case m.err != nil:
		b.WriteString(lipgloss.NewStyle().Foreground(colRed).Render("  error: "+m.err.Error()) + "\n")
	case m.creatingWorktree:
		b.WriteString(styleHints.Render("enter: create worktree  esc: cancel"))
	case m.cloningRepo:
		b.WriteString(styleHints.Render("enter: clone  esc: cancel"))
	case m.confirmingDelete:
		b.WriteString(m.renderDeleteConfirm())
	case m.pickingPR:
		b.WriteString(styleHints.Render("enter: checkout PR  esc: cancel"))
	case m.cfg.VimMode && m.mode == modeVisual:
		b.WriteString(lipgloss.NewStyle().Foreground(colMauve).Bold(true).Render(" VISUAL ") +
			styleHints.Render(" j/k gg/G ^d/^u: extend  d: delete selected  ^f: fetch selected  esc/V: cancel"))
	case m.cfg.VimMode && m.mode == modeNormal:
		b.WriteString(lipgloss.NewStyle().Foreground(colBlue).Bold(true).Render(" NORMAL ") +
			styleHints.Render(" i//: search  j/k gg/G ^d/^u: move  enter/l: open  h: collapse  V: visual  dd: delete  tab: active view  ^t/^g/^f/^x/^r/^p: worktree/clone/fetch/delete/review/prune  q: quit"))
	case m.cfg.VimMode:
		b.WriteString(lipgloss.NewStyle().Foreground(colGreen).Bold(true).Render(" INSERT ") +
			styleHints.Render(" esc: normal mode  enter: open  tab: active view  ↑↓: navigate  ^t/^g/^f/^x/^r/^p: worktree/clone/fetch/delete/review/prune"))
	default:
		b.WriteString(styleHints.Render("enter: open  tab: active view  ctrl+t: new worktree  ctrl+g: clone repo  ctrl+f: fetch  ctrl+x: delete worktree  ctrl+r: review PRs  ctrl+p: prune merged  ↑↓: navigate  esc/ctrl+c: quit"))
	}

	return b.String()
}

// renderViewTabs renders the "active"/"all" tab strip that tab toggles
// between, highlighting whichever one is currently showing — the same idea
// as tmux bolding the current window in its status line.
func (m Model) renderViewTabs() string {
	active, all := styleTabInactive, styleTabInactive
	if m.activeView {
		active = styleTabActive
	} else {
		all = styleTabActive
	}
	return active.Render("active") + " " + all.Render("all")
}

// listHeight returns the number of item rows that fit in the terminal,
// matching the layout accounting used by View.
func (m Model) listHeight() int {
	h := m.height - 8
	if h < 1 {
		h = 1
	}
	return h
}

// halfPage returns the vim ctrl+d/ctrl+u scroll distance: half a screen of items.
func (m Model) halfPage() int {
	h := m.listHeight() / 2
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) renderItem(i int) string {
	item := m.filtered[i]
	selected := i == m.cursor
	inVisual := false
	if m.cfg.VimMode && m.mode == modeVisual {
		lo, hi := m.visualSelectionRange()
		inVisual = i >= lo && i <= hi
	}
	ws := item.ws

	if item.wtIdx >= 0 {
		wt := ws.Worktrees[item.wtIdx]
		label := wt.Branch
		if m.activeView {
			label = ws.Name + " / " + wt.Branch
		}
		// Plain text body first, coloured parts appended outside the style render
		// to avoid ANSI codes inside a Width-constrained Render call.
		body := "   " + m.worktreeIcon() + label
		status := ""
		if wt.Dirty {
			status += styleDirty.Render(" " + iconDirty)
		}
		if wt.MergedLocal {
			status += styleMerged.Render(" " + iconMerged)
		}
		if wt.PushedRemote {
			status += stylePushed.Render(" " + iconPushed)
		}
		if !wt.LastCommit.IsZero() {
			status += styleLastCommit.Render(" " + humanizeSince(wt.LastCommit))
		}
		dot := styleSessionNone.Render(" " + iconDot)
		if wt.HasSession {
			dot = styleSessionActive.Render(" " + iconDot)
		}
		claude := m.renderClaudeHint(wt.HasSession, wt.ClaudeState)
		switch {
		case selected:
			return styleWorktreeSelected.Render(body) + status + dot + claude
		case inVisual:
			return styleWorktreeVisual.Render(body) + status + dot + claude
		}
		return styleWorktreeItem.Render(body) + status + dot + claude
	}

	icon := m.icon(ws)

	expandHint := ""
	if !m.activeView && canExpand(ws) {
		if m.expanded[ws.Path] {
			expandHint = " ▾"
		} else {
			expandHint = " ▸"
		}
	}

	// Build plain-text body so Width calculation inside Render is accurate.
	body := icon + ws.Name + expandHint

	// Coloured parts are appended after Render to stay outside the width budget.
	branch := ""
	if ws.Branch != "" {
		branch = styleBranch.Render(" " + ws.Branch)
	}
	dot := styleSessionNone.Render(" " + iconDot)
	if ws.HasSession {
		dot = styleSessionActive.Render(" " + iconDot)
	}
	if m.fetchingPaths[ws.Path] {
		dot = styleSpinner.Render(" " + spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
	}
	claude := m.renderClaudeHint(ws.HasSession, ws.ClaudeState)

	switch {
	case selected:
		bodyWidth := m.width - 2 - lipgloss.Width(branch) - lipgloss.Width(dot) - lipgloss.Width(claude)
		if bodyWidth < lipgloss.Width(body) {
			bodyWidth = lipgloss.Width(body)
		}
		return styleSelected.Width(bodyWidth).Render(body) + branch + dot + claude
	case inVisual:
		bodyWidth := m.width - 2 - lipgloss.Width(branch) - lipgloss.Width(dot) - lipgloss.Width(claude)
		if bodyWidth < lipgloss.Width(body) {
			bodyWidth = lipgloss.Width(body)
		}
		return styleVisual.Width(bodyWidth).Render(body) + branch + dot + claude
	}
	return styleNormal.Render(body) + branch + dot + claude
}

// renderClaudeHint renders a small indicator of a workspace's Claude Code
// turn state (running / waiting on you / needs attention), or "" if there's
// no session or no known state.
func (m Model) renderClaudeHint(hasSession bool, state scanner.ClaudeState) string {
	if !hasSession {
		return ""
	}
	switch state {
	case scanner.ClaudeStateRunning:
		return styleSpinner.Render(" " + spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
	case scanner.ClaudeStateWaiting:
		return styleClaudeWaiting.Render(" " + iconClaudeWaiting)
	case scanner.ClaudeStateAttention:
		return styleClaudeAttention.Render(" " + iconClaudeAttention)
	default:
		return ""
	}
}

// renderDeleteConfirm renders the confirmation prompt for m.deleteTargets: a
// single-line summary for one target, or a listed breakdown (flagging dirty
// or unpushed targets) when several are queued from a visual selection.
func (m Model) renderDeleteConfirm() string {
	warn := lipgloss.NewStyle().Foreground(colRed)

	if len(m.deleteTargets) == 1 {
		t := m.deleteTargets[0]
		what := "worktree "
		if t.kind == deleteWorkspaceKind {
			what = "workspace "
		}
		return warn.Render("  delete "+what+t.label+deleteTargetNote(t)+"? ") +
			styleHints.Render("y: confirm  any other key: cancel")
	}

	var b strings.Builder
	b.WriteString(warn.Render(fmt.Sprintf("  delete %d selected:", len(m.deleteTargets))) + "\n")
	for _, t := range m.deleteTargets {
		kind := "worktree"
		if t.kind == deleteWorkspaceKind {
			kind = "workspace"
		}
		b.WriteString(warn.Render("    "+kind+" "+t.label+deleteTargetNote(t)) + "\n")
	}
	b.WriteString(styleHints.Render("y: confirm  any other key: cancel"))
	return b.String()
}

// deleteTargetNote renders the inline warnings shown next to a queued
// delete target: dirty/unpushed state, and whether a tmux session will also
// be killed.
func deleteTargetNote(t deleteTarget) string {
	note := ""
	if t.dirty {
		note += " [dirty]"
	}
	if t.unpushed {
		note += " [unpushed]"
	}
	if t.hasSession {
		note += " +session"
	}
	return note
}

// renderPRList renders the Ctrl+R PR picker in place of the workspace list.
func (m Model) renderPRList() string {
	if m.prLoading && len(m.prAll) == 0 {
		return styleHints.Render(" " + spinnerFrames[m.spinnerFrame%len(spinnerFrames)] + " loading PRs...")
	}
	if len(m.prFiltered) == 0 {
		return styleHints.Render(" no open PRs")
	}

	height := m.listHeight()
	start, end := 0, len(m.prFiltered)
	if end > height {
		start = m.prCursor - height/2
		if start < 0 {
			start = 0
		}
		end = start + height
		if end > len(m.prFiltered) {
			end = len(m.prFiltered)
			start = end - height
			if start < 0 {
				start = 0
			}
		}
	}

	var b strings.Builder
	for i := start; i < end; i++ {
		pr := m.prFiltered[i]
		body := fmt.Sprintf(" #%-6d %s", pr.Number, pr.Title)
		author := styleBranch.Render(" @" + pr.Author)
		if i == m.prCursor {
			b.WriteString(styleSelected.Render(body) + author + "\n")
		} else {
			b.WriteString(styleNormal.Render(body) + author + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// humanizeSince renders t as a short relative age, e.g. "2h ago", "3d ago".
func humanizeSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

func (m Model) icon(ws scanner.Workspace) string {
	if ws.Type != scanner.TypeGitRepo {
		if !m.cfg.NerdFontIcons {
			return "dir "
		}
		return iconDir + " "
	}
	if ws.IsBare {
		if !m.cfg.NerdFontIcons {
			return "bare "
		}
		return iconWorktree + " "
	}
	if !m.cfg.NerdFontIcons {
		return "git "
	}
	return iconGit + " "
}

// worktreeIcon returns the icon prefix for a linked worktree row (a checked
// out branch nested under a repo), distinct from icon()'s top-level
// repo/dir/bare-container icon.
func (m Model) worktreeIcon() string {
	if !m.cfg.NerdFontIcons {
		return "wt "
	}
	return iconWorktree + " "
}

// toggleActiveView flips between the full workspace tree and the flattened
// list of workspaces/worktrees that currently have a live tmux session, so a
// second tab press always returns to the exact view just left.
func (m *Model) toggleActiveView() {
	m.activeView = !m.activeView
	if m.activeView {
		m.input.Placeholder = "search active worktrees..."
	} else {
		m.input.Placeholder = "search workspaces..."
	}
	m.cursor = 0
	m.refilter()
}

// expandSelected implements vim tree-navigation "l": if the selected row is a
// collapsed, expandable repo, reveal its worktrees instead of opening it.
// Returns true if it expanded, so the caller skips the open action. The
// active view is already flat, so it never expands — "l" just opens.
func (m *Model) expandSelected() bool {
	if m.activeView || m.cursor >= len(m.filtered) {
		return false
	}
	item := m.filtered[m.cursor]
	if item.wtIdx >= 0 || !canExpand(item.ws) || m.expanded[item.ws.Path] {
		return false
	}
	m.expanded[item.ws.Path] = true
	m.refilter()
	return true
}

// canExpand reports whether ws's top-level row has worktree children worth
// revealing. A bare-layout repo (ws.Path itself has no working tree) can
// expand with even a single worktree; a plain repo's Worktrees list already
// includes itself as the first entry, so it only expands past that.
func canExpand(ws scanner.Workspace) bool {
	if ws.Type != scanner.TypeGitRepo {
		return false
	}
	if ws.IsBare {
		return len(ws.Worktrees) >= 1
	}
	return len(ws.Worktrees) > 1
}

func (m *Model) startCreateWorktree() {
	if m.cursor >= len(m.filtered) {
		return
	}
	item := m.filtered[m.cursor]
	if item.ws.Type != scanner.TypeGitRepo {
		return
	}
	prefill := m.cfg.BranchPrefixFor(item.ws.Path)
	if prefill != "" {
		prefill += "/"
	}
	m.beginWorktreePrompt(item.ws.Path, item.ws.Name, prefill)
}

// beginWorktreePrompt switches the search box into branch-entry mode for
// repoPath, optionally pre-filling a suggested branch (e.g. a remote's
// default branch right after cloning).
func (m *Model) beginWorktreePrompt(repoPath, repoName, prefill string) {
	m.creatingWorktree = true
	m.createRepoPath = repoPath
	m.input.SetValue(prefill)
	m.input.CursorEnd()
	m.input.Placeholder = "new worktree branch for " + repoName + "..."
	m.input.Focus()
}

func (m *Model) cancelCreateWorktree() {
	m.creatingWorktree = false
	m.createRepoPath = ""
	m.input.SetValue("")
	m.input.Placeholder = "search workspaces..."
	m.restoreFocusForMode()
}

// restoreFocusForMode re-applies vim mode's focus invariant (input focused
// only in insert mode) after leaving a modal prompt like worktree creation.
func (m *Model) restoreFocusForMode() {
	if m.cfg.VimMode && m.mode == modeNormal {
		m.input.Blur()
	} else {
		m.input.Focus()
	}
}

func (m Model) createWorktree(repoPath, branch string) tea.Cmd {
	return func() tea.Msg {
		_, err := scanner.AddWorktree(repoPath, branch)
		return worktreeCreatedMsg{repoPath: repoPath, err: err}
	}
}

// startDeleteWorktree arms the delete confirmation for the selected worktree
// (Ctrl+X). Only linked worktrees (wtIdx >= 0) qualify — the main
// worktree/bare container row is never deletable this way; use "dd" (via
// startDeleteWorkspace) to delete a whole workspace.
func (m *Model) startDeleteWorktree() {
	if m.cursor >= len(m.filtered) {
		return
	}
	item := m.filtered[m.cursor]
	if item.wtIdx < 0 {
		return
	}
	m.confirmingDelete = true
	m.deleteTargets = []deleteTarget{worktreeDeleteTarget(item.ws, item.wtIdx)}
}

// startDeleteWorkspace arms the delete confirmation for the selected row's
// entire workspace directory (all worktrees + git history, or a plain
// directory). Repo rows only (wtIdx == -1).
func (m *Model) startDeleteWorkspace() {
	if m.cursor >= len(m.filtered) {
		return
	}
	item := m.filtered[m.cursor]
	if item.wtIdx >= 0 {
		return
	}
	m.confirmingDelete = true
	m.deleteTargets = []deleteTarget{workspaceDeleteTarget(item.ws)}
}

// startDeleteSelected implements vim "dd": delete whatever's on the current
// row — a linked worktree, or the whole workspace if the row is a repo.
func (m *Model) startDeleteSelected() {
	if m.cursor >= len(m.filtered) {
		return
	}
	if m.filtered[m.cursor].wtIdx >= 0 {
		m.startDeleteWorktree()
		return
	}
	m.startDeleteWorkspace()
}

// startDeleteVisualSelection arms the delete confirmation for every row
// spanned by visual mode, then returns to normal mode.
func (m *Model) startDeleteVisualSelection() {
	targets := m.deleteTargetsForVisualSelection()
	m.mode = modeNormal
	if len(targets) == 0 {
		return
	}
	m.confirmingDelete = true
	m.deleteTargets = targets
}

// deleteTargetsForVisualSelection builds one deleteTarget per row in the
// visual selection, deduplicated so that a repo row selected alongside its
// own worktree rows only produces a single whole-workspace target (deleting
// the workspace already removes its worktrees).
func (m Model) deleteTargetsForVisualSelection() []deleteTarget {
	lo, hi := m.visualSelectionRange()

	workspaceSelected := make(map[string]bool)
	for i := lo; i <= hi && i < len(m.filtered); i++ {
		item := m.filtered[i]
		if item.wtIdx < 0 {
			workspaceSelected[item.ws.Path] = true
		}
	}

	var targets []deleteTarget
	seenWorkspace := make(map[string]bool)
	for i := lo; i <= hi && i < len(m.filtered); i++ {
		item := m.filtered[i]
		if item.wtIdx < 0 {
			if seenWorkspace[item.ws.Path] {
				continue
			}
			seenWorkspace[item.ws.Path] = true
			targets = append(targets, workspaceDeleteTarget(item.ws))
			continue
		}
		if workspaceSelected[item.ws.Path] {
			continue // parent workspace target already covers this worktree
		}
		targets = append(targets, worktreeDeleteTarget(item.ws, item.wtIdx))
	}
	return targets
}

func worktreeDeleteTarget(ws scanner.Workspace, wtIdx int) deleteTarget {
	wt := ws.Worktrees[wtIdx]
	return deleteTarget{
		kind:        deleteWorktreeKind,
		repoPath:    ws.Path,
		path:        wt.Path,
		label:       wt.Branch,
		sessionName: wt.TmuxSession,
		hasSession:  wt.HasSession,
		dirty:       wt.Dirty,
		unpushed:    !wt.PushedRemote,
	}
}

func workspaceDeleteTarget(ws scanner.Workspace) deleteTarget {
	t := deleteTarget{
		kind:        deleteWorkspaceKind,
		repoPath:    ws.Path,
		path:        ws.Path,
		label:       ws.Name,
		sessionName: ws.TmuxSession,
		hasSession:  ws.HasSession,
		ws:          ws,
	}
	for _, wt := range ws.Worktrees {
		if wt.Dirty {
			t.dirty = true
		}
		if !wt.PushedRemote {
			t.unpushed = true
		}
	}
	return t
}

func (m *Model) cancelDelete() {
	m.confirmingDelete = false
	m.deleteTargets = nil
}

// executeDelete carries out every target, killing tmux sessions and pruning
// state as it goes, then reports which repos need their worktree list
// refreshed (worktree-kind targets) and which workspace paths no longer
// exist on disk at all (workspace-kind targets).
func (m Model) executeDelete(targets []deleteTarget) tea.Cmd {
	st := m.st
	return func() tea.Msg {
		var msg bulkDeletedMsg
		for _, t := range targets {
			switch t.kind {
			case deleteWorktreeKind:
				if err := scanner.RemoveWorktree(t.repoPath, t.path); err != nil {
					msg.errs = append(msg.errs, err)
					continue
				}
				if t.hasSession {
					_ = tmux.KillSession(t.sessionName)
				}
				st.Remove(t.path)
				msg.refreshRepoPaths = append(msg.refreshRepoPaths, t.repoPath)

			case deleteWorkspaceKind:
				if err := scanner.RemoveWorkspace(t.ws); err != nil {
					msg.errs = append(msg.errs, err)
					continue
				}
				for _, wt := range t.ws.Worktrees {
					if wt.HasSession {
						_ = tmux.KillSession(wt.TmuxSession)
					}
					st.Remove(wt.Path)
				}
				if t.hasSession {
					_ = tmux.KillSession(t.sessionName)
				}
				st.Remove(t.path)
				msg.removedPaths = append(msg.removedPaths, t.path)
			}
		}
		_ = st.Save()
		return msg
	}
}

func joinErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// removeWorkspace drops path from the workspace list entirely, for use after
// its whole directory has been deleted from disk (as opposed to
// refreshWorkspace, which rescans a workspace that still exists).
func (m *Model) removeWorkspace(path string) {
	for i := range m.all {
		if m.all[i].Path == path {
			m.all = append(m.all[:i], m.all[i+1:]...)
			break
		}
	}
	delete(m.expanded, path)
	m.refilter()
}

func (m *Model) startCloneRepo() {
	m.cloningRepo = true
	m.input.SetValue("")
	m.input.Placeholder = "clone repo (owner/repo or git URL)..."
	m.input.Focus()
}

func (m *Model) cancelCloneRepo() {
	m.cloningRepo = false
	m.input.SetValue("")
	m.input.Placeholder = "search workspaces..."
	m.restoreFocusForMode()
}

func (m Model) cloneRepo(src string) tea.Cmd {
	scanPaths := m.cfg.ScanPaths
	defaultHost := m.cfg.DefaultGitHost
	protocol := m.cfg.CloneProtocol

	return func() tea.Msg {
		if len(scanPaths) == 0 {
			return repoClonedMsg{err: fmt.Errorf("no scan_paths configured")}
		}
		url, name, owner, err := scanner.ResolveCloneSource(src, defaultHost, protocol)
		if err != nil {
			return repoClonedMsg{err: err}
		}
		scanRoot := scanPaths[0]
		for _, p := range scanPaths {
			if strings.EqualFold(filepath.Base(p), owner) {
				scanRoot = p
				break
			}
		}
		repoPath, branch, err := scanner.CloneBare(scanRoot, name, url)
		if err != nil {
			return repoClonedMsg{err: err}
		}
		return repoClonedMsg{repoPath: repoPath, repoName: name, defaultBranch: branch}
	}
}

// startFetch runs `git fetch origin` for the selected repo in the background,
// so new remote branches (bots, teammates, CI) become available to Ctrl+T.
func (m *Model) startFetch() tea.Cmd {
	if m.cursor >= len(m.filtered) {
		return nil
	}
	item := m.filtered[m.cursor]
	if item.ws.Type != scanner.TypeGitRepo {
		return nil
	}
	return m.startFetchMany([]string{item.ws.Path})
}

// startFetchVisualSelection fetches every repo spanned by visual mode, then
// returns to normal mode.
func (m *Model) startFetchVisualSelection() tea.Cmd {
	paths := m.fetchPathsForVisualSelection()
	m.mode = modeNormal
	return m.startFetchMany(paths)
}

// fetchPathsForVisualSelection returns the deduplicated repo paths among the
// rows spanned by visual mode (worktree rows resolve to their parent repo).
func (m Model) fetchPathsForVisualSelection() []string {
	lo, hi := m.visualSelectionRange()
	seen := make(map[string]bool)
	var paths []string
	for i := lo; i <= hi && i < len(m.filtered); i++ {
		item := m.filtered[i]
		if item.ws.Type != scanner.TypeGitRepo || seen[item.ws.Path] {
			continue
		}
		seen[item.ws.Path] = true
		paths = append(paths, item.ws.Path)
	}
	return paths
}

// startFetchMany runs `git fetch origin` for each of paths concurrently in
// the background, skipping any already in flight.
func (m *Model) startFetchMany(paths []string) tea.Cmd {
	var cmds []tea.Cmd
	for _, p := range paths {
		if m.fetchingPaths[p] {
			continue
		}
		m.fetchingPaths[p] = true
		repoPath := p
		cmds = append(cmds, func() tea.Msg {
			return repoFetchedMsg{repoPath: repoPath, err: scanner.Fetch(repoPath)}
		})
	}
	if len(cmds) == 0 {
		return nil
	}
	m.spinnerFrame = 0
	cmds = append(cmds, spinnerTick())
	return tea.Batch(cmds...)
}

func spinnerTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// startPruneMerged implements Ctrl+P: it collects every worktree that can
// be safely cleaned up — those whose HEAD is merged to local main/master
// (MergedLocal), those whose remote-tracking ref has disappeared after a
// prune (RemoteMissing), and PR-created worktrees (pr-<n>) whose GitHub
// PR is in "MERGED" state (checked via gh pr view). The combined list is
// shown in the existing delete confirmation dialog, reusing all of its
// infrastructure (dirty/unpushed warnings, tmux session kill, state removal).
func (m *Model) startPruneMerged() tea.Cmd {
	type prTarget struct {
		ws   scanner.Workspace
		idx  int
		prN  int
	}

	var immediate []deleteTarget
	var prs []prTarget

	for _, ws := range m.all {
		for j, wt := range ws.Worktrees {
			if wt.MergedLocal || wt.RemoteMissing {
				immediate = append(immediate, worktreeDeleteTarget(ws, j))
			}
			if m := scanner.PRBranchPattern.FindStringSubmatch(wt.Branch); m != nil && !wt.MergedLocal && !wt.RemoteMissing {
				n, err := strconv.Atoi(m[1])
				if err != nil {
					continue
				}
				prs = append(prs, prTarget{ws: ws, idx: j, prN: n})
			}
		}
	}

	if len(prs) == 0 {
		if len(immediate) == 0 {
			return nil
		}
		m.confirmingDelete = true
		m.deleteTargets = immediate
		return nil
	}

	m.pruningPRs++
	token := m.pruningPRs

	return func() tea.Msg {
		var all []deleteTarget
		all = append(all, immediate...)
		for _, pr := range prs {
			if scanner.PRMerged(pr.ws.Path, pr.prN) {
				all = append(all, worktreeDeleteTarget(pr.ws, pr.idx))
			}
		}
		return pruneReadyMsg{token: token, targets: all}
	}
}

// refreshWorkspace re-detects the workspace at path (branch + worktree list)
// after an external change, preserving known tmux session state, then
// expands it so the change is immediately visible. If path isn't already
// known (e.g. a freshly cloned repo), it's appended. The returned tea.Cmd
// re-fetches status (dirty/merged/pushed/last-commit) for the rescanned
// worktrees, since Rescan itself only returns the cheap structural info.
func (m *Model) refreshWorkspace(path string) tea.Cmd {
	for i := range m.all {
		if m.all[i].Path != path {
			continue
		}
		old := m.all[i]
		fresh := scanner.Rescan(path)
		fresh.HasSession = old.HasSession
		fresh.TmuxSession = old.TmuxSession
		for j := range fresh.Worktrees {
			for _, ow := range old.Worktrees {
				if ow.Path == fresh.Worktrees[j].Path {
					fresh.Worktrees[j].HasSession = ow.HasSession
					fresh.Worktrees[j].TmuxSession = ow.TmuxSession
					break
				}
			}
		}
		m.all[i] = fresh
		m.expanded[path] = true
		m.refilter()
		m.selectWorkspace(path)
		return annotateWorktreesCmd(fresh)
	}

	fresh := scanner.Rescan(path)
	m.all = append(m.all, fresh)
	m.expanded[path] = true
	m.refilter()
	m.selectWorkspace(path)
	return annotateWorktreesCmd(fresh)
}

// applyWorktreeStatus patches in a background-fetched status result for a
// single worktree, then refilters so the change (and any icons it affects)
// is picked up by the next render.
func (m *Model) applyWorktreeStatus(msg worktreeStatusMsg) {
	for i := range m.all {
		if m.all[i].Path != msg.repoPath {
			continue
		}
		for j := range m.all[i].Worktrees {
			if m.all[i].Worktrees[j].Path != msg.worktreePath {
				continue
			}
			wt := &m.all[i].Worktrees[j]
			wt.Dirty = msg.status.Dirty
			wt.MergedLocal = msg.status.MergedLocal
			wt.PushedRemote = msg.status.PushedRemote
			wt.RemoteMissing = msg.status.RemoteMissing
			wt.LastCommit = msg.status.LastCommit
			wt.ClaudeState = msg.status.ClaudeState
			break
		}
		break
	}
	m.refilter()
}

// selectWorkspace moves the cursor to the top-level entry for path, if present.
func (m *Model) selectWorkspace(path string) {
	for i, item := range m.filtered {
		if item.wtIdx == -1 && item.ws.Path == path {
			m.cursor = i
			return
		}
	}
}

func (m *Model) refilter() {
	if m.activeView {
		m.filtered = m.buildActiveFiltered()
	} else {
		m.filtered = m.buildTreeFiltered()
	}
	if m.cursor >= len(m.filtered) && len(m.filtered) > 0 {
		m.cursor = len(m.filtered) - 1
	}
}

// buildTreeFiltered builds the normal, expandable workspace tree, fuzzy
// filtered by the search box over workspace names.
func (m *Model) buildTreeFiltered() []listItem {
	query := m.input.Value()
	var base []scanner.Workspace

	if query == "" {
		base = m.all
	} else {
		names := make([]string, len(m.all))
		for i, ws := range m.all {
			names[i] = ws.Name
		}
		matches := fuzzy.Find(query, names)
		base = make([]scanner.Workspace, 0, len(matches))
		for _, match := range matches {
			base = append(base, m.all[match.Index])
		}
	}

	var final []listItem
	for _, ws := range base {
		parentIdx := len(final)
		final = append(final, listItem{ws: ws, wtIdx: -1})
		if ws.Type == scanner.TypeGitRepo && m.expanded[ws.Path] {
			for j := range ws.Worktrees {
				final = append(final, listItem{ws: ws, wtIdx: j, parentIdx: parentIdx})
			}
		}
	}
	return final
}

// buildActiveFiltered builds the flattened "active view" list: one row per
// workspace or worktree that currently has a live tmux session, in the same
// order as m.all (already MRU-sorted at scan time), fuzzy filtered by the
// search box over "repo" or "repo/branch".
func (m *Model) buildActiveFiltered() []listItem {
	var items []listItem
	var labels []string

	for _, ws := range m.all {
		if ws.HasSession {
			items = append(items, listItem{ws: ws, wtIdx: -1, parentIdx: -1})
			labels = append(labels, ws.Name)
		}
		for j, wt := range ws.Worktrees {
			// A plain (non-bare) repo's main worktree shares ws.Path and was
			// already covered by the workspace-level row above.
			if wt.Path == ws.Path || !wt.HasSession {
				continue
			}
			items = append(items, listItem{ws: ws, wtIdx: j, parentIdx: -1})
			labels = append(labels, ws.Name+"/"+wt.Branch)
		}
	}

	query := m.input.Value()
	if query == "" {
		return items
	}

	matches := fuzzy.Find(query, labels)
	filtered := make([]listItem, 0, len(matches))
	for _, match := range matches {
		filtered = append(filtered, items[match.Index])
	}
	return filtered
}

func (m *Model) visibleRange(height int) (int, int) {
	total := len(m.filtered)
	if total <= height {
		return 0, total
	}
	start := m.cursor - height/2
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > total {
		end = total
		start = end - height
		if start < 0 {
			start = 0
		}
	}
	return start, end
}

func (m Model) openSelected() tea.Cmd {
	if m.cursor >= len(m.filtered) {
		return nil
	}
	item := m.filtered[m.cursor]
	if item.wtIdx < 0 && item.ws.IsBare {
		// The bare container itself has no working tree to open — only its
		// worktrees (revealed by expanding) are checkoutable.
		return nil
	}

	var dir, branch string
	if item.wtIdx >= 0 {
		wt := item.ws.Worktrees[item.wtIdx]
		dir = wt.Path
		branch = wt.Branch
	} else {
		dir = item.ws.Path
		branch = item.ws.Branch
	}

	var sessionName string
	if item.wtIdx >= 0 && branch != "" {
		sessionName = tmux.WorktreeSessionName(m.cfg.SessionPrefix, item.ws.Name, branch)
	} else {
		sessionName = tmux.SessionName(m.cfg.SessionPrefix, dir)
	}

	return m.openPath(dir, sessionName, branch)
}

// openPath prepares and opens a tmux session for dir/sessionName. The "diff"
// window runs cfg.ReviewCommand (substituted for that PR number) if branch
// matches the "pr-<n>" convention FetchPR creates, else cfg.DiffCommand.
func (m Model) openPath(dir, sessionName, branch string) tea.Cmd {
	diffCmd := scanner.ReviewWindow(branch, m.cfg.ReviewCommand)
	if diffCmd == "" {
		diffCmd = m.cfg.DiffCommand
	}

	cfg := m.cfg
	st := m.st

	return func() tea.Msg {
		attachCmd, err := tmux.PrepareOpen(sessionName, dir, cfg.Editor, cfg.AssistantFor(dir), diffCmd)
		if err != nil {
			return errMsg(err)
		}
		st.Touch(dir, sessionName)
		_ = st.Save()
		return sessionReadyMsg{attachCmd: attachCmd}
	}
}

// startPickPR opens the Ctrl+R PR picker for the selected bare-repo row,
// kicking off an async gh pr list load. Returns nil if the selection is not
// a bare repo container row.
func (m *Model) startPickPR() tea.Cmd {
	if m.cursor >= len(m.filtered) {
		return nil
	}
	item := m.filtered[m.cursor]
	if item.wtIdx != -1 || !item.ws.IsBare {
		return nil
	}

	m.pickingPR = true
	m.prRepoPath = item.ws.Path
	m.prAll = nil
	m.prFiltered = nil
	m.prCursor = 0
	m.prLoading = true
	m.spinnerFrame = 0
	m.input.SetValue("")
	m.input.Placeholder = "filter PRs for " + item.ws.Name + "..."
	m.restoreFocusForMode()

	return m.loadPRs(item.ws.Path)
}

func (m *Model) cancelPickPR() {
	m.pickingPR = false
	m.prRepoPath = ""
	m.prAll = nil
	m.prFiltered = nil
	m.prLoading = false
	m.input.SetValue("")
	m.input.Placeholder = "search workspaces..."
	m.restoreFocusForMode()
}

// updatePickPRNormal handles keys in the PR picker while in vim Normal
// mode: single keys navigate and act, matching the main list's convention.
func (m Model) updatePickPRNormal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.cancelPickPR()
		return m, nil
	case "i", "a":
		m.mode = modeInsert
		m.input.CursorEnd()
		return m, m.input.Focus()
	case "/":
		m.input.SetValue("")
		m.refilterPR()
		m.mode = modeInsert
		return m, m.input.Focus()
	case "enter":
		return m.confirmPickPR()
	case "j", "down", "ctrl+n", "ctrl+j":
		m.movePRCursor(1)
		return m, nil
	case "k", "up", "ctrl+p", "ctrl+k":
		m.movePRCursor(-1)
		return m, nil
	}
	return m, nil
}

// updatePickPRInsert handles keys in the PR picker while typing filters —
// either because vim mode is disabled, or the user pressed "i"/"a"/"/".
func (m Model) updatePickPRInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.cfg.VimMode {
			m.mode = modeNormal
			m.input.Blur()
			return m, nil
		}
		m.cancelPickPR()
		return m, nil
	case "ctrl+c":
		m.cancelPickPR()
		return m, nil
	case "enter":
		return m.confirmPickPR()
	case "up", "ctrl+p":
		m.movePRCursor(-1)
		return m, nil
	case "down", "ctrl+n", "ctrl+j":
		m.movePRCursor(1)
		return m, nil
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.refilterPR()
		return m, cmd
	}
}

// confirmPickPR checks out the highlighted PR, closing the picker first.
func (m Model) confirmPickPR() (tea.Model, tea.Cmd) {
	if m.prCursor >= len(m.prFiltered) {
		return m, nil
	}
	pr := m.prFiltered[m.prCursor]
	repoPath := m.prRepoPath
	m.cancelPickPR()
	return m, m.checkoutPR(repoPath, pr.Number)
}

func (m Model) loadPRs(repoPath string) tea.Cmd {
	load := func() tea.Msg {
		prs, err := scanner.ListOpenPRs(repoPath)
		return prListMsg{repoPath: repoPath, prs: prs, err: err}
	}
	return tea.Batch(load, spinnerTick())
}

func (m *Model) movePRCursor(delta int) {
	if len(m.prFiltered) == 0 {
		return
	}
	m.prCursor += delta
	if m.prCursor < 0 {
		m.prCursor = 0
	}
	if m.prCursor > len(m.prFiltered)-1 {
		m.prCursor = len(m.prFiltered) - 1
	}
}

// refilterPR rebuilds prFiltered from prAll using the shared search input as
// a fuzzy filter over "#<number> <title> <author>".
func (m *Model) refilterPR() {
	query := m.input.Value()
	if query == "" {
		m.prFiltered = m.prAll
		if m.prCursor >= len(m.prFiltered) {
			m.prCursor = 0
		}
		return
	}

	labels := make([]string, len(m.prAll))
	for i, pr := range m.prAll {
		labels[i] = fmt.Sprintf("#%d %s %s", pr.Number, pr.Title, pr.Author)
	}
	matches := fuzzy.Find(query, labels)
	filtered := make([]scanner.PRInfo, 0, len(matches))
	for _, match := range matches {
		filtered = append(filtered, m.prAll[match.Index])
	}
	m.prFiltered = filtered
	if m.prCursor >= len(m.prFiltered) {
		m.prCursor = 0
	}
}

// checkoutPR fetches PR n for repoPath and creates a worktree for it, then
// reports the session name to open (WorktreeSessionName needs the repo name,
// computed here rather than threaded through the message).
func (m Model) checkoutPR(repoPath string, n int) tea.Cmd {
	repoName := filepath.Base(repoPath)
	sessionPrefix := m.cfg.SessionPrefix

	return func() tea.Msg {
		branch, err := scanner.FetchPR(repoPath, n)
		if err != nil {
			return prCheckedOutMsg{repoPath: repoPath, err: err}
		}
		dest, err := scanner.AddWorktree(repoPath, branch)
		if err != nil {
			return prCheckedOutMsg{repoPath: repoPath, err: err}
		}
		sessionName := tmux.WorktreeSessionName(sessionPrefix, repoName, branch)
		return prCheckedOutMsg{repoPath: repoPath, worktreePath: dest, sessionName: sessionName, branch: branch}
	}
}
