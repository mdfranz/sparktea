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
# Usage: ./monty_codegen.sh [-m provider:model_id] [-j jobs] [-o outdir] [-l] [case ...]
#   -m  model (default: first from -list-models, or $SPARKTEA_TEST_MODEL)
#   -j  cases to run in parallel (default 4)
#   -o  directory for per-case stdout/stderr (default: a new temp dir)
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
SUFFIX=' Use the run_code tool to compute it. End your reply with one line of the form "ANSWER: <value>", writing numbers without thousands separators unless asked otherwise.'

# name<TAB>expected (substring of the ANSWER line)<TAB>prompt
CASES=$(cat <<'EOF'
basic_arith	338350	What is the sum of the squares of the integers 1 through 100?
statistics	4.5	For the data [2, 4, 4, 4, 5, 5, 7, 9], use the statistics module to find the mean, median and population standard deviation. Give the median as the answer.
primes	15	How many prime numbers are there below 50?
inheritance	7.14	Write a Shape base class with Circle and Square subclasses that each override an area() method. What is the total area of a circle of radius 1 and a square of side 2, rounded to 2 decimal places?
property	212	Write a Temperature class that stores Celsius and exposes a fahrenheit @property. What is the fahrenheit value for 100 Celsius?
generator	832040	Write a generator function that yields Fibonacci numbers (F1=1, F2=1) and use it to find F30.
match	8	Parse the commands ["move 3", "turn left", "move 5", "stop"] with a match statement and return the total distance moved.
custom_exc	2	Define a custom ValidationError exception class. Validate the ages [25, -3, 40, 200, 18] (valid range 0-150), raising ValidationError for invalid ones. How many raise it?
enum	6	Define an Enum Color with RED=1, GREEN=2, BLUE=3. What is the sum of all member values?
dataclass	14	Use a dataclass Item(name, price, qty) with dataclasses.field defaults, build items apple(0.5, 10), pear(0.75, 4), fig(2.0, 3), convert them with dataclasses.asdict, and report the total inventory value (price * qty).
counter	3	Using collections.Counter, how many times does the most common word occur in "the cat and the hat and the bat"?
regex	3	How many valid email addresses are in this text: "alice@example.com, bob.smith@test.org; invalid@; carol@site.io"? Use a verbose (re.VERBOSE) regular expression.
json	17	Parse this JSON and compute the order total (sum of qty * price): {"orders": [{"qty": 2, "price": 3.5}, {"qty": 1, "price": 10}]}
datetime	992	How many days are there between 2024-01-15 and 2026-10-03?
sha256	2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824	What is the SHA-256 hex digest of the ASCII string "hello"?
asyncio	29	Using asyncio, run three coroutines concurrently that each return the square of 2, 3 and 4, and report the sum of the results.
itertools	10	How many 3-element combinations of the numbers 1 through 10 sum to exactly 15?
lru_cache	1346269	Using functools.lru_cache and recursion, how many ways are there to climb 30 stairs taking 1 or 2 steps at a time?
latin1	233	Encode the string "café" using the latin-1 codec. What is the integer value of the last byte?
sorting	cy, bo, dana, ali, eve	Sort these (name, age) records by age descending, then name ascending, and give the names comma-separated: [("dana", 31), ("ali", 25), ("bo", 31), ("cy", 40), ("eve", 25)]
formatting	1,234,567	Format 1234567 with comma thousands separators using an f-string.
deep_recursion	125250	Without using any loops or sum(), write a plain recursive function that adds the integers 1 through 500. What is the result?
naive_fib	2178309	Using naive recursion with no memoization or loops, compute fib(32) where fib(0)=0 and fib(1)=1.
EOF
)

model="${SPARKTEA_TEST_MODEL:-}"
jobs=4
outdir=""
list_only=0
while getopts "m:j:o:lh" opt; do
	case "$opt" in
	m) model="$OPTARG" ;;
	j) jobs="$OPTARG" ;;
	o) outdir="$OPTARG" ;;
	l) list_only=1 ;;
	*) sed -n '2,23p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
	esac
done
shift $((OPTIND - 1))
filters=("$@")

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
	"$BINARY" -model "$model" -code -prompt "$prompt$SUFFIX" >"$out" 2>"$err"
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

echo "model: $model"
echo "logs:  $outdir"
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
