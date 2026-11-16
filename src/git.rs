use std::collections::HashMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::LazyLock;
use std::time::{SystemTime, UNIX_EPOCH};

use chrono::{DateTime, FixedOffset, Local, NaiveDateTime, TimeZone};
use regex::Regex;

pub type Result<T> = std::result::Result<T, String>;

#[derive(Clone, Debug, Default, PartialEq)]
pub struct RepositoryState {
    pub path: String,
    pub clean: bool,
    pub detached_head: bool,
    pub current_branch: String,
    pub origin_url: String,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct CommitRecord {
    pub hash: String,
    pub short_hash: String,
    pub author_name: String,
    pub author_email: String,
    pub author_date: String,
    pub committer_name: String,
    pub committer_email: String,
    pub committer_date: String,
    pub refs: Vec<String>,
    pub tree: String,
    pub parents: Vec<String>,
    pub subject: String,
    pub message: String,
}

#[derive(Clone, Debug, Default)]
pub struct ApplyRequest {
    pub commits: Vec<CommitRecord>,
    pub force_push: bool,
    pub push_tags: bool,
    pub remove_co_authors: bool,
}

#[derive(Clone, Debug, Default)]
pub struct ApplyResult {
    pub rewritten_commits: usize,
    pub removed_co_authors: usize,
    pub force_pushed: bool,
    pub backup_reference: String,
    pub warnings: Vec<String>,
}

#[derive(Default)]
pub struct App {
    repo_path: Option<String>,
}

impl App {
    pub fn set_repository(&mut self, path: &str) -> Result<RepositoryState> {
        let normalized = normalize_path(path)?;
        validate_repository(&normalized)?;
        self.repo_path = Some(normalized.clone());
        inspect_repository(&normalized)
    }

    pub fn repository_state(&self) -> Result<RepositoryState> {
        match &self.repo_path {
            Some(repo) => inspect_repository(repo),
            None => Ok(RepositoryState::default()),
        }
    }

    fn repo(&self) -> Result<&str> {
        self.repo_path
            .as_deref()
            .ok_or_else(|| "no repository selected".to_string())
    }

    /// Oldest first, across all refs.
    pub fn load_history(&self) -> Result<Vec<CommitRecord>> {
        let output = run_git(
            self.repo()?,
            &[
                "log",
                "--all",
                "--reverse",
                "--date=iso-strict",
                "--pretty=format:%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%cn%x1f%ce%x1f%cI%x1f%D%x1f%T%x1f%P%x1f%B%x1e",
            ],
        )?;
        let mut commits = Vec::new();
        for raw in output.split('\x1e') {
            if raw.trim().is_empty() {
                continue;
            }
            let p: Vec<&str> = raw.splitn(12, '\x1f').collect();
            if p.len() != 12 {
                return Err(format!("could not parse commit record: {raw:?}"));
            }
            let message = normalize_message(p[11]);
            commits.push(CommitRecord {
                // records after the first start with the newline git puts between entries
                hash: p[0].trim_start().to_string(),
                short_hash: p[1].to_string(),
                author_name: p[2].to_string(),
                author_email: p[3].to_string(),
                author_date: p[4].to_string(),
                committer_name: p[5].to_string(),
                committer_email: p[6].to_string(),
                committer_date: p[7].to_string(),
                refs: split_and_trim(p[8], ","),
                tree: p[9].to_string(),
                parents: split_and_trim(p[10], " "),
                subject: first_line(&message),
                message,
            });
        }
        Ok(commits)
    }

