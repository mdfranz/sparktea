# Results

Results from `monty_codegen.sh` (see "Scripting (non-interactive mode)" in the main [README](../README.md#scripting-non-interactive-mode)).
The script runs 23 Code Mode tasks, each written to tempt the model into
Python that Monty doesn't support, and checks the answer. Each model ran
once per configuration.

## Reports

| File | Contents |
|---|---|
| [claude-5-5-monty-analysis.md](claude-5-5-monty-analysis.md) | Claude Haiku, Sonnet and Opus 5.5 (October 10, 2026): scores, cost, and a case-by-case review of the generated Python |
| [openrouter-monty-codegen.md](openrouter-monty-codegen.md) | Qwen 3.8 Flash, DeepSeek V4 Flash, GLM 5.3 Flash and DeepSeek V4 Pro 0813: the October 12 retest compared with Claude 5.5, and the October 10 runs before and after the `StartRun` fix |

## Infographics

| File | Contents |
|---|---|
| [monty-python-model-comparison.png](monty-python-model-comparison.png) | Claude 5.5: score, cost, time and tool calls |
| [monty-python-code-review.png](monty-python-code-review.png) | Claude 5.5: first-call completions, code size and coding style |
| [openrouter-monty-model-comparison.png](openrouter-monty-model-comparison.png) | OpenRouter models: score, cost, time and tool calls |
| [openrouter-monty-code-review.png](openrouter-monty-code-review.png) | OpenRouter models: first-call completions, code size and coding style |

## Summary

All seven models completed 23/23 on the current code. Haiku's one scoring
miss was a formatting error in its final answer.

| Model | Cost | Extra calls | Avg time per case |
|---|---:|---:|---:|
| Claude Haiku 5.5 | $0.0106 | 7 | 2.97 s |
| GLM 5.3 Flash | $0.0118 | 10 | 11.90 s |
| DeepSeek V4 Flash | $0.0151 | 11 | 9.84 s |
| Qwen 3.8 Flash | $0.0152 | 15 | 14.60 s |
| DeepSeek V4 Pro 0813 | $0.1190 | 25 | 14.47 s |
| Claude Sonnet 5.5 | $0.1905 | 5 | 3.63 s |
| Claude Opus 5.5 | $0.3996 | 2 | 5.61 s |

## Method

- **Cost, tokens and time:** from Logfire (project `tomfoolery`, service
  `sparktea`). Cost is `operation.cost` summed over `chat` spans; time is the
  `sparktea turn` span duration summed over the 23 cases.
- **Calls and errors:** from the per-case `.result` files that
  `monty_codegen.sh` writes.
- **Code size:** from the `[tool call] run_code` lines on stderr.
- **Logs:** the raw run logs aren't kept in the repo.
