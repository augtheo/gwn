package ui

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
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
type worktreeDeletedMsg struct {
	repoPath string
	err      error
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

// prBranchPattern recognizes the local branch names FetchPR creates
// ("pr-<n>"), so openPath knows to add a "diff" review window.
var prBranchPattern = regexp.MustCompile(`^pr-(\d+)$`)

type mode int

const (
	modeNormal mode = iota
	modeInsert
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

	mode         mode
	pendingG     bool // "g" seen, waiting for a second "g" (gg = go to top)
	pendingCount int  // numeric prefix accumulated so far, e.g. "5" in "5j"

	creatingWorktree bool
	createRepoPath   string

	cloningRepo bool

	confirmingDeleteWorktree bool
	deleteRepoPath           string
	deleteWorktreePath       string
	deleteWorktreeLabel      string
	deleteSessionName        string
	deleteHasSession         bool

	fetchingPath string
	spinnerFrame int

	pickingPR  bool
	prRepoPath string
	prAll      []scanner.PRInfo
	prFiltered []scanner.PRInfo
	prCursor   int
	prLoading  bool
}

type listItem struct {
	ws        scanner.Workspace
	wtIdx     int // -1 = the repo itself, >=0 = worktree index
	parentIdx int // index in filtered of the parent repo (for worktrees)
}

func New(cfg *config.Config, st *state.State, workspaces []scanner.Workspace) Model {
	ti := textinput.New()
	ti.Placeholder = "search workspaces..."
	ti.Focus()
	ti.CharLimit = 80
	ti.PromptStyle = lipgloss.NewStyle().Foreground(colBlue)
	ti.TextStyle = lipgloss.NewStyle().Foreground(colText)

	m := Model{
		cfg:      cfg,
		st:       st,
		all:      workspaces,
		input:    ti,
		expanded: make(map[string]bool),
		mode:     modeInsert,
	}
	if cfg.VimMode {
		m.mode = modeNormal
		m.input.Blur()
	}
	m.refilter()
	return m
}

func (m Model) Init() tea.Cmd {
	return textinput.Blink
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

		if m.confirmingDeleteWorktree {
			switch msg.String() {
			case "y", "enter":
				repoPath, worktreePath := m.deleteRepoPath, m.deleteWorktreePath
				sessionName, hasSession := m.deleteSessionName, m.deleteHasSession
				m.cancelDeleteWorktree()
				return m, m.deleteWorktree(repoPath, worktreePath, sessionName, hasSession)
			default:
				m.cancelDeleteWorktree()
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
			m.toggleExpand()
			return m, nil
		case "ctrl+w":
			m.startCreateWorktree()
			return m, nil
		case "ctrl+g":
			m.startCloneRepo()
			return m, nil
		case "ctrl+f":
			return m, m.startFetch()
		case "ctrl+x":
			m.startDeleteWorktree()
			return m, nil
		case "ctrl+r":
			return m, m.startPickPR()
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
		m.refreshWorkspace(msg.repoPath)
		return m, nil

	case repoClonedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.refreshWorkspace(msg.repoPath)
		m.beginWorktreePrompt(msg.repoPath, msg.repoName, msg.defaultBranch)
		return m, nil

	case repoFetchedMsg:
		if m.fetchingPath == msg.repoPath {
			m.fetchingPath = ""
		}
		m.err = msg.err
		return m, nil

	case worktreeDeletedMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.err = nil
		m.refreshWorkspace(msg.repoPath)
		return m, nil

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
		m.refreshWorkspace(msg.repoPath)
		return m, m.openPath(msg.worktreePath, msg.sessionName, msg.branch)

	case spinnerTickMsg:
		if m.fetchingPath == "" && !m.prLoading {
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
	case "up", "ctrl+p", "ctrl+k":
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
			return m, nil
		}
	}

	hadCount := m.pendingCount > 0
	count := m.pendingCount
	if count == 0 {
		count = 1
	}
	m.pendingCount = 0

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
	if m.cursor >= len(m.filtered) {
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

	b.WriteString(styleTitle.Render("gwn") + "\n")

	inputWidth := m.width - 6
	if inputWidth < 20 {
		inputWidth = 20
	}
	inputBorder := styleInput
	if m.cfg.VimMode && m.mode == modeNormal {
		inputBorder = inputBorder.BorderForeground(colOverlay)
	}
	b.WriteString(inputBorder.Width(inputWidth).Render(m.input.View()) + "\n\n")

	if m.pickingPR {
		b.WriteString(m.renderPRList() + "\n")
	} else {
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
	case m.confirmingDeleteWorktree:
		sessionNote := ""
		if m.deleteHasSession {
			sessionNote = " + kill its tmux session"
		}
		b.WriteString(lipgloss.NewStyle().Foreground(colRed).Render("  delete worktree "+m.deleteWorktreeLabel+sessionNote+"? ") +
			styleHints.Render("y: confirm  any other key: cancel"))
	case m.pickingPR:
		b.WriteString(styleHints.Render("enter: checkout PR  esc: cancel"))
	case m.cfg.VimMode && m.mode == modeNormal:
		b.WriteString(lipgloss.NewStyle().Foreground(colBlue).Bold(true).Render(" NORMAL ") +
			styleHints.Render(" i//: search  j/k gg/G ^d/^u: move  enter/l: open  h: collapse  tab: expand  ^w/^g/^f/^x/^r: worktree/clone/fetch/delete/review  q: quit"))
	case m.cfg.VimMode:
		b.WriteString(lipgloss.NewStyle().Foreground(colGreen).Bold(true).Render(" INSERT ") +
			styleHints.Render(" esc: normal mode  enter: open  tab: expand  ↑↓: navigate  ^w/^g/^f/^x/^r: worktree/clone/fetch/delete/review"))
	default:
		b.WriteString(styleHints.Render("enter: open  tab: expand worktrees  ctrl+w: new worktree  ctrl+g: clone repo  ctrl+f: fetch  ctrl+x: delete worktree  ctrl+r: review PRs  ↑↓: navigate  esc/ctrl+c: quit"))
	}

	return b.String()
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
	ws := item.ws

	if item.wtIdx >= 0 {
		wt := ws.Worktrees[item.wtIdx]
		// Plain text body first, coloured parts appended outside the style render
		// to avoid ANSI codes inside a Width-constrained Render call.
		body := "   " + wt.Branch
		dot := styleSessionNone.Render(" " + iconDot)
		if wt.HasSession {
			dot = styleSessionActive.Render(" " + iconDot)
		}
		if selected {
			return styleWorktreeSelected.Render(body) + dot
		}
		return styleWorktreeItem.Render(body) + dot
	}

	icon := m.icon(ws.Type == scanner.TypeGitRepo)

	expandHint := ""
	if canExpand(ws) {
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
	if ws.Path == m.fetchingPath {
		dot = styleSpinner.Render(" " + spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
	}

	if selected {
		bodyWidth := m.width - 2 - lipgloss.Width(branch) - lipgloss.Width(dot)
		if bodyWidth < lipgloss.Width(body) {
			bodyWidth = lipgloss.Width(body)
		}
		return styleSelected.Width(bodyWidth).Render(body) + branch + dot
	}
	return styleNormal.Render(body) + branch + dot
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

func (m Model) icon(isGit bool) string {
	if !m.cfg.NerdFontIcons {
		if isGit {
			return "git "
		}
		return "dir "
	}
	if isGit {
		return iconGit + " "
	}
	return iconDir + " "
}

func (m *Model) toggleExpand() {
	if m.cursor >= len(m.filtered) {
		return
	}
	item := m.filtered[m.cursor]
	if item.wtIdx >= 0 || !canExpand(item.ws) {
		return
	}
	m.expanded[item.ws.Path] = !m.expanded[item.ws.Path]
	m.refilter()
}

// expandSelected implements vim tree-navigation "l": if the selected row is a
// collapsed, expandable repo, reveal its worktrees instead of opening it.
// Returns true if it expanded, so the caller skips the open action.
func (m *Model) expandSelected() bool {
	if m.cursor >= len(m.filtered) {
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

// startDeleteWorktree arms the delete confirmation for the selected worktree.
// Only linked worktrees (wtIdx >= 0) qualify — the main worktree/bare
// container row is never deletable this way.
func (m *Model) startDeleteWorktree() {
	if m.cursor >= len(m.filtered) {
		return
	}
	item := m.filtered[m.cursor]
	if item.wtIdx < 0 {
		return
	}
	wt := item.ws.Worktrees[item.wtIdx]
	m.confirmingDeleteWorktree = true
	m.deleteRepoPath = item.ws.Path
	m.deleteWorktreePath = wt.Path
	m.deleteWorktreeLabel = wt.Branch
	m.deleteSessionName = wt.TmuxSession
	m.deleteHasSession = wt.HasSession
}

func (m *Model) cancelDeleteWorktree() {
	m.confirmingDeleteWorktree = false
	m.deleteRepoPath = ""
	m.deleteWorktreePath = ""
	m.deleteWorktreeLabel = ""
	m.deleteSessionName = ""
	m.deleteHasSession = false
}

func (m Model) deleteWorktree(repoPath, worktreePath, sessionName string, hasSession bool) tea.Cmd {
	st := m.st
	return func() tea.Msg {
		if err := scanner.RemoveWorktree(repoPath, worktreePath); err != nil {
			return worktreeDeletedMsg{repoPath: repoPath, err: err}
		}
		if hasSession {
			_ = tmux.KillSession(sessionName)
		}
		st.Remove(worktreePath)
		_ = st.Save()
		return worktreeDeletedMsg{repoPath: repoPath}
	}
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
// so new remote branches (bots, teammates, CI) become available to Ctrl+W.
func (m *Model) startFetch() tea.Cmd {
	if m.cursor >= len(m.filtered) {
		return nil
	}
	item := m.filtered[m.cursor]
	if item.ws.Type != scanner.TypeGitRepo {
		return nil
	}
	if m.fetchingPath == item.ws.Path {
		return nil // already fetching
	}

	m.fetchingPath = item.ws.Path
	m.spinnerFrame = 0
	repoPath := item.ws.Path

	fetch := func() tea.Msg {
		return repoFetchedMsg{repoPath: repoPath, err: scanner.Fetch(repoPath)}
	}
	return tea.Batch(fetch, spinnerTick())
}

func spinnerTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

// refreshWorkspace re-detects the workspace at path (branch + worktree list)
// after an external change, preserving known tmux session state, then
// expands it so the change is immediately visible. If path isn't already
// known (e.g. a freshly cloned repo), it's appended.
func (m *Model) refreshWorkspace(path string) {
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
		return
	}

	m.all = append(m.all, scanner.Rescan(path))
	m.expanded[path] = true
	m.refilter()
	m.selectWorkspace(path)
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

	m.filtered = final
	if m.cursor >= len(m.filtered) && len(m.filtered) > 0 {
		m.cursor = len(m.filtered) - 1
	}
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

// openPath prepares and opens a tmux session for dir/sessionName. If branch
// matches the "pr-<n>" convention FetchPR creates, an extra "diff" window is
// added running cfg.ReviewCommand for that PR number.
func (m Model) openPath(dir, sessionName, branch string) tea.Cmd {
	extraName, extraCmd := "", ""
	if match := prBranchPattern.FindStringSubmatch(branch); match != nil {
		extraName = "diff"
		extraCmd = strings.ReplaceAll(m.cfg.ReviewCommand, "{pr}", match[1])
	}

	cfg := m.cfg
	st := m.st

	return func() tea.Msg {
		attachCmd, err := tmux.PrepareOpen(sessionName, dir, cfg.Editor, cfg.Assistant, extraName, extraCmd)
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
	case "up", "ctrl+p", "ctrl+k":
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
