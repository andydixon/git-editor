use std::collections::{HashMap, HashSet};
use std::sync::LazyLock;

use chrono::{
    DateTime, Datelike, Duration, FixedOffset, Local, Months, NaiveDate, NaiveTime, TimeZone,
    Timelike, Utc,
};
use ratatui::Frame;
use ratatui::crossterm::event::{KeyCode, KeyEvent, KeyModifiers};
use ratatui::layout::{Constraint, Layout, Rect};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, BorderType, Paragraph};
use regex::Regex;
use tui_textarea::{CursorMove, TextArea};
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

use crate::git::{
    App, ApplyRequest, ApplyResult, CommitRecord, RepositoryState, Result, editable_fields_changed,
    first_line, format_git_date, normalize_git_date, normalize_message, normalize_path,
    parse_git_date, remove_co_author_lines,
};

const FORM_AUTHOR_DATE: usize = 2;
const FORM_COMMITTER_DATE: usize = 5;
const FORM_MESSAGE: usize = 6;
const FORM_FIELD_COUNT: usize = 7;
const FORM_LABELS: [&str; 6] = [
    "Author name",
    "Author email",
    "Author date",
    "Committer name",
    "Committer email",
    "Committer date",
];

const BULK_SEARCH: usize = 0;
const BULK_NAME: usize = 1;
const BULK_EMAIL: usize = 2;
const BULK_FIELD_COUNT: usize = 3;

const SPREAD_START: usize = 0;
const SPREAD_END: usize = 1;
const SPREAD_FROM: usize = 2;
const SPREAD_TO: usize = 3;
const SPREAD_FIELD_COUNT: usize = 4;

#[derive(Clone, Copy, PartialEq, Debug)]
enum Focus {
    List,
    Form,
}

#[derive(Clone, Copy, PartialEq, Debug)]
enum Overlay {
    None,
    Search,
    Path,
    BulkAuthor,
    ConfirmApply,
    ConfirmForce,
    Help,
    DatePicker,
    DateSpread,
}

#[derive(Clone, Copy, PartialEq, Debug)]
enum StatusKind {
    Info,
    Success,
    Error,
}

const INK: Color = Color::Rgb(0xDC, 0xE7, 0xF7);
const MUTED: Color = Color::Rgb(0x7E, 0x8D, 0xA3);
const DIM: Color = Color::Rgb(0x50, 0x60, 0x75);
const PANEL: Color = Color::Rgb(0x11, 0x1B, 0x27);
const PANEL_LIFT: Color = Color::Rgb(0x17, 0x25, 0x36);
const SELECTED: Color = Color::Rgb(0x20, 0x36, 0x4B);
const CYAN: Color = Color::Rgb(0x55, 0xD6, 0xFF);
const GREEN: Color = Color::Rgb(0x78, 0xF0, 0xB4);
const MAGENTA: Color = Color::Rgb(0xD6, 0x8D, 0xFF);
const RED: Color = Color::Rgb(0xFF, 0x6B, 0x7A);
const BORDER: Color = Color::Rgb(0x2B, 0x43, 0x5A);
const BASE_BG: Color = Color::Rgb(0x08, 0x11, 0x1B);
const BAR_BG: Color = Color::Rgb(0x0C, 0x18, 0x24);

fn header_style() -> Style {
    Style::default().fg(CYAN).add_modifier(Modifier::BOLD)
}
fn muted() -> Style {
    Style::default().fg(MUTED)
}
fn dim() -> Style {
    Style::default().fg(DIM)
}

type Input = TextArea<'static>;

fn new_input(placeholder: &str) -> Input {
    input_with(placeholder, "")
}

fn input_with(placeholder: &str, value: &str) -> Input {
    let mut ta = TextArea::from(value.split('\n'));
    ta.set_placeholder_text(placeholder);
    ta.set_placeholder_style(dim());
    ta.set_style(Style::default().fg(INK));
    ta.set_cursor_line_style(Style::default());
    ta.move_cursor(CursorMove::Bottom);
    ta.move_cursor(CursorMove::End);
    ta
}

fn value(ta: &Input) -> String {
    ta.lines().join("\n")
}

/// Replaces the content and drops undo history, so undo cannot leak another commit's text.
fn set_value(ta: &mut Input, v: &str) {
    *ta = input_with(ta.placeholder_text(), v);
}

/// Feeds a key to a single-line input, ignoring the keys that would insert a newline.
fn edit_line(ta: &mut Input, key: KeyEvent) {
    let newline = key.code == KeyCode::Enter
        || (key.modifiers.contains(KeyModifiers::CONTROL) && key.code == KeyCode::Char('m'));
    if !newline {
        ta.input(key);
    }
}

fn key_name(k: &KeyEvent) -> String {
    let shift = k.modifiers.contains(KeyModifiers::SHIFT);
    match k.code {
        KeyCode::Char(c) if k.modifiers.contains(KeyModifiers::CONTROL) => {
            format!("ctrl+{}", c.to_ascii_lowercase())
        }
        KeyCode::Char(c) => c.to_string(),
        KeyCode::Enter => "enter".into(),
        KeyCode::Esc => "esc".into(),
        KeyCode::Tab => "tab".into(),
        KeyCode::BackTab => "shift+tab".into(),
        KeyCode::Up => "up".into(),
        KeyCode::Down => "down".into(),
        KeyCode::Left if shift => "shift+left".into(),
        KeyCode::Right if shift => "shift+right".into(),
        KeyCode::Left => "left".into(),
        KeyCode::Right => "right".into(),
        KeyCode::PageUp => "pgup".into(),
        KeyCode::PageDown => "pgdown".into(),
        KeyCode::Home => "home".into(),
        KeyCode::End => "end".into(),
        _ => String::new(),
    }
}

pub enum Pending {
    Load(String),
    Apply(ApplyRequest),
}

pub struct Model {
    app: App,
    /// Slow git work the main loop runs after drawing the "working" state.
    pub pending: Option<Pending>,
    pub quit: bool,

    repository: RepositoryState,
    repo_loaded: bool,
    /// Newest first.
    commits: Vec<CommitRecord>,
    original: HashMap<String, CommitRecord>,
    drafts: HashMap<String, CommitRecord>,

    selected_hash: String,
    selected_index: usize,
    list_offset: usize,
    height: u16,

    focus: Focus,
    overlay: Overlay,

    form_focus: usize,
    inputs: Vec<Input>,
    message: Input,

    search: Input,
    path: Input,
    confirm: Input,
    picker_time: DateTime<FixedOffset>,
    picker_target: usize,
    bulk_focus: usize,
    bulk_inputs: Vec<Input>,
    bulk_exact: bool,
    spread_focus: usize,
    spread_inputs: Vec<Input>,

    force_push: bool,
    push_tags: bool,
    remove_co_authors: bool,
    ai_only: bool,
    ai_hashes: HashSet<String>,
    busy: bool,

    status: String,
    status_kind: StatusKind,
}

pub fn initial_repository_path(
    args: &[String],
    getwd: impl FnOnce() -> std::io::Result<std::path::PathBuf>,
) -> Result<String> {
    match args {
        [] => normalize_path(&getwd().map_err(|e| e.to_string())?.to_string_lossy()),
        [path] => normalize_path(path),
        _ => Err("usage: git edit [repo-path]".into()),
    }
}

impl Model {
    pub fn new(initial_path: &str) -> Self {
        Model {
            app: App::default(),
            pending: Some(Pending::Load(initial_path.to_string())),
            quit: false,
            repository: RepositoryState::default(),
            repo_loaded: false,
            commits: Vec::new(),
            original: HashMap::new(),
            drafts: HashMap::new(),
            selected_hash: String::new(),
            selected_index: 0,
            list_offset: 0,
            height: 0,
            focus: Focus::List,
            overlay: Overlay::None,
            form_focus: 0,
            inputs: (0..FORM_FIELD_COUNT - 1).map(|_| new_input("")).collect(),
            message: new_input("Commit message"),
            search: new_input("Search hash, author, email, or message"),
            path: input_with("/path/to/repository", initial_path),
            confirm: new_input("FORCE"),
            picker_time: Local::now().fixed_offset(),
            picker_target: FORM_AUTHOR_DATE,
            bulk_focus: 0,
            bulk_inputs: [
                "Existing author name or email",
                "Replacement author name",
                "Replacement author email",
            ]
            .iter()
            .map(|p| new_input(p))
            .collect(),
            bulk_exact: false,
            spread_focus: 0,
            spread_inputs: ["YYYY-MM-DD", "YYYY-MM-DD", "08:00", "12:00"]
                .iter()
                .map(|p| new_input(p))
                .collect(),
            force_push: false,
            push_tags: true,
            remove_co_authors: false,
            ai_only: false,
            ai_hashes: HashSet::new(),
            busy: true,
            status: "Loading repository...".into(),
            status_kind: StatusKind::Info,
        }
    }

    pub fn run_pending(&mut self, pending: Pending) {
        match pending {
            Pending::Load(path) => {
                let loaded = self
                    .app
                    .set_repository(&path)
                    .and_then(|repo| Ok((repo, self.app.load_history()?)));
                self.handle_repo_loaded(&path, loaded);
            }
            Pending::Apply(req) => {
                let result = self.app.apply_changes(&req);
                self.handle_apply_finished(result);
            }
        }
        self.busy = false;
    }

    fn set_commits(&mut self, oldest_first: Vec<CommitRecord>) {
        self.commits = oldest_first.into_iter().rev().collect();
        self.original = map_by_hash(&self.commits);
        self.drafts = map_by_hash(&self.commits);
        self.ai_hashes = ai_commit_hashes(&self.commits);
    }

