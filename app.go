package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type App struct {
	mu       sync.RWMutex
	repoPath string
}

func NewApp() *App {
	return &App{}
}

type RepositoryState struct {
	Path          string `json:"path"`
	Clean         bool   `json:"clean"`
	DetachedHead  bool   `json:"detachedHead"`
	CurrentBranch string `json:"currentBranch"`
	OriginURL     string `json:"originUrl"`
}

type CommitRecord struct {
	Hash           string   `json:"hash"`
	ShortHash      string   `json:"shortHash"`
	AuthorName     string   `json:"authorName"`
	AuthorEmail    string   `json:"authorEmail"`
	AuthorDate     string   `json:"authorDate"`
	CommitterName  string   `json:"committerName"`
	CommitterEmail string   `json:"committerEmail"`
	CommitterDate  string   `json:"committerDate"`
	Refs           []string `json:"refs"`
	Tree           string   `json:"tree"`
	Parents        []string `json:"parents"`
	Subject        string   `json:"subject"`
	Message        string   `json:"message"`
}

type ApplyRequest struct {
	Commits         []CommitRecord `json:"commits"`
	ForcePush       bool           `json:"forcePush"`
	PushTags        bool           `json:"pushTags"`
	RemoveCoAuthors bool           `json:"removeCoAuthors"`
}

type ApplyResult struct {
	RewrittenCommits int      `json:"rewrittenCommits"`
	RemovedCoAuthors int      `json:"removedCoAuthors"`
	ForcePushed      bool     `json:"forcePushed"`
	BackupReference  string   `json:"backupReference"`
	Warnings         []string `json:"warnings"`
}

func (a *App) SetRepository(path string) (RepositoryState, error) {
	normalized, err := normalizePath(path)
	if err != nil {
		return RepositoryState{}, err
	}
	if err := validateRepository(normalized); err != nil {
		return RepositoryState{}, err
	}

	a.mu.Lock()
	a.repoPath = normalized
	a.mu.Unlock()

	return a.inspectRepository(normalized)
}

func (a *App) GetRepositoryState() (RepositoryState, error) {
	repo, ok := a.currentRepository()
	if !ok {
		return RepositoryState{}, nil
	}
	return a.inspectRepository(repo)
}

func (a *App) LoadHistory() ([]CommitRecord, error) {
	repo, ok := a.currentRepository()
	if !ok {
		return nil, errors.New("no repository selected")
	}

	output, err := runGit(repo,
		"log",
		"--all",
		"--reverse",
		"--date=iso-strict",
		"--pretty=format:%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%cn%x1f%ce%x1f%cI%x1f%D%x1f%T%x1f%P%x1f%B%x1e",
	)
	if err != nil {
		return nil, err
	}

	records := strings.Split(output, "\x1e")
	commits := make([]CommitRecord, 0, len(records))

	for _, raw := range records {
		if strings.TrimSpace(raw) == "" {
			continue
		}

		parts := strings.SplitN(raw, "\x1f", 12)
		if len(parts) != 12 {
			return nil, fmt.Errorf("could not parse commit record: %q", raw)
		}

		message := normalizeMessage(parts[11])
		commits = append(commits, CommitRecord{
			Hash:           parts[0],
			ShortHash:      parts[1],
			AuthorName:     parts[2],
			AuthorEmail:    parts[3],
			AuthorDate:     parts[4],
			CommitterName:  parts[5],
			CommitterEmail: parts[6],
			CommitterDate:  parts[7],
			Refs:           splitAndTrim(parts[8], ","),
			Tree:           parts[9],
			Parents:        splitAndTrim(parts[10], " "),
			Subject:        firstLine(message),
			Message:        message,
		})
	}

	return commits, nil
}

