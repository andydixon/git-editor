package main

import (
	"errors"
	"path/filepath"
	"testing"

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
