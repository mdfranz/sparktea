#!/usr/bin/env bash
# monty_codegen.sh — asks a model to solve small tasks with Code Mode's
# run_code tool and reports how well its generated Python fares on Monty.
#
# Each case is a natural-language task with a known answer. Most are worded
# to tempt the model toward a Python feature Monty doesn't support (class
# inheritance, generators, match, enum, hashlib, deep recursion, ...; see
# https://github.com/pydantic/monty/tree/main/docs/limitations), so the
# interesting columns are how many run_code calls failed and with what
# error before the model found a Monty-compatible version.
#
# Usage: ./monty_codegen.sh [-m provider:model_id] [-b backend] [-j jobs] [-o outdir] [-c cases.json] [-l] [case ...]
#   -m  model (default: first from -list-models, or $SPARKTEA_TEST_MODEL)
#   -b  code backend: monty (default) or cpython (local CPython, not
#       sandboxed), to compare the same tasks without Monty's limits
#   -j  cases to run in parallel (default 4)
#   -o  directory for per-case stdout/stderr (default: a new temp dir)
#   -c  cases JSON file (default: monty_cases.json)
#   -l  list cases and exit
#   case ...  run only cases whose name contains one of these substrings
#
# Status: PASS = correct answer (whitespace ignored) and run_code was used; WRONG = wrong or
# missing ANSWER line; NOCODE = answered without calling run_code;
# ERROR = sparktea exited non-zero.
# Exit status: 0 if every case passed, 1 otherwise.

set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

BINARY="./sparktea"
CASES_FILE="monty_cases.json"

model="${SPARKTEA_TEST_MODEL:-}"

backend=monty
jobs=4
outdir=""
list_only=0
while getopts "m:b:j:o:c:lh" opt; do
	case "$opt" in
	m) model="$OPTARG" ;;
	b) backend="$OPTARG" ;;
	j) jobs="$OPTARG" ;;
	o) outdir="$OPTARG" ;;
	c) CASES_FILE="$OPTARG" ;;
	l) list_only=1 ;;
	*) sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
	esac
done
shift $((OPTIND - 1))
filters=("$@")

if ! command -v jq >/dev/null 2>&1; then
	echo "jq is required to read $CASES_FILE" >&2
	exit 1
fi
if [ ! -f "$CASES_FILE" ]; then
	echo "cases file not found: $CASES_FILE" >&2
	exit 1
fi
if ! jq -e '
	. as $root |
	def safe_string: type == "string" and length > 0 and (test("[\\t\\r\\n]") | not);
	($root.suffixes.run_code | safe_string) and ($root.cases | type == "array") and
	([$root.cases[] | select((.name | safe_string) and (.name | test("^[A-Za-z0-9_-]+$")) and (.expected | safe_string) and (.prompt | safe_string))] | length == ($root.cases | length)) and
	([$root.cases[].name] | length == (unique | length))
' "$CASES_FILE" >/dev/null; then
	echo "invalid cases file (need unique safe names and non-empty string fields without tabs/newlines): $CASES_FILE" >&2
	exit 1
fi
SUFFIX=$(jq -r '.suffixes.run_code' "$CASES_FILE")
CASES=$(jq -r '.cases[] | [.name,.expected,.prompt] | @tsv' "$CASES_FILE")

selected() {
	local name="$1" f
	[ ${#filters[@]} -eq 0 ] && return 0
	for f in "${filters[@]}"; do
		[[ "$name" == *"$f"* ]] && return 0
	done
	return 1
}

if [ "$list_only" -eq 1 ]; then
	while IFS=$'\t' read -r name expected prompt; do
		selected "$name" && printf '%-15s %s\n' "$name" "$prompt"
	done <<<"$CASES"
	exit 0
fi

if ! go build -o "$BINARY" ./cmd/sparktea; then
	echo "go build failed" >&2
	exit 1
fi
if [ -z "$model" ]; then
	model=$("$BINARY" -list-models 2>/dev/null | head -1 | cut -f1)
fi
if [ -z "$model" ]; then
	echo "no model available: set a provider API key (see -list-models)" >&2
	exit 1
fi
if [ -z "$outdir" ]; then
	outdir=$(mktemp -d -t monty-codegen.XXXXXX)
fi
mkdir -p "$outdir"

# run_case NAME EXPECTED PROMPT — writes $outdir/NAME.{stdout,stderr,result}.
# The result line is: name status calls errors seconds error_types answer.
run_case() {
	local name="$1" expected="$2" prompt="$3"
	local out="$outdir/$name.stdout" err="$outdir/$name.stderr"
	local start=$SECONDS code status calls errors types answer
	"$BINARY" -model "$model" -code -code-backend "$backend" -prompt "$prompt$SUFFIX" >"$out" 2>"$err"
	code=$?
	calls=$(grep -c '^\[tool call\] run_code' "$err")
	errors=$(grep -c '^\[tool error\] run_code' "$err")
	types=$(sed -nE '/^\[tool error\] run_code /{
		s/^\[tool error\] run_code ([A-Za-z]+(Error|Exception)).*/\1/;t p
		s/^\[tool error\] run_code invalid progress payload.*/msgpack/;t p
		s/.*/other/
		:p
		p
	}' "$err" | sort | uniq -c |
		awk '{printf "%s%s×%s", (NR>1?",":""), $2, $1}')
	answer=$(grep -o 'ANSWER:.*' "$out" | tail -1 | sed 's/^ANSWER:[[:space:]]*//; s/[*`]//g')
	if [ "$code" -ne 0 ]; then
		status=ERROR
	elif [ "$calls" -eq 0 ]; then
		status=NOCODE
	elif [[ "${answer//[[:space:]]/}" == *"${expected//[[:space:]]/}"* ]]; then
		status=PASS
	else
		status=WRONG
	fi
	printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$status" "$calls" "$errors" \
		"$((SECONDS - start))" "${types:--}" "${answer:--}" >"$outdir/$name.result"
}

echo "model:   $model"
echo "backend: $backend"
echo "logs:    $outdir"
echo
names=()
while IFS=$'\t' read -r name expected prompt; do
	selected "$name" || continue
	names+=("$name")
	while [ "$(jobs -rp | wc -l)" -ge "$jobs" ]; do
		wait -n
	done
	run_case "$name" "$expected" "$prompt" &
done <<<"$CASES"
wait

if [ ${#names[@]} -eq 0 ]; then
	echo "no cases matched: ${filters[*]}" >&2
	exit 1
fi

printf '%-15s %-6s %5s %6s %5s  %-28s %s\n' CASE STATUS CALLS ERRORS SECS "ERROR TYPES" ANSWER
pass=0 total=0 calls_sum=0 errors_sum=0
for name in "${names[@]}"; do
	IFS=$'\t' read -r n status calls errors secs types answer <"$outdir/$name.result"
	printf '%-15s %-6s %5s %6s %5s  %-28s %.50s\n' "$n" "$status" "$calls" "$errors" "$secs" "$types" "$answer"
	total=$((total + 1))
	calls_sum=$((calls_sum + calls))
	errors_sum=$((errors_sum + errors))
	[ "$status" = PASS ] && pass=$((pass + 1))
done
echo
echo "$pass/$total passed; $calls_sum run_code calls, $errors_sum failed"
[ "$pass" -eq "$total" ]