func (a *App) ApplyChanges(req ApplyRequest) (ApplyResult, error) {
	repo, ok := a.currentRepository()
	if !ok {
		return ApplyResult{}, errors.New("no repository selected")
	}
	if len(req.Commits) == 0 {
		return ApplyResult{}, errors.New("nothing to apply: no commit payload supplied")
	}

	dirty, err := hasUncommittedChanges(repo)
	if err != nil {
		return ApplyResult{}, err
	}
	if dirty {
		return ApplyResult{}, errors.New("repository has uncommitted changes; commit or stash before rewriting history")
	}

	currentHistory, err := a.LoadHistory()
	if err != nil {
		return ApplyResult{}, err
	}

	incomingByHash := make(map[string]CommitRecord, len(req.Commits))
	for _, incoming := range req.Commits {
		incomingByHash[incoming.Hash] = incoming
	}

	updatedByHash := make(map[string]CommitRecord)
	messageOverride := make(map[string]bool)
	removedCoAuthors := 0

	for _, current := range currentHistory {
		incoming, exists := incomingByHash[current.Hash]
		if !exists {
			incoming = current
		}

		normalized, err := normalizeIncomingCommit(incoming, current)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("commit %s is invalid: %w", incoming.ShortHash, err)
		}

		if req.RemoveCoAuthors {
			var removed int
			normalized.Message, removed = removeCoAuthorLines(normalized.Message)
			normalized.Subject = firstLine(normalized.Message)
			removedCoAuthors += removed
		}

		if editableFieldsChanged(current, normalized) {
			updatedByHash[incoming.Hash] = normalized
			messageOverride[incoming.Hash] = normalizeMessage(current.Message) != normalizeMessage(normalized.Message)
		}
	}

	result := ApplyResult{
		RewrittenCommits: len(updatedByHash),
		RemovedCoAuthors: removedCoAuthors,
		Warnings:         []string{},
	}

	if len(updatedByHash) > 0 {
		headBefore, _ := runGit(repo, "rev-parse", "--verify", "HEAD")
		backupTag := fmt.Sprintf("nexus-backup-%d", time.Now().UTC().Unix())
		if _, err := runGit(repo, "tag", backupTag); err != nil {
			return ApplyResult{}, fmt.Errorf("failed to create backup tag: %w", err)
		}
		result.BackupReference = "refs/tags/" + backupTag

		if err := rewriteHistory(repo, updatedByHash, messageOverride); err != nil {
			return ApplyResult{}, err
		}

		if err := cleanupFilterBranchRefs(repo); err != nil {
			result.Warnings = append(result.Warnings, err.Error())
		}

		headAfter, _ := runGit(repo, "rev-parse", "--verify", "HEAD")
		if strings.TrimSpace(headBefore) != "" &&
			strings.TrimSpace(headAfter) != "" &&
			strings.TrimSpace(headBefore) == strings.TrimSpace(headAfter) {
			result.Warnings = append(result.Warnings, "HEAD did not change; edited commits may be outside the current branch")
		}
	}

	if req.ForcePush {
		if err := forcePushToOrigin(repo, req.PushTags); err != nil {
			return result, err
		}
		result.ForcePushed = true
	}

	return result, nil
}

func (a *App) inspectRepository(repo string) (RepositoryState, error) {
	dirty, err := hasUncommittedChanges(repo)
	if err != nil {
		return RepositoryState{}, err
	}

	branchName, branchErr := runGit(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	detachedHead := false
	currentBranch := strings.TrimSpace(branchName)
	if branchErr != nil {
		detachedHead = true
		currentBranch = ""
	}

	origin, err := runGit(repo, "remote", "get-url", "origin")
	if err != nil {
		origin = ""
	}

	return RepositoryState{
		Path:          repo,
		Clean:         !dirty,
		DetachedHead:  detachedHead,
		CurrentBranch: currentBranch,
		OriginURL:     strings.TrimSpace(origin),
	}, nil
}

func (a *App) currentRepository() (string, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.repoPath == "" {
		return "", false
	}
	return a.repoPath, true
}

func normalizePath(path string) (string, error) {
	candidate := strings.TrimSpace(path)
	if candidate == "" {
		return "", errors.New("repository path is required")
	}

	absolute, err := filepath.Abs(filepath.Clean(candidate))
	if err != nil {
		return "", err
	}
	return absolute, nil
}

func validateRepository(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("selected path is not a directory")
	}

	output, err := runGit(path, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return fmt.Errorf("selected path is not a git worktree: %w", err)
	}
	if strings.TrimSpace(output) != "true" {
		return errors.New("selected path is not inside a git worktree")
	}
	return nil
}

