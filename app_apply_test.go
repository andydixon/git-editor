package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyChangesRewritesMetadata(t *testing.T) {
	repo := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
		return string(out)
	}

	run("init")
	run("config", "user.name", "Original User")
	run("config", "user.email", "original@example.com")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "first")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "second")

	app := NewApp()
	if _, err := app.SetRepository(repo); err != nil {
		t.Fatal(err)
	}

	history, err := app.LoadHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) < 2 {
		t.Fatalf("expected at least 2 commits, got %d", len(history))
	}

	first := history[0]
	first.AuthorName = "Edited Author"
	first.Message = "edited first message"

	req := ApplyRequest{
		Commits:   history,
		ForcePush: false,
		PushTags:  false,
	}
	req.Commits[0] = first

	result, err := app.ApplyChanges(req)
	if err != nil {
		t.Fatal(err)
	}
	if result.RewrittenCommits == 0 {
		t.Fatalf("expected rewritten commits > 0, got %d", result.RewrittenCommits)
	}

	out := run("log", "--reverse", "--pretty=format:%an%x1f%B%x1e")
	if !strings.Contains(out, "Edited Author") {
		t.Fatalf("expected rewritten author in log, got: %s", out)
	}
	if !strings.Contains(out, "edited first message") {
		t.Fatalf("expected rewritten message in log, got: %s", out)
	}
}

func TestRewriteHistoryFailsWhenNoRefsAreRewritten(t *testing.T) {
	repo := t.TempDir()

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
		return string(out)
	}

	run("init")
	run("config", "user.name", "Original User")
	run("config", "user.email", "original@example.com")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "first")

	app := NewApp()
	if _, err := app.SetRepository(repo); err != nil {
		t.Fatal(err)
	}
	history, err := app.LoadHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected one commit, got %d", len(history))
	}

	unchanged := history[0]
	err = rewriteHistory(repo, map[string]CommitRecord{
		unchanged.Hash: unchanged,
	}, map[string]bool{})
	if err == nil {
		t.Fatal("expected rewriteHistory to fail when git reports unchanged refs")
	}
	if !strings.Contains(err.Error(), "did not rewrite any refs") {
		t.Fatalf("unexpected error: %v", err)
	}
}
