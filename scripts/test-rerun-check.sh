#!/usr/bin/env bash
# Prove `go run mage.go testci` (#736, #728) — the target CI's test step
# runs — against tests that fail on purpose, in a throwaway module. Scenarios
# 1–4 pin VINCENT_TEST_RACE=all, so they prove the same thing on every host:
#
#   1. a test that fails once and then passes is rerun, the run exits 0, the
#      job summary names it, and junit.xml and test.json are written, with an
#      Elapsed for it in test.json
#   2. a test that fails every attempt runs three times (one run, two
#      reruns), fails the target, and is still named in the summary
#   3. a clean run exits 0 and the summary says no test needed a rerun
#   4. a test the race detector fails is never rerun, so a race that would not
#      recur on a rerun still fails the target
#   5. with VINCENT_TEST_RACE=none the race detector is really off: scenario
#      4's racy test passes, the target exits 0, and an earlier run's race
#      reports are not left beside this run's
#   6. with VINCENT_TEST_RACE naming one package of two, the race pass over
#      that package catches its race without a rerun, the other package's
#      test runs exactly once (in the plain pass), the two passes run at the
#      same time, and both passes' reports — test.json and test-race.json —
#      are written beside the summary
#
#   7. with VINCENT_TEST_SHARD (#730), shards 1/3, 2/3 and 3/3 partition the
#      suite: every test of the package VINCENT_TEST_SPLIT names runs exactly
#      once, in one shard, each other package runs whole in exactly one
#      shard, every shard exits 0, and each summary heading names its shard
#   8. inside a shard, a test that fails once is rerun alone — the shard's
#      -run neither widens the rerun nor blocks it — and a race in a split
#      package raced by a package-list scope is still never rerun
#   9. a malformed VINCENT_TEST_SHARD (0/4, 5/4, x) fails the target before
#      any test runs or any report is written
#
# Together 4–6 prove each scope the target defaults to: all (linux), none
# (darwin) and a package list (windows).
#
# The target runs the throwaway module through VINCENT_TEST_DIR and writes its
# reports to VINCENT_TEST_REPORT_DIR, with GITHUB_STEP_SUMMARY pointed at a
# temp file. Each test counts its attempts by appending a byte to a file named
# by FLAKE_ATTEMPTS, since a rerun is a fresh process.
#
# Not wired into CI: it proves the target, and the target itself then runs on
# all three platforms in ci.yml's `ci` job, each with its default scope. VINCENT_GATE_SCENARIO=N runs one
# scenario.
#
# Requirements: bash, go (with cgo and a C compiler, for -race), jq.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"

ONLY="${VINCENT_GATE_SCENARIO:-}"

fail() { echo "CHECK FAIL: $*" >&2; exit 1; }

cleanup() {
  rm -rf "$TMP"
}
trap cleanup EXIT

hostpath() {
  if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf '%s\n' "$1"; fi
}

# Build mage's binary once, so each scenario's exit code is the target's.
echo "== compile mage"
MAGE="$TMP/mage"
if [[ "${OS:-}" == "Windows_NT" ]]; then
  MAGE+=".exe"
fi
(cd "$ROOT" && go run mage.go -compile "$(hostpath "$MAGE")")

