#!/usr/bin/env bash
# ramp-report.sh: one Markdown row per ramp step from a k6 --summary-export
# file of a RAMP_STEPS run (k6-users.js).
#
# Usage: loadtest/ramp-report.sh <k6-summary.json> <hold seconds> <steps>
#   <steps> is RAMP_STEPS, for example 50,100,200.
#
# A step passes when its user-journey requests have p95 < 500 ms and
# p99 < 1000 ms, the stack page load has p95 < 1000 ms, and errors stay below
# 1 %. A step without data fails. Requests per second = the requests tagged
# with the step / the hold seconds.
set -euo pipefail

usage="usage: ramp-report.sh <k6-summary.json> <hold seconds> <steps>"
summary="${1:?$usage}"
hold="${2:?$usage}"
steps="${3:?$usage}"
command -v jq >/dev/null || { echo "ERROR: jq is not installed" >&2; exit 1; }

printf '| VUs | req/s | p50 ms | p95 ms | p99 ms | max ms | page load p95 ms | errors | result |\n'
printf '|---|---|---|---|---|---|---|---|---|\n'
jq -r --argjson hold "$hold" --arg steps "$steps" '
  def ms: if type == "number" then floor | tostring else "-" end;
  .metrics as $m
  | $steps | split(",")[] | gsub("\\s"; "") | select(. != "")
  | . as $s
  | ($m["http_req_duration{scenario:user-journey,step:" + $s + "}"] // {}) as $d
  | ($m["journey_stack_page_load{step:" + $s + "}"]["p(95)"]) as $page
  | ($m["errors{step:" + $s + "}"].value) as $err
  | ($m["http_reqs{scenario:user-journey,step:" + $s + "}"].count // 0) as $n
  | ([$d["p(95)"], $d["p(99)"], $page, $err] | all(type == "number")) as $complete
  | ($complete and $n > 0 and $d["p(95)"] < 500 and $d["p(99)"] < 1000 and $page < 1000 and $err < 0.01) as $ok
  | "| \($s) | \($n / $hold | floor) | \($d.med | ms) | \($d["p(95)"] | ms) | \($d["p(99)"] | ms) | \($d.max | ms) | \($page | ms) | \(if ($err | type) == "number" then (($err * 10000 | round) / 100 | tostring) + "%" else "-" end) | \(if $ok then "pass" else "FAIL" end) |"
' "$summary"
