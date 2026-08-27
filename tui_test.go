package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestCommitPreviewLinesWrapsToAtMostTwoLines(t *testing.T) {
	lines := commitPreviewLines("Add the initial terminal interface with a detailed commit editor and safer apply workflow", 24, 2)
	if len(lines) == 0 || len(lines) > 2 {
		t.Fatalf("expected 1-2 preview lines, got %d: %#v", len(lines), lines)
	}
	for _, line := range lines {
		if lipgloss.Width(line) > 24 {
			t.Fatalf("preview line exceeds width: %q", line)
		}
	}
}

func TestDirtyCommitCount(t *testing.T) {
	commits := []CommitRecord{
		{Hash: "one", ShortHash: "one", AuthorName: "A", AuthorEmail: "a@example.com", AuthorDate: "2024-01-01T10:00:00Z", CommitterName: "A", CommitterEmail: "a@example.com", CommitterDate: "2024-01-01T10:00:00Z", Message: "first"},
		{Hash: "two", ShortHash: "two", AuthorName: "B", AuthorEmail: "b@example.com", AuthorDate: "2024-01-02T10:00:00Z", CommitterName: "B", CommitterEmail: "b@example.com", CommitterDate: "2024-01-02T10:00:00Z", Message: "second"},
	}
	original := mapByHash(commits)
	drafts := cloneMapByHash(commits)
	edited := drafts["two"]
	edited.Message = "edited second"
	drafts["two"] = edited

	if got := dirtyCommitCount(commits, original, drafts); got != 1 {
		t.Fatalf("expected one dirty commit, got %d", got)
	}
}

func TestStageAuthorReplacementMatchesPartialIdentityCaseInsensitively(t *testing.T) {
	commits := []CommitRecord{
		{Hash: "one", AuthorName: "Foo Bar", AuthorEmail: "foo@bar.baz", CommitterName: "Release Bot", CommitterEmail: "bot@example.com"},
		{Hash: "two", AuthorName: "Someone Else", AuthorEmail: "FOO@EXAMPLE.COM", CommitterName: "Other Bot", CommitterEmail: "other@example.com"},
		{Hash: "three", AuthorName: "Unaffected", AuthorEmail: "unaffected@example.com", CommitterName: "Keep Me", CommitterEmail: "keep@example.com"},
	}
	drafts := cloneMapByHash(commits)

	matched, err := stageAuthorReplacement(commits, drafts, "fOo", "New Author", "new@example.com", false)
	if err != nil {
		t.Fatal(err)
	}
	if matched != 2 {
		t.Fatalf("expected two matching commits, got %d", matched)
	}
	if got := drafts["one"]; got.AuthorName != "New Author" || got.AuthorEmail != "new@example.com" {
		t.Fatalf("expected first author identity to be replaced, got %#v", got)
	} else if got.CommitterName != "Release Bot" || got.CommitterEmail != "bot@example.com" {
		t.Fatalf("expected first committer identity to be preserved, got %#v", got)
	}
	if got := drafts["two"]; got.AuthorName != "New Author" || got.AuthorEmail != "new@example.com" {
		t.Fatalf("expected second author identity to be replaced, got %#v", got)
	} else if got.CommitterName != "Other Bot" || got.CommitterEmail != "other@example.com" {
		t.Fatalf("expected second committer identity to be preserved, got %#v", got)
	}
	if got := drafts["three"]; got.AuthorName != "Unaffected" || got.AuthorEmail != "unaffected@example.com" {
		t.Fatalf("expected non-matching author identity to be preserved, got %#v", got)
	}
}

func TestStageAuthorReplacementExactModeRequiresWholeFieldMatch(t *testing.T) {
	commits := []CommitRecord{
		{Hash: "partial", AuthorName: "Foo Bar", AuthorEmail: "foo@bar.baz"},
		{Hash: "exact", AuthorName: "FOO", AuthorEmail: "someone@example.com"},
	}
	drafts := cloneMapByHash(commits)

	matched, err := stageAuthorReplacement(commits, drafts, "foo", "New Author", "new@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	if matched != 1 {
		t.Fatalf("expected one exact match, got %d", matched)
	}
	if got := drafts["partial"]; got.AuthorName != "Foo Bar" || got.AuthorEmail != "foo@bar.baz" {
		t.Fatalf("expected partial match to remain unchanged in exact mode, got %#v", got)
	}
	if got := drafts["exact"]; got.AuthorName != "New Author" || got.AuthorEmail != "new@example.com" {
		t.Fatalf("expected exact match to be replaced, got %#v", got)
	}
}