    fn handle_repo_loaded(
        &mut self,
        path: &str,
        loaded: Result<(RepositoryState, Vec<CommitRecord>)>,
    ) {
        let (repo, commits) = match loaded {
            Ok(v) => v,
            Err(e) => {
                self.repo_loaded = false;
                self.repository = RepositoryState::default();
                self.set_commits(Vec::new());
                self.selected_hash.clear();
                self.selected_index = 0;
                self.list_offset = 0;
                self.overlay = Overlay::Path;
                self.focus = Focus::List;
                set_value(&mut self.path, path);
                self.set_status(format!("Repository unavailable: {e}"), StatusKind::Error);
                return;
            }
        };
        self.repo_loaded = true;
        set_value(&mut self.path, &repo.path);
        let name = base_name(&repo.path);
        self.repository = repo;
        self.set_commits(commits);
        self.reconcile_selection();
        self.load_selected_into_form();
        self.overlay = Overlay::None;
        self.set_status(
            format!("Loaded {} commits from {name}", self.commits.len()),
            StatusKind::Success,
        );
    }

    fn handle_apply_finished(&mut self, result: Result<ApplyResult>) {
        let result = match result {
            Ok(r) => r,
            Err(e) => return self.set_status(e, StatusKind::Error),
        };
        match self.app.repository_state() {
            Ok(repo) if !repo.path.is_empty() => {
                self.repository = repo;
                self.repo_loaded = true;
            }
            Ok(_) => {}
            Err(e) => return self.set_status(e, StatusKind::Error),
        }
        match self.app.load_history() {
            Ok(commits) => {
                self.remove_co_authors = false;
                self.set_commits(commits);
                self.reconcile_selection();
                self.load_selected_into_form();
            }
            Err(e) => return self.set_status(e, StatusKind::Error),
        }
        self.set_status(apply_result_summary(&result), StatusKind::Success);
    }

    pub fn handle_key(&mut self, key: KeyEvent) {
        let name = key_name(&key);
        if name == "ctrl+c" {
            self.quit = true;
            return;
        }
        if self.busy {
            return;
        }
        match self.overlay {
            Overlay::Help => {
                if matches!(name.as_str(), "esc" | "?" | "q") {
                    self.overlay = Overlay::None;
                }
                return;
            }
            Overlay::Search => return self.update_search(key, &name),
            Overlay::Path => return self.update_path(key, &name),
            Overlay::BulkAuthor => return self.update_bulk_author(key, &name),
            Overlay::DateSpread => return self.update_date_spread(key, &name),
            Overlay::ConfirmApply | Overlay::ConfirmForce => {
                return self.update_confirmation(key, &name);
            }
            Overlay::DatePicker => return self.update_date_picker(&name),
            Overlay::None => {}
        }
        if self.focus == Focus::Form {
            return self.update_form(key, &name);
        }

        match name.as_str() {
            "q" => self.quit = true,
            "?" => self.overlay = Overlay::Help,
            "/" => self.overlay = Overlay::Search,
            "p" | "o" => self.overlay = Overlay::Path,
            "b" if self.repo_loaded => self.open_bulk_author(),
            "b" => self.set_status(
                "Load a repository before replacing authors.",
                StatusKind::Error,
            ),
            "d" if self.repo_loaded => {
                self.overlay = Overlay::DateSpread;
                self.spread_focus = SPREAD_START;
                self.set_status(
                    "Enter a date range and daily time window.",
                    StatusKind::Info,
                );
            }
            "d" => self.set_status(
                "Load a repository before rewriting dates.",
                StatusKind::Error,
            ),
            "r" if !self.repository.path.is_empty() => {
                self.busy = true;
                self.set_status("Reloading history...", StatusKind::Info);
                self.pending = Some(Pending::Load(self.repository.path.clone()));
            }
            "r" => self.overlay = Overlay::Path,
            "f" => {
                self.force_push = !self.force_push;
                self.set_status(
                    format!("Force push {}", on_off(self.force_push)),
                    StatusKind::Info,
                );
            }
            "t" => {
                self.push_tags = !self.push_tags;
                self.set_status(
                    format!("Push tags {}", on_off(self.push_tags)),
                    StatusKind::Info,
                );
            }
            "c" => {
                self.remove_co_authors = !self.remove_co_authors;
                let (count, lines) = co_author_cleanup_impact(&self.commits, &self.drafts);
                self.set_status(
                    format!(
                        "Co-author cleanup {}: {lines} line{} across {count} commit{}",
                        on_off(self.remove_co_authors),
                        plural(lines),
                        plural(count)
                    ),
                    StatusKind::Info,
                );
            }
            "i" => {
                self.ai_only = !self.ai_only;
                self.reconcile_selection();
                self.load_selected_into_form();
                let n = self.ai_hashes.len();
                self.set_status(
                    format!(
                        "Claude/Codex filter {}: {n} commit{}",
                        on_off(self.ai_only),
                        plural(n)
                    ),
                    StatusKind::Info,
                );
            }
            "a" => self.begin_apply(),
            "x" => self.reset_selected_commit(),
            "tab" if !self.selected_hash.is_empty() => {
                self.focus = Focus::Form;
                self.form_focus = 0;
            }
            "up" | "k" => self.move_selection(-1),
            "down" | "j" => self.move_selection(1),
            "pgup" => self.move_selection(-5),
            "pgdown" => self.move_selection(5),
            "home" => self.move_selection_to(0),
            "end" => self.move_selection_to(usize::MAX),
            _ => {}
        }
    }

    fn update_search(&mut self, key: KeyEvent, name: &str) {
        if name == "esc" || name == "enter" {
            self.overlay = Overlay::None;
        } else {
            edit_line(&mut self.search, key);
        }
        self.reconcile_selection();
    }

    fn update_path(&mut self, key: KeyEvent, name: &str) {
        match name {
            "esc" => {
                if self.repo_loaded {
                    self.overlay = Overlay::None;
                }
            }
            "enter" => match normalize_path(&value(&self.path)) {
                Ok(path) => {
                    self.busy = true;
                    self.set_status("Loading repository...", StatusKind::Info);
                    self.pending = Some(Pending::Load(path));
                }
                Err(e) => self.set_status(e, StatusKind::Error),
            },
            _ => edit_line(&mut self.path, key),
        }
    }

    fn update_bulk_author(&mut self, key: KeyEvent, name: &str) {
        match name {
            "esc" => {
                self.overlay = Overlay::None;
                self.set_status("Bulk author replacement cancelled.", StatusKind::Info);
            }
            "tab" => self.bulk_focus = (self.bulk_focus + 1) % BULK_FIELD_COUNT,
            "shift+tab" => {
                self.bulk_focus = (self.bulk_focus + BULK_FIELD_COUNT - 1) % BULK_FIELD_COUNT
            }
            "ctrl+t" => {
                self.bulk_exact = !self.bulk_exact;
                self.set_status(
                    format!("Author match mode: {}.", author_match_mode(self.bulk_exact)),
                    StatusKind::Info,
                );
            }
            "enter" => {
                self.sync_form_to_draft();
                let staged = stage_author_replacement(
                    &self.commits,
                    &mut self.drafts,
                    &value(&self.bulk_inputs[BULK_SEARCH]),
                    &value(&self.bulk_inputs[BULK_NAME]),
                    &value(&self.bulk_inputs[BULK_EMAIL]),
                    self.bulk_exact,
                );
                match staged {
                    Err(e) => self.set_status(e, StatusKind::Error),
                    Ok(0) => self.set_status(
                        "No author names or emails matched the search.",
                        StatusKind::Info,
                    ),
                    Ok(n) => {
                        self.overlay = Overlay::None;
                        self.reconcile_selection();
                        self.load_selected_into_form();
                        self.set_status(
                            format!("Staged author replacement for {n} commit{}.", plural(n)),
                            StatusKind::Success,
                        );
                    }
                }
            }
            _ => edit_line(&mut self.bulk_inputs[self.bulk_focus], key),
        }
    }

    fn update_date_spread(&mut self, key: KeyEvent, name: &str) {
        match name {
            "esc" => {
                self.overlay = Overlay::None;
                self.set_status("Date rewrite cancelled.", StatusKind::Info);
            }
            "tab" => self.spread_focus = (self.spread_focus + 1) % SPREAD_FIELD_COUNT,
            "shift+tab" => {
                self.spread_focus =
                    (self.spread_focus + SPREAD_FIELD_COUNT - 1) % SPREAD_FIELD_COUNT
            }
            "enter" => {
                self.sync_form_to_draft();
                let v = |i: usize| value(&self.spread_inputs[i]);
                let (start, end, from, to) =
                    (v(SPREAD_START), v(SPREAD_END), v(SPREAD_FROM), v(SPREAD_TO));
                let mut rng = rand::rng();
                let staged = stage_date_spread(
                    &self.commits,
                    &mut self.drafts,
                    &start,
                    &end,
                    &from,
                    &to,
                    &mut |n| rand::Rng::random_range(&mut rng, 0..n),
                );
                match staged {
                    Err(e) => self.set_status(e, StatusKind::Error),
                    Ok(n) => {
                        self.overlay = Overlay::None;
                        self.load_selected_into_form();
                        self.set_status(
                            format!("Staged new timestamps for {n} commit{}.", plural(n)),
                            StatusKind::Success,
                        );
                    }
                }
            }
            _ => edit_line(&mut self.spread_inputs[self.spread_focus], key),
        }
    }