    pub fn apply_changes(&self, req: &ApplyRequest) -> Result<ApplyResult> {
        let repo = self.repo()?;
        if req.commits.is_empty() {
            return Err("nothing to apply: no commit payload supplied".into());
        }
        if has_uncommitted_changes(repo)? {
            return Err(
                "repository has uncommitted changes; commit or stash before rewriting history"
                    .into(),
            );
        }

        let current_history = self.load_history()?;
        let incoming_by_hash: HashMap<&str, &CommitRecord> =
            req.commits.iter().map(|c| (c.hash.as_str(), c)).collect();

        let mut updated: HashMap<String, CommitRecord> = HashMap::new();
        let mut message_override: HashMap<String, bool> = HashMap::new();
        let mut removed_co_authors = 0;

        for current in &current_history {
            let incoming = incoming_by_hash
                .get(current.hash.as_str())
                .copied()
                .unwrap_or(current);
            let mut normalized = normalize_incoming_commit(incoming, current)
                .map_err(|e| format!("commit {} is invalid: {e}", incoming.short_hash))?;
            if req.remove_co_authors {
                let (msg, removed) = remove_co_author_lines(&normalized.message);
                normalized.subject = first_line(&msg);
                normalized.message = msg;
                removed_co_authors += removed;
            }
            if editable_fields_changed(current, &normalized) {
                message_override.insert(
                    current.hash.clone(),
                    normalize_message(&current.message) != normalize_message(&normalized.message),
                );
                updated.insert(current.hash.clone(), normalized);
            }
        }

        let mut result = ApplyResult {
            rewritten_commits: updated.len(),
            removed_co_authors,
            ..Default::default()
        };

        if !updated.is_empty() {
            let head_before = run_git(repo, &["rev-parse", "--verify", "HEAD"]).unwrap_or_default();
            let secs = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map(|d| d.as_secs())
                .unwrap_or(0);
            let backup_tag = format!("nexus-backup-{secs}");
            run_git(repo, &["tag", &backup_tag])
                .map_err(|e| format!("failed to create backup tag: {e}"))?;
            result.backup_reference = format!("refs/tags/{backup_tag}");

            rewrite_history(repo, &updated, &message_override)?;

            if let Err(e) = cleanup_filter_branch_refs(repo) {
                result.warnings.push(e);
            }

            let head_after = run_git(repo, &["rev-parse", "--verify", "HEAD"]).unwrap_or_default();
            if !head_before.trim().is_empty() && head_before.trim() == head_after.trim() {
                result.warnings.push(
                    "HEAD did not change; edited commits may be outside the current branch".into(),
                );
            }
        }

        if req.force_push {
            force_push_to_origin(repo, req.push_tags)?;
            result.force_pushed = true;
        }
        Ok(result)
    }
}

fn inspect_repository(repo: &str) -> Result<RepositoryState> {
    let dirty = has_uncommitted_changes(repo)?;
    let (detached_head, current_branch) =
        match run_git(repo, &["symbolic-ref", "--quiet", "--short", "HEAD"]) {
            Ok(b) => (false, b.trim().to_string()),
            Err(_) => (true, String::new()),
        };
    let origin = run_git(repo, &["remote", "get-url", "origin"]).unwrap_or_default();
    Ok(RepositoryState {
        path: repo.to_string(),
        clean: !dirty,
        detached_head,
        current_branch,
        origin_url: origin.trim().to_string(),
    })
}

pub fn normalize_path(path: &str) -> Result<String> {
    let candidate = path.trim();
    if candidate.is_empty() {
        return Err("repository path is required".into());
    }
    let expanded = match candidate.strip_prefix("~/") {
        Some(rest) => std::env::var_os("HOME")
            .map(|h| PathBuf::from(h).join(rest))
            .unwrap_or_else(|| candidate.into()),
        None => PathBuf::from(candidate),
    };
    let absolute = std::path::absolute(&expanded).map_err(|e| e.to_string())?;
    Ok(absolute.to_string_lossy().into_owned())
}

fn validate_repository(path: &str) -> Result<()> {
    let meta = fs::metadata(path).map_err(|e| format!("{path}: {e}"))?;
    if !meta.is_dir() {
        return Err("selected path is not a directory".into());
    }
    let out = run_git(path, &["rev-parse", "--is-inside-work-tree"])
        .map_err(|e| format!("selected path is not a git worktree: {e}"))?;
    if out.trim() != "true" {
        return Err("selected path is not inside a git worktree".into());
    }
    Ok(())
}

fn has_uncommitted_changes(repo: &str) -> Result<bool> {
    Ok(!run_git(repo, &["status", "--porcelain"])?.trim().is_empty())
}

pub(crate) fn rewrite_history(
    repo: &str,
    updated: &HashMap<String, CommitRecord>,
    message_override: &HashMap<String, bool>,
) -> Result<()> {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let temp_dir =
        std::env::temp_dir().join(format!("giteditor-rewrite-{}-{nanos}", std::process::id()));
    create_private_dir(&temp_dir)?;
    let result = write_filters_and_rewrite(repo, &temp_dir, updated, message_override);
    let _ = fs::remove_dir_all(&temp_dir);
    result
}

fn create_private_dir(path: &Path) -> Result<()> {
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    std::os::unix::fs::DirBuilderExt::mode(&mut builder, 0o700);
    builder
        .create(path)
        .map_err(|e| format!("{}: {e}", path.display()))
}

fn write_filters_and_rewrite(
    repo: &str,
    temp_dir: &Path,
    updated: &HashMap<String, CommitRecord>,
    message_override: &HashMap<String, bool>,
) -> Result<()> {
    let io = |e: std::io::Error| e.to_string();
    let message_dir = temp_dir.join("messages");
    fs::create_dir(&message_dir).map_err(io)?;

    let mut keys: Vec<&String> = updated.keys().collect();
    keys.sort();

    let mut env_filter = String::from("case \"$GIT_COMMIT\" in\n");
    let mut msg_filter = String::from("case \"$GIT_COMMIT\" in\n");
    for hash in keys {
        let c = &updated[hash];
        env_filter.push_str(&format!("{hash})\n"));
        for (var, value) in [
            ("GIT_AUTHOR_NAME", &c.author_name),
            ("GIT_AUTHOR_EMAIL", &c.author_email),
            ("GIT_AUTHOR_DATE", &c.author_date),
            ("GIT_COMMITTER_NAME", &c.committer_name),
            ("GIT_COMMITTER_EMAIL", &c.committer_email),
            ("GIT_COMMITTER_DATE", &c.committer_date),
        ] {
            env_filter.push_str(&format!("export {var}={}\n", shell_quote(value)));
        }
        env_filter.push_str(";;\n");

        if message_override.get(hash).copied().unwrap_or(false) {
            let message_path = message_dir.join(format!("{hash}.txt"));
            fs::write(&message_path, &c.message).map_err(io)?;
            msg_filter.push_str(&format!(
                "{hash})\ncat {}\n;;\n",
                shell_quote(&message_path.to_string_lossy())
            ));
        }
    }
    env_filter.push_str("esac\n");
    msg_filter.push_str("*)\ncat\n;;\nesac\n");

    let env_path = temp_dir.join("env-filter.sh");
    let msg_path = temp_dir.join("msg-filter.sh");
    fs::write(&env_path, env_filter).map_err(io)?;
    fs::write(&msg_path, msg_filter).map_err(io)?;

    let env_cmd = format!(". {}", shell_quote(&env_path.to_string_lossy()));
    let msg_cmd = format!(". {}", shell_quote(&msg_path.to_string_lossy()));
    let output = run_git(
        repo,
        &[
            "filter-branch",
            "-f",
            "--env-filter",
            &env_cmd,
            "--msg-filter",
            &msg_cmd,
            "--tag-name-filter",
            "cat",
            "--",
            "--all",
        ],
    )
    .map_err(|e| format!("history rewrite failed: {e}"))?;
    if output.matches(" was rewritten").count() == 0 {
        return Err(
            "git did not rewrite any refs; no effective history changes were applied".into(),
        );
    }
    Ok(())
}

fn cleanup_filter_branch_refs(repo: &str) -> Result<()> {
    let refs = run_git(
        repo,
        &["for-each-ref", "--format=%(refname)", "refs/original/"],
    )?;
    for r in split_and_trim(&refs, "\n") {
        run_git(repo, &["update-ref", "-d", &r])?;
    }
    run_git(repo, &["reflog", "expire", "--expire=now", "--all"])?;
    run_git(repo, &["gc", "--prune=now"])?;
    Ok(())
}

fn force_push_to_origin(repo: &str, push_tags: bool) -> Result<()> {
    if run_git(repo, &["remote", "get-url", "origin"]).is_err() {
        return Err("origin remote is not configured; cannot force push".into());
    }
    run_git(repo, &["push", "--force-with-lease", "origin", "--all"])?;
    if push_tags {
        run_git(repo, &["push", "--force-with-lease", "origin", "--tags"])?;
    }
    Ok(())
}

fn normalize_incoming_commit(
    incoming: &CommitRecord,
    current: &CommitRecord,
) -> Result<CommitRecord> {
    let mut c = incoming.clone();
    c.author_name = c.author_name.trim().to_string();
    c.author_email = c.author_email.trim().to_string();
    c.committer_name = c.committer_name.trim().to_string();
    c.committer_email = c.committer_email.trim().to_string();
    c.message = normalize_message(&c.message);
    if c.author_name.is_empty() || c.author_email.is_empty() {
        return Err("author name and email are required".into());
    }
    if c.committer_name.is_empty() || c.committer_email.is_empty() {
        return Err("committer name and email are required".into());
    }
    c.author_date = normalize_git_date(&c.author_date)
        .map_err(|_| format!("invalid author date {:?}", c.author_date))?;
    c.committer_date = normalize_git_date(&c.committer_date)
        .map_err(|_| format!("invalid committer date {:?}", c.committer_date))?;
    c.hash = current.hash.clone();
    c.short_hash = current.short_hash.clone();
    c.refs = current.refs.clone();
    c.tree = current.tree.clone();
    c.parents = current.parents.clone();
    c.subject = first_line(&c.message);
    Ok(c)
}

pub fn normalize_git_date(input: &str) -> Result<String> {
    Ok(format_git_date(&parse_git_date(input)?))
}

pub fn parse_git_date(input: &str) -> Result<DateTime<FixedOffset>> {
    let s = input.trim();
    if s.is_empty() {
        return Err("date cannot be empty".into());
    }
    if let Ok(t) = DateTime::parse_from_rfc3339(s).or_else(|_| DateTime::parse_from_rfc2822(s)) {
        return Ok(t);
    }
    for f in ["%Y-%m-%d %H:%M:%S %z", "%d %b %y %H:%M %z"] {
        if let Ok(t) = DateTime::parse_from_str(s, f) {
            return Ok(t);
        }
    }
    for f in [
        "%Y-%m-%dT%H:%M:%S",
        "%Y-%m-%dT%H:%M",
        "%Y-%m-%d %H:%M:%S",
        "%Y-%m-%d %H:%M",
    ] {
        if let Ok(n) = NaiveDateTime::parse_from_str(s, f)
            && let Some(t) = Local.from_local_datetime(&n).earliest()
        {
            return Ok(t.fixed_offset());
        }
    }
    Err("date must be ISO 8601, a standard Git date, or YYYY-MM-DD HH:MM[:SS]".into())
}

pub fn format_git_date<Tz: TimeZone>(t: &DateTime<Tz>) -> String
where
    Tz::Offset: std::fmt::Display,
{
    t.format("%Y-%m-%dT%H:%M:%S%:z").to_string()
}

static CO_AUTHOR_LINE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)^\s*co-authored(?:-|\s+)by\s*:").unwrap());
static AI_ATTRIBUTION: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?i)^\s*co-authored(?:-|\s+)by\s*:.*\b(claude|codex)\b").unwrap()
});
static BLANK_RUN: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"\n(?:[ \t]*\n){2,}").unwrap());

