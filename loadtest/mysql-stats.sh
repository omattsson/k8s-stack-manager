#!/usr/bin/env bash
# Reset and report the MySQL statement statistics of a load test.
# See "MySQL statement statistics" in loadtest/README.md.
# Works with bash 3.2 (macOS) and Linux.

set -uo pipefail

usage() {
  cat <<'EOF2'
Usage: loadtest/mysql-stats.sh reset
       loadtest/mysql-stats.sh report DIR

reset   Enable the statement consumers and clear
        performance_schema.events_statements_summary_by_digest. Save the
        SHOW GLOBAL STATUS counters for the report.
report  Write mysql-digests.md (top 25 statements by total time and by
        count, statements without an index or with a high ROWS_EXAMINED to
        ROWS_SENT ratio) and mysql-status.tsv (counter change since reset)
        into DIR.

Connection:
  LOADTEST_MYSQL=docker (default)  docker exec into the MySQL container
                                   (MYSQL_CONTAINER, default app-mysql-dev;
                                   GO_ENV changes the default name)
  LOADTEST_MYSQL=external          mysql client with DB_HOST, DB_PORT,
                                   DB_USER and DB_PASSWORD
The script never prints the password.
EOF2
}

script_dir=$(cd "$(dirname "$0")" && pwd)
state_file="$script_dir/results/.mysql-status-before.tsv"

die() { echo "ERROR: $*" >&2; exit 1; }

mode=${LOADTEST_MYSQL:-docker}
db_user=${DB_USER:-root}
db_password=${DB_PASSWORD:-rootpassword}

# run_sql: read SQL from stdin, print tab-separated rows without a header.
run_sql() {
  case "$mode" in
    docker)
      MYSQL_PWD="$db_password" docker exec -i -e MYSQL_PWD "${MYSQL_CONTAINER:-app-mysql-${GO_ENV:-dev}}" \
        mysql -u"$db_user" --batch --skip-column-names
      ;;
    external)
      command -v mysql >/dev/null 2>&1 || die "mysql client not found"
      MYSQL_PWD="$db_password" mysql -h"${DB_HOST:-127.0.0.1}" -P"${DB_PORT:-3306}" -u"$db_user" \
        --batch --skip-column-names
      ;;
    *) die "LOADTEST_MYSQL must be docker or external" ;;
  esac
}

# Counters of SHOW GLOBAL STATUS that the report compares.
status_sql="SELECT VARIABLE_NAME, VARIABLE_VALUE FROM performance_schema.global_status
WHERE VARIABLE_NAME IN ('Threads_connected','Threads_running','Threads_created','Max_used_connections',
'Innodb_row_lock_waits','Innodb_row_lock_time','Innodb_row_lock_time_max','Innodb_row_lock_current_waits',
'Created_tmp_tables','Created_tmp_disk_tables','Select_full_join','Sort_merge_passes','Slow_queries',
'Questions','Aborted_connects') ORDER BY VARIABLE_NAME;"

cmd_reset() {
  mkdir -p "$script_dir/results" || die "cannot create results folder"
  run_sql <<'SQL' || die "cannot reach MySQL. Is the container running (make mysql-start)?"
UPDATE performance_schema.setup_consumers SET ENABLED='YES'
  WHERE NAME IN ('statements_digest','events_statements_current','events_statements_history');
UPDATE performance_schema.setup_instruments SET ENABLED='YES', TIMED='YES'
  WHERE NAME LIKE 'statement/%';
TRUNCATE TABLE performance_schema.events_statements_summary_by_digest;
SQL
  echo "$status_sql" | run_sql > "$state_file" || die "cannot read the status counters"
  echo "MySQL statement statistics reset."
}

