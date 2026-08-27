package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/text/cases"
)

const (
	formAuthorName = iota
	formAuthorEmail
	formAuthorDate
	formCommitterName
	formCommitterEmail
	formCommitterDate
	formMessage
	formFieldCount
)

const (
	bulkAuthorSearch = iota
	bulkAuthorName
	bulkAuthorEmail
	bulkAuthorFieldCount
)

type uiFocus int

const (
	focusList uiFocus = iota
	focusForm
)

type uiOverlay int

const (
	overlayNone uiOverlay = iota
	overlaySearch
	overlayPath
	overlayBulkAuthor
	overlayConfirmApply
	overlayConfirmForce
	overlayHelp
)

type statusKind string

const (
	statusInfo    statusKind = "info"
	statusSuccess statusKind = "success"
	statusError   statusKind = "error"
)

var (
	colorInk       = lipgloss.Color("#DCE7F7")
	colorMuted     = lipgloss.Color("#7E8DA3")
	colorDim       = lipgloss.Color("#506075")
	colorPanel     = lipgloss.Color("#111B27")
	colorPanelLift = lipgloss.Color("#172536")
	colorSelected  = lipgloss.Color("#20364B")
	colorCyan      = lipgloss.Color("#55D6FF")
	colorGreen     = lipgloss.Color("#78F0B4")
	colorMagenta   = lipgloss.Color("#D68DFF")
	colorAmber     = lipgloss.Color("#FFD166")
	colorRed       = lipgloss.Color("#FF6B7A")
	colorBorder    = lipgloss.Color("#2B435A")

	appBaseStyle = lipgloss.NewStyle().
			Foreground(colorInk).
			Background(lipgloss.Color("#08111B"))
	panelStyle = lipgloss.NewStyle().
			Background(colorPanel).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder)
	activePanelStyle = panelStyle.Copy().
				BorderForeground(colorCyan)
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorCyan)
	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)
	dimStyle = lipgloss.NewStyle().
			Foreground(colorDim)
	selectedItemStyle = lipgloss.NewStyle().
				Background(colorSelected).
				Foreground(colorInk).
				Bold(true).
				Padding(0, 1)
	itemStyle = lipgloss.NewStyle().
			Foreground(colorInk).
			Padding(0, 1)
	fieldLabelStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Bold(true)
	focusedFieldStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorCyan).
				Background(colorPanelLift).
				Padding(0, 1)
	blurredFieldStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(colorBorder).
				Background(colorPanel).
				Padding(0, 1)
)

type repoLoadedMsg struct {
	repo    RepositoryState
	commits []CommitRecord
	path    string
	err     error
}

type applyFinishedMsg struct {
	result  ApplyResult
	repo    RepositoryState
	commits []CommitRecord
	err     error
}

type tuiModel struct {
	app         *App
	initialPath string

	repository RepositoryState
	repoLoaded bool
	commits    []CommitRecord

	originalByHash map[string]CommitRecord
	draftByHash    map[string]CommitRecord

	selectedHash  string
	selectedIndex int
	listOffset    int

	width  int
	height int

	focus   uiFocus
	overlay uiOverlay

	formFocus int
	inputs    []textinput.Model
	message   textarea.Model

	searchInput      textinput.Model
	pathInput        textinput.Model
	confirmInput     textinput.Model
	bulkAuthorFocus  int
	bulkAuthorInputs []textinput.Model
	bulkAuthorExact  bool

	forcePush bool
	pushTags  bool
	busy      bool

	status     string
	statusKind statusKind
}

func initialRepositoryPath(args []string, getwd func() (string, error)) (string, error) {
	if len(args) > 1 {
		return "", errors.New("usage: nexus [repo-path]")
	}
	if len(args) == 1 {
		return normalizePath(args[0])
	}
	wd, err := getwd()
	if err != nil {
		return "", err
	}
	return normalizePath(wd)
}

func newTUIModel(app *App, initialPath string) tuiModel {
	inputs := make([]textinput.Model, formFieldCount-1)
	for i := range inputs {
		inputs[i] = newTextInput()
	}

	message := textarea.New()
	message.Prompt = ""
	message.Placeholder = "Commit message"
	message.ShowLineNumbers = false
	message.CharLimit = 0
	message.Blur()

	searchInput := newTextInput()
	searchInput.Placeholder = "Search hash, author, email, or message"

	pathInput := newTextInput()
	pathInput.Placeholder = "/path/to/repository"
	pathInput.SetValue(initialPath)

	confirmInput := newTextInput()
	confirmInput.Placeholder = "FORCE"

	bulkAuthorInputs := make([]textinput.Model, bulkAuthorFieldCount)
	for i := range bulkAuthorInputs {
		bulkAuthorInputs[i] = newTextInput()
	}
	bulkAuthorInputs[bulkAuthorSearch].Placeholder = "Existing author name or email"
	bulkAuthorInputs[bulkAuthorName].Placeholder = "Replacement author name"
	bulkAuthorInputs[bulkAuthorEmail].Placeholder = "Replacement author email"

	return tuiModel{
		app:              app,
		initialPath:      initialPath,
		originalByHash:   map[string]CommitRecord{},
		draftByHash:      map[string]CommitRecord{},
		inputs:           inputs,
		message:          message,
		searchInput:      searchInput,
		pathInput:        pathInput,
		confirmInput:     confirmInput,
		bulkAuthorInputs: bulkAuthorInputs,
		pushTags:         true,
		status:           "Loading repository...",
		statusKind:       statusInfo,
	}
}

func newTextInput() textinput.Model {
	input := textinput.New()
	input.Prompt = ""
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(colorDim)
	input.TextStyle = lipgloss.NewStyle().Foreground(colorInk)
	input.Cursor.Style = lipgloss.NewStyle().Foreground(colorCyan)
	return input
}

