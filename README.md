# Nexus

Nexus is a pure ANSI terminal UI for viewing and editing Git commit metadata, then applying rewritten history in a guided workflow.

Repository: https://github.com/andydixon/nexus

## Features

- Launches in the current Git repository by default, or with `nexus /path/to/repo`
- Left-hand commit browser with hash, timestamp, author, and wrapped commit summary
- Right-hand commit editor for:
  - author name, email, and date/time
  - committer name, email, and date/time
  - full commit message
- Search commits by hash, message, author name, or email
- Track edited commits and reset the selected commit
- Apply all staged metadata changes in one action
- Optionally force push rewritten branches and tags to `origin`

## Keyboard

- `Tab`: move from the commit list into the form, then through each editable field
- `Shift+Tab`: move backward through form fields
- `Up`/`Down`, `PgUp`/`PgDn`, `Home`/`End`: navigate commits
- `/`: search commits
- `p`: switch repository path
- `r`: reload history
- `x`: reset the selected commit
- `a`: apply changes
- `f`: toggle force push
- `t`: toggle tag push
- `?`: show help
- `q` or `Ctrl+C`: quit

## Apply Workflow

When you apply changes, Nexus:

1. Checks that the selected repository is clean.
2. Creates a backup tag (`nexus-backup-<timestamp>`).
3. Rewrites history with `git filter-branch` across all refs.
4. Removes temporary rewrite references and runs repository cleanup.
5. Optionally force pushes branches and tags using `--force-with-lease`.

Force push requires an explicit `FORCE` confirmation in the terminal.

## Run

```bash
go run .          # open the current directory
go run . ~/repo   # open a specific repository
```

If the launch directory is not a Git worktree, Nexus opens a path-entry screen instead of exiting.

## Build

```bash
./build.sh
./bin/nexus
```

## Safety

History rewriting changes commit hashes and can disrupt shared branches. Coordinate with collaborators before rewriting shared history or force pushing.

## Licence

This project is proprietary software. It is **not** released under the MIT licence.

