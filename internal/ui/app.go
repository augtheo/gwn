package ui

import (
	"fmt"
	"strings"

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

	case openedMsg:
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	b.WriteString(styleTitle.Render("  gwn") + "\n")

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
	if m.err != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(colRed).Render("  error: "+m.err.Error()) + "\n")
	} else {
		b.WriteString(styleHints.Render("enter: open  tab: expand worktrees  ↑↓: navigate  q: quit"))
	}

	return b.String()
}

func (m Model) renderItem(i int) string {
	item := m.filtered[i]
	selected := i == m.cursor
	ws := item.ws

	if item.wtIdx >= 0 {
		wt := ws.Worktrees[item.wtIdx]
		sessionMark := styleSessionNone.Render(iconDot)
		if wt.HasSession {
			sessionMark = styleSessionActive.Render(iconDot)
		}
		line := fmt.Sprintf("%s %s %s", iconWorktree, styleBranch.Render(wt.Branch), sessionMark)
		if selected {
			return styleWorktreeSelected.Render(line)
		}
		return styleWorktreeItem.Render(line)
	}

	icon := iconDir
	if ws.Type == scanner.TypeGitRepo {
		icon = iconGit
	}

	sessionMark := styleSessionNone.Render(iconDot)
	if ws.HasSession {
		sessionMark = styleSessionActive.Render(iconDot)
	}

	meta := ""
	if ws.Branch != "" {
		meta = styleBranch.Render(" " + ws.Branch)
	}

	expandHint := ""
	if ws.Type == scanner.TypeGitRepo && len(ws.Worktrees) > 1 {
		if m.expanded[ws.Path] {
			expandHint = styleDim.Render(" ▾")
		} else {
			expandHint = styleDim.Render(" ▸")
		}
	}

	line := fmt.Sprintf("%s %s%s%s %s", icon, ws.Name, expandHint, meta, sessionMark)
	if selected {
		return styleSelected.Width(m.width - 2).Render(line)
	}
	return styleNormal.Render(line)
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
		if err := tmux.OpenWorkspace(sessionName, dir, cfg.Editor, cfg.Assistant); err != nil {
			return errMsg(err)
		}
		st.Touch(dir, sessionName)
		_ = st.Save()
		return openedMsg{}
	}
}
