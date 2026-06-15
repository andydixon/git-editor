#!/usr/bin/env bash
# Exit immediately if a command fails.
set -euo pipefail

# Print a small usage message.
usage() {
  cat <<'EOF'
Usage:
  ./rewrite-history.sh "New Name" "[email protected]"

What it does:
  - Rewrites reachable Git history in the current repository.
  - Removes lines starting with "Co-Authored" from commit messages.
  - Sets author and committer name/email to the values you provide.
  - Moves commit times to either:
      00:00-07:30
      19:00-23:59
  - Tries to leave at least one hour between commits.
  - Never makes a rewritten commit earlier than the previous rewritten commit.

Notes:
  - This rewrites history destructively.
  - Run it on a fresh clone or a throwaway copy first.
  - You will need to force-push afterwards.
EOF
}

# Require exactly two arguments.
if [[ $# -ne 2 ]]; then
  usage
  exit 1
fi

# Store the new author name.
NEW_NAME="$1"

# Store the new author email.
NEW_EMAIL="$2"

# Make sure we are inside a Git working tree.
git rev-parse --is-inside-work-tree >/dev/null 2>&1

# Make sure git-filter-repo is installed.
if ! command -v git-filter-repo >/dev/null 2>&1; then
  echo "Error: git-filter-repo is not installed." >&2
  echo "Install it first, then re-run this script." >&2
  exit 1
fi

# Make sure Python 3 is available, because the callbacks use Python.
if ! command -v python3 >/dev/null 2>&1; then
  echo "Error: python3 is not installed." >&2
  exit 1
fi

# Create a safety backup ref pointing at the current HEAD.
BACKUP_REF="refs/backup/pre-rewrite-$(date +%Y%m%d-%H%M%S)"
git update-ref "${BACKUP_REF}" HEAD

# Tell the user where the backup ref was stored.
echo "Created backup ref at ${BACKUP_REF}"

# Create a temporary file for the commit callback.
COMMIT_CALLBACK_FILE="$(mktemp)"

# Create a temporary file for the message callback.
MESSAGE_CALLBACK_FILE="$(mktemp)"

# Ensure the temporary files are removed when the script exits.
cleanup() {
  rm -f "${COMMIT_CALLBACK_FILE}" "${MESSAGE_CALLBACK_FILE}"
}
trap cleanup EXIT

# Write the commit callback.
cat >"${COMMIT_CALLBACK_FILE}" <<'PY'
# Persist the previously assigned rewritten timestamp across callback calls.
if "prev_assigned_ts" not in globals():
    prev_assigned_ts = None

# Read the requested replacement identity from the environment.
new_name = os.environ["REWRITE_NEW_NAME"].encode("utf-8")
new_email = os.environ["REWRITE_NEW_EMAIL"].encode("utf-8")

# Read the preferred minimum gap, in seconds, from the environment.
min_gap_seconds = int(os.environ.get("REWRITE_MIN_GAP_SECONDS", "3600"))

# Replace the author identity.
commit.author_name = new_name
commit.author_email = new_email

# Replace the committer identity as well, so both sides match.
commit.committer_name = new_name
commit.committer_email = new_email

# Use the original author date as the calendar anchor if present.
raw_date = commit.author_date or commit.committer_date

# Parse the fast-export style date string into a timezone-aware datetime.
original_dt = string_to_date(raw_date)

# Work with the original timezone attached to the commit.
tz = original_dt.tzinfo

# Keep the original calendar date as the first preference.
candidate_date = original_dt.date()

# Build a lower bound based on the previous rewritten commit.
earliest_allowed_dt = None
if prev_assigned_ts is not None:
    earliest_allowed_dt = datetime.fromtimestamp(prev_assigned_ts + min_gap_seconds, tz)

# Build the two allowed windows for a given date.
def windows_for(day):
    return [
        (
            datetime(day.year, day.month, day.day, 0, 0, 0, tzinfo=tz),
            datetime(day.year, day.month, day.day, 7, 30, 0, tzinfo=tz),
        ),
        (
            datetime(day.year, day.month, day.day, 19, 0, 0, tzinfo=tz),
            datetime(day.year, day.month, day.day, 23, 59, 0, tzinfo=tz),
        ),
    ]

# Choose the first valid timestamp on or after the lower bound.
chosen_dt = None

# Search forward until we find a valid slot.
while chosen_dt is None:
    for window_start, window_end in windows_for(candidate_date):
        # Start at the opening of the window.
        slot = window_start

        # If we already have a monotonic lower bound, honour it.
        if earliest_allowed_dt is not None and slot < earliest_allowed_dt:
            slot = earliest_allowed_dt

        # If the original time is already inside an allowed window on this date,
        # prefer to keep it, provided it does not violate monotonic ordering.
        if candidate_date == original_dt.date() and window_start <= original_dt <= window_end:
            if earliest_allowed_dt is None:
                slot = original_dt
            elif original_dt >= earliest_allowed_dt:
                slot = original_dt

        # Accept the slot if it still lands inside the current window.
        if slot <= window_end:
            chosen_dt = slot
            break

    # If there was no room on that date, move to the next calendar day.
    if chosen_dt is None:
        candidate_date = candidate_date + timedelta(days=1)

# Convert the chosen datetime back into fast-export's expected byte format.
chosen_raw = date_to_string(chosen_dt)

# Apply the same rewritten datetime to both author and committer dates.
commit.author_date = chosen_raw
commit.committer_date = chosen_raw

# Remember this timestamp so later commits are never earlier.
prev_assigned_ts = int(chosen_dt.timestamp())
PY

# Write the message callback.
cat >"${MESSAGE_CALLBACK_FILE}" <<'PY'
# Remove any line that starts with "Co-Authored", case-insensitively.
message = re.sub(
    br'(?im)^[ \t]*Co-Authored[^\r\n]*(?:\r?\n|$)',
    b'',
    message,
)

# Trim excessive blank lines from the end of the message.
message = re.sub(br'(?:\r?\n){3,}$', b'\n\n', message)

# Return the rewritten message bytes.
return message
PY

# Export the new identity so the Python callbacks can read it.
export REWRITE_NEW_NAME="${NEW_NAME}"
export REWRITE_NEW_EMAIL="${NEW_EMAIL}"

# Ask for a one-hour preferred gap between rewritten commits.
export REWRITE_MIN_GAP_SECONDS="3600"

# Run the history rewrite in the current repository.
git filter-repo \
  --force \
  --message-callback "$(cat "${MESSAGE_CALLBACK_FILE}")" \
  --commit-callback "$(cat "${COMMIT_CALLBACK_FILE}")"

# Tell the user the rewrite finished.
echo
echo "Rewrite complete."
echo "Backup ref: ${BACKUP_REF}"
echo
echo "Check the rewritten history with:"
echo "  git log --pretty=fuller --date=iso"
echo
echo "If it looks right, force-push with:"
echo "  git push --force --all"
echo "  git push --force --tags"
