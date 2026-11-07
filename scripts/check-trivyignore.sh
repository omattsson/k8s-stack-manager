#!/bin/sh
# Checks the Trivy ignore file (default .trivyignore, override with TRIVYIGNORE).
# Every entry line must
#   - have a "#" comment line directly above it that names the reason, and
#   - carry an "exp:YYYY-MM-DD" date that is at most 90 days from today.
# Blank lines are skipped. A missing or empty file passes.
# Prints "file:line: reason" for each violation and exits 1 if there is any.

file="${TRIVYIGNORE:-.trivyignore}"
[ -f "$file" ] || exit 0

# Latest allowed date. GNU date (Linux runners) first, then BSD date (macOS).
limit=$(date -d '+90 days' +%F 2>/dev/null || date -v+90d +%F) || exit 2
today=$(date +%F)

status=0
lineno=0
prev_comment=0

while IFS= read -r line || [ -n "$line" ]; do
  lineno=$((lineno + 1))
  trimmed=$(printf '%s' "$line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')

  if [ -z "$trimmed" ]; then
    prev_comment=0
    continue
  fi

  case "$trimmed" in
    '#'*)
      # Only a comment with text counts as a reason.
      if [ -n "$(printf '%s' "$trimmed" | sed 's/^#*[[:space:]]*//')" ]; then
        prev_comment=1
      else
        prev_comment=0
      fi
      continue
      ;;
  esac

  if [ "$prev_comment" -ne 1 ]; then
    echo "$file:$lineno: entry has no '#' reason comment on the line above" >&2
    status=1
  fi
  prev_comment=0

  exp=$(printf '%s' "$trimmed" | sed -n 's/.*[[:space:]]exp:\([^[:space:]]*\).*/\1/p')
  if [ -z "$exp" ]; then
    echo "$file:$lineno: entry has no exp:YYYY-MM-DD date" >&2
    status=1
    continue
  fi

  # Form check, then a real calendar date (round trip through date).
  case "$exp" in
    [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]) ;;
    *)
      echo "$file:$lineno: exp date '$exp' is not in the form YYYY-MM-DD" >&2
      status=1
      continue
      ;;
  esac
  norm=$(date -d "$exp" +%F 2>/dev/null || date -j -f %F "$exp" +%F 2>/dev/null)
  if [ "$norm" != "$exp" ]; then
    echo "$file:$lineno: exp date '$exp' is not a valid date" >&2
    status=1
    continue
  fi

  # ISO dates compare as numbers once the dashes are gone (YYYYMMDD).
  if [ "$(printf '%s' "$exp" | tr -d -)" -gt "$(printf '%s' "$limit" | tr -d -)" ]; then
    echo "$file:$lineno: exp date $exp is more than 90 days from today ($today); latest allowed is $limit" >&2
    status=1
  fi
done < "$file"

exit "$status"