/// Drops Co-authored-by trailer lines and Claude-Session: lines. A Claude or Codex
/// co-author line also takes the contiguous non-blank lines that follow it.
/// Returns the cleaned message and the number of lines removed.
pub fn remove_co_author_lines(message: &str) -> (String, usize) {
    let normalized = message.replace("\r\n", "\n");
    let mut kept = Vec::new();
    let mut removed = 0;
    let mut in_block = false;
    for line in normalized.split('\n') {
        if AI_ATTRIBUTION.is_match(line) {
            in_block = true;
        } else if in_block && !line.trim().is_empty() {
        } else if CO_AUTHOR_LINE.is_match(line) || line.to_lowercase().contains("claude-session:") {
            in_block = false;
        } else {
            in_block = false;
            kept.push(line);
            continue;
        }
        removed += 1;
    }
    (
        normalize_message(&BLANK_RUN.replace_all(&kept.join("\n"), "\n\n")),
        removed,
    )
}

pub fn editable_fields_changed(current: &CommitRecord, incoming: &CommitRecord) -> bool {
    current.author_name != incoming.author_name
        || current.author_email != incoming.author_email
        || !git_dates_equal(&current.author_date, &incoming.author_date)
        || current.committer_name != incoming.committer_name
        || current.committer_email != incoming.committer_email
        || !git_dates_equal(&current.committer_date, &incoming.committer_date)
        || normalize_message(&current.message) != normalize_message(&incoming.message)
}