MOD="" REPORT="" SUMMARY="" ATTEMPTS=""
setup() { # setup NAME TEST_BODY — a module with one TestFlake plus a passing TestSteady
  local dir="$TMP/$1"
  MOD="$dir/mod" REPORT="$dir/report" SUMMARY="$dir/summary.md" ATTEMPTS="$dir/attempts"
  mkdir -p "$MOD"
  printf 'module flake\n\ngo 1.22\n' >"$MOD/go.mod"
  cat >"$MOD/flake_test.go" <<EOF
package flake

import (
	"os"
	"testing"
)

// attempt records one more run of TestFlake and returns how many there
// have been, this one included.
func attempt(t *testing.T) int {
	t.Helper()
	f, err := os.OpenFile(os.Getenv("FLAKE_ATTEMPTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Size())
}

func TestFlake(t *testing.T) {
	n := attempt(t)
	$2
}

func TestSteady(t *testing.T) {}
EOF
}

run_target() { # run_target [SCOPE] — the target's exit code, its output on stdout
  # VINCENT_TEST_SHARD and VINCENT_TEST_SPLIT reach the target only when a
  # scenario sets them on the call.
  local rc=0
  (cd "$ROOT" && FLAKE_ATTEMPTS="$(hostpath "$ATTEMPTS")" \
    VINCENT_TEST_SHARD="${VINCENT_TEST_SHARD:-}" \
    VINCENT_TEST_SPLIT="${VINCENT_TEST_SPLIT:-}" \
    VINCENT_TEST_RACE="${1:-all}" \
    VINCENT_TEST_DIR="$(hostpath "$MOD")" \
    VINCENT_TEST_REPORT_DIR="$(hostpath "$REPORT")" \
    GITHUB_STEP_SUMMARY="$(hostpath "$SUMMARY")" \
    "$MAGE" testci) || rc=$?
  return "$rc"
}

attempts() { wc -c <"$ATTEMPTS" | tr -d ' \r'; }

meet() { # meet FILE PACKAGE MINE THEIRS — a TestMeet that marks MINE, then waits for THEIRS
  cat >"$1" <<EOF
package $2

import (
	"os"
	"testing"
	"time"
)

func TestMeet(t *testing.T) {
	base := os.Getenv("FLAKE_ATTEMPTS")
	if err := os.WriteFile(base+".$3", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(base + ".$4"); err == nil {
			return
		}
	}
	t.Fatal("the $4 pass never started while the $3 pass ran: the passes ran in series")
}
EOF
}

scenario_1() {
  echo "== scenario 1: a test that fails once passes on a rerun"
  setup s1 'if n == 1 { t.Fatal("first attempt fails on purpose") }'
  run_target || fail "target exited $? on a test that passed on its rerun"
  [[ "$(attempts)" == 2 ]] || fail "TestFlake ran $(attempts) times, want 2"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -q '^### Tests rerun' <<<"$summary" || fail "no Tests rerun section: $summary"
  grep -qF 'flake.TestFlake: 2 runs, 1 failures' <<<"$summary" || fail "summary does not name TestFlake: $summary"
  [[ -s "$REPORT/junit.xml" ]] || fail "no junit.xml"
  [[ -s "$REPORT/test.json" ]] || fail "no test.json"
  local elapsed
  elapsed="$(jq -r 'select(.Test == "TestFlake" and .Action == "pass") | .Elapsed' "$REPORT/test.json" | tr -d '\r')"
  [[ -n "$elapsed" ]] || fail "test.json has no Elapsed for TestFlake's passing run"
  local junit
  junit="$(tr -d '\r' <"$REPORT/junit.xml")"
  grep -qF 'name="TestFlake"' <<<"$junit" || fail "junit.xml has no TestFlake case"
}

scenario_2() {
  echo "== scenario 2: a test that fails every attempt fails the target"
  setup s2 't.Fatalf("attempt %d fails on purpose", n)'
  if run_target; then
    fail "target exited 0 on a test that failed every attempt"
  fi
  [[ "$(attempts)" == 3 ]] || fail "TestFlake ran $(attempts) times, want 3 (one run, two reruns)"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'flake.TestFlake: 3 runs, 3 failures' <<<"$summary" || fail "summary does not name TestFlake: $summary"
}

scenario_3() {
  echo "== scenario 3: a clean run reruns nothing"
  setup s3 '_ = n'
  run_target || fail "target exited $? on a clean run"
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'No test needed a rerun.' <<<"$summary" || fail "summary does not say nothing was rerun: $summary"
}

scenario_4() {
  echo "== scenario 4: a data race is never rerun"
  # Races on its first attempt only, so a rerun would pass.
  setup s4 "$RACY"
  if run_target; then
    fail "target exited 0 on a test the race detector failed"
  fi
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1 (a race is not rerun)"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'No test was rerun, and the run failed' <<<"$summary" || fail "summary does not say the failed run was not rerun: $summary"
}

RACY='if n == 1 { x := 0; done := make(chan struct{}); go func() { x++; close(done) }(); x++; <-done; _ = x }'

scenario_5() {
  echo "== scenario 5: VINCENT_TEST_RACE=none runs without the race detector"
  setup s5 "$RACY"
  # An earlier run's race-pass reports, which this run must not leave
  # beside its own.
  mkdir -p "$REPORT"
  for f in test-race.json junit-race.xml reruns-race.txt; do echo stale >"$REPORT/$f"; done
  run_target none || fail "target exited $? on a racy test with the race detector off"
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1"
  [[ -s "$REPORT/test.json" ]] || fail "no test.json"
  local f
  for f in test-race.json junit-race.xml reruns-race.txt; do
    [[ ! -e "$REPORT/$f" ]] || fail "$f is left over from an earlier run, or a race pass ran with VINCENT_TEST_RACE=none"
  done
}

scenario_6() {
  echo "== scenario 6: a package list races only the listed packages"
  setup s6 "$RACY"
  # A second package beside the racy one, counting its own runs.
  mkdir -p "$MOD/steady"
  cat >"$MOD/steady/steady_test.go" <<'EOF'
package steady

import (
	"os"
	"testing"
)

func TestOnce(t *testing.T) {
	f, err := os.OpenFile(os.Getenv("FLAKE_ATTEMPTS")+".steady", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
}
EOF
  # One test in each pass, each waiting for the other to have started: run
  # in series, the first pass's waits out its budget and fails.
  meet "$MOD/steady/meet_test.go" steady plain race
  meet "$MOD/meet_test.go" flake race plain
  if run_target .; then
    fail "target exited 0 on a race in a listed package"
  fi
  [[ "$(attempts)" == 1 ]] || fail "TestFlake ran $(attempts) times, want 1 (a race is not rerun)"
  local steady
  steady="$(wc -c <"$ATTEMPTS.steady" | tr -d ' \r')"
  [[ "$steady" == 1 ]] || fail "steady.TestOnce ran $steady times, want 1 (plain pass only)"
  [[ -s "$REPORT/test.json" ]] || fail "no test.json from the plain pass"
  [[ -s "$REPORT/test-race.json" ]] || fail "no test-race.json from the race pass"
  [[ -s "$REPORT/junit-race.xml" ]] || fail "no junit-race.xml from the race pass"
  local plain raced
  plain="$(jq -r 'select(.Action == "pass" and .Test != null) | .Package + "." + .Test' "$REPORT/test.json" | tr -d '\r')"
  raced="$(jq -r 'select(.Test != null) | .Package' "$REPORT/test-race.json" | tr -d '\r' | sort -u)"
  grep -qx 'flake/steady.TestOnce' <<<"$plain" || fail "the plain pass did not run steady.TestOnce: $plain"
  grep -qx 'flake/steady.TestMeet' <<<"$plain" || fail "the plain pass never met the race pass: $plain"
  local met
  met="$(jq -r 'select(.Action == "pass" and .Test == "TestMeet") | .Package' "$REPORT/test-race.json" | tr -d '\r')"
  [[ "$met" == flake ]] || fail "the race pass never met the plain pass: $met"
  [[ "$raced" == flake ]] || fail "the race pass ran packages other than flake: $raced"
  local summary
  summary="$(tr -d '\r' <"$SUMMARY")"
  grep -qF 'No test was rerun, and the run failed' <<<"$summary" || fail "summary does not say the failed run was not rerun: $summary"
}

# A module for the shard scenarios: shard/many, the package split by test name,
# with TestS01–TestS12 and any extra test bodies given, and two small
# packages, shard/small1 (two tests) and shard/small2 (one). Every test
# appends a byte to FLAKE_ATTEMPTS.<package>.<test>, so its file's size is its
# run count.
shard_setup() { # shard_setup NAME [EXTRA_GO]
  local dir="$TMP/$1"
  MOD="$dir/mod" ATTEMPTS="$dir/attempts"
  mkdir -p "$MOD/many" "$MOD/small1" "$MOD/small2"
  printf 'module shard\n\ngo 1.22\n' >"$MOD/go.mod"
  local pkg
  for pkg in many small1 small2; do
    cat >"$MOD/$pkg/mark_test.go" <<EOF
package $pkg

import (
	"os"
	"testing"
)

// mark records one more run of t and returns how many there have been,
// this one included.
func mark(t *testing.T) int {
	t.Helper()
	f, err := os.OpenFile(os.Getenv("FLAKE_ATTEMPTS")+".$pkg."+t.Name(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return int(fi.Size())
}
EOF
  done
  {
    printf 'package many\n\nimport "testing"\n'
    local i
    for i in 01 02 03 04 05 06 07 08 09 10 11 12; do
      printf '\nfunc TestS%s(t *testing.T) { mark(t) }\n' "$i"
    done
    printf '%s\n' "${2:-}"
  } >"$MOD/many/many_test.go"
  printf 'package small1\n\nimport "testing"\n\nfunc TestOne(t *testing.T) { mark(t) }\n\nfunc TestTwo(t *testing.T) { mark(t) }\n' >"$MOD/small1/small1_test.go"
  printf 'package small2\n\nimport "testing"\n\nfunc TestOne(t *testing.T) { mark(t) }\n' >"$MOD/small2/small2_test.go"
}

runs() { # runs PACKAGE.TEST — how many times that test of the shard module ran
  if [[ -e "$ATTEMPTS.$1" ]]; then wc -c <"$ATTEMPTS.$1" | tr -d ' \r'; else echo 0; fi
}

SHARD_RC=() # each shard's exit code, by shard number, from the last run_shards
run_shards() { # run_shards SCOPE — shards 1/3, 2/3 and 3/3, each with its own report dir and summary
  local s rc
  SHARD_RC=()
  for s in 1 2 3; do
    REPORT="$TMP/$CASE/report$s" SUMMARY="$TMP/$CASE/summary$s.md"
    rc=0
    VINCENT_TEST_SHARD="$s/3" VINCENT_TEST_SPLIT=shard/many run_target "$1" || rc=$?
    SHARD_RC[s]=$rc
  done
}

shard_tests() { # shard_tests S — PACKAGE.TEST for every test that passed or failed in shard S's reports
  local f
  for f in "$TMP/$CASE/report$1"/test*.json; do
    [[ -e "$f" ]] || continue
    jq -r 'select((.Action == "pass" or .Action == "fail") and .Test != null) | .Package + "." + .Test' "$f"
  done | tr -d '\r' | sort -u
}

scenario_7() {
  echo "== scenario 7: shards partition the suite"
  CASE=s7
  shard_setup s7
  run_shards none
  local s
  for s in 1 2 3; do
    [[ "${SHARD_RC[s]}" == 0 ]] || fail "shard $s/3 exited ${SHARD_RC[s]}"
    local summary
    summary="$(tr -d '\r' <"$TMP/s7/summary$s.md")"
    grep -qE "^### Tests rerun \(.*, shard $s/3\)$" <<<"$summary" || fail "shard $s's summary heading does not name it: $summary"
  done
  local t
  for t in many.TestS01 many.TestS02 many.TestS03 many.TestS04 many.TestS05 many.TestS06 \
    many.TestS07 many.TestS08 many.TestS09 many.TestS10 many.TestS11 many.TestS12 \
    small1.TestOne small1.TestTwo small2.TestOne; do
    [[ "$(runs "$t")" == 1 ]] || fail "$t ran $(runs "$t") times across the shards, want 1"
  done
  # The reports agree: every test is in exactly one shard's.
  local listed
  listed="$(for s in 1 2 3; do shard_tests "$s"; done)"
  local dups
  dups="$(sort <<<"$listed" | uniq -d)"
  [[ -z "$dups" ]] || fail "tests reported by more than one shard: $dups"
  local many
  many="$(grep -c '^shard/many\.' <<<"$listed" || true)"
  [[ "$many" == 12 ]] || fail "the shards reported $many of shard/many's 12 tests: $listed"
  local pkg owners
  for pkg in small1 small2; do
    owners=0
    for s in 1 2 3; do
      local mine
      mine="$(shard_tests "$s")"
      if grep -q "^shard/$pkg\." <<<"$mine"; then
        owners=$((owners + 1))
        [[ "$pkg" != small1 ]] || grep -qx 'shard/small1.TestTwo' <<<"$mine" || fail "shard $s ran only part of small1: $mine"
      fi
    done
    [[ "$owners" == 1 ]] || fail "shard/$pkg ran in $owners shards, want 1"
  done
  # A split package's share writes its own reports.
  ls "$TMP/s7"/report*/test-split-many.json >/dev/null 2>&1 || fail "no shard wrote test-split-many.json"
}

scenario_8() {
  echo "== scenario 8: a rerun inside a shard reruns only the failed test"
  CASE=s8
  shard_setup s8 'func TestFlaky(t *testing.T) { if mark(t) == 1 { t.Fatal("first attempt fails on purpose") } }'
  run_shards none
  local s owner=""
  for s in 1 2 3; do
    [[ "${SHARD_RC[s]}" == 0 ]] || fail "shard $s/3 exited ${SHARD_RC[s]} on a test that passed on its rerun"
    if grep -qx 'shard/many.TestFlaky' <<<"$(shard_tests "$s")"; then owner=$s; fi
  done
  [[ -n "$owner" ]] || fail "no shard ran TestFlaky"
  [[ "$(runs many.TestFlaky)" == 2 ]] || fail "TestFlaky ran $(runs many.TestFlaky) times, want 2"
  local t
  while IFS= read -r t; do
    [[ "$t" == shard/many.TestFlaky ]] && continue
    [[ "$(runs "${t#shard/}")" == 1 ]] || fail "$t, in TestFlaky's shard $owner, ran $(runs "${t#shard/}") times, want 1"
  done <<<"$(shard_tests "$owner")"
  local summary
  summary="$(tr -d '\r' <"$TMP/s8/summary$owner.md")"
  grep -qF 'shard/many.TestFlaky: 2 runs, 1 failures' <<<"$summary" || fail "shard $owner's summary does not name TestFlaky: $summary"

  echo "== scenario 8: a race in a raced split package is never rerun"
  CASE=s8race
  shard_setup s8race "func TestRacy(t *testing.T) { if mark(t) == 1 { x := 0; done := make(chan struct{}); go func() { x++; close(done) }(); x++; <-done; _ = x } }"
  run_shards ./many
  local failed=0
  owner=""
  for s in 1 2 3; do
    if [[ "${SHARD_RC[s]}" != 0 ]]; then failed=$((failed + 1)) owner=$s; fi
  done
  [[ "$failed" == 1 ]] || fail "$failed shards failed on one racy test, want 1"
  [[ "$(runs many.TestRacy)" == 1 ]] || fail "TestRacy ran $(runs many.TestRacy) times, want 1 (a race is not rerun)"
  [[ -s "$TMP/s8race/report$owner/test-split-many-race.json" ]] || fail "shard $owner wrote no test-split-many-race.json"
}

scenario_9() {
  echo "== scenario 9: a malformed VINCENT_TEST_SHARD is refused"
  local v i=0
  for v in 0/4 5/4 x; do
    i=$((i + 1))
    setup "s9-$i" '_ = n'
    if VINCENT_TEST_SHARD="$v" run_target none; then
      fail "target exited 0 with VINCENT_TEST_SHARD=$v"
    fi
    [[ ! -e "$ATTEMPTS" ]] || fail "a test ran with VINCENT_TEST_SHARD=$v"
    local left=""
    [[ ! -d "$REPORT" ]] || left="$(ls -A "$REPORT")"
    [[ -z "$left" ]] || fail "VINCENT_TEST_SHARD=$v wrote reports: $left"
    [[ ! -e "$SUMMARY" ]] || fail "VINCENT_TEST_SHARD=$v wrote a summary"
  done
}

for n in 1 2 3 4 5 6 7 8 9; do
  if [[ -z "$ONLY" || "$ONLY" == "$n" ]]; then
    "scenario_$n"
  fi
done
echo "== PASS"