    fn update_confirmation(&mut self, key: KeyEvent, name: &str) {
        if name == "esc" || name == "n" {
            self.overlay = Overlay::None;
            self.set_status("Apply cancelled.", StatusKind::Info);
            return;
        }
        if self.overlay == Overlay::ConfirmApply {
            if name == "y" {
                self.apply_now();
            }
            return;
        }
        if name == "enter" {
            if value(&self.confirm) != "FORCE" {
                return self.set_status(
                    "Type FORCE to confirm rewriting and force pushing.",
                    StatusKind::Error,
                );
            }
            return self.apply_now();
        }
        edit_line(&mut self.confirm, key);
    }

    fn update_form(&mut self, key: KeyEvent, name: &str) {
        let on_date = self.form_focus == FORM_AUTHOR_DATE || self.form_focus == FORM_COMMITTER_DATE;
        match name {
            "enter" if on_date => {
                match parse_git_date(&value(&self.inputs[self.form_focus])) {
                    Ok(t) => {
                        self.picker_time = t;
                        self.picker_target = self.form_focus;
                        self.overlay = Overlay::DatePicker;
                        self.set_status("Date/time picker opened.", StatusKind::Info);
                    }
                    Err(e) => {
                        self.set_status(format!("Cannot open date picker: {e}"), StatusKind::Error)
                    }
                }
                return;
            }
            "ctrl+t" => {
                if on_date {
                    set_value(
                        &mut self.inputs[self.form_focus],
                        &format_git_date(&Local::now()),
                    );
                    self.sync_form_to_draft();
                    self.set_status("Updated the focused date/time to now.", StatusKind::Info);
                }
                return;
            }
            "esc" | "tab" | "shift+tab" => {
                self.normalize_focused_date_input();
                self.sync_form_to_draft();
                let (next, to_list) = if name == "esc" {
                    (self.form_focus, true)
                } else {
                    next_form_focus(self.form_focus, name == "shift+tab")
                };
                if to_list {
                    self.focus = Focus::List;
                } else {
                    self.form_focus = next;
                }
                return;
            }
            _ => {}
        }
        if self.form_focus == FORM_MESSAGE {
            self.message.input(key);
        } else {
            edit_line(&mut self.inputs[self.form_focus], key);
        }
        self.sync_form_to_draft();
    }

    fn update_date_picker(&mut self, name: &str) {
        let t = self.picker_time;
        self.picker_time = match name {
            "esc" => {
                self.overlay = Overlay::None;
                return self.set_status("Date/time change cancelled.", StatusKind::Info);
            }
            "enter" => {
                set_value(&mut self.inputs[self.picker_target], &format_git_date(&t));
                self.overlay = Overlay::None;
                self.sync_form_to_draft();
                return self.set_status("Date/time updated.", StatusKind::Info);
            }
            "left" | "h" => t - Duration::days(1),
            "right" | "l" => t + Duration::days(1),
            "up" | "k" => t + Duration::hours(1),
            "down" | "j" => t - Duration::hours(1),
            "pgup" | "[" => shift_calendar_month(t, -1),
            "pgdown" | "]" => shift_calendar_month(t, 1),
            "shift+left" | "H" => shift_calendar_month(t, -12),
            "shift+right" | "L" => shift_calendar_month(t, 12),
            "-" | "_" => t - Duration::minutes(5),
            "+" | "=" => t + Duration::minutes(5),
            "t" => Local::now().fixed_offset(),
            "u" => Utc.from_utc_datetime(&t.naive_local()).fixed_offset(),
            "z" => Local
                .from_local_datetime(&t.naive_local())
                .earliest()
                .map(|l| l.fixed_offset())
                .unwrap_or(t),
            "0" => t.with_second(0).unwrap_or(t),
            _ => t,
        };
    }

    fn begin_apply(&mut self) {
        if !self.repo_loaded {
            return self.set_status(
                "Load a repository before applying changes.",
                StatusKind::Error,
            );
        }
        self.sync_form_to_draft();
        if let Err(e) = validate_draft_dates(&self.commits, &self.drafts) {
            return self.set_status(e, StatusKind::Error);
        }
        let count = planned_rewrite_count(
            &self.commits,
            &self.original,
            &self.drafts,
            self.remove_co_authors,
        );
        if count == 0 && !self.force_push {
            return self.set_status(
                "No history edits or co-author lines to apply.",
                StatusKind::Info,
            );
        }
        if self.force_push {
            self.overlay = Overlay::ConfirmForce;
            set_value(&mut self.confirm, "");
            return self.set_status("Force push confirmation required.", StatusKind::Info);
        }
        self.overlay = Overlay::ConfirmApply;
        self.set_status(
            format!("Confirm rewriting {count} edited commit{}.", plural(count)),
            StatusKind::Info,
        );
    }

    fn apply_now(&mut self) {
        self.sync_form_to_draft();
        self.overlay = Overlay::None;
        self.busy = true;
        self.set_status("Applying commit metadata changes...", StatusKind::Info);
        let commits = self
            .commits
            .iter()
            .map(|c| self.drafts.get(&c.hash).unwrap_or(c).clone())
            .collect();
        self.pending = Some(Pending::Apply(ApplyRequest {
            commits,
            force_push: self.force_push,
            push_tags: self.push_tags,
            remove_co_authors: self.remove_co_authors,
        }));
    }

    fn open_bulk_author(&mut self) {
        for input in &mut self.bulk_inputs {
            set_value(input, "");
        }
        self.bulk_exact = false;
        self.bulk_focus = BULK_SEARCH;
        self.overlay = Overlay::BulkAuthor;
        self.set_status(
            "Enter an author search and replacement identity.",
            StatusKind::Info,
        );
    }

    /// Hashes of the visible (filtered) list, the selected index within it, and the selected hash.
    fn visible(&self) -> (Vec<String>, usize, String) {
        let list: Vec<&CommitRecord> = self
            .commits
            .iter()
            .filter(|c| !self.ai_only || self.ai_hashes.contains(&c.hash))
            .collect();
        let (filtered, index, hash) = selection_after_filter(
            &list,
            &self.drafts,
            &self.selected_hash,
            &value(&self.search),
        );
        (
            filtered.iter().map(|c| c.hash.clone()).collect(),
            index,
            hash,
        )
    }

    fn reconcile_selection(&mut self) {
        let previous = self.selected_hash.clone();
        let (filtered, index, hash) = self.visible();
        if filtered.is_empty() {
            self.selected_hash.clear();
            self.selected_index = 0;
            self.list_offset = 0;
            self.focus = Focus::List;
            return;
        }
        self.selected_hash = hash;
        self.selected_index = index;
        self.ensure_selection_visible(filtered.len());
        if previous != self.selected_hash {
            self.load_selected_into_form();
        }
    }

    fn ensure_selection_visible(&mut self, total: usize) {
        let slots = if self.height > 0 {
            ((self.height as usize).saturating_sub(8) / 5).max(1)
        } else {
            5
        };
        if self.selected_index < self.list_offset {
            self.list_offset = self.selected_index;
        }
        if self.selected_index >= self.list_offset + slots {
            self.list_offset = self.selected_index + 1 - slots;
        }
        self.list_offset = self.list_offset.min(total.saturating_sub(slots));
    }

    fn move_selection(&mut self, delta: isize) {
        let index = self.visible().1;
        self.move_selection_to(index.saturating_add_signed(delta));
    }

    fn move_selection_to(&mut self, index: usize) {
        let (filtered, _, _) = self.visible();
        if filtered.is_empty() {
            return;
        }
        self.sync_form_to_draft();
        let next = index.min(filtered.len() - 1);
        self.selected_hash = filtered[next].clone();
        self.selected_index = next;
        self.ensure_selection_visible(filtered.len());
        self.load_selected_into_form();
    }

    fn load_selected_into_form(&mut self) {
        let Some(d) = self.drafts.get(&self.selected_hash) else {
            return;
        };
        let values = [
            &d.author_name,
            &d.author_email,
            &d.author_date,
            &d.committer_name,
            &d.committer_email,
            &d.committer_date,
        ];
        for (input, v) in self.inputs.iter_mut().zip(values) {
            set_value(input, v);
        }
        set_value(&mut self.message, &d.message);
    }

    fn sync_form_to_draft(&mut self) {
        let v: Vec<String> = self.inputs.iter().map(value).collect();
        let message = value(&self.message);
        let Some(d) = self.drafts.get_mut(&self.selected_hash) else {
            return;
        };
        [
            d.author_name,
            d.author_email,
            d.author_date,
            d.committer_name,
            d.committer_email,
            d.committer_date,
        ] = v.try_into().expect("six form inputs");
        d.subject = first_line(&message);
        d.message = message;
    }

    fn normalize_focused_date_input(&mut self) {
        if self.form_focus != FORM_AUTHOR_DATE && self.form_focus != FORM_COMMITTER_DATE {
            return;
        }
        if let Ok(n) = normalize_git_date(&value(&self.inputs[self.form_focus])) {
            set_value(&mut self.inputs[self.form_focus], &n);
        }
    }

    fn reset_selected_commit(&mut self) {
        let Some(original) = self.original.get(&self.selected_hash).cloned() else {
            return;
        };
        let short = original.short_hash.clone();
        self.drafts.insert(self.selected_hash.clone(), original);
        self.load_selected_into_form();
        self.set_status(format!("Reset {short}"), StatusKind::Info);
    }

    fn set_status(&mut self, message: impl Into<String>, kind: StatusKind) {
        self.status = message.into();
        self.status_kind = kind;
    }

    // ---------- rendering ----------