func (m tuiModel) Init() tea.Cmd {
	return loadRepositoryCmd(m.app, m.initialPath)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateComponentSizes()
		return m, nil
	case repoLoadedMsg:
		m.busy = false
		m.handleRepoLoaded(msg)
		return m, nil
	case applyFinishedMsg:
		m.busy = false
		m.handleApplyFinished(msg)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	default:
		return m, nil
	}
}

func (m *tuiModel) handleRepoLoaded(msg repoLoadedMsg) {
	if msg.err != nil {
		m.repoLoaded = false
		m.repository = RepositoryState{}
		m.commits = nil
		m.originalByHash = map[string]CommitRecord{}
		m.draftByHash = map[string]CommitRecord{}
		m.selectedHash = ""
		m.selectedIndex = 0
		m.listOffset = 0
		m.overlay = overlayPath
		m.focus = focusList
		m.pathInput.SetValue(msg.path)
		m.focusPathInput()
		m.setStatus(fmt.Sprintf("Repository unavailable: %v", msg.err), statusError)
		return
	}

	m.repoLoaded = true
	m.repository = msg.repo
	m.commits = newestFirst(msg.commits)
	m.originalByHash = mapByHash(m.commits)
	m.draftByHash = cloneMapByHash(m.commits)
	m.pathInput.SetValue(msg.repo.Path)
	m.reconcileSelection()
	m.loadSelectedCommitIntoForm()
	m.overlay = overlayNone
	m.setStatus(fmt.Sprintf("Loaded %d commits from %s", len(m.commits), filepath.Base(msg.repo.Path)), statusSuccess)
}

func (m *tuiModel) handleApplyFinished(msg applyFinishedMsg) {
	if msg.err != nil {
		m.setStatus(msg.err.Error(), statusError)
		return
	}

	if msg.repo.Path != "" {
		m.repository = msg.repo
		m.repoLoaded = true
	}
	if msg.commits != nil {
		m.commits = newestFirst(msg.commits)
		m.originalByHash = mapByHash(m.commits)
		m.draftByHash = cloneMapByHash(m.commits)
		m.reconcileSelection()
		m.loadSelectedCommitIntoForm()
	}

	m.setStatus(applyResultSummary(msg.result), statusSuccess)
}

func (m tuiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	if m.busy {
		return m, nil
	}

	switch m.overlay {
	case overlayHelp:
		if key == "esc" || key == "?" || key == "q" {
			m.overlay = overlayNone
		}
		return m, nil
	case overlaySearch:
		return m.updateSearch(msg)
	case overlayPath:
		return m.updatePathEntry(msg)
	case overlayBulkAuthor:
		return m.updateBulkAuthor(msg)
	case overlayConfirmApply, overlayConfirmForce:
		return m.updateConfirmation(msg)
	}

	if m.focus == focusForm {
		return m.updateForm(msg)
	}

	switch key {
	case "q":
		return m, tea.Quit
	case "?":
		m.overlay = overlayHelp
	case "/":
		m.overlay = overlaySearch
		m.searchInput.Focus()
		m.searchInput.CursorEnd()
	case "p", "o":
		m.overlay = overlayPath
		m.focusPathInput()
	case "b":
		if m.repoLoaded {
			m.openBulkAuthor()
		} else {
			m.setStatus("Load a repository before replacing authors.", statusError)
		}
	case "r":
		if m.repository.Path != "" {
			m.busy = true
			m.setStatus("Reloading history...", statusInfo)
			return m, loadRepositoryCmd(m.app, m.repository.Path)
		}
		m.overlay = overlayPath
		m.focusPathInput()
	case "f":
		m.forcePush = !m.forcePush
		m.setStatus(fmt.Sprintf("Force push %s", onOff(m.forcePush)), statusInfo)
	case "t":
		m.pushTags = !m.pushTags
		m.setStatus(fmt.Sprintf("Push tags %s", onOff(m.pushTags)), statusInfo)
	case "a":
		return m.beginApply()
	case "x":
		m.resetSelectedCommit()
	case "tab":
		if m.selectedHash != "" {
			m.focus = focusForm
			m.focusFormField(0)
		}
	case "up", "k":
		m.moveSelection(-1)
	case "down", "j":
		m.moveSelection(1)
	case "pgup":
		m.moveSelection(-5)
	case "pgdown":
		m.moveSelection(5)
	case "home":
		m.moveSelectionTo(0)
	case "end":
		filtered, _, _ := selectionAfterFilter(m.commits, m.draftByHash, m.selectedHash, m.searchInput.Value())
		m.moveSelectionTo(len(filtered) - 1)
	}

	return m, nil
}

func (m tuiModel) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "enter":
		m.overlay = overlayNone
		m.searchInput.Blur()
		m.reconcileSelection()
		return m, nil
	}

	var cmd tea.Cmd
	m.searchInput, cmd = m.searchInput.Update(msg)
	m.reconcileSelection()
	return m, cmd
}

func (m tuiModel) updatePathEntry(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		if m.repoLoaded {
			m.overlay = overlayNone
			m.pathInput.Blur()
		}
		return m, nil
	case "enter":
		path := strings.TrimSpace(m.pathInput.Value())
		if path == "" {
			m.setStatus("Repository path is required.", statusError)
			return m, nil
		}
		normalized, err := normalizePath(path)
		if err != nil {
			m.setStatus(err.Error(), statusError)
			return m, nil
		}
		m.busy = true
		m.pathInput.Blur()
		m.setStatus("Loading repository...", statusInfo)
		return m, loadRepositoryCmd(m.app, normalized)
	}

	var cmd tea.Cmd
	m.pathInput, cmd = m.pathInput.Update(msg)
	return m, cmd
}