func hasUncommittedChanges(repo string) (bool, error) {
	status, err := runGit(repo, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(status) != "", nil
}

func rewriteHistory(repo string, updatedByHash map[string]CommitRecord, messageOverride map[string]bool) error {
	tempDir, err := os.MkdirTemp("", "giteditor-rewrite-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)

	messageDir := filepath.Join(tempDir, "messages")
	if err := os.MkdirAll(messageDir, 0o700); err != nil {
		return err
	}

	keys := make([]string, 0, len(updatedByHash))
	for hash := range updatedByHash {
		keys = append(keys, hash)
	}
	sort.Strings(keys)

	messageFileByHash := make(map[string]string)
	for _, hash := range keys {
		if !messageOverride[hash] {
			continue
		}
		commit := updatedByHash[hash]
		messagePath := filepath.Join(messageDir, hash+".txt")
		if err := os.WriteFile(messagePath, []byte(commit.Message), 0o600); err != nil {
			return err
		}
		messageFileByHash[hash] = messagePath
	}

	var envFilter strings.Builder
	envFilter.WriteString("case \"$GIT_COMMIT\" in\n")
	for _, hash := range keys {
		commit := updatedByHash[hash]
		fmt.Fprintf(&envFilter, "%s)\n", hash)
		fmt.Fprintf(&envFilter, "export GIT_AUTHOR_NAME=%s\n", shellQuote(commit.AuthorName))
		fmt.Fprintf(&envFilter, "export GIT_AUTHOR_EMAIL=%s\n", shellQuote(commit.AuthorEmail))
		fmt.Fprintf(&envFilter, "export GIT_AUTHOR_DATE=%s\n", shellQuote(commit.AuthorDate))
		fmt.Fprintf(&envFilter, "export GIT_COMMITTER_NAME=%s\n", shellQuote(commit.CommitterName))
		fmt.Fprintf(&envFilter, "export GIT_COMMITTER_EMAIL=%s\n", shellQuote(commit.CommitterEmail))
		fmt.Fprintf(&envFilter, "export GIT_COMMITTER_DATE=%s\n", shellQuote(commit.CommitterDate))
		envFilter.WriteString(";;\n")
	}
	envFilter.WriteString("esac\n")

	var msgFilter strings.Builder
	msgFilter.WriteString("case \"$GIT_COMMIT\" in\n")
	for _, hash := range keys {
		messagePath, hasOverride := messageFileByHash[hash]
		if !hasOverride {
			continue
		}
		fmt.Fprintf(&msgFilter, "%s)\n", hash)
		fmt.Fprintf(&msgFilter, "cat %s\n", shellQuote(messagePath))
		msgFilter.WriteString(";;\n")
	}
	msgFilter.WriteString("*)\ncat\n;;\nesac\n")

	envFilterPath := filepath.Join(tempDir, "env-filter.sh")
	msgFilterPath := filepath.Join(tempDir, "msg-filter.sh")

	if err := os.WriteFile(envFilterPath, []byte(envFilter.String()), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(msgFilterPath, []byte(msgFilter.String()), 0o700); err != nil {
		return err
	}

	envFilterCmd := ". " + shellQuote(envFilterPath)
	msgFilterCmd := ". " + shellQuote(msgFilterPath)

	output, err := runGit(
		repo,
		"filter-branch",
		"-f",
		"--env-filter", envFilterCmd,
		"--msg-filter", msgFilterCmd,
		"--tag-name-filter", "cat",
		"--",
		"--all",
	)
	if err != nil {
		return fmt.Errorf("history rewrite failed: %w", err)
	}
	if rewrittenRefCount(output) == 0 {
		return errors.New("git did not rewrite any refs; no effective history changes were applied")
	}

	return nil
}

func rewrittenRefCount(filterBranchOutput string) int {
	return strings.Count(filterBranchOutput, " was rewritten")
}

func cleanupFilterBranchRefs(repo string) error {
	refsRaw, err := runGit(repo, "for-each-ref", "--format=%(refname)", "refs/original/")
	if err != nil {
		return err
	}

	for _, ref := range splitAndTrim(refsRaw, "\n") {
		if _, err := runGit(repo, "update-ref", "-d", ref); err != nil {
			return err
		}
	}

	if _, err := runGit(repo, "reflog", "expire", "--expire=now", "--all"); err != nil {
		return err
	}
	if _, err := runGit(repo, "gc", "--prune=now"); err != nil {
		return err
	}

	return nil
}

func forcePushToOrigin(repo string, pushTags bool) error {
	if _, err := runGit(repo, "remote", "get-url", "origin"); err != nil {
		return errors.New("origin remote is not configured; cannot force push")
	}

	if _, err := runGit(repo, "push", "--force-with-lease", "origin", "--all"); err != nil {
		return err
	}
	if pushTags {
		if _, err := runGit(repo, "push", "--force-with-lease", "origin", "--tags"); err != nil {
			return err
		}
	}

	return nil
}

func normalizeIncomingCommit(incoming CommitRecord, current CommitRecord) (CommitRecord, error) {
	incoming.AuthorName = strings.TrimSpace(incoming.AuthorName)
	incoming.AuthorEmail = strings.TrimSpace(incoming.AuthorEmail)
	incoming.CommitterName = strings.TrimSpace(incoming.CommitterName)
	incoming.CommitterEmail = strings.TrimSpace(incoming.CommitterEmail)
	incoming.Message = normalizeMessage(incoming.Message)

	if incoming.AuthorName == "" || incoming.AuthorEmail == "" {
		return CommitRecord{}, errors.New("author name and email are required")
	}
	if incoming.CommitterName == "" || incoming.CommitterEmail == "" {
		return CommitRecord{}, errors.New("committer name and email are required")
	}

	authorDate, err := normalizeGitDate(incoming.AuthorDate)
	if err != nil {
		return CommitRecord{}, fmt.Errorf("invalid author date %q", incoming.AuthorDate)
	}
	committerDate, err := normalizeGitDate(incoming.CommitterDate)
	if err != nil {
		return CommitRecord{}, fmt.Errorf("invalid committer date %q", incoming.CommitterDate)
	}

	incoming.AuthorDate = authorDate
	incoming.CommitterDate = committerDate
	incoming.Hash = current.Hash
	incoming.ShortHash = current.ShortHash
	incoming.Refs = current.Refs
	incoming.Tree = current.Tree
	incoming.Parents = current.Parents
	incoming.Subject = firstLine(incoming.Message)

	return incoming, nil
}

func normalizeGitDate(input string) (string, error) {
	parsed, err := parseGitDate(input)
	if err != nil {
		return "", err
	}
	return formatGitDate(parsed), nil
}

func parseGitDate(input string) (time.Time, error) {
	date := strings.TrimSpace(input)
	if date == "" {
		return time.Time{}, errors.New("date cannot be empty")
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05 -0700",
		time.RFC1123Z,
		time.RFC822Z,
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}

	for _, layout := range layouts {
		var (
			t   time.Time
			err error
		)

		if strings.Contains(layout, "Z07") || strings.Contains(layout, "-0700") || layout == time.RFC1123Z || layout == time.RFC822Z {
			t, err = time.Parse(layout, date)
		} else {
			t, err = time.ParseInLocation(layout, date, time.Local)
		}
		if err == nil {
			return t, nil
		}
	}

	return time.Time{}, errors.New("date must be ISO 8601, a standard Git date, or YYYY-MM-DD HH:MM[:SS]")
}

func formatGitDate(value time.Time) string {
	return value.Format("2006-01-02T15:04:05-07:00")
}

var coAuthorLinePattern = regexp.MustCompile(`(?i)^\s*co-authored(?:-|\s+)by\s*:`)

func removeCoAuthorLines(message string) (string, int) {
	lines := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines))
	removed := 0
	for _, line := range lines {
		if coAuthorLinePattern.MatchString(line) {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	return normalizeMessage(strings.Join(kept, "\n")), removed
}

func editableFieldsChanged(current CommitRecord, incoming CommitRecord) bool {
	return current.AuthorName != incoming.AuthorName ||
		current.AuthorEmail != incoming.AuthorEmail ||
		!gitDatesEqual(current.AuthorDate, incoming.AuthorDate) ||
		current.CommitterName != incoming.CommitterName ||
		current.CommitterEmail != incoming.CommitterEmail ||
		!gitDatesEqual(current.CommitterDate, incoming.CommitterDate) ||
		normalizeMessage(current.Message) != normalizeMessage(incoming.Message)
}

func gitDatesEqual(left string, right string) bool {
	leftDate, leftErr := normalizeGitDate(left)
	rightDate, rightErr := normalizeGitDate(right)
	if leftErr != nil || rightErr != nil {
		return strings.TrimSpace(left) == strings.TrimSpace(right)
	}
	return leftDate == rightDate
}

func firstLine(message string) string {
	parts := strings.SplitN(message, "\n", 2)
	return strings.TrimSpace(parts[0])
}

func normalizeMessage(message string) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	return strings.TrimRight(message, "\n")
}

func splitAndTrim(value string, sep string) []string {
	raw := strings.Split(value, sep)
	result := make([]string, 0, len(raw))
	for _, candidate := range raw {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		result = append(result, candidate)
	}
	return result
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func runGit(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "FILTER_BRANCH_SQUELCH_WARNING=1")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), detail)
	}

	return stdout.String(), nil
}
