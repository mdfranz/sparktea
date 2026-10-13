# Results

Results from [`monty_codegen.sh`](../monty_codegen.sh) and [`pyrunner/monty_codegen.py`](../pyrunner/monty_codegen.py) (see "Scripting (non-interactive mode)" in the main [README](../README.md#scripting-non-interactive-mode)).
The script runs 23 Code Mode tasks, each written to tempt the model into
Python that Monty doesn't support, and checks the answer. Each model ran
once per configuration.

## Reports

| File | Contents |
|---|---|
| [claude-5-5-monty-analysis.md](claude-5-5-monty-analysis.md) | Claude Haiku, Sonnet and Opus 5.5 (October 10, 2026): scores, cost, and a case-by-case review of the generated Python |
| [monty-vs-cpython.md](monty-vs-cpython.md) | Six models on the Monty and CPython Code Mode backends (October 12, 2026): cost, retries, error types and interpreter speed |
| [go-vs-python-pydantic-ai.md](go-vs-python-pydantic-ai.md) | GLM 5.3 Flash and Claude Haiku 5.5 on sparktea (pydantic-ai-go) and Python pydantic-ai with five backends (October 13, 2026): matched and stock CodeMode, CPython, harness Shell and Bubblewrap |
| [openrouter-monty-codegen.md](openrouter-monty-codegen.md) | Qwen 3.8 Flash, DeepSeek V4 Flash, GLM 5.3 Flash and DeepSeek V4 Pro 0813: the October 12 retest compared with Claude 5.5, and the October 10 runs before and after the `StartRun` fix |

## Infographics

| File | Contents |
|---|---|
| [monty-python-model-comparison.png](monty-python-model-comparison.png) | Claude 5.5: score, cost, time and tool calls |
| [monty-python-code-review.png](monty-python-code-review.png) | Claude 5.5: first-call completions, code size and coding style |
| [openrouter-monty-model-comparison.png](openrouter-monty-model-comparison.png) | OpenRouter models: score, cost, time and tool calls |
| [openrouter-monty-code-review.png](openrouter-monty-code-review.png) | OpenRouter models: first-call completions, code size and coding style |
| [monty-vs-cpython.png](monty-vs-cpython.png) | Monty vs. CPython per model: cost, failed calls, first-call completions and case time |

## Summary

The tables below are the Monty backend. On the CPython backend, the same
tasks cost 51–76% less with the same accuracy; see
[monty-vs-cpython.md](monty-vs-cpython.md).

All seven models completed 23/23 on the current code. Haiku's one scoring
miss was a formatting error in its final answer.

Outliers are in bold.

| Model | Cost | Extra calls | Failed calls | Code across all calls | Avg time per case |
|---|---:|---:|---:|---:|---:|
| Claude Haiku 5.5 | $0.0106 | 7 | 6 | 8,053 | 2.97 s |
| GLM 5.3 Flash | $0.0118 | 10 | 7 | 15,096 | 11.90 s |
| DeepSeek V4 Flash | $0.0151 | 11 | 7 | 14,100 | 9.84 s |
| Qwen 3.8 Flash | $0.0152 | 15 | 7 | **21,959** | **14.60 s** |
| DeepSeek V4 Pro 0813 | $0.1190 | **25** | **18** | 18,947 | **14.47 s** |
| Claude Sonnet 5.5 | $0.1905 | 5 | 3 | 7,207 | 3.63 s |
| Claude Opus 5.5 | **$0.3996** | **2** | **2** | 7,435 | 5.61 s |

### Outliers

- **Opus 5.5 cost:** $0.40, 38 times Haiku and twice Sonnet. It made
  the fewest extra calls (2) and had the fewest failures (2).
- **DeepSeek V4 Pro retries:** 25 extra calls (next highest 15) and 18 failed
  calls (next highest 7). It also cost 8–10 times as much as the other open
  models. It probes what the sandbox supports, for example trying
  `re.VERBOSE`, then `re.X`, then `dir(re)` in its `regex` case.
- **Qwen code volume:** 22k chars across all calls, about three times the
  Claude models. Most of it is self-checking. Its `sha256` case took five
  calls and about 10k chars to write SHA-256, test it against known digests,
  and fix it.
- **Open-model speed:** every open model averaged 10–15 s per case, against
  3–6 s for Claude. The slowest single cases were Qwen and DeepSeek Pro at
  about 84 s each.
- **Haiku's miss:** its only scoring failure was in the final answer.
  `run_code` returned `1,234,567`, and Haiku wrote `1234567`.
- **GLM `sha256` (October 10):** ran for 821 s and ended when the OpenRouter
  stream reset. That was under `RunStream`; the October 12 retest solved it
  in one call.
- **`RunStream` (October 10):** before the `StartRun` fix, DeepSeek V4 Pro
  scored 9/23 and DeepSeek Flash and GLM about 15/23. It was a sparktea bug,
  not the models.

## Method

- **Cost, tokens and time:** from Logfire (project `tomfoolery`, service
  `sparktea`). Cost is `operation.cost` summed over `chat` spans; time is the
  `sparktea turn` span duration summed over the 23 cases.
- **Calls and errors:** from the per-case `.result` files that
  `monty_codegen.sh` writes.
- **Code size:** from the `[tool call] run_code` lines on stderr.
- **Logs:** the raw run logs aren't kept in the repo.