func (m tuiModel) updateBulkAuthor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.overlay = overlayNone
		m.blurBulkAuthorInputs()
		m.setStatus("Bulk author replacement cancelled.", statusInfo)
		return m, nil
	case "tab", "shift+tab":
		delta := 1
		if msg.String() == "shift+tab" {
			delta = -1
		}
		next := (m.bulkAuthorFocus + delta + bulkAuthorFieldCount) % bulkAuthorFieldCount
		m.focusBulkAuthorField(next)
		return m, nil
	case "ctrl+t":
		m.bulkAuthorExact = !m.bulkAuthorExact
		m.setStatus(fmt.Sprintf("Author match mode: %s.", authorMatchMode(m.bulkAuthorExact)), statusInfo)
		return m, nil
	case "enter":
		m.syncFormToDraft()
		matched, err := stageAuthorReplacement(
			m.commits,
			m.draftByHash,
			m.bulkAuthorInputs[bulkAuthorSearch].Value(),
			m.bulkAuthorInputs[bulkAuthorName].Value(),
			m.bulkAuthorInputs[bulkAuthorEmail].Value(),
			m.bulkAuthorExact,
		)
		if err != nil {
			m.setStatus(err.Error(), statusError)
			return m, nil
		}
		if matched == 0 {
			m.setStatus("No author names or emails matched the search.", statusInfo)
			return m, nil
		}
		m.overlay = overlayNone
		m.blurBulkAuthorInputs()
		m.reconcileSelection()
		m.loadSelectedCommitIntoForm()
		m.setStatus(fmt.Sprintf("Staged author replacement for %d commit%s.", matched, plural(matched)), statusSuccess)
		return m, nil
	}

	var cmd tea.Cmd
	m.bulkAuthorInputs[m.bulkAuthorFocus], cmd = m.bulkAuthorInputs[m.bulkAuthorFocus].Update(msg)
	return m, cmd
}

func (m tuiModel) updateConfirmation(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "esc" || key == "n" {
		m.overlay = overlayNone
		m.confirmInput.Blur()
		m.setStatus("Apply cancelled.", statusInfo)
		return m, nil
	}

	if m.overlay == overlayConfirmApply {
		if key == "y" {
			return m.applyNow()
		}
		return m, nil
	}

	if key == "enter" {
		if m.confirmInput.Value() != "FORCE" {
			m.setStatus("Type FORCE to confirm rewriting and force pushing.", statusError)
			return m, nil
		}
		return m.applyNow()
	}

	var cmd tea.Cmd
	m.confirmInput, cmd = m.confirmInput.Update(msg)
	return m, cmd
}

func (m tuiModel) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		m.syncFormToDraft()
		m.focus = focusList
		m.blurForm()
		return m, nil
	case "tab", "shift+tab":
		m.syncFormToDraft()
		next, returnToList := nextFormFocus(m.formFocus, key == "shift+tab")
		if returnToList {
			m.focus = focusList
			m.blurForm()
			return m, nil
		}
		m.focusFormField(next)
		return m, nil
	}

	var cmd tea.Cmd
	if m.formFocus == formMessage {
		m.message, cmd = m.message.Update(msg)
	} else if m.formFocus >= 0 && m.formFocus < len(m.inputs) {
		m.inputs[m.formFocus], cmd = m.inputs[m.formFocus].Update(msg)
	}
	m.syncFormToDraft()
	return m, cmd
}

func (m tuiModel) beginApply() (tea.Model, tea.Cmd) {
	if !m.repoLoaded {
		m.setStatus("Load a repository before applying changes.", statusError)
		return m, nil
	}

	m.syncFormToDraft()
	count := dirtyCommitCount(m.commits, m.originalByHash, m.draftByHash)
	if count == 0 && !m.forcePush {
		m.setStatus("No history edits to apply.", statusInfo)
		return m, nil
	}

	if m.forcePush {
		m.overlay = overlayConfirmForce
		m.confirmInput.SetValue("")
		m.confirmInput.Focus()
		m.setStatus("Force push confirmation required.", statusInfo)
		return m, nil
	}

	m.overlay = overlayConfirmApply
	m.setStatus(fmt.Sprintf("Confirm rewriting %d edited commit%s.", count, plural(count)), statusInfo)
	return m, nil
}

func (m tuiModel) applyNow() (tea.Model, tea.Cmd) {
	m.syncFormToDraft()
	m.overlay = overlayNone
	m.confirmInput.Blur()
	m.busy = true
	m.setStatus("Applying commit metadata changes...", statusInfo)
	return m, applyChangesCmd(m.app, m.applyRequest())
}

func (m tuiModel) View() string {
	m.updateComponentSizes()
	width := m.width
	height := m.height
	if width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 36
	}
	width = maxInt(1, width-1)

	top := m.renderTopBar(width)
	bottom := m.renderBottomBar(width)
	bodyHeight := maxInt(1, height-lipgloss.Height(top)-lipgloss.Height(bottom))
	leftWidth, rightWidth, separator := tuiColumnWidths(width)

	left := m.renderCommitPane(leftWidth, bodyHeight)
	right := m.renderDetailPane(rightWidth, bodyHeight)
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", separator), right)

	return appBaseStyle.Width(width).Render(strings.Join([]string{top, body, bottom}, "\n"))
}

func (m tuiModel) renderTopBar(width int) string {
	barStyle := lipgloss.NewStyle().
		Width(contentWidthFor(lipgloss.NewStyle().Padding(0, 1), width)).
		Background(lipgloss.Color("#0C1824")).
		Padding(0, 1)
	contentWidth := contentWidthFor(barStyle, width)
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorCyan).
		Render("NEXUS")

	repoText := "No repository loaded"
	if m.repoLoaded {
		clean := "clean"
		if !m.repository.Clean {
			clean = "dirty"
		}
		branch := "detached HEAD"
		if !m.repository.DetachedHead && m.repository.CurrentBranch != "" {
			branch = m.repository.CurrentBranch
		}
		repoText = fmt.Sprintf("%s  %s  %s", filepath.Base(m.repository.Path), branch, clean)
	}

	flags := fmt.Sprintf("force:%s tags:%s edits:%d", onOff(m.forcePush), onOff(m.pushTags), dirtyCommitCount(m.commits, m.originalByHash, m.draftByHash))
	if m.busy {
		flags = "working..."
	}

	repoWidth := maxInt(4, contentWidth-lipgloss.Width(title)-lipgloss.Width(flags)-6)
	line := lipgloss.JoinHorizontal(
		lipgloss.Center,
		title,
		"  ",
		mutedStyle.Render(truncateText(repoText, repoWidth)),
	)
	line = line + strings.Repeat(" ", maxInt(1, contentWidth-lipgloss.Width(line)-lipgloss.Width(flags))) + dimStyle.Render(flags)
	line = truncateText(line, contentWidth)

	if m.overlay == overlaySearch {
		search := renderBox(focusedFieldStyle, width, 0, fieldLabelStyle.Render("Search")+"\n"+m.searchInput.View())
		return lipgloss.NewStyle().Width(width).Render(line + "\n" + search)
	}

	return barStyle.Render(line)
}