pub fn git_dates_equal(left: &str, right: &str) -> bool {
    match (normalize_git_date(left), normalize_git_date(right)) {
        (Ok(l), Ok(r)) => l == r,
        _ => left.trim() == right.trim(),
    }
}

pub fn first_line(message: &str) -> String {
    message.split('\n').next().unwrap_or("").trim().to_string()
}

pub fn normalize_message(message: &str) -> String {
    message
        .replace("\r\n", "\n")
        .trim_end_matches('\n')
        .to_string()
}

fn split_and_trim(value: &str, sep: &str) -> Vec<String> {
    value
        .split(sep)
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(String::from)
        .collect()
}

fn shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', r#"'"'"'"#))
}

pub fn run_git(repo: &str, args: &[&str]) -> Result<String> {
    let out = Command::new("git")
        .arg("-C")
        .arg(repo)
        .args(args)
        .env("FILTER_BRANCH_SQUELCH_WARNING", "1")
        .output()
        .map_err(|e| format!("git {}: {e}", args.join(" ")))?;
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
        let detail = if stderr.is_empty() {
            out.status.to_string()
        } else {
            stderr
        };
        return Err(format!("git {}: {detail}", args.join(" ")));
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

#[cfg(test)]
mod tests {
    use super::*;

    struct TempRepo(PathBuf);
    impl TempRepo {
        fn new(name: &str) -> Self {
            let dir =
                std::env::temp_dir().join(format!("git-edit-test-{name}-{}", std::process::id()));
            let _ = fs::remove_dir_all(&dir);
            fs::create_dir_all(&dir).unwrap();
            let repo = TempRepo(dir);
            repo.run(&["init", "-b", "master"]);
            repo.run(&["config", "user.name", "Original User"]);
            repo.run(&["config", "user.email", "original@example.com"]);
            repo
        }
        fn path(&self) -> &str {
            self.0.to_str().unwrap()
        }
        fn run(&self, args: &[&str]) -> String {
            run_git(self.path(), args).unwrap_or_else(|e| panic!("{e}"))
        }
        fn commit(&self, content: &str, args: &[&str]) {
            fs::write(self.0.join("a.txt"), content).unwrap();
            self.run(&["add", "a.txt"]);
            let mut full = vec!["commit"];
            full.extend_from_slice(args);
            self.run(&full);
        }
    }
    impl Drop for TempRepo {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn apply_changes_rewrites_metadata() {
        let repo = TempRepo::new("meta");
        repo.commit("one\n", &["-m", "first"]);
        repo.commit("two\n", &["-m", "second"]);
        let mut app = App::default();
        app.set_repository(repo.path()).unwrap();
        let mut history = app.load_history().unwrap();
        assert_eq!(history.len(), 2);
        history[0].author_name = "Edited Author".into();
        history[0].message = "edited first message".into();
        let result = app
            .apply_changes(&ApplyRequest {
                commits: history,
                ..Default::default()
            })
            .unwrap();
        assert!(result.rewritten_commits > 0);
        let out = repo.run(&["log", "--reverse", "--pretty=format:%an%x1f%B%x1e"]);
        assert!(out.contains("Edited Author"), "{out}");
        assert!(out.contains("edited first message"), "{out}");
    }

    #[test]
    fn rewrite_history_fails_when_no_refs_are_rewritten() {
        let repo = TempRepo::new("noop");
        repo.commit("one\n", &["-m", "first"]);
        let mut app = App::default();
        app.set_repository(repo.path()).unwrap();
        let history = app.load_history().unwrap();
        assert_eq!(history.len(), 1);
        let updated = HashMap::from([(history[0].hash.clone(), history[0].clone())]);
        let err = rewrite_history(repo.path(), &updated, &HashMap::new()).unwrap_err();
        assert!(err.contains("did not rewrite any refs"), "{err}");
    }

    #[test]
    fn apply_changes_updates_dates_and_removes_co_author_trailers() {
        let repo = TempRepo::new("dates");
        repo.commit(
            "one\n",
            &["-m", "subject", "-m", "Context: Co-authored-by: stays because it is prose.\n\nCo-authored-by: One <one@example.com>\nco-authored by: Two <two@example.com>"],
        );
        let mut app = App::default();
        app.set_repository(repo.path()).unwrap();
        let mut history = app.load_history().unwrap();
        history[0].author_date = "2020-06-15 09:30:45 +0530".into();
        history[0].committer_date = "Mon, 15 Jun 2020 04:00:45 +0000".into();
        let result = app
            .apply_changes(&ApplyRequest {
                commits: history,
                remove_co_authors: true,
                ..Default::default()
            })
            .unwrap();
        assert_eq!(result.removed_co_authors, 2);
        let log = repo.run(&["log", "-1", "--pretty=format:%aI%x1f%cI%x1f%B"]);
        let f: Vec<&str> = log.splitn(3, '\x1f').collect();
        assert!(git_dates_equal(f[0], "2020-06-15T09:30:45+05:30"), "{log}");
        assert!(git_dates_equal(f[1], "2020-06-15T04:00:45Z"), "{log}");
        let lower = log.to_lowercase();
        assert!(
            !lower.contains("co-authored-by: one") && !lower.contains("co-authored by: two"),
            "{log}"
        );
        assert!(
            log.contains("Context: Co-authored-by: stays because it is prose."),
            "{log}"
        );
    }

    #[test]
    fn remove_co_author_lines_drops_ai_attribution_blocks() {
        let message = "subject\n\nClaude-Session: https://example.com/a\nbody\n\nCo-authored-by: Ada <ada@example.com>\nSigned-off-by: Bob <bob@example.com>\n\nCo-Authored-By: Claude Opus 5 <noreply@anthropic.com>\nClaude-Session: https://example.com/b\nSomething else\n\nCo-authored-by: Codex <codex@openai.com>\nGenerated by Codex\n\ntail";
        let (cleaned, removed) = remove_co_author_lines(message);
        assert_eq!(removed, 7);
        assert_eq!(
            cleaned,
            "subject\n\nbody\n\nSigned-off-by: Bob <bob@example.com>\n\ntail"
        );
    }

    #[test]
    fn remove_co_author_lines_only_matches_whole_trailer_lines() {
        let message = "subject\r\n\r\nCo-authored-by: Ada <ada@example.com>\r\n  CO-AUTHORED BY: Grace <grace@example.com>\r\nNote: Co-authored-by: this is prose";
        let (cleaned, removed) = remove_co_author_lines(message);
        assert_eq!(removed, 2);
        assert!(
            !cleaned.contains("Ada") && !cleaned.contains("Grace"),
            "{cleaned:?}"
        );
        assert!(
            cleaned.contains("Note: Co-authored-by: this is prose"),
            "{cleaned:?}"
        );
    }

    #[test]
    fn parses_supported_date_formats() {
        for s in [
            "2024-01-02T03:04:05Z",
            "2024-01-02 03:04:05 +0100",
            "Tue, 02 Jan 2024 03:04:05 +0000",
            "2024-01-02 03:04",
        ] {
            assert!(parse_git_date(s).is_ok(), "{s}");
        }
        assert!(parse_git_date("nope").is_err());
    }
}