    pub fn view(&mut self, f: &mut Frame) {
        let area = f.area();
        self.height = area.height;
        f.render_widget(
            Block::default().style(Style::default().fg(INK).bg(BASE_BG)),
            area,
        );
        let top_h = if self.overlay == Overlay::Search {
            4
        } else {
            1
        };
        let [top, body, bottom] = Layout::vertical([
            Constraint::Length(top_h),
            Constraint::Min(1),
            Constraint::Length(2),
        ])
        .areas(area);
        self.render_top_bar(f, top);
        self.render_bottom_bar(f, bottom);
        let (left_w, sep, _) = tui_column_widths(body.width);
        let [left, _, right] = Layout::horizontal([
            Constraint::Length(left_w),
            Constraint::Length(sep),
            Constraint::Min(1),
        ])
        .areas(body);
        self.render_commit_pane(f, left);
        self.render_detail_pane(f, right);
    }

    fn render_top_bar(&mut self, f: &mut Frame, area: Rect) {
        let bar = Rect { height: 1, ..area }.inner(ratatui::layout::Margin {
            horizontal: 1,
            vertical: 0,
        });
        f.render_widget(
            Block::default().style(Style::default().bg(BAR_BG)),
            Rect { height: 1, ..area },
        );
        let repo_text = if self.repo_loaded {
            let branch =
                if !self.repository.detached_head && !self.repository.current_branch.is_empty() {
                    self.repository.current_branch.as_str()
                } else {
                    "detached HEAD"
                };
            let clean = if self.repository.clean {
                "clean"
            } else {
                "dirty"
            };
            format!("{}  {branch}  {clean}", base_name(&self.repository.path))
        } else {
            "No repository loaded".to_string()
        };
        let flags = if self.busy {
            "working...".to_string()
        } else {
            format!(
                "force:{} tags:{} coauthors:{} edits:{}",
                on_off(self.force_push),
                on_off(self.push_tags),
                on_off(self.remove_co_authors),
                planned_rewrite_count(
                    &self.commits,
                    &self.original,
                    &self.drafts,
                    self.remove_co_authors
                )
            )
        };
        let repo_w = (bar.width as usize)
            .saturating_sub(flags.width() + 11)
            .max(4);
        f.render_widget(
            Paragraph::new(Line::from(vec![
                Span::styled("NEXUS", header_style()),
                Span::raw("  "),
                Span::styled(truncate_text(&repo_text, repo_w), muted()),
            ])),
            bar,
        );
        f.render_widget(
            Paragraph::new(Span::styled(flags, dim())).right_aligned(),
            bar,
        );

        if self.overlay == Overlay::Search {
            let search_area = Rect {
                y: area.y + 1,
                height: 3,
                ..area
            };
            draw_input(f, &mut self.search, search_area, "Search", true);
        }
    }

    fn render_bottom_bar(&self, f: &mut Frame, area: Rect) {
        let color = match self.status_kind {
            StatusKind::Info => CYAN,
            StatusKind::Success => GREEN,
            StatusKind::Error => RED,
        };
        let help = match self.overlay {
            Overlay::Path => "Enter load repository  Esc close",
            Overlay::BulkAuthor => {
                "Tab next field  Shift+Tab previous  Ctrl+T partial/exact  Enter stage  Esc cancel"
            }
            Overlay::DateSpread => "Tab next field  Shift+Tab previous  Enter stage  Esc cancel",
            Overlay::ConfirmApply => "Press y to rewrite history, n/Esc to cancel",
            Overlay::ConfirmForce => {
                "Type FORCE then Enter to rewrite and force push, Esc to cancel"
            }
            Overlay::DatePicker => {
                "←/→ day  ↑/↓ hour  PgUp/PgDn month  +/- 5 min  H/L year  t now  z local  u UTC  Enter save  Esc cancel"
            }
            _ if self.focus == Focus::Form => {
                "Tab next field  Shift+Tab previous  Ctrl+T set focused date to now  Esc list  Ctrl+C quit"
            }
            _ => {
                "Tab form/list  / search  b bulk author  d dates  p path  r reload  x reset  a apply  c co-authors  i claude/codex  f force  t tags  ? help  q quit"
            }
        };
        let w = area.width.saturating_sub(2) as usize;
        f.render_widget(
            Paragraph::new(vec![
                Line::styled(truncate_text(&self.status, w), Style::default().fg(color)),
                Line::styled(truncate_text(help, w), dim()),
            ])
            .style(Style::default().bg(BAR_BG))
            .block(Block::default().padding(ratatui::widgets::Padding::horizontal(1))),
            area,
        );
    }

    fn render_commit_pane(&self, f: &mut Frame, area: Rect) {
        let active = self.focus == Focus::List && self.overlay == Overlay::None;
        let block = panel_block(active);
        let inner = block.inner(area);
        f.render_widget(block, area);
        let width = inner.width as usize;

        let (filtered, selected, _) = self.visible();
        let mut header = vec![Span::styled(
            format!("Commits {}/{}", filtered.len(), self.commits.len()),
            header_style(),
        )];
        if self.ai_only {
            header.push(Span::styled(" [AI]", dim()));
        }
        let query = value(&self.search);
        if !query.is_empty() {
            header.push(Span::styled(
                format!(" /{}", truncate_text(&query, width.saturating_sub(12))),
                dim(),
            ));
        }
        f.render_widget(
            Paragraph::new(Line::from(header)),
            Rect { height: 1, ..inner },
        );

        if filtered.is_empty() {
            let r = Rect {
                y: inner.y + 2,
                height: inner.height.saturating_sub(2),
                ..inner
            };
            f.render_widget(
                Paragraph::new(Span::styled("No commits match this filter.", muted())),
                r,
            );
            return;
        }

        let slots = (inner.height.saturating_sub(2) as usize / 5).max(1);
        let mut offset = self.list_offset.min(filtered.len().saturating_sub(slots));
        if selected < offset {
            offset = selected;
        }
        if selected >= offset + slots {
            offset = selected + 1 - slots;
        }
        let mut y = inner.y + 1;
        for (i, hash) in filtered.iter().enumerate().skip(offset) {
            if y + 4 > inner.bottom() {
                break;
            }
            let item = Rect {
                y,
                height: 4,
                ..inner
            };
            let style = if i == selected {
                Style::default()
                    .bg(SELECTED)
                    .fg(INK)
                    .add_modifier(Modifier::BOLD)
            } else {
                Style::default().fg(INK)
            };
            f.render_widget(
                Paragraph::new(self.commit_item_lines(hash, width.saturating_sub(2)))
                    .style(style)
                    .block(Block::default().padding(ratatui::widgets::Padding::horizontal(1))),
                item,
            );
            y += 5;
        }
    }