func (m tuiModel) renderBottomBar(width int) string {
	barStyle := lipgloss.NewStyle().
		Width(contentWidthFor(lipgloss.NewStyle().Padding(0, 1), width)).
		Background(lipgloss.Color("#0C1824")).
		Padding(0, 1)
	contentWidth := contentWidthFor(barStyle, width)
	statusColor := colorCyan
	switch m.statusKind {
	case statusSuccess:
		statusColor = colorGreen
	case statusError:
		statusColor = colorRed
	}

	status := lipgloss.NewStyle().Foreground(statusColor).Render(truncateText(m.status, contentWidth))
	help := "Tab form/list  / search  b bulk author  p path  r reload  x reset  a apply  f force  t tags  ? help  q quit"
	if m.focus == focusForm {
		help = "Tab next field  Shift+Tab previous  Esc list  Enter newline in message  Ctrl+C quit"
	}
	if m.overlay == overlayPath {
		help = "Enter load repository  Esc close"
	}
	if m.overlay == overlayBulkAuthor {
		help = "Tab next field  Shift+Tab previous  Ctrl+T partial/exact  Enter stage  Esc cancel"
	}
	if m.overlay == overlayConfirmApply {
		help = "Press y to rewrite history, n/Esc to cancel"
	}
	if m.overlay == overlayConfirmForce {
		help = "Type FORCE then Enter to rewrite and force push, Esc to cancel"
	}

	return barStyle.Render(status + "\n" + dimStyle.Render(truncateText(help, contentWidth)))
}

func (m tuiModel) renderCommitPane(width int, height int) string {
	style := panelStyle
	if m.focus == focusList && m.overlay == overlayNone {
		style = activePanelStyle
	}
	innerWidth := maxInt(1, contentWidthFor(style, width))
	innerHeight := maxInt(1, contentHeightFor(style, height))

	filtered, selectedIndex, _ := selectionAfterFilter(m.commits, m.draftByHash, m.selectedHash, m.searchInput.Value())
	header := headerStyle.Render(fmt.Sprintf("Commits %d/%d", len(filtered), len(m.commits)))
	if m.searchInput.Value() != "" {
		header += " " + dimStyle.Render("/"+truncateText(m.searchInput.Value(), innerWidth-12))
	}

	lines := []string{truncateText(header, innerWidth)}
	if len(filtered) == 0 {
		lines = append(lines, "", mutedStyle.Render("No commits match this filter."))
		return renderBox(style, width, height, fitLines(lines, innerHeight, innerWidth))
	}

	visibleSlots := maxInt(1, (innerHeight-2)/5)
	offset := clamp(m.listOffset, 0, maxInt(0, len(filtered)-visibleSlots))
	if selectedIndex < offset {
		offset = selectedIndex
	}
	if selectedIndex >= offset+visibleSlots {
		offset = selectedIndex - visibleSlots + 1
	}

	for i := offset; i < len(filtered) && len(lines) < innerHeight-1; i++ {
		item := m.renderCommitItem(filtered[i], i == selectedIndex, innerWidth)
		lines = append(lines, strings.Split(item, "\n")...)
		if len(lines) < innerHeight {
			lines = append(lines, "")
		}
	}

	return renderBox(style, width, height, fitLines(lines, innerHeight, innerWidth))
}

func (m tuiModel) renderCommitItem(commit CommitRecord, selected bool, width int) string {
	draft := m.draftByHash[commit.Hash]
	if draft.Hash == "" {
		draft = commit
	}

	marker := " "
	if commitDirty(commit.Hash, m.originalByHash, m.draftByHash) {
		marker = "*"
	}

	top := fmt.Sprintf("%s %s  %s", marker, draft.ShortHash, formatCommitTime(draft.AuthorDate))
	author := draft.AuthorName
	if author == "" {
		author = "(no author)"
	}
	preview := commitPreviewLines(draft.Message, maxInt(8, width-2), 2)
	for len(preview) < 2 {
		preview = append(preview, "")
	}

	block := strings.Join([]string{
		truncateText(top, width-2),
		truncateText(author, width-2),
		truncateText(preview[0], width-2),
		truncateText(preview[1], width-2),
	}, "\n")

	if selected {
		return renderBox(selectedItemStyle, width, 0, block)
	}
	return renderBox(itemStyle, width, 0, block)
}

func (m tuiModel) renderDetailPane(width int, height int) string {
	style := panelStyle
	if m.focus == focusForm && m.overlay == overlayNone {
		style = activePanelStyle
	}
	innerWidth := maxInt(1, contentWidthFor(style, width))
	innerHeight := maxInt(1, contentHeightFor(style, height))

	var content string
	switch m.overlay {
	case overlayPath:
		content = m.renderPathPane(innerWidth, innerHeight)
	case overlayBulkAuthor:
		content = m.renderBulkAuthorPane(innerWidth, innerHeight)
	case overlayConfirmApply, overlayConfirmForce:
		content = m.renderConfirmPane(innerWidth, innerHeight)
	case overlayHelp:
		content = m.renderHelpPane(innerWidth, innerHeight)
	default:
		content = m.renderFormPane(innerWidth, innerHeight)
	}

	return renderBox(style, width, height, content)
}

