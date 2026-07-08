package ui

import (
	"fmt"
	"os/exec"
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
type spinnerTickMsg struct{}

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

	creatingWorktree bool
	createRepoPath   string

	cloningRepo bool

	fetchingPath string
	spinnerFrame int
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

		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "esc":
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
		case "up", "ctrl+p", "ctrl+k":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "ctrl+n", "ctrl+j":
			if m.cursor < len(m.filtered)-1 {
				m.cursor++
			}
			return m, nil
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.refilter()
			m.cursor = 0
			return m, cmd
		}

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

	case spinnerTickMsg:
		if m.fetchingPath == "" {
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
	b.WriteString(styleInput.Width(inputWidth).Render(m.input.View()) + "\n\n")

	listHeight := m.height - 8
	if listHeight < 1 {
		listHeight = 1
	}

	start, end := m.visibleRange(listHeight)
	for i := start; i < end && i < len(m.filtered); i++ {
		b.WriteString(m.renderItem(i) + "\n")
	}

	b.WriteString("\n")
	switch {
	case m.err != nil:
		b.WriteString(lipgloss.NewStyle().Foreground(colRed).Render("  error: "+m.err.Error()) + "\n")
	case m.creatingWorktree:
		b.WriteString(styleHints.Render("enter: create worktree  esc: cancel"))
	case m.cloningRepo:
		b.WriteString(styleHints.Render("enter: clone  esc: cancel"))
	default:
		b.WriteString(styleHints.Render("enter: open  tab: expand worktrees  ctrl+w: new worktree  ctrl+g: clone repo  ctrl+f: fetch  ↑↓: navigate  esc/ctrl+c: quit"))
	}

	return b.String()
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
	if ws.Type == scanner.TypeGitRepo && len(ws.Worktrees) > 1 {
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
	if item.wtIdx >= 0 || item.ws.Type != scanner.TypeGitRepo || len(item.ws.Worktrees) <= 1 {
		return
	}
	m.expanded[item.ws.Path] = !m.expanded[item.ws.Path]
	m.refilter()
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
}

func (m *Model) cancelCreateWorktree() {
	m.creatingWorktree = false
	m.createRepoPath = ""
	m.input.SetValue("")
	m.input.Placeholder = "search workspaces..."
}

func (m Model) createWorktree(repoPath, branch string) tea.Cmd {
	return func() tea.Msg {
		_, err := scanner.AddWorktree(repoPath, branch)
		return worktreeCreatedMsg{repoPath: repoPath, err: err}
	}
}

func (m *Model) startCloneRepo() {
	m.cloningRepo = true
	m.input.SetValue("")
	m.input.Placeholder = "clone repo (owner/repo or git URL)..."
}

func (m *Model) cancelCloneRepo() {
	m.cloningRepo = false
	m.input.SetValue("")
	m.input.Placeholder = "search workspaces..."
}

func (m Model) cloneRepo(src string) tea.Cmd {
	scanPaths := m.cfg.ScanPaths
	defaultHost := m.cfg.DefaultGitHost
	protocol := m.cfg.CloneProtocol

	return func() tea.Msg {
		if len(scanPaths) == 0 {
			return repoClonedMsg{err: fmt.Errorf("no scan_paths configured")}
		}
		url, name, err := scanner.ResolveCloneSource(src, defaultHost, protocol)
		if err != nil {
			return repoClonedMsg{err: err}
		}
		repoPath, branch, err := scanner.CloneBare(scanPaths[0], name, url)
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
	for i, ws := range base {
		final = append(final, listItem{ws: ws, wtIdx: -1})
		if ws.Type == scanner.TypeGitRepo && m.expanded[ws.Path] {
			for j := range ws.Worktrees {
				final = append(final, listItem{ws: ws, wtIdx: j, parentIdx: i})
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

	cfg := m.cfg
	st := m.st

	return func() tea.Msg {
		attachCmd, err := tmux.PrepareOpen(sessionName, dir, cfg.Editor, cfg.Assistant)
		if err != nil {
			return errMsg(err)
		}
		st.Touch(dir, sessionName)
		_ = st.Save()
		return sessionReadyMsg{attachCmd: attachCmd}
	}
}