    fn commit_item_lines(&self, hash: &str, width: usize) -> Vec<Line<'static>> {
        let Some(d) = self.drafts.get(hash) else {
            return Vec::new();
        };
        let marker = if commit_dirty(hash, &self.original, &self.drafts) {
            "*"
        } else {
            " "
        };
        let author = if d.author_name.is_empty() {
            "(no author)"
        } else {
            &d.author_name
        };
        let mut preview = commit_preview_lines(&d.message, width.max(8), 2);
        preview.resize(2, String::new());
        [
            format!(
                "{marker} {}  {}",
                d.short_hash,
                format_commit_time(&d.author_date)
            ),
            author.to_string(),
            preview[0].clone(),
            preview[1].clone(),
        ]
        .into_iter()
        .map(|s| Line::raw(truncate_text(&s, width)))
        .collect()
    }

    fn render_detail_pane(&mut self, f: &mut Frame, area: Rect) {
        let active = self.focus == Focus::Form && self.overlay == Overlay::None;
        let block = panel_block(active);
        let inner = block.inner(area);
        f.render_widget(block, area);
        let mut s = Stack {
            area: inner,
            y: inner.y,
        };
        match self.overlay {
            Overlay::Path => self.render_path_pane(f, &mut s),
            Overlay::BulkAuthor => self.render_bulk_author_pane(f, &mut s),
            Overlay::DateSpread => self.render_date_spread_pane(f, &mut s),
            Overlay::ConfirmApply | Overlay::ConfirmForce => self.render_confirm_pane(f, &mut s),
            Overlay::Help => render_help_pane(f, &mut s),
            Overlay::DatePicker => self.render_date_picker_pane(f, &mut s),
            Overlay::None | Overlay::Search => self.render_form_pane(f, &mut s),
        }
    }

    fn render_date_picker_pane(&self, f: &mut Frame, s: &mut Stack) {
        let target = if self.picker_target == FORM_COMMITTER_DATE {
            "Committer date"
        } else {
            "Author date"
        };
        let t = self.picker_time;
        let mut lines = vec![
            Line::styled("Date & Time Picker", header_style()),
            Line::styled(target, muted()),
            Line::raw(""),
            Line::styled(
                t.format("%A, %d %B %Y").to_string(),
                Style::default().fg(GREEN).add_modifier(Modifier::BOLD),
            ),
            Line::styled(
                t.format("%H:%M:%S  UTC%:z").to_string(),
                Style::default().fg(CYAN).add_modifier(Modifier::BOLD),
            ),
            Line::raw(""),
        ];
        lines.extend(render_month_calendar(t));
        lines.extend([
            Line::raw(""),
            Line::raw("←/→ or h/l  day        ↑/↓ or k/j  hour"),
            Line::raw("PgUp/PgDn or [/]  month   H/L  year   +/-  five minutes"),
            Line::raw("t  now    z  local timezone    u  UTC    0  clear seconds"),
            Line::raw(""),
            Line::styled(
                "Enter saves the selection. Esc keeps the original timestamp.",
                dim(),
            ),
        ]);
        s.lines(f, lines);
    }

    fn render_bulk_author_pane(&mut self, f: &mut Frame, s: &mut Stack) {
        s.lines(f, vec![Line::styled("Bulk replace author", header_style())]);
        s.wrapped(f, "Search current draft author names and emails, then stage a new author identity on every matching commit.", Style::default());
        s.skip(1);
        for (i, label) in [
            "Search",
            "Replacement author name",
            "Replacement author email",
        ]
        .iter()
        .enumerate()
        {
            s.input(f, &mut self.bulk_inputs[i], label, self.bulk_focus == i);
        }
        s.skip(1);
        s.lines(
            f,
            vec![Line::from(vec![
                Span::raw(format!(
                    "Match mode: {}  ",
                    author_match_mode(self.bulk_exact)
                )),
                Span::styled("(Ctrl+T to toggle)", dim()),
            ])],
        );
        s.wrapped(
            f,
            "Press Enter to stage these changes. Use a to apply the history rewrite.",
            dim(),
        );
    }

    fn render_date_spread_pane(&mut self, f: &mut Frame, s: &mut Stack) {
        s.lines(
            f,
            vec![Line::styled("Rewrite all timestamps", header_style())],
        );
        s.wrapped(f, "Give every commit, oldest first, a random author and committer timestamp between the start and end dates, within the daily time window. Each timestamp is never earlier than the one before it.", Style::default());
        s.skip(1);
        for (i, label) in [
            "Start date",
            "End date",
            "Daily window from (HH:MM)",
            "Daily window to (HH:MM)",
        ]
        .iter()
        .enumerate()
        {
            s.input(f, &mut self.spread_inputs[i], label, self.spread_focus == i);
        }
        s.skip(1);
        s.wrapped(
            f,
            "Press Enter to stage these changes. Use a to apply the history rewrite.",
            dim(),
        );
    }

    fn render_path_pane(&mut self, f: &mut Frame, s: &mut Stack) {
        s.lines(
            f,
            vec![
                Line::styled("Open Repository", header_style()),
                Line::raw(""),
            ],
        );
        s.wrapped(f, "Enter a Git worktree path. Nexus starts with the current directory, but you can switch repositories here at any time.", Style::default());
        s.skip(1);
        s.input(f, &mut self.path, "Path", true);
        s.skip(1);
        s.wrapped(
            f,
            "Press Enter to load. Press Esc to return to the current repository.",
            dim(),
        );
    }

    fn render_confirm_pane(&mut self, f: &mut Frame, s: &mut Stack) {
        let count = planned_rewrite_count(
            &self.commits,
            &self.original,
            &self.drafts,
            self.remove_co_authors,
        );
        s.lines(
            f,
            vec![
                Line::styled("Confirm Rewrite", header_style()),
                Line::raw(""),
            ],
        );
        s.wrapped(f, &format!("This will rewrite Git history for {count} edited commit{}. A backup tag will be created first.", plural(count)), Style::default());
        if self.remove_co_authors {
            let (commits, lines) = co_author_cleanup_impact(&self.commits, &self.drafts);
            s.skip(1);
            s.wrapped(f, &format!("Co-author cleanup will remove {lines} matching line{} from {commits} commit{}.", plural(lines), plural(commits)), Style::default());
        }
        s.skip(1);
        if self.force_push {
            s.lines(
                f,
                vec![Line::styled(
                    "Force push is enabled.",
                    Style::default().fg(RED).add_modifier(Modifier::BOLD),
                )],
            );
            s.wrapped(
                f,
                "Type FORCE to confirm rewriting history and pushing with --force-with-lease.",
                Style::default(),
            );
            s.skip(1);
            if let Some(r) = s.next(3) {
                draw_input(
                    f,
                    &mut self.confirm,
                    Rect {
                        width: r.width.min(24),
                        ..r
                    },
                    "Confirmation",
                    true,
                );
            }
        } else {
            s.lines(
                f,
                vec![Line::raw("Press y to continue, or n/Esc to cancel.")],
            );
        }
    }

    fn render_form_pane(&mut self, f: &mut Frame, s: &mut Stack) {
        if !self.repo_loaded {
            return s.lines(
                f,
                vec![
                    Line::styled("No Repository", header_style()),
                    Line::raw(""),
                    Line::raw("Enter a repository path to begin."),
                    Line::raw(""),
                    Line::styled("Press p to open the path prompt.", dim()),
                ],
            );
        }
        let Some(d) = self.drafts.get(&self.selected_hash).cloned() else {
            return s.lines(
                f,
                vec![
                    Line::styled("No Commits", header_style()),
                    Line::raw(""),
                    Line::raw("This repository has no commits to edit."),
                ],
            );
        };
        let width = s.area.width as usize;
        s.lines(
            f,
            vec![
                Line::styled(
                    truncate_text(
                        &format!("{}  {}", d.short_hash, first_line(&d.message)),
                        width,
                    ),
                    header_style(),
                ),
                Line::raw(""),
            ],
        );

        let active = self.focus == Focus::Form && self.overlay == Overlay::None;
        let rows: Vec<Vec<usize>> = if width >= 44 {
            vec![vec![0, 1], vec![2, 3], vec![4, 5]]
        } else {
            (0..6).map(|i| vec![i]).collect()
        };
        for row in rows {
            let Some(r) = s.next(3) else { break };
            let cells = Layout::horizontal(vec![Constraint::Fill(1); row.len()])
                .spacing(2)
                .split(r);
            for (&field, &cell) in row.iter().zip(cells.iter()) {
                draw_input(
                    f,
                    &mut self.inputs[field],
                    cell,
                    FORM_LABELS[field],
                    active && self.form_focus == field,
                );
            }
        }

        let remaining = s.area.bottom().saturating_sub(s.y);
        if let Some(r) = s.next(remaining.saturating_sub(1).max(3)) {
            draw_input(
                f,
                &mut self.message,
                r,
                "Commit message",
                active && self.form_focus == FORM_MESSAGE,
            );
        }
        let parents = if d.parents.is_empty() {
            "(root commit)".to_string()
        } else {
            d.parents.join(", ")
        };
        let refs = if d.refs.is_empty() {
            "(none)".to_string()
        } else {
            d.refs.join(", ")
        };
        let meta = format!(
            "Hash: {}  Parents: {parents}  Tree: {}  Refs: {refs}",
            d.hash, d.tree
        );
        s.lines(f, vec![Line::styled(truncate_text(&meta, width), dim())]);
    }
}

fn render_help_pane(f: &mut Frame, s: &mut Stack) {
    s.lines(
        f,
        vec![Line::styled("Keyboard", header_style()), Line::raw("")],
    );
    for text in [
        "List: Up/Down, PgUp/PgDn, Home/End select commits.",
        "Tab moves from the list into the commit form.",
        "Form: Tab and Shift+Tab move through every editable value. Esc returns to the list.",
        "/ searches commits. b bulk-replaces author identities. d spreads all timestamps across a date range. p opens a repository path prompt. r reloads history.",
        "x resets the selected commit. a applies edits. c toggles bulk Co-authored-by cleanup. i shows only Claude/Codex commits and merges that brought them in.",
        "f toggles force push. t toggles tag push. Ctrl+T stamps the focused date field with the current local time.",
    ] {
        s.wrapped(f, text, Style::default());
    }
    s.skip(1);
    s.lines(
        f,
        vec![Line::styled("Safety", header_style()), Line::raw("")],
    );
    s.wrapped(
        f,
        "Apply creates a backup tag before rewriting history. Force push uses --force-with-lease.",
        Style::default(),
    );
    s.skip(1);
    s.lines(
        f,
        vec![Line::styled("Press ? or Esc to close help.", dim())],
    );
}

/// Lays widgets out top to bottom, dropping whatever does not fit.
struct Stack {
    area: Rect,
    y: u16,
}

impl Stack {
    fn next(&mut self, height: u16) -> Option<Rect> {
        if height == 0 || self.y + height > self.area.bottom() {
            return None;
        }
        let r = Rect {
            y: self.y,
            height,
            ..self.area
        };
        self.y += height;
        Some(r)
    }
    fn skip(&mut self, n: u16) {
        self.y = (self.y + n).min(self.area.bottom());
    }
    fn lines(&mut self, f: &mut Frame, lines: Vec<Line<'static>>) {
        let h = (lines.len() as u16).min(self.area.bottom() - self.y);
        if let Some(r) = self.next(h) {
            f.render_widget(Paragraph::new(lines), r);
        }
    }
    fn wrapped(&mut self, f: &mut Frame, text: &str, style: Style) {
        let lines = wrap_text(text, self.area.width as usize, 99)
            .into_iter()
            .map(|l| Line::styled(l, style))
            .collect();
        self.lines(f, lines);
    }
    fn input(&mut self, f: &mut Frame, ta: &mut Input, label: &str, focused: bool) {
        if let Some(r) = self.next(3) {
            draw_input(f, ta, r, label, focused);
        }
    }
}

fn panel_block(active: bool) -> Block<'static> {
    Block::bordered()
        .border_type(BorderType::Rounded)
        .border_style(Style::default().fg(if active { CYAN } else { BORDER }))
        .style(Style::default().bg(PANEL))
}