func (m tuiModel) renderBulkAuthorPane(width int, height int) string {
	lines := []string{
		headerStyle.Render("Bulk replace author"),
		"Search current draft author names and emails, then stage a new author identity on every matching commit.",
		"",
		m.renderBulkAuthorField(bulkAuthorSearch, "Search", width),
		m.renderBulkAuthorField(bulkAuthorName, "Replacement author name", width),
		m.renderBulkAuthorField(bulkAuthorEmail, "Replacement author email", width),
		"",
		fmt.Sprintf("Match mode: %s  %s", authorMatchMode(m.bulkAuthorExact), dimStyle.Render("(Ctrl+T to toggle)")),
		dimStyle.Render("Press Enter to stage these changes. Use a to apply the history rewrite."),
	}
	return fitLines(wrapBlockLines(lines, width), height, width)
}

func (m tuiModel) renderBulkAuthorField(index int, label string, width int) string {
	style := blurredFieldStyle
	if m.overlay == overlayBulkAuthor && m.bulkAuthorFocus == index {
		style = focusedFieldStyle
	}
	return renderBox(style, width, 0, fieldLabelStyle.Render(label)+"\n"+m.bulkAuthorInputs[index].View())
}

func (m tuiModel) renderPathPane(width int, height int) string {
	lines := []string{
		headerStyle.Render("Open Repository"),
		"",
		"Enter a Git worktree path. Nexus starts with the current directory, but you can switch repositories here at any time.",
		"",
		renderBox(focusedFieldStyle, width, 0, fieldLabelStyle.Render("Path")+"\n"+m.pathInput.View()),
		"",
		dimStyle.Render("Press Enter to load. Press Esc to return to the current repository."),
	}
	return fitLines(wrapBlockLines(lines, width), height, width)
}

func (m tuiModel) renderConfirmPane(width int, height int) string {
	count := dirtyCommitCount(m.commits, m.originalByHash, m.draftByHash)
	lines := []string{
		headerStyle.Render("Confirm Rewrite"),
		"",
		fmt.Sprintf("This will rewrite Git history for %d edited commit%s. A backup tag will be created first.", count, plural(count)),
	}

	if m.forcePush {
		lines = append(lines,
			"",
			lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("Force push is enabled."),
			"Type FORCE to confirm rewriting history and pushing with --force-with-lease.",
			"",
			renderBox(focusedFieldStyle, minInt(width, 24), 0, fieldLabelStyle.Render("Confirmation")+"\n"+m.confirmInput.View()),
		)
	} else {
		lines = append(lines, "", "Press y to continue, or n/Esc to cancel.")
	}

	return fitLines(wrapBlockLines(lines, width), height, width)
}

func (m tuiModel) renderHelpPane(width int, height int) string {
	lines := []string{
		headerStyle.Render("Keyboard"),
		"",
		"List: Up/Down, PgUp/PgDn, Home/End select commits.",
		"Tab moves from the list into the commit form.",
		"Form: Tab and Shift+Tab move through every editable value. Esc returns to the list.",
		"/ searches commits. b bulk-replaces author identities. p opens a repository path prompt. r reloads history.",
		"x resets the selected commit. a applies edits. f toggles force push. t toggles tag push.",
		"",
		headerStyle.Render("Safety"),
		"",
		"Apply creates a backup tag before rewriting history. Force push uses --force-with-lease.",
		"",
		dimStyle.Render("Press ? or Esc to close help."),
	}
	return fitLines(wrapBlockLines(lines, width), height, width)
}

func (m tuiModel) renderFormPane(width int, height int) string {
	if !m.repoLoaded {
		return fitLines([]string{
			headerStyle.Render("No Repository"),
			"",
			"Enter a repository path to begin.",
			"",
			dimStyle.Render("Press p to open the path prompt."),
		}, height, width)
	}
	if m.selectedHash == "" {
		return fitLines([]string{
			headerStyle.Render("No Commits"),
			"",
			"This repository has no commits to edit.",
		}, height, width)
	}

	draft := m.draftByHash[m.selectedHash]
	title := fmt.Sprintf("%s  %s", draft.ShortHash, firstLine(draft.Message))
	lines := []string{headerStyle.Render(truncateText(title, width)), ""}

	cellGap := 2
	cellWidth := maxInt(1, (width-cellGap)/2)
	rows := [][2]int{
		{formAuthorName, formAuthorEmail},
		{formAuthorDate, formCommitterName},
		{formCommitterEmail, formCommitterDate},
	}
	labels := []string{
		"Author name",
		"Author email",
		"Author date",
		"Committer name",
		"Committer email",
		"Committer date",
	}

	if width >= 44 {
		for _, row := range rows {
			left := m.renderTextField(row[0], labels[row[0]], cellWidth)
			right := m.renderTextField(row[1], labels[row[1]], cellWidth)
			lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", cellGap), right))
		}
	} else {
		for _, row := range rows {
			lines = append(lines, m.renderTextField(row[0], labels[row[0]], width))
			lines = append(lines, m.renderTextField(row[1], labels[row[1]], width))
		}
	}

	messageHeight := clamp(height-lipgloss.Height(strings.Join(lines, "\n"))-8, 5, 10)
	lines = append(lines, m.renderMessageField(width, messageHeight))

	meta := []string{
		fmt.Sprintf("Hash: %s", draft.Hash),
		fmt.Sprintf("Parents: %s", strings.Join(draft.Parents, ", ")),
		fmt.Sprintf("Tree: %s", draft.Tree),
		fmt.Sprintf("Refs: %s", strings.Join(draft.Refs, ", ")),
	}
	if len(draft.Parents) == 0 {
		meta[1] = "Parents: (root commit)"
	}
	if len(draft.Refs) == 0 {
		meta[3] = "Refs: (none)"
	}
	lines = append(lines, dimStyle.Render(truncateText(strings.Join(meta, "  "), width)))

	return fitLines(lines, height, width)
}

