#!/usr/bin/env bash
# test_sparktea.sh — exercises the sparktea CLI's non-interactive mode
# (see "Scripting (non-interactive mode)" in README.md).
#
# Flag-parsing and error-path checks always run. Live checks (an actual
# model call, with and without -code) only run if at least one provider API
# key is set — they cost real money and hit a real network. Set
# SPARKTEA_TEST_LIVE=0 to skip them even when a key is present, e.g. to run
# this offline-only in CI. Set SPARKTEA_TEST_MODEL (as provider:model_id,
# see -list-models) to pin which model the live checks use instead of
# whichever one -list-models happens to print first.
#
# Usage: ./test_sparktea.sh
# Exit status: 0 if every non-skipped check passed, 1 otherwise.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

BINARY="./sparktea"
pass_count=0
fail_count=0
skip_count=0

note() { printf '  %s\n' "$*"; }

pass() {
	pass_count=$((pass_count + 1))
	printf 'PASS: %s\n' "$1"
}

fail() {
	fail_count=$((fail_count + 1))
	printf 'FAIL: %s\n' "$1"
	shift
	for line in "$@"; do
		note "$line"
	done
}

skip() {
	skip_count=$((skip_count + 1))
	printf 'SKIP: %s\n' "$1"
}

# run NAME EXPECTED_EXIT -- ARGS...
# Runs the binary with ARGS, capturing stdout/stderr separately into
# $stdout_file/$stderr_file (paths, not content — check-out with
# `stdout_contains`/`stderr_contains` below) and $exit_code. Doesn't itself
# pass/fail; call check_exit after.
run() {
	local args=("$@")
	stdout_file="$tmpdir/stdout"
	stderr_file="$tmpdir/stderr"
	"$BINARY" "${args[@]}" >"$stdout_file" 2>"$stderr_file"
	exit_code=$?
}

check_exit() {
	local name="$1" want="$2"
	if [ "$exit_code" -eq "$want" ]; then
		return 0
	fi
	fail "$name" "exit code $exit_code, want $want" "stdout: $(cat "$stdout_file")" "stderr: $(cat "$stderr_file")"
	return 1
}

contains() {
	local file="$1" pattern="$2"
	grep -qF -- "$pattern" "$file"
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

echo "== build =="
if ! go build -o "$BINARY" ./cmd/sparktea; then
	echo "FAIL: go build ./cmd/sparktea"
	exit 1
fi
pass "go build ./cmd/sparktea"

echo
echo "== flag parsing (no API key required) =="

run -h
# Go's flag package prints usage to stderr, not stdout.
if [ "$exit_code" -eq 0 ] && contains "$stderr_file" "Usage of sparktea"; then
	pass "-h exits 0 with usage"
else
	fail "-h exits 0 with usage" "exit code $exit_code" "stderr: $(cat "$stderr_file")"
fi

run -this-flag-does-not-exist
if [ "$exit_code" -eq 2 ]; then
	pass "unknown flag exits 2"
else
	fail "unknown flag exits 2" "exit code $exit_code, want 2"
fi

echo
echo "== -list-models / no-API-key path =="

run -list-models
no_keys_expected=1
for var in OPENROUTER_API_KEY GEMINI_API_KEY GOOGLE_API_KEY ANTHROPIC_API_KEY MISTRAL_API_KEY; do
	if [ -n "${!var:-}" ]; then
		no_keys_expected=0
	fi
done

if [ "$no_keys_expected" -eq 1 ]; then
	if check_exit "-list-models with no API keys set" 1 && contains "$stderr_file" "no API keys found"; then
		pass "-list-models with no API keys set"
	fi
else
	if check_exit "-list-models with an API key set" 0 && [ -s "$stdout_file" ]; then
		if grep -qE '^[a-z]+:[^	]+	.+$' "$stdout_file"; then
			pass "-list-models prints provider:model_id<TAB>label lines"
		else
			fail "-list-models prints provider:model_id<TAB>label lines" "stdout: $(cat "$stdout_file")"
		fi
	fi
fi

# first_model holds one "provider:model_id" line from -list-models, used by
# every check below that needs a real model to resolve or run against.
# -list-models's own first line is whatever's first in models.go's catalog,
# not necessarily the cheapest — set SPARKTEA_TEST_MODEL to pin a specific
# (ideally cheap) model instead, e.g. for repeated local runs.
first_model="${SPARKTEA_TEST_MODEL:-}"
if [ -z "$first_model" ] && [ -s "$stdout_file" ]; then
	first_model="$(cut -f1 "$stdout_file" | head -n1)"
fi

echo
if [ -z "$first_model" ]; then
	skip "-model error-path checks (need at least one API key)"
	skip "live prompt (need at least one API key)"
	skip "live Code Mode run_code call (need at least one API key)"
else
	echo "== -model resolution errors (using $first_model) =="

	run -model bogus-model-id-xyz -prompt "hi"
	if check_exit "-model with an unknown id fails" 1 && contains "$stderr_file" "no available model matches"; then
		pass "-model with an unknown id fails"
	fi

	run -model "bogus-provider:${first_model#*:}" -prompt "hi"
	if check_exit "-model with an unknown provider fails" 1 && contains "$stderr_file" "no available model matches"; then
		pass "-model with an unknown provider fails"
	fi

	run -model "$first_model" -prompt "hi"
	# Not asserting exit 0 here — this is the first live call and network/key
	# problems belong to the live-prompt check below, not this resolution
	# check. Just confirm it didn't fail the way the two bogus cases above
	# did (an unresolved model).
	if ! contains "$stderr_file" "no available model matches"; then
		pass "-model $first_model resolves"
	else
		fail "-model $first_model resolves" "stderr: $(cat "$stderr_file")"
	fi

	live=1
	if [ "${SPARKTEA_TEST_LIVE:-auto}" = "0" ]; then
		live=0
	fi

	if [ "$live" -eq 0 ]; then
		skip "live prompt (SPARKTEA_TEST_LIVE=0)"
		skip "live Code Mode run_code call (SPARKTEA_TEST_LIVE=0)"
	else
		echo
		echo "== live: single prompt ($first_model) =="
		run -model "$first_model" -prompt "Reply with exactly one word: OK"
		if check_exit "live prompt succeeds" 0 && contains "$stdout_file" "OK" && contains "$stderr_file" "usage:"; then
			pass "live prompt succeeds and prints a usage line"
		fi

		echo
		echo "== live: Code Mode run_code ($first_model) =="
		run -model "$first_model" -code -prompt "Use run_code to compute 6 * 7. Reply with exactly the resulting number, nothing else."
		if check_exit "live Code Mode call succeeds" 0 \
			&& contains "$stderr_file" "[tool call] run_code" \
			&& contains "$stderr_file" "[tool result] run_code" \
			&& contains "$stdout_file" "42"; then
			pass "live Code Mode call runs run_code and returns 42"
		fi
	fi
fi

echo
echo "== summary =="
printf '%d passed, %d failed, %d skipped\n' "$pass_count" "$fail_count" "$skip_count"
[ "$fail_count" -eq 0 ]