fn draw_input(f: &mut Frame, ta: &mut Input, area: Rect, label: &str, focused: bool) {
    ta.set_block(
        Block::bordered()
            .title(Span::styled(
                label.to_string(),
                Style::default().fg(MUTED).add_modifier(Modifier::BOLD),
            ))
            .border_style(Style::default().fg(if focused { CYAN } else { BORDER }))
            .style(Style::default().bg(if focused { PANEL_LIFT } else { PANEL })),
    );
    ta.set_cursor_style(if focused {
        Style::default().add_modifier(Modifier::REVERSED)
    } else {
        Style::default()
    });
    f.render_widget(&*ta, area);
}

fn map_by_hash(commits: &[CommitRecord]) -> HashMap<String, CommitRecord> {
    commits
        .iter()
        .map(|c| (c.hash.clone(), c.clone()))
        .collect()
}

fn base_name(path: &str) -> String {
    std::path::Path::new(path)
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| path.to_string())
}

pub fn stage_author_replacement(
    commits: &[CommitRecord],
    drafts: &mut HashMap<String, CommitRecord>,
    query: &str,
    name: &str,
    email: &str,
    exact: bool,
) -> Result<usize> {
    let (query, name, email) = (query.trim(), name.trim(), email.trim());
    if query.is_empty() {
        return Err("author search is required".into());
    }
    if name.is_empty() {
        return Err("replacement author name is required".into());
    }
    if email.is_empty() {
        return Err("replacement author email is required".into());
    }
    let folded = query.to_lowercase();
    let mut matched = 0;
    for commit in commits {
        let draft = drafts
            .entry(commit.hash.clone())
            .or_insert_with(|| commit.clone());
        let (n, e) = (
            draft.author_name.to_lowercase(),
            draft.author_email.to_lowercase(),
        );
        let hit = if exact {
            n == folded || e == folded
        } else {
            n.contains(&folded) || e.contains(&folded)
        };
        if hit {
            draft.author_name = name.to_string();
            draft.author_email = email.to_string();
            matched += 1;
        }
    }
    Ok(matched)
}

/// Gives every commit (oldest first) the same author and committer timestamp: a random
/// day in [start, end] at a random time in [from, to), local time. Sorting the random
/// instants keeps them non-decreasing. `rand_n(n)` returns a value in [0, n).
pub fn stage_date_spread(
    commits: &[CommitRecord],
    drafts: &mut HashMap<String, CommitRecord>,
    start_day: &str,
    end_day: &str,
    from_time: &str,
    to_time: &str,
    rand_n: &mut dyn FnMut(i64) -> i64,
) -> Result<usize> {
    let date = |s: &str, what: &str| {
        NaiveDate::parse_from_str(s.trim(), "%Y-%m-%d")
            .map_err(|_| format!("{what} date must be YYYY-MM-DD"))
    };
    let time = |s: &str, what: &str| {
        NaiveTime::parse_from_str(s.trim(), "%H:%M")
            .map_err(|_| format!("window {what} must be HH:MM"))
    };
    let start = date(start_day, "start")?;
    let end = date(end_day, "end")?;
    if end < start {
        return Err("end date must not be before start date".into());
    }
    let from = time(from_time, "start")?;
    let to = time(to_time, "end")?;
    let window = (to - from).num_seconds();
    if window <= 0 {
        return Err("window end must be after window start".into());
    }
    let days = (end - start).num_days() + 1;
    let mut stamps: Vec<DateTime<Local>> = commits
        .iter()
        .map(|_| {
            let naive = (start + Duration::days(rand_n(days))).and_time(from);
            // shortcut: a window start inside a DST gap falls back to treating it as UTC.
            let base = Local
                .from_local_datetime(&naive)
                .earliest()
                .unwrap_or_else(|| Local.from_utc_datetime(&naive));
            base + Duration::seconds(rand_n(window))
        })
        .collect();
    stamps.sort();
    // commits is newest first; walk it oldest first.
    for (commit, stamp) in commits.iter().rev().zip(&stamps) {
        let draft = drafts
            .entry(commit.hash.clone())
            .or_insert_with(|| commit.clone());
        draft.author_date = format_git_date(stamp);
        draft.committer_date = draft.author_date.clone();
    }
    Ok(commits.len())
}

fn author_match_mode(exact: bool) -> &'static str {
    if exact { "Exact" } else { "Partial" }
}

pub fn selection_after_filter<'a>(
    commits: &[&'a CommitRecord],
    drafts: &HashMap<String, CommitRecord>,
    selected: &str,
    query: &str,
) -> (Vec<&'a CommitRecord>, usize, String) {
    let filtered = filter_commits(commits, drafts, query);
    match filtered.iter().position(|c| c.hash == selected) {
        Some(i) => (filtered, i, selected.to_string()),
        None => {
            let hash = filtered.first().map(|c| c.hash.clone()).unwrap_or_default();
            (filtered, 0, hash)
        }
    }
}

fn filter_commits<'a>(
    commits: &[&'a CommitRecord],
    drafts: &HashMap<String, CommitRecord>,
    query: &str,
) -> Vec<&'a CommitRecord> {
    let query = query.trim().to_lowercase();
    commits
        .iter()
        .copied()
        .filter(|c| {
            if query.is_empty() {
                return true;
            }
            let d = drafts.get(&c.hash).unwrap_or(c);
            [
                &c.hash,
                &c.short_hash,
                &d.author_name,
                &d.author_email,
                &d.subject,
                &d.message,
            ]
            .iter()
            .any(|s| s.to_lowercase().contains(&query))
        })
        .collect()
}

static AI_IDENTITY: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)\b(claude|codex|anthropic)\b").unwrap());

/// Commits whose author, committer, or message mentions Claude/Codex, plus merge
/// commits whose merged-in side contains such a commit.
pub fn ai_commit_hashes(commits: &[CommitRecord]) -> HashSet<String> {
    let by_hash: HashMap<&str, &CommitRecord> =
        commits.iter().map(|c| (c.hash.as_str(), c)).collect();
    let mut ai: HashSet<String> = commits
        .iter()
        .filter(|c| {
            [
                &c.author_name,
                &c.author_email,
                &c.committer_name,
                &c.committer_email,
                &c.message,
            ]
            .iter()
            .any(|s| AI_IDENTITY.is_match(s))
        })
        .map(|c| c.hash.clone())
        .collect();
    // Walks ancestors from `start`, skipping `skip`; stops early once `found` returns true.
    let walk = |start: &[String],
                skip: &HashSet<String>,
                found: &dyn Fn(&str) -> bool|
     -> (HashSet<String>, bool) {
        let mut seen = HashSet::new();
        let mut stack: Vec<String> = start.to_vec();
        while let Some(h) = stack.pop() {
            if skip.contains(&h) || !seen.insert(h.clone()) {
                continue;
            }
            if found(&h) {
                return (seen, true);
            }
            if let Some(c) = by_hash.get(h.as_str()) {
                stack.extend(c.parents.iter().cloned());
            }
        }
        (seen, false)
    };
    // shortcut: full ancestor walk per merge, O(commits x merges); memoise if huge repos crawl.
    for c in commits {
        if c.parents.len() < 2 || ai.contains(&c.hash) {
            continue;
        }
        let (base, _) = walk(&c.parents[..1], &HashSet::new(), &|_| false);
        if walk(&c.parents[1..], &base, &|h| ai.contains(h)).1 {
            ai.insert(c.hash.clone());
        }
    }
    ai
}

fn planned_rewrite_count(
    commits: &[CommitRecord],
    original: &HashMap<String, CommitRecord>,
    drafts: &HashMap<String, CommitRecord>,
    remove_co_authors: bool,
) -> usize {
    commits
        .iter()
        .filter(|c| {
            commit_dirty(&c.hash, original, drafts)
                || (remove_co_authors
                    && drafts
                        .get(&c.hash)
                        .is_some_and(|d| remove_co_author_lines(&d.message).1 > 0))
        })
        .count()
}

fn co_author_cleanup_impact(
    commits: &[CommitRecord],
    drafts: &HashMap<String, CommitRecord>,
) -> (usize, usize) {
    let mut counts = (0, 0);
    for c in commits {
        let removed = drafts
            .get(&c.hash)
            .map_or(0, |d| remove_co_author_lines(&d.message).1);
        if removed > 0 {
            counts.0 += 1;
            counts.1 += removed;
        }
    }
    counts
}

fn validate_draft_dates(
    commits: &[CommitRecord],
    drafts: &HashMap<String, CommitRecord>,
) -> Result<()> {
    for c in commits {
        let Some(d) = drafts.get(&c.hash) else {
            continue;
        };
        normalize_git_date(&d.author_date)
            .map_err(|e| format!("commit {} has an invalid author date: {e}", d.short_hash))?;
        normalize_git_date(&d.committer_date)
            .map_err(|e| format!("commit {} has an invalid committer date: {e}", d.short_hash))?;
    }
    Ok(())
}

fn commit_dirty(
    hash: &str,
    original: &HashMap<String, CommitRecord>,
    drafts: &HashMap<String, CommitRecord>,
) -> bool {
    match (original.get(hash), drafts.get(hash)) {
        (Some(o), Some(d)) => editable_fields_changed(o, d),
        _ => false,
    }
}

fn next_form_focus(current: usize, backwards: bool) -> (usize, bool) {
    if backwards {
        if current == 0 {
            (0, true)
        } else {
            (current - 1, false)
        }
    } else if current >= FORM_FIELD_COUNT - 1 {
        (current, true)
    } else {
        (current + 1, false)
    }
}

fn commit_preview_lines(message: &str, width: usize, max_lines: usize) -> Vec<String> {
    let summary = normalize_message(message)
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ");
    let summary = if summary.is_empty() {
        "(no message)".to_string()
    } else {
        summary
    };
    wrap_text(&summary, width.max(4), max_lines.max(1))
}