func (m tuiModel) renderTextField(index int, label string, width int) string {
	style := blurredFieldStyle
	if m.focus == focusForm && m.overlay == overlayNone && m.formFocus == index {
		style = focusedFieldStyle
	}
	return renderBox(style, width, 0, fieldLabelStyle.Render(label)+"\n"+m.inputs[index].View())
}

func (m tuiModel) renderMessageField(width int, height int) string {
	style := blurredFieldStyle
	if m.focus == focusForm && m.overlay == overlayNone && m.formFocus == formMessage {
		style = focusedFieldStyle
	}
	return renderBox(style, width, height, fieldLabelStyle.Render("Commit message")+"\n"+m.message.View())
}

func (m *tuiModel) updateComponentSizes() {
	width := m.width
	height := m.height
	if width <= 0 {
		width = 120
	}
	if height <= 0 {
		height = 36
	}
	_, rightWidth, _ := tuiColumnWidths(width)
	innerRight := maxInt(1, contentWidthFor(panelStyle, rightWidth))
	cellWidth := maxInt(18, (innerRight-2)/2)

	for i := range m.inputs {
		m.inputs[i].Width = maxInt(4, contentWidthFor(blurredFieldStyle, cellWidth))
	}
	m.message.SetWidth(maxInt(4, contentWidthFor(blurredFieldStyle, innerRight)))
	m.message.SetHeight(clamp(height/4, 5, 10))
	m.searchInput.Width = maxInt(16, width-10)
	m.pathInput.Width = maxInt(4, contentWidthFor(focusedFieldStyle, innerRight))
	m.confirmInput.Width = 12
	for i := range m.bulkAuthorInputs {
		m.bulkAuthorInputs[i].Width = maxInt(4, contentWidthFor(blurredFieldStyle, innerRight))
	}
}

func (m *tuiModel) openBulkAuthor() {
	m.blurForm()
	m.searchInput.Blur()
	m.pathInput.Blur()
	m.confirmInput.Blur()
	for i := range m.bulkAuthorInputs {
		m.bulkAuthorInputs[i].SetValue("")
	}
	m.bulkAuthorExact = false
	m.overlay = overlayBulkAuthor
	m.focusBulkAuthorField(bulkAuthorSearch)
	m.setStatus("Enter an author search and replacement identity.", statusInfo)
}

func (m *tuiModel) focusBulkAuthorField(index int) {
	m.blurBulkAuthorInputs()
	m.bulkAuthorFocus = clamp(index, 0, bulkAuthorFieldCount-1)
	m.bulkAuthorInputs[m.bulkAuthorFocus].Focus()
	m.bulkAuthorInputs[m.bulkAuthorFocus].CursorEnd()
}

func (m *tuiModel) blurBulkAuthorInputs() {
	for i := range m.bulkAuthorInputs {
		m.bulkAuthorInputs[i].Blur()
	}
}

func (m *tuiModel) focusPathInput() {
	m.blurForm()
	m.searchInput.Blur()
	m.confirmInput.Blur()
	m.pathInput.Focus()
	m.pathInput.CursorEnd()
}

func (m *tuiModel) focusFormField(index int) {
	m.blurForm()
	m.formFocus = clamp(index, 0, formFieldCount-1)
	if m.formFocus == formMessage {
		m.message.Focus()
		return
	}
	m.inputs[m.formFocus].Focus()
	m.inputs[m.formFocus].CursorEnd()
}

func (m *tuiModel) blurForm() {
	for i := range m.inputs {
		m.inputs[i].Blur()
	}
	m.message.Blur()
}

func (m *tuiModel) reconcileSelection() {
	previousHash := m.selectedHash
	filtered, index, hash := selectionAfterFilter(m.commits, m.draftByHash, m.selectedHash, m.searchInput.Value())
	if len(filtered) == 0 {
		m.selectedHash = ""
		m.selectedIndex = 0
		m.listOffset = 0
		m.blurForm()
		m.focus = focusList
		return
	}
	m.selectedHash = hash
	m.selectedIndex = index
	m.ensureSelectionVisible(len(filtered))
	if previousHash != m.selectedHash {
		m.loadSelectedCommitIntoForm()
	}
}

func (m *tuiModel) ensureSelectionVisible(total int) {
	visibleSlots := 5
	if m.height > 0 {
		visibleSlots = maxInt(1, (m.height-8)/5)
	}
	if m.selectedIndex < m.listOffset {
		m.listOffset = m.selectedIndex
	}
	if m.selectedIndex >= m.listOffset+visibleSlots {
		m.listOffset = m.selectedIndex - visibleSlots + 1
	}
	m.listOffset = clamp(m.listOffset, 0, maxInt(0, total-visibleSlots))
}

func (m *tuiModel) moveSelection(delta int) {
	filtered, selectedIndex, _ := selectionAfterFilter(m.commits, m.draftByHash, m.selectedHash, m.searchInput.Value())
	if len(filtered) == 0 {
		return
	}
	m.syncFormToDraft()
	next := clamp(selectedIndex+delta, 0, len(filtered)-1)
	m.selectedHash = filtered[next].Hash
	m.selectedIndex = next
	m.ensureSelectionVisible(len(filtered))
	m.loadSelectedCommitIntoForm()
}

func (m *tuiModel) moveSelectionTo(index int) {
	filtered, _, _ := selectionAfterFilter(m.commits, m.draftByHash, m.selectedHash, m.searchInput.Value())
	if len(filtered) == 0 {
		return
	}
	m.syncFormToDraft()
	next := clamp(index, 0, len(filtered)-1)
	m.selectedHash = filtered[next].Hash
	m.selectedIndex = next
	m.ensureSelectionVisible(len(filtered))
	m.loadSelectedCommitIntoForm()
}

