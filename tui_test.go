package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDatePickerOpensAndSavesFromDateField(t *testing.T) {
	model := newTUIModel(NewApp(), "/tmp/repo")
	commit := CommitRecord{
		Hash: "one", ShortHash: "one", AuthorName: "A", AuthorEmail: "a@example.com",
		AuthorDate: "2024-01-02T10:00:00+00:00", CommitterName: "A", CommitterEmail: "a@example.com",
		CommitterDate: "2024-01-02T10:00:00+00:00", Message: "subject",
	}
	model.handleRepoLoaded(repoLoadedMsg{repo: RepositoryState{Path: "/tmp/repo"}, commits: []CommitRecord{commit}})
	model.focus = focusForm
	model.focusFormField(formAuthorDate)

	updated, _ := model.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tuiModel)
	if model.overlay != overlayDatePicker {
		t.Fatalf("expected date picker overlay, got %v", model.overlay)
	}

	model.datePickerTime = time.Date(2025, time.March, 4, 12, 35, 45, 0, time.FixedZone("test", 90*60))
	updated, _ = model.updateDatePicker(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tuiModel)
	if got := model.draftByHash["one"].AuthorDate; got != "2025-03-04T12:35:45+01:30" {
		t.Fatalf("expected selected date to be saved, got %q", got)
	}
}

func TestPlannedRewriteCountIncludesCoAuthorCleanup(t *testing.T) {
	commits := []CommitRecord{
		{Hash: "one", Message: "subject\n\nCo-authored-by: A <a@example.com>"},
		{Hash: "two", Message: "another subject"},
	}
	original := mapByHash(commits)
	drafts := cloneMapByHash(commits)

	if got := plannedRewriteCount(commits, original, drafts, false); got != 0 {
		t.Fatalf("expected no planned rewrites with cleanup disabled, got %d", got)
	}
	if got := plannedRewriteCount(commits, original, drafts, true); got != 1 {
		t.Fatalf("expected one planned cleanup rewrite, got %d", got)
	}
}

func TestCalendarShiftsClampToValidDays(t *testing.T) {
	jan31 := time.Date(2024, time.January, 31, 9, 15, 30, 0, time.UTC)
	if got := shiftCalendarMonth(jan31, 1); got != time.Date(2024, time.February, 29, 9, 15, 30, 0, time.UTC) {
		t.Fatalf("expected leap-year February to clamp to the 29th, got %s", got)
	}
	leapDay := time.Date(2024, time.February, 29, 9, 15, 30, 0, time.UTC)
	if got := shiftCalendarYear(leapDay, 1); got != time.Date(2025, time.February, 28, 9, 15, 30, 0, time.UTC) {
		t.Fatalf("expected non-leap year to clamp to 28 February, got %s", got)
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