func TestStageAuthorReplacementRejectsBlankInputsWithoutChangingDrafts(t *testing.T) {
	commits := []CommitRecord{{Hash: "one", AuthorName: "Foo Bar", AuthorEmail: "foo@bar.baz"}}
	tests := []struct {
		name             string
		query            string
		replacementName  string
		replacementEmail string
	}{
		{name: "search", query: "  ", replacementName: "New Author", replacementEmail: "new@example.com"},
		{name: "author name", query: "Foo", replacementName: "  ", replacementEmail: "new@example.com"},
		{name: "author email", query: "Foo", replacementName: "New Author", replacementEmail: "  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			drafts := cloneMapByHash(commits)
			matched, err := stageAuthorReplacement(commits, drafts, tt.query, tt.replacementName, tt.replacementEmail, false)
			if err == nil {
				t.Fatal("expected blank input to be rejected")
			}
			if matched != 0 {
				t.Fatalf("expected no matches on validation failure, got %d", matched)
			}
			if got := drafts["one"]; got.AuthorName != "Foo Bar" || got.AuthorEmail != "foo@bar.baz" {
				t.Fatalf("expected drafts to remain unchanged on validation failure, got %#v", got)
			}
		})
	}
}

func TestSelectionAfterFilterPreservesSelectedCommit(t *testing.T) {
	commits := []CommitRecord{
		{Hash: "one", ShortHash: "one", AuthorName: "A", Message: "alpha"},
		{Hash: "two", ShortHash: "two", AuthorName: "B", Message: "beta"},
		{Hash: "three", ShortHash: "three", AuthorName: "C", Message: "gamma"},
	}
	drafts := cloneMapByHash(commits)

	filtered, index, hash := selectionAfterFilter(commits, drafts, "two", "")
	if len(filtered) != 3 || index != 1 || hash != "two" {
		t.Fatalf("expected selected commit to be preserved, got len=%d index=%d hash=%q", len(filtered), index, hash)
	}

	filtered, index, hash = selectionAfterFilter(commits, drafts, "two", "gamma")
	if len(filtered) != 1 || index != 0 || hash != "three" {
		t.Fatalf("expected selection to move to first filtered commit, got len=%d index=%d hash=%q", len(filtered), index, hash)
	}
}

func TestNextFormFocusTraversal(t *testing.T) {
	next, done := nextFormFocus(formAuthorName, false)
	if done || next != formAuthorEmail {
		t.Fatalf("expected tab to advance to author email, got next=%d done=%v", next, done)
	}

	next, done = nextFormFocus(formMessage, false)
	if !done || next != formMessage {
		t.Fatalf("expected tab from final field to return to list, got next=%d done=%v", next, done)
	}

	next, done = nextFormFocus(formAuthorName, true)
	if !done || next != formAuthorName {
		t.Fatalf("expected shift-tab from first field to return to list, got next=%d done=%v", next, done)
	}
}

func TestInitialRepositoryPath(t *testing.T) {
	wd := t.TempDir()
	got, err := initialRepositoryPath(nil, func() (string, error) { return wd, nil })
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(wd) {
		t.Fatalf("expected cwd path %q, got %q", filepath.Clean(wd), got)
	}

	explicit := filepath.Join(wd, "repo")
	got, err = initialRepositoryPath([]string{explicit}, func() (string, error) { return "", errors.New("should not be called") })
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(explicit) {
		t.Fatalf("expected explicit path %q, got %q", filepath.Clean(explicit), got)
	}

	if _, err := initialRepositoryPath([]string{"one", "two"}, func() (string, error) { return wd, nil }); err == nil {
		t.Fatal("expected usage error for too many arguments")
	}
}

func TestRepositoryLoadErrorOpensPathEntryState(t *testing.T) {
	path := t.TempDir()
	model := newTUIModel(NewApp(), path)
	model.handleRepoLoaded(repoLoadedMsg{path: path, err: errors.New("not a git worktree")})

	if model.repoLoaded {
		t.Fatal("expected repository to remain unloaded")
	}
	if model.overlay != overlayPath {
		t.Fatalf("expected path overlay, got %v", model.overlay)
	}
	if model.pathInput.Value() != path {
		t.Fatalf("expected path input to keep attempted path %q, got %q", path, model.pathInput.Value())
	}
}