func (m *tuiModel) loadSelectedCommitIntoForm() {
	if m.selectedHash == "" {
		return
	}
	draft, ok := m.draftByHash[m.selectedHash]
	if !ok {
		return
	}
	values := []string{
		draft.AuthorName,
		draft.AuthorEmail,
		draft.AuthorDate,
		draft.CommitterName,
		draft.CommitterEmail,
		draft.CommitterDate,
	}
	for i, value := range values {
		m.inputs[i].SetValue(value)
	}
	m.message.SetValue(draft.Message)
}

func (m *tuiModel) syncFormToDraft() {
	if m.selectedHash == "" {
		return
	}
	draft, ok := m.draftByHash[m.selectedHash]
	if !ok {
		return
	}
	draft.AuthorName = m.inputs[formAuthorName].Value()
	draft.AuthorEmail = m.inputs[formAuthorEmail].Value()
	draft.AuthorDate = m.inputs[formAuthorDate].Value()
	draft.CommitterName = m.inputs[formCommitterName].Value()
	draft.CommitterEmail = m.inputs[formCommitterEmail].Value()
	draft.CommitterDate = m.inputs[formCommitterDate].Value()
	draft.Message = m.message.Value()
	draft.Subject = firstLine(draft.Message)
	m.draftByHash[m.selectedHash] = draft
}

func (m *tuiModel) resetSelectedCommit() {
	if m.selectedHash == "" {
		return
	}
	original, ok := m.originalByHash[m.selectedHash]
	if !ok {
		return
	}
	m.draftByHash[m.selectedHash] = cloneCommitRecord(original)
	m.loadSelectedCommitIntoForm()
	m.setStatus(fmt.Sprintf("Reset %s", original.ShortHash), statusInfo)
}

func (m tuiModel) applyRequest() ApplyRequest {
	commits := make([]CommitRecord, 0, len(m.commits))
	for _, commit := range m.commits {
		draft, ok := m.draftByHash[commit.Hash]
		if !ok {
			draft = commit
		}
		commits = append(commits, cloneCommitRecord(draft))
	}
	return ApplyRequest{
		Commits:   commits,
		ForcePush: m.forcePush,
		PushTags:  m.pushTags,
	}
}

func (m *tuiModel) setStatus(message string, kind statusKind) {
	m.status = message
	m.statusKind = kind
}

func loadRepositoryCmd(app *App, path string) tea.Cmd {
	return func() tea.Msg {
		repo, err := app.SetRepository(path)
		if err != nil {
			return repoLoadedMsg{path: path, err: err}
		}
		commits, err := app.LoadHistory()
		if err != nil {
			return repoLoadedMsg{path: path, err: err}
		}
		return repoLoadedMsg{repo: repo, commits: commits, path: path}
	}
}

func applyChangesCmd(app *App, req ApplyRequest) tea.Cmd {
	return func() tea.Msg {
		result, err := app.ApplyChanges(req)
		if err != nil {
			return applyFinishedMsg{err: err}
		}
		repo, repoErr := app.GetRepositoryState()
		if repoErr != nil {
			return applyFinishedMsg{result: result, err: repoErr}
		}
		commits, historyErr := app.LoadHistory()
		if historyErr != nil {
			return applyFinishedMsg{result: result, repo: repo, err: historyErr}
		}
		return applyFinishedMsg{result: result, repo: repo, commits: commits}
	}
}

func newestFirst(commits []CommitRecord) []CommitRecord {
	out := make([]CommitRecord, 0, len(commits))
	for i := len(commits) - 1; i >= 0; i-- {
		out = append(out, cloneCommitRecord(commits[i]))
	}
	return out
}

func mapByHash(commits []CommitRecord) map[string]CommitRecord {
	result := make(map[string]CommitRecord, len(commits))
	for _, commit := range commits {
		result[commit.Hash] = cloneCommitRecord(commit)
	}
	return result
}

func cloneMapByHash(commits []CommitRecord) map[string]CommitRecord {
	return mapByHash(commits)
}

func cloneCommitRecord(commit CommitRecord) CommitRecord {
	clone := commit
	clone.Refs = append([]string(nil), commit.Refs...)
	clone.Parents = append([]string(nil), commit.Parents...)
	return clone
}

func stageAuthorReplacement(commits []CommitRecord, draftByHash map[string]CommitRecord, query string, replacementName string, replacementEmail string, exact bool) (int, error) {
	query = strings.TrimSpace(query)
	replacementName = strings.TrimSpace(replacementName)
	replacementEmail = strings.TrimSpace(replacementEmail)
	if query == "" {
		return 0, errors.New("author search is required")
	}
	if replacementName == "" {
		return 0, errors.New("replacement author name is required")
	}
	if replacementEmail == "" {
		return 0, errors.New("replacement author email is required")
	}
	fold := cases.Fold()
	foldedQuery := fold.String(query)
	matched := 0
	for _, commit := range commits {
		draft, ok := draftByHash[commit.Hash]
		if !ok {
			draft = cloneCommitRecord(commit)
		}
		nameMatches := strings.EqualFold(draft.AuthorName, query)
		emailMatches := strings.EqualFold(draft.AuthorEmail, query)
		if !exact {
			nameMatches = strings.Contains(fold.String(draft.AuthorName), foldedQuery)
			emailMatches = strings.Contains(fold.String(draft.AuthorEmail), foldedQuery)
		}
		if !nameMatches && !emailMatches {
			continue
		}
		draft.AuthorName = replacementName
		draft.AuthorEmail = replacementEmail
		draftByHash[commit.Hash] = draft
		matched++
	}
	return matched, nil
}

func authorMatchMode(exact bool) string {
	if exact {
		return "Exact"
	}
	return "Partial"
}

func selectionAfterFilter(commits []CommitRecord, draftByHash map[string]CommitRecord, selectedHash string, query string) ([]CommitRecord, int, string) {
	filtered := filterCommits(commits, draftByHash, query)
	if len(filtered) == 0 {
		return filtered, 0, ""
	}
	for index, commit := range filtered {
		if commit.Hash == selectedHash {
			return filtered, index, selectedHash
		}
	}
	return filtered, 0, filtered[0].Hash
}