fn wrap_text(value: &str, width: usize, max_lines: usize) -> Vec<String> {
    let mut lines = Vec::new();
    let mut current = String::new();
    for word in value.split_whitespace() {
        if current.is_empty() {
            current = word.to_string();
        } else if current.width() + 1 + word.width() <= width {
            current.push(' ');
            current.push_str(word);
        } else {
            lines.push(truncate_text(&current, width));
            current = word.to_string();
            if lines.len() == max_lines {
                return lines;
            }
        }
    }
    if lines.len() < max_lines {
        lines.push(truncate_text(&current, width));
    }
    lines
}

fn truncate_text(value: &str, width: usize) -> String {
    if value.width() <= width {
        return value.to_string();
    }
    if width <= 3 {
        return ".".repeat(width);
    }
    let mut out = String::new();
    let mut used = 0;
    for c in value.chars() {
        let w = c.width().unwrap_or(0);
        if used + w + 3 > width {
            break;
        }
        out.push(c);
        used += w;
    }
    out + "..."
}

fn format_commit_time(value: &str) -> String {
    DateTime::parse_from_rfc3339(value)
        .map(|t| t.with_timezone(&Local).format("%Y-%m-%d %H:%M").to_string())
        .unwrap_or_else(|_| value.to_string())
}

fn days_in_month(year: i32, month: u32) -> u32 {
    let first = NaiveDate::from_ymd_opt(year, month, 1).expect("valid month");
    (first + Months::new(1))
        .pred_opt()
        .expect("valid date")
        .day()
}

/// Moves by whole months, clamping the day to the target month's length.
fn shift_calendar_month(t: DateTime<FixedOffset>, months: i32) -> DateTime<FixedOffset> {
    let total = t.year() * 12 + t.month0() as i32 + months;
    let (year, month) = (total.div_euclid(12), total.rem_euclid(12) as u32 + 1);
    let Some(date) = NaiveDate::from_ymd_opt(year, month, t.day().min(days_in_month(year, month)))
    else {
        return t;
    };
    t.offset()
        .from_local_datetime(&date.and_time(t.time()))
        .single()
        .unwrap_or(t)
}

fn render_month_calendar(selected: DateTime<FixedOffset>) -> Vec<Line<'static>> {
    let first = NaiveDate::from_ymd_opt(selected.year(), selected.month(), 1).expect("valid month");
    let leading = first.weekday().num_days_from_monday() as usize;
    let mut lines = vec![
        Line::styled(
            selected.format("%B %Y").to_string(),
            Style::default().fg(MAGENTA).add_modifier(Modifier::BOLD),
        ),
        Line::styled("Mo Tu We Th Fr Sa Su", dim()),
    ];
    let mut week: Vec<Span<'static>> = vec![Span::raw("   "); leading];
    for day in 1..=days_in_month(selected.year(), selected.month()) {
        let cell = format!("{day:2}");
        week.push(if day == selected.day() {
            Span::styled(
                cell,
                Style::default()
                    .fg(PANEL)
                    .bg(CYAN)
                    .add_modifier(Modifier::BOLD),
            )
        } else {
            Span::raw(cell)
        });
        week.push(Span::raw(" "));
        if (leading + day as usize).is_multiple_of(7) {
            lines.push(Line::from(std::mem::take(&mut week)));
        }
    }
    if !week.is_empty() {
        lines.push(Line::from(week));
    }
    lines
}

fn apply_result_summary(r: &ApplyResult) -> String {
    let mut parts = vec![if r.rewritten_commits == 0 {
        "No commits required rewriting.".to_string()
    } else {
        format!(
            "Rewrote {} commit{}.",
            r.rewritten_commits,
            plural(r.rewritten_commits)
        )
    }];
    if !r.backup_reference.is_empty() {
        parts.push(format!("Backup {}.", r.backup_reference));
    }
    if r.removed_co_authors > 0 {
        parts.push(format!(
            "Removed {} co-author line{}.",
            r.removed_co_authors,
            plural(r.removed_co_authors)
        ));
    }
    if r.force_pushed {
        parts.push("Force push completed.".into());
    }
    if !r.warnings.is_empty() {
        parts.push(format!("Warning: {}", r.warnings.join(" ")));
    }
    parts.join(" ")
}

fn on_off(v: bool) -> &'static str {
    if v { "on" } else { "off" }
}

fn plural(n: usize) -> &'static str {
    if n == 1 { "" } else { "s" }
}