func TestBulkAuthorKeyOpensReplacementDialogInPartialMode(t *testing.T) {
	model := newTUIModel(NewApp(), "/tmp/repo")
	model.width = 100
	model.height = 30
	model.handleRepoLoaded(repoLoadedMsg{
		repo: RepositoryState{Path: "/tmp/repo", Clean: true, CurrentBranch: "main"},
		commits: []CommitRecord{
			{Hash: "one", ShortHash: "one", AuthorName: "Foo Bar", AuthorEmail: "foo@bar.baz", Message: "first"},
		},
	})

	updated, _ := model.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	view := updated.(tuiModel).View()
	if !strings.Contains(view, "Bulk replace author") {
		t.Fatalf("expected bulk author dialog after pressing b, got:\n%s", view)
	}
	if !strings.Contains(view, "Match mode: Partial") {
		t.Fatalf("expected partial match mode by default, got:\n%s", view)
	}
}

func TestBulkAuthorDialogStagesExactAuthorReplacement(t *testing.T) {
	model := newTUIModel(NewApp(), "/tmp/repo")
	model.handleRepoLoaded(repoLoadedMsg{
		repo: RepositoryState{Path: "/tmp/repo", Clean: true, CurrentBranch: "main"},
		commits: []CommitRecord{
			{Hash: "partial", ShortHash: "partial", AuthorName: "Foo Bar", AuthorEmail: "foo@bar.baz", Message: "partial"},
			{Hash: "exact", ShortHash: "exact", AuthorName: "FOO", AuthorEmail: "someone@example.com", Message: "exact"},
		},
	})
	send := func(msg tea.KeyMsg) {
		t.Helper()
		updated, _ := model.handleKey(msg)
		model = updated.(tuiModel)
	}

	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("foo")})
	send(tea.KeyMsg{Type: tea.KeyTab})
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("New Author")})
	send(tea.KeyMsg{Type: tea.KeyTab})
	send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("new@example.com")})
	send(tea.KeyMsg{Type: tea.KeyCtrlT})
	send(tea.KeyMsg{Type: tea.KeyEnter})

	if got := model.draftByHash["partial"]; got.AuthorName != "Foo Bar" || got.AuthorEmail != "foo@bar.baz" {
		t.Fatalf("expected partial-only match to remain unchanged in exact mode, got %#v", got)
	}
	if got := model.draftByHash["exact"]; got.AuthorName != "New Author" || got.AuthorEmail != "new@example.com" {
		t.Fatalf("expected exact match to be staged through the dialog, got %#v", got)
	}
	if model.overlay != overlayNone {
		t.Fatalf("expected successful replacement to close the dialog, got overlay %v", model.overlay)
	}
}

func TestViewFitsTerminalDimensions(t *testing.T) {
	model := newTUIModel(NewApp(), "/tmp/repo")
	model.width = 80
	model.height = 24

	initialView := model.View()
	if height := lipgloss.Height(initialView); height > model.height {
		t.Fatalf("expected initial view height <= %d, got %d", model.height, height)
	}
	if width := maxRenderedLineWidth(initialView); width > model.width-1 {
		t.Fatalf("expected initial max line width <= %d, got %d", model.width-1, width)
	}

	model.handleRepoLoaded(repoLoadedMsg{
		repo: RepositoryState{
			Path:          "/tmp/repo",
			Clean:         true,
			CurrentBranch: "main",
		},
		commits: []CommitRecord{
			{Hash: "1111111111111111111111111111111111111111", ShortHash: "1111111", AuthorName: "Ada Lovelace", AuthorEmail: "ada@example.com", AuthorDate: "2024-01-01T10:00:00Z", CommitterName: "Ada Lovelace", CommitterEmail: "ada@example.com", CommitterDate: "2024-01-01T10:00:00Z", Message: "Add a compact terminal interface that should not wrap every line"},
			{Hash: "2222222222222222222222222222222222222222", ShortHash: "2222222", AuthorName: "Grace Hopper", AuthorEmail: "grace@example.com", AuthorDate: "2024-01-02T10:00:00Z", CommitterName: "Grace Hopper", CommitterEmail: "grace@example.com", CommitterDate: "2024-01-02T10:00:00Z", Message: "Fix rendering dimensions"},
		},
	})

	view := model.View()
	if height := lipgloss.Height(view); height > model.height {
		t.Fatalf("expected view height <= %d, got %d", model.height, height)
	}
	if width := maxRenderedLineWidth(view); width > model.width-1 {
		t.Fatalf("expected max line width <= %d, got %d", model.width-1, width)
	}
}

func maxRenderedLineWidth(value string) int {
	width := 0
	for _, line := range strings.Split(value, "\n") {
		if lineWidth := lipgloss.Width(line); lineWidth > width {
			width = lineWidth
		}
	}
	return width
}