func filterCommits(commits []CommitRecord, draftByHash map[string]CommitRecord, query string) []CommitRecord {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return append([]CommitRecord(nil), commits...)
	}
	filtered := make([]CommitRecord, 0, len(commits))
	for _, commit := range commits {
		draft := commit
		if item, ok := draftByHash[commit.Hash]; ok {
			draft = item
		}
		haystack := strings.ToLower(strings.Join([]string{
			commit.Hash,
			commit.ShortHash,
			draft.AuthorName,
			draft.AuthorEmail,
			draft.Subject,
			draft.Message,
		}, "\n"))
		if strings.Contains(haystack, query) {
			filtered = append(filtered, commit)
		}
	}
	return filtered
}

func dirtyCommitCount(commits []CommitRecord, originalByHash map[string]CommitRecord, draftByHash map[string]CommitRecord) int {
	count := 0
	for _, commit := range commits {
		if commitDirty(commit.Hash, originalByHash, draftByHash) {
			count++
		}
	}
	return count
}

func commitDirty(hash string, originalByHash map[string]CommitRecord, draftByHash map[string]CommitRecord) bool {
	original, hasOriginal := originalByHash[hash]
	draft, hasDraft := draftByHash[hash]
	if !hasOriginal || !hasDraft {
		return false
	}
	return editableFieldsChanged(original, draft)
}

func nextFormFocus(current int, backwards bool) (int, bool) {
	if backwards {
		if current <= 0 {
			return 0, true
		}
		return current - 1, false
	}
	if current >= formFieldCount-1 {
		return current, true
	}
	return current + 1, false
}

func commitPreviewLines(message string, width int, maxLines int) []string {
	summary := strings.Join(strings.Fields(normalizeMessage(message)), " ")
	if summary == "" {
		summary = "(no message)"
	}
	return wrapText(summary, maxInt(4, width), maxInt(1, maxLines))
}

func wrapText(value string, width int, maxLines int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, maxLines)
	current := ""
	for _, word := range words {
		if current == "" {
			current = word
			continue
		}
		if lipgloss.Width(current)+1+lipgloss.Width(word) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, truncateText(current, width))
		current = word
		if len(lines) == maxLines {
			break
		}
	}
	if len(lines) < maxLines && current != "" {
		lines = append(lines, truncateText(current, width))
	}
	if len(lines) == maxLines && len(words) > 0 {
		last := strings.Join(lines, " ")
		if lipgloss.Width(last) < lipgloss.Width(value) {
			lines[len(lines)-1] = truncateText(lines[len(lines)-1], width)
		}
	}
	return lines
}

func wrapBlockLines(lines []string, width int) []string {
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.Contains(line, "\x1b[") {
			out = append(out, line)
			continue
		}
		wrapped := wrapText(line, width, 99)
		out = append(out, wrapped...)
	}
	return out
}

func fitLines(lines []string, height int, width int) string {
	out := make([]string, 0, height)
	for _, block := range lines {
		for _, line := range strings.Split(block, "\n") {
			if len(out) >= height {
				break
			}
			out = append(out, truncateText(line, width))
		}
		if len(out) >= height {
			break
		}
	}
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

func truncateText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		return strings.Repeat(".", width)
	}
	var builder strings.Builder
	for _, r := range value {
		if lipgloss.Width(builder.String()+string(r)+"...") > width {
			break
		}
		builder.WriteRune(r)
	}
	return builder.String() + "..."
}

func formatCommitTime(value string) string {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339Nano, value)
	}
	if err != nil {
		return value
	}
	return parsed.Local().Format("2006-01-02 15:04")
}

func applyResultSummary(result ApplyResult) string {
	parts := []string{}
	if result.RewrittenCommits == 0 {
		parts = append(parts, "No commits required rewriting.")
	} else {
		parts = append(parts, fmt.Sprintf("Rewrote %d commit%s.", result.RewrittenCommits, plural(result.RewrittenCommits)))
	}
	if result.BackupReference != "" {
		parts = append(parts, "Backup "+result.BackupReference+".")
	}
	if result.ForcePushed {
		parts = append(parts, "Force push completed.")
	}
	if len(result.Warnings) > 0 {
		parts = append(parts, "Warning: "+strings.Join(result.Warnings, " "))
	}
	return strings.Join(parts, " ")
}

func onOff(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func clamp(value int, low int, high int) int {
	if high < low {
		return low
	}
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func contentWidthFor(style lipgloss.Style, outerWidth int) int {
	return maxInt(1, outerWidth-style.GetHorizontalFrameSize())
}

func contentHeightFor(style lipgloss.Style, outerHeight int) int {
	return maxInt(1, outerHeight-style.GetVerticalFrameSize())
}

func renderBox(style lipgloss.Style, outerWidth int, outerHeight int, content string) string {
	if outerWidth > 0 {
		style = style.Width(contentWidthFor(style, outerWidth))
	}
	if outerHeight > 0 {
		style = style.Height(contentHeightFor(style, outerHeight))
	}
	return style.Render(content)
}

func tuiColumnWidths(totalWidth int) (int, int, int) {
	if totalWidth <= 2 {
		return maxInt(1, totalWidth), 1, 0
	}

	separator := 1
	if totalWidth < 50 {
		separator = 0
	}

	left := clamp(totalWidth*38/100, 24, 54)
	minRight := 24
	if totalWidth < 70 {
		minRight = maxInt(10, totalWidth/2)
	}
	maxLeft := totalWidth - separator - minRight
	if maxLeft < 10 {
		maxLeft = maxInt(1, totalWidth-separator-1)
	}
	left = clamp(left, 1, maxLeft)
	right := totalWidth - separator - left
	if right < 1 {
		right = 1
		left = maxInt(1, totalWidth-separator-right)
	}
	return left, right, separator
}
