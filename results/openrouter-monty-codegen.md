# OpenRouter models on Monty: `monty_codegen.sh` runs

These runs used `monty_codegen.sh` (23 Code Mode tasks) on October 10, 2026,
during the pydantic-ai-go v0.5.0 upgrade (PR #12). Four OpenRouter models were
tested at three stages of the branch. The run logs were in `/tmp/monty-codegen.*`.
Each log directory was matched to its model using Logfire (project
`tomfoolery`, service `sparktea`), because the per-case `.result` files don't
record the model. Costs are summed from Logfire's `operation.cost` on the
`chat` spans.

## Stages

| Stage | Commit | Change |
|---|---|---|
| A | `a51c3de` / `8779fa5` | pydantic-ai-go v0.5.0, gomonty v0.0.18, expanded `run_code` description; turns use `RunStream` |
| B | `c679dbf` | adds the Monty instruction to the system prompt when code mode is on |
| C | `4f58411` | turns use `StartRun` instead of `RunStream` |

## Summary

Cases passed out of 23:

| Model | A | B | C |
|---|---|---|---|
| qwen/qwen3.8-flash | 22, 23 | 20 (3 errors) | 23 (over three runs) |
| ~deepseek/deepseek-v4-flash-latest | 14, 15 | 14 | 23 |
| z-ai/glm-5.3-flash | 15 | not run | 23 |
| deepseek/deepseek-v4-pro-0813 | 9 | 11 | 23 |

- Stage A had two runs each for Qwen and DeepSeek Flash.
- **Stage B errors:** Qwen's three errors were provider errors, not wrong
  answers. Logfire shows six failed `chat` spans in that window.
- **Stage C, Qwen:** the first run returned an HTTP 429 on every case;
  OpenRouter reported Alibaba as rate-limited upstream. The 23 passes came
  from three later runs, one case at a time (6 + 14 + 3). See issue #11.

The Monty instruction (B) didn't help. Switching to `StartRun` (C) brought
every model to 23/23. Under `RunStream`, a response with text before a tool
call ended the turn with that text as the answer, and the tool result never
went back to the model. In A and B, almost every non-Qwen `WRONG` was that
case, not bad Python. For the cause and how it was found, see the
`RunStream` section of [ISSUES.md](../ISSUES.md).

## Per case

Each cell is `status(run_code calls/failed calls)`. Stage A uses the second
run where there were two; stage C Qwen is the passing run from the
three.

| Case | Qwen A | DS Flash A | GLM A | DS Pro A | Qwen C | DS Flash C | GLM C | DS Pro C |
|---|---|---|---|---|---|---|---|---|
| basic_arith | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| statistics | PASS(1/0) | WRONG(1/1) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| primes | PASS(1/0) | PASS(1/0) | PASS(2/1) | WRONG(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| inheritance | PASS(2/1) | WRONG(1/0) | PASS(2/1) | WRONG(1/1) | PASS(1/0) | PASS(2/1) | PASS(2/1) | PASS(2/1) |
| property | PASS(4/2) | WRONG(4/2) | WRONG(2/2) | WRONG(1/1) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(14/6) |
| generator | PASS(3/2) | WRONG(2/1) | WRONG(1/0) | WRONG(1/1) | PASS(2/1) | PASS(2/1) | PASS(2/1) | PASS(2/1) |
| match | PASS(2/1) | PASS(2/1) | WRONG(2/1) | WRONG(2/1) | PASS(1/0) | PASS(1/0) | PASS(3/2) | PASS(1/0) |
| custom_exc | PASS(4/2) | PASS(2/1) | PASS(3/2) | WRONG(1/1) | PASS(1/0) | PASS(2/1) | PASS(2/1) | PASS(2/1) |
| enum | PASS(3/1) | WRONG(2/1) | WRONG(2/1) | WRONG(1/1) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| dataclass | PASS(5/4) | PASS(6/3) | PASS(1/0) | WRONG(1/1) | PASS(6/3) | PASS(2/1) | PASS(5/3) | PASS(6/2) |
| counter | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| regex | PASS(3/0) | PASS(4/0) | WRONG(2/1) | WRONG(1/1) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(2/1) |
| json | PASS(1/0) | WRONG(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| datetime | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(2/0) | PASS(1/0) | PASS(1/0) |
| sha256 | PASS(12/3) | PASS(2/1) | ERROR(0/0) | WRONG(1/1) | PASS(4/1) | PASS(9/1) | PASS(1/0) | PASS(2/1) |
| asyncio | PASS(1/0) | WRONG(1/0) | PASS(1/0) | WRONG(1/0) | PASS(2/1) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| itertools | PASS(1/0) | WRONG(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| lru_cache | PASS(2/0) | PASS(2/1) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| latin1 | PASS(1/0) | PASS(1/0) | WRONG(3/1) | WRONG(1/1) | PASS(2/1) | PASS(1/0) | PASS(2/1) | PASS(1/0) |
| sorting | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(2/1) | PASS(1/0) | PASS(1/0) |
| formatting | PASS(1/0) | PASS(1/0) | WRONG(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |
| deep_recursion | PASS(1/0) | PASS(1/0) | PASS(1/0) | WRONG(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(4/3) |
| naive_fib | PASS(1/0) | PASS(1/0) | PASS(1/0) | WRONG(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) | PASS(1/0) |

GLM's stage A `sha256` ERROR came after 821 seconds. The OpenRouter
stream reset (`connection reset by peer`) while the model was writing a
pure-Python SHA-256.

## run_code totals and cost

| Model, stage C | run_code calls | failed | cost (USD) |
|---|---|---|---|
| ~deepseek/deepseek-v4-flash-latest | 37 | 6 | 0.017 |
| z-ai/glm-5.3-flash | 33 | 9 | 0.015 |
| deepseek/deepseek-v4-pro-0813 | 49 | 16 | 0.189 |
| qwen/qwen3.8-flash | 47 | 12 | 0.015 (includes the 429 run) |

In stage A, single runs of DeepSeek Pro and GLM cost $0.050 and $0.009. Both
cost more in stage C because their turns now go on past the first text part.

Failed `run_code` calls by Monty error type, summed over the runs in each
stage:

| Error | A (6 runs) | C (4 models) |
|---|---|---|
| NotImplementedError | 29 | 15 |
| LookupError | 13 | 6 |
| TypeError | 10 | 3 |
| ImportError / ModuleNotFoundError | 11 | 5 |
| AttributeError | 8 | 7 |
| SyntaxError | 3 | 2 |
| ValueError | 0 | 3 |
| RecursionError | 0 | 2 |
| TimeoutError / RuntimeError | 2 | 0 |

`NotImplementedError` is mostly class inheritance, `@property`, `match`, and
`asyncio.sleep`. `LookupError` is mostly the `latin-1` codec.

## Other live checks

- `test_sparktea.sh` against Qwen 3.8 Flash on OpenRouter: 9 passed, 1
  skipped.
- **Streaming after the `StartRun` switch:** text still streams in chunks.
  A DeepSeek reply of 206 bytes arrived in 24 stdout writes.
- Logfire shows the Monty instruction in `gen_ai.system_instructions` when
  `-code` is set.

## Limits

These are single runs per model and stage, so the per-case results are
noisy. In stage A, the two Qwen runs scored 22 and 23 and the two DeepSeek
Flash runs 14 and 15. Qwen's stage C result is combined from three partial
runs.
