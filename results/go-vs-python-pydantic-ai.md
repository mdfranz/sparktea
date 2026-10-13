# Go vs. Python pydantic-ai: Code Mode backends

On October 13, 2026 (UTC), GLM 5.3 Flash ran the 23 `monty_cases.json`
tasks on seven configurations, one run at a time:

- two in sparktea, which uses pydantic-ai-go ([`monty_codegen.sh`](../monty_codegen.sh));
- five in Python pydantic-ai 2.55.0 with pydantic-ai-harness 0.55.0
  ([`pyrunner/monty_codegen.py`](../pyrunner/monty_codegen.py)).

For what each Python backend sets, see the "Python pydantic-ai comparison"
section of [CODE-MODE.md](../CODE-MODE.md#python-pydantic-ai-comparison).

## Summary

| Implementation | Backend | Passed | Calls | Failed | Requests | Input tokens | Output tokens | Cost (list price) | Model time |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Go | `monty` | 23/23 | 30 | 6 | 52 | 58,858 | 16,011 | $0.0120 | 220 s |
| Python | `monty-sparktea` | 23/23 | 31 | 6 | 54 | 66,016 | 16,213 | $0.0118 | 216 s |
| Python | `monty-stock` | **22/23** | **53** | **24** | **75** | **98,715** | 18,017 | **$0.0145** | **334 s** |
| Go | `cpython` | 23/23 | 24 | 1 | 47 | 21,462 | 5,797 | $0.0047 | 107 s |
| Python | `cpython-sparktea` | 23/23 | 23 | 0 | 46 | 19,775 | 5,050 | $0.0041 | 108 s |
| Python | `shell` | 23/23 | 24 | 1 | 47 | 20,766 | 6,889 | $0.0054 | 89 s |
| Python | `shell-bwrap` | 23/23 | 23 | 0 | 46 | 19,201 | 5,860 | $0.0042 | 77 s |

- **With the same prompt, Go and Python behave the same.** Go `monty` and
  Python `monty-sparktea` differ by one call and 2% in cost. Go `cpython`
  and Python `cpython-sparktea` are equally close.
- **The prompt matters more than the implementation.** On the same harness,
  stock CodeMode had four times the failures of `monty-sparktea` (24 against
  6), and cost 23% more.
- **Execution environment doesn't show in the results.** The three
  CPython-based backends (`cpython-sparktea`, `shell`, `shell-bwrap`) used
  about the same calls, tokens and cost.

## Findings

### Matched Monty: Go and Python agree

Both runs sent the same tool description and Monty instruction (the shared
`codemode/*.txt` files). Calls, failures, tokens, cost and model time are
within a few percent.

They failed on different cases:

| Case | Go `monty` | Python `monty-sparktea` |
|---|---|---|
| `deep_recursion` | `RecursionError` | passed first call |
| `custom_exc` | `NotImplementedError`, `TypeError` | `NotImplementedError` |
| `enum` | `NotImplementedError` | passed first call |
| `generator`, `match` | `NotImplementedError` | `NotImplementedError` |
| `inheritance` | passed first call | `NotImplementedError` |
| `latin1` | passed first call | `LookupError` |
| `dataclass` | passed first call | `ImportError` (`dataclasses.field`) |

Only `deep_recursion` is explained by a configuration difference. Both
models wrote a plain recursive sum of 1–500. Go's Monty caps recursion at
100, but the harness has no recursion limit, so the same code ran (#19). The
other differences are the model making different first choices in different
runs; one run per configuration can't separate them further.

### Stock CodeMode: the description is doing the work

The harness's own `run_code` description is 1,699 characters and adds no
system instruction. sparktea's is 2,236 characters plus a 319-character
instruction. Stock still used 50% more input tokens, because each retry
resends the growing conversation: 75 requests against 54.

Its 24 failures were mostly gaps that sparktea's description lists:

- `hashlib`, and `int.to_bytes` in the hand-written SHA-256 that followed;
- `functools.lru_cache`, `re.VERBOSE`, the `latin-1` codec;
- `dataclasses.field` and `asdict`, and `@property`;
- `dir()`, which the model called three times to see what a module offers.

The one miss, `dataclass`, ran out of retries. The stock limit is 3:

1. `SyntaxError`: an assignment expression in a comprehension's iterable;
2. `ImportError`: `dataclasses.field`;
3. `ImportError`: `dataclasses.asdict`;
4. `dir(dataclasses)` to see what exists. `dir` is also missing, so the
   fourth failure ended the run.

The `.result` file counts three failures for this case: the runner doesn't
see the call that exhausts the retries.

### CPython: same in both implementations

Go `cpython` had one failure, Python `cpython-sparktea` none. Go's was a
model bug, not a backend difference: `for i in dicts_src := items` in a
list comprehension, which CPython rejects too. Stock CodeMode's `dataclass`
case opened with the same mistake.

### Shell and Bubblewrap

The model ran Python through `python3 -c` for 19 calls and a heredoc
(`python3 -`) for 4. Tokens and cost match the `run_code` CPython backends,
so going through a shell tool costs nothing extra.

The one `shell` failure was `cat`, which isn't on the allowed list; the
model switched to `python3` on the next call. `shell-bwrap` had no failures
and no isolation-related errors.

### Interpreter time is negligible

| Run | Tool span p50 | Notes |
|---|---:|---|
| Go `monty` | 0.5 ms | gomonty's `monty.run` |
| Python `monty-sparktea` / `monty-stock` | 8 ms / 6 ms | the whole harness tool call |
| Go `cpython` | 49 ms | process start; script itself averaged 14 ms |
| Python `cpython-sparktea` | 95 ms | process start; script itself averaged 10 ms |
| Python `shell` | 42 ms | |
| Python `shell-bwrap` | 140 ms | sandbox setup adds about 100 ms |

Model requests averaged 1.7–4.5 s each, depending on the run, so none of these registers in case
time. Wall-clock time per run (23–162 s) depends mostly on the slowest case
in each batch of four; model time in the table above is the comparable
figure.

### Recorded costs differ by implementation

`operation.cost` isn't comparable across the two implementations:

- **pydantic-ai-go** uses the cost OpenRouter reports for the request
  (sparktea asks for it with `usage: {include: true}`). For this run, most
  requests were billed at about $0.07 per million input tokens and $0.05 per
  million output tokens.
- **Python pydantic-ai** calculates cost from genai-prices list prices:
  $0.15 per million input, $0.03 per million cached input and $0.50 per
  million output for GLM 5.3 Flash.

As recorded, Go `monty` cost $0.0043 and Python `monty-sparktea` $0.0118,
though their token counts are close. The table above reprices every run at
the list prices; Python's repriced figures equal its recorded ones.

## Method

- **Runs:** `monty_codegen.sh -m openrouter:z-ai/glm-5.3-flash -b <monty|cpython>`
  and `pyrunner/monty_codegen.py -m openrouter:z-ai/glm-5.3-flash -b <backend>`,
  each at the default four cases in parallel, one run at a time, from
  00:56 to 01:03 UTC on main at `9d40d70`.
- **Passed, calls and failures:** from the per-case `.result` files.
- **Requests, tokens, model time and cost:** from Logfire (project
  `tomfoolery`; services `sparktea` and `sparktea-runner`), summed over
  `chat` spans. Model time is the sum of `chat` span durations. Cost is
  (input − cached) × $0.15/M + cached × $0.03/M + output × $0.50/M.
- **Tool span times:** `monty.run`, `python.run` and `execute_tool
  run_command` span durations.
- **Monty versions:** sparktea embeds Monty 1.0.1 (gomonty); the harness
  uses pydantic-monty 1.1.0.
- **Limits:** one model, one run per configuration. Differences of one or two
  failed calls are within run-to-run variation.