/// Returns (left width, separator width, right width).
fn tui_column_widths(total: u16) -> (u16, u16, u16) {
    if total <= 2 {
        return (total.max(1), 0, 1);
    }
    let sep = if total < 50 { 0 } else { 1 };
    let min_right = if total < 70 { (total / 2).max(10) } else { 24 };
    let mut max_left = total.saturating_sub(sep + min_right);
    if max_left < 10 {
        max_left = total.saturating_sub(sep + 1).max(1);
    }
    let left = ((total as u32 * 38 / 100) as u16)
        .clamp(24, 54)
        .clamp(1, max_left);
    (left, sep, total - sep - left)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ratatui::Terminal;
    use ratatui::backend::TestBackend;

    fn rec(hash: &str, name: &str, email: &str) -> CommitRecord {
        CommitRecord {
            hash: hash.into(),
            short_hash: hash.into(),
            author_name: name.into(),
            author_email: email.into(),
            ..Default::default()
        }
    }

    fn key(code: KeyCode) -> KeyEvent {
        KeyEvent::new(code, KeyModifiers::NONE)
    }

    fn type_str(m: &mut Model, s: &str) {
        for c in s.chars() {
            m.handle_key(key(KeyCode::Char(c)));
        }
    }

    fn loaded_model(commits_newest_first: Vec<CommitRecord>) -> Model {
        let mut m = Model::new("/tmp/repo");
        m.pending = None;
        m.busy = false;
        let repo = RepositoryState {
            path: "/tmp/repo".into(),
            clean: true,
            current_branch: "master".into(),
            ..Default::default()
        };
        m.handle_repo_loaded(
            "/tmp/repo",
            Ok((repo, commits_newest_first.into_iter().rev().collect())),
        );
        m
    }

    fn render(m: &mut Model, w: u16, h: u16) -> String {
        let mut term = Terminal::new(TestBackend::new(w, h)).unwrap();
        term.draw(|f| m.view(f)).unwrap();
        let buf = term.backend().buffer().clone();
        buf.content.iter().map(|c| c.symbol()).collect::<String>()
    }

    #[test]
    fn commit_preview_lines_wrap_to_at_most_two_lines() {
        let lines = commit_preview_lines(
            "Add the initial terminal interface with a detailed commit editor and safer apply workflow",
            24,
            2,
        );
        assert!((1..=2).contains(&lines.len()), "{lines:?}");
        assert!(lines.iter().all(|l| l.width() <= 24), "{lines:?}");
    }

    #[test]
    fn dirty_commit_detection() {
        let mut a = rec("one", "A", "a@example.com");
        a.author_date = "2024-01-01T10:00:00Z".into();
        a.committer_date = a.author_date.clone();
        let commits = vec![
            a.clone(),
            CommitRecord {
                hash: "two".into(),
                message: "second".into(),
                ..a
            },
        ];
        let original = map_by_hash(&commits);
        let mut drafts = map_by_hash(&commits);
        drafts.get_mut("two").unwrap().message = "edited second".into();
        assert!(!commit_dirty("one", &original, &drafts));
        assert!(commit_dirty("two", &original, &drafts));
    }

    #[test]
    fn stage_author_replacement_partial_case_insensitive() {
        let mut commits = vec![
            rec("one", "Foo Bar", "foo@bar.baz"),
            rec("two", "Someone Else", "FOO@EXAMPLE.COM"),
            rec("three", "Unaffected", "unaffected@example.com"),
        ];
        commits[0].committer_name = "Release Bot".into();
        let mut drafts = map_by_hash(&commits);
        assert_eq!(
            stage_author_replacement(
                &commits,
                &mut drafts,
                "fOo",
                "New Author",
                "new@example.com",
                false
            ),
            Ok(2)
        );
        assert_eq!(drafts["one"].author_name, "New Author");
        assert_eq!(drafts["one"].committer_name, "Release Bot");
        assert_eq!(drafts["two"].author_email, "new@example.com");
        assert_eq!(drafts["three"].author_name, "Unaffected");
    }

    #[test]
    fn stage_author_replacement_exact_and_unicode() {
        let commits = vec![
            rec("partial", "Foo Bar", "foo@bar.baz"),
            rec("exact", "FOO", "someone@example.com"),
        ];
        let mut drafts = map_by_hash(&commits);
        assert_eq!(
            stage_author_replacement(
                &commits,
                &mut drafts,
                "foo",
                "New Author",
                "new@example.com",
                true
            ),
            Ok(1)
        );
        assert_eq!(drafts["partial"].author_name, "Foo Bar");
        assert_eq!(drafts["exact"].author_name, "New Author");

        let greek = vec![rec("greek", "Σωκράτης", "socrates@example.com")];
        let mut drafts = map_by_hash(&greek);
        assert_eq!(
            stage_author_replacement(
                &greek,
                &mut drafts,
                "ΣΩΚΡΆΤΗΣ",
                "Socrates",
                "new@example.com",
                false
            ),
            Ok(1)
        );
    }

    #[test]
    fn stage_author_replacement_rejects_blank_inputs() {
        let commits = vec![rec("one", "Foo Bar", "foo@bar.baz")];
        for (q, n, e) in [
            ("  ", "New", "n@e.com"),
            ("Foo", "  ", "n@e.com"),
            ("Foo", "New", "  "),
        ] {
            let mut drafts = map_by_hash(&commits);
            assert!(stage_author_replacement(&commits, &mut drafts, q, n, e, false).is_err());
            assert_eq!(drafts["one"].author_name, "Foo Bar");
        }
    }

    #[test]
    fn selection_after_filter_preserves_selection() {
        let mut commits = vec![
            rec("one", "A", ""),
            rec("two", "B", ""),
            rec("three", "C", ""),
        ];
        for (c, m) in commits.iter_mut().zip(["alpha", "beta", "gamma"]) {
            c.message = m.into();
        }
        let drafts = map_by_hash(&commits);
        let refs: Vec<&CommitRecord> = commits.iter().collect();
        let (f, i, h) = selection_after_filter(&refs, &drafts, "two", "");
        assert_eq!((f.len(), i, h.as_str()), (3, 1, "two"));
        let (f, i, h) = selection_after_filter(&refs, &drafts, "two", "gamma");
        assert_eq!((f.len(), i, h.as_str()), (1, 0, "three"));
    }

    #[test]
    fn next_form_focus_traversal() {
        assert_eq!(next_form_focus(0, false), (1, false));
        assert_eq!(next_form_focus(FORM_MESSAGE, false), (FORM_MESSAGE, true));
        assert_eq!(next_form_focus(0, true), (0, true));
    }

    #[test]
    fn initial_repository_path_handles_args() {
        let wd = std::env::temp_dir();
        assert_eq!(
            initial_repository_path(&[], || Ok(wd.clone())).unwrap(),
            wd.to_string_lossy()
        );
        let explicit = wd.join("repo").to_string_lossy().into_owned();
        assert_eq!(
            initial_repository_path(&[explicit.clone()], || panic!("not called")).unwrap(),
            explicit
        );
        assert!(initial_repository_path(&["a".into(), "b".into()], || Ok(wd.clone())).is_err());
    }

    #[test]
    fn repository_load_error_opens_path_entry() {
        let mut m = Model::new("/tmp/repo");
        m.handle_repo_loaded("/tmp/missing", Err("boom".into()));
        assert_eq!(m.overlay, Overlay::Path);
        assert_eq!(value(&m.path), "/tmp/missing");
        assert_eq!(m.status_kind, StatusKind::Error);
    }

    #[test]
    fn bulk_author_dialog_stages_exact_replacement() {
        let mut m = loaded_model(vec![
            rec("partial", "Foo Bar", "foo@bar.baz"),
            rec("exact", "FOO", "someone@example.com"),
        ]);
        m.handle_key(key(KeyCode::Char('b')));
        assert_eq!(m.overlay, Overlay::BulkAuthor);
        assert!(render(&mut m, 120, 40).contains("Match mode: Partial"));
        type_str(&mut m, "foo");
        m.handle_key(key(KeyCode::Tab));
        type_str(&mut m, "New Author");
        m.handle_key(key(KeyCode::Tab));
        type_str(&mut m, "new@example.com");
        m.handle_key(KeyEvent::new(KeyCode::Char('t'), KeyModifiers::CONTROL));
        m.handle_key(key(KeyCode::Enter));
        assert_eq!(m.drafts["partial"].author_name, "Foo Bar");
        assert_eq!(m.drafts["exact"].author_name, "New Author");
        assert_eq!(m.drafts["exact"].author_email, "new@example.com");
        assert_eq!(m.overlay, Overlay::None);
    }

    #[test]
    fn renders_without_panicking_at_many_sizes() {
        let mut c = rec(
            "1111111111111111111111111111111111111111",
            "Ada Lovelace",
            "ada@example.com",
        );
        c.author_date = "2024-01-01T10:00:00Z".into();
        c.committer_date = c.author_date.clone();
        c.message = "Add a compact terminal interface that should not wrap every line".into();
        let mut m = loaded_model(vec![c]);
        for (w, h) in [(80, 24), (40, 10), (10, 5), (200, 60), (1, 1)] {
            for overlay in [
                Overlay::None,
                Overlay::Help,
                Overlay::BulkAuthor,
                Overlay::DateSpread,
                Overlay::DatePicker,
                Overlay::ConfirmForce,
                Overlay::Path,
                Overlay::Search,
            ] {
                m.overlay = overlay;
                m.force_push = true;
                render(&mut m, w, h);
            }
        }
        m.overlay = Overlay::None;
        assert!(render(&mut m, 80, 24).contains("Ada Lovelace"));
    }

    #[test]
    fn date_picker_opens_and_saves_from_date_field() {
        let mut c = rec("one", "A", "a@example.com");
        c.author_date = "2024-01-02T10:00:00+00:00".into();
        c.committer_name = "A".into();
        c.committer_email = "a@example.com".into();
        c.committer_date = c.author_date.clone();
        let mut m = loaded_model(vec![c]);
        m.focus = Focus::Form;
        m.form_focus = FORM_AUTHOR_DATE;
        m.handle_key(key(KeyCode::Enter));
        assert_eq!(m.overlay, Overlay::DatePicker);
        m.picker_time = FixedOffset::east_opt(90 * 60)
            .unwrap()
            .with_ymd_and_hms(2025, 3, 4, 12, 35, 45)
            .unwrap();
        m.handle_key(key(KeyCode::Enter));
        assert_eq!(m.drafts["one"].author_date, "2025-03-04T12:35:45+01:30");
    }

    #[test]
    fn planned_rewrite_count_includes_co_author_cleanup() {
        let mut commits = vec![rec("one", "", ""), rec("two", "", "")];
        commits[0].message = "subject\n\nCo-authored-by: A <a@example.com>".into();
        commits[1].message = "another subject".into();
        let (o, d) = (map_by_hash(&commits), map_by_hash(&commits));
        assert_eq!(planned_rewrite_count(&commits, &o, &d, false), 0);
        assert_eq!(planned_rewrite_count(&commits, &o, &d, true), 1);
    }

    #[test]
    fn calendar_shifts_clamp_to_valid_days() {
        let utc = FixedOffset::east_opt(0).unwrap();
        let jan31 = utc.with_ymd_and_hms(2024, 1, 31, 9, 15, 30).unwrap();
        assert_eq!(
            shift_calendar_month(jan31, 1),
            utc.with_ymd_and_hms(2024, 2, 29, 9, 15, 30).unwrap()
        );
        let leap = utc.with_ymd_and_hms(2024, 2, 29, 9, 15, 30).unwrap();
        assert_eq!(
            shift_calendar_month(leap, 12),
            utc.with_ymd_and_hms(2025, 2, 28, 9, 15, 30).unwrap()
        );
        assert_eq!(
            shift_calendar_month(jan31, -1),
            utc.with_ymd_and_hms(2023, 12, 31, 9, 15, 30).unwrap()
        );
    }

    #[test]
    fn stage_date_spread_orders_timestamps_within_window() {
        // newest first, as the TUI holds them
        let commits = vec![rec("c", "", ""), rec("b", "", ""), rec("a", "", "")];
        let mut drafts = map_by_hash(&commits);
        // Reverse-ordered random draws would break ordering without the sort.
        let draws = [2i64, 3000, 0, 100, 1, 50];
        let mut i = 0;
        let mut rand_n = |n: i64| {
            let v = draws[i % draws.len()] % n;
            i += 1;
            v
        };
        assert_eq!(
            stage_date_spread(
                &commits,
                &mut drafts,
                "2024-03-01",
                "2024-03-03",
                "08:00",
                "12:00",
                &mut rand_n
            ),
            Ok(3)
        );
        let mut prev = None;
        for h in ["a", "b", "c"] {
            let d = &drafts[h];
            assert_eq!(d.author_date, d.committer_date);
            let ts = parse_git_date(&d.author_date).unwrap();
            assert!(prev.is_none_or(|p| ts >= p), "{h} out of order");
            assert!(
                (8..12).contains(&ts.hour()) && (1..=3).contains(&ts.day()),
                "{ts}"
            );
            prev = Some(ts);
        }
        assert!(
            stage_date_spread(
                &commits,
                &mut drafts,
                "2024-03-01",
                "2024-03-03",
                "12:00",
                "08:00",
                &mut rand_n
            )
            .is_err()
        );
    }

    #[test]
    fn ai_commit_hashes_includes_merges_of_ai_branches() {
        let mk = |h: &str, parents: &[&str], author: &str, msg: &str| CommitRecord {
            hash: h.into(),
            parents: parents.iter().map(|s| s.to_string()).collect(),
            author_name: author.into(),
            message: msg.into(),
            ..Default::default()
        };
        let commits = vec![
            mk("root", &[], "", "init"),
            mk("human", &["root"], "", "human work"),
            mk("bot", &["root"], "Codex", "feature"),
            mk(
                "trailer",
                &["human"],
                "",
                "fix\n\nCo-Authored-By: Claude <noreply@anthropic.com>",
            ),
            mk("mergeAI", &["trailer", "bot"], "", "Merge branch 'feature'"),
            mk("side", &["root"], "", "plain"),
            mk(
                "mergePlain",
                &["mergeAI", "side"],
                "",
                "Merge branch 'other'",
            ),
        ];
        let got = ai_commit_hashes(&commits);
        for h in ["bot", "trailer", "mergeAI"] {
            assert!(got.contains(h), "{h} should be flagged");
        }
        for h in ["root", "human", "side", "mergePlain"] {
            assert!(!got.contains(h), "{h} should not be flagged");
        }
    }
}