cmd_report() {
  local dir=$1 md tsv
  [ -n "$dir" ] || { usage >&2; die "report needs a folder"; }
  mkdir -p "$dir" || die "cannot create $dir"
  md="$dir/mysql-digests.md"
  tsv="$dir/mysql-status.tsv"
  echo "SELECT 1;" | run_sql >/dev/null || die "cannot reach MySQL"

  digest_cols="LEFT(REPLACE(DIGEST_TEXT, CHAR(10), ' '), 160) AS stmt, COUNT_STAR AS cnt,
ROUND(SUM_TIMER_WAIT/1e9) AS total_ms, ROUND(AVG_TIMER_WAIT/1e9, 2) AS avg_ms,
ROUND(MAX_TIMER_WAIT/1e9, 2) AS max_ms, SUM_ROWS_EXAMINED AS examined, SUM_ROWS_SENT AS sent,
SUM_NO_INDEX_USED AS no_index, SUM_CREATED_TMP_DISK_TABLES AS tmp_disk, ROUND(SUM_LOCK_TIME/1e9) AS lock_ms"
  base="FROM performance_schema.events_statements_summary_by_digest WHERE SCHEMA_NAME IS NOT NULL AND DIGEST_TEXT NOT LIKE '%performance_schema%'"

  # table TITLE ORDER_AND_FILTER_SQL
  table() {
    echo ""
    echo "## $1"
    echo ""
    echo "| statement | count | total ms | avg ms | max ms | rows examined | rows sent | no index | tmp disk | lock ms |"
    echo "|---|---|---|---|---|---|---|---|---|---|"
    echo "SELECT $digest_cols $base $2 LIMIT 25;" | run_sql \
      | awk -F'\t' '{ gsub(/`/, "", $1); gsub(/\|/, "\\|", $1); printf "| `%s` |", $1; for (i = 2; i <= NF; i++) printf " %s |", $i; print "" }'
  }

  {
    echo "# MySQL statement statistics"
    echo ""
    echo "Source: performance_schema.events_statements_summary_by_digest since the last reset."
    table "Top 25 by total time" "ORDER BY SUM_TIMER_WAIT DESC"
    table "Top 25 by count" "ORDER BY COUNT_STAR DESC"
    table "Top 25 without an index or with many rows examined (ratio above 100)" \
      "AND (SUM_NO_INDEX_USED > 0 OR SUM_ROWS_EXAMINED > 100 * GREATEST(SUM_ROWS_SENT, 1)) ORDER BY SUM_ROWS_EXAMINED DESC"
  } > "$md" || die "cannot write $md"

  [ -f "$state_file" ] || echo "WARNING: no reset state found. Counters show no change. Run reset before the test." >&2
  echo "$status_sql" | run_sql > "$dir/.mysql-status-after.tsv" || die "cannot read the status counters"
  {
    printf 'counter\tbefore\tafter\tchange\n'
    awk -F'\t' -v before="$state_file" '
      BEGIN { while ((getline line < before) > 0) { split(line, a, "\t"); b[a[1]] = a[2] } }
      {
        name = $1; val = $2
        if (name ~ /^(Threads_connected|Threads_running|Max_used_connections|Innodb_row_lock_time_max|Innodb_row_lock_current_waits)$/) {
          printf "%s\t%s\t%s\t-\n", name, (name in b ? b[name] : "-"), val
        } else if (name in b) {
          printf "%s\t%s\t%s\t%d\n", name, b[name], val, val - b[name]
        } else {
          printf "%s\t-\t%s\t-\n", name, val
        }
      }' "$dir/.mysql-status-after.tsv"
  } > "$tsv"
  rm -f "$dir/.mysql-status-after.tsv"

  {
    echo ""
    echo "## Server counters"
    echo ""
    echo "| counter | before | after | change |"
    echo "|---|---|---|---|"
    tail -n +2 "$tsv" | awk -F'\t' '{ printf "| %s | %s | %s | %s |\n", $1, $2, $3, $4 }'
  } >> "$md"
  echo "MySQL report: $md"
  echo "MySQL counters: $tsv"
}

case "${1:-}" in
  reset) cmd_reset ;;
  report) cmd_report "${2:-}" ;;
  -h | --help | help) usage ;;
  *) usage >&2; exit 2 ;;
esac
