# Python sidekick: a pydantic-ai runner for the monty_codegen tasks

## Status

- Branch: `pyrunner`.
- Steps 1–4 are implemented: the shared 23-case file is consumed by both runners, Go and Python share the model-facing prompt assets, the five-backend Python runner is in place, and the README / Code Mode docs describe it. The changes are uncommitted.
- Verification so far: Go Code Mode tests and `go vet ./...` passed; both runners list the same 23 cases. The four-case GLM sample passed 4/4 on `monty-sparktea` and `cpython-sparktea`. Logfire confirmed the Monty instructions and actual `monty` / `cpython` tags. The full telemetry reconciliation, Python offline test matrix and paired multi-model comparison remain.

## Context

`monty_codegen.sh` benchmarks sparktea's Code Mode, which is Go (pydantic-ai-go plus gomonty or local CPython). The primary goal is to run the same 23 tasks through Python pydantic-ai 2.55.0 with the OpenRouter and current Anthropic models used in `results/`. The Python runs must preserve the measurements used in those reports: score, correct computation, first-call completion, execution calls and failures, submitted code, error types, requests, tokens, cost and case time. The cases were hard-coded in the shell script, so they move into a shared file.

There are two comparisons:

1. **Matched:** keep the model-facing prompt, limits, retry budget, scoring and telemetry as close to sparktea as the Python APIs allow. This covers Monty and a local CPython `run_code` tool.
2. **Upstream options:** run stock harness CodeMode and harness Shell, with and without Bubblewrap. These are useful reference configurations, but Shell is not a direct equivalent of sparktea's CPython backend: it exposes `run_command`, requires the model to launch Python, and has different result and error semantics.

Python side, as checked against the `v2.55.0` tag of `pydantic/pydantic-ai`:

- **Packages:** `pydantic-ai` 2.55.0 plus `pydantic-ai-harness` 0.55.0. The harness is a workspace member of the pydantic-ai repo (`src/pydantic_ai_harness/`), published as a separate PyPI package and released in lockstep: harness 0.55.0 pins `pydantic-ai-slim==2.55.0`.
- **`CodeMode`** (`pydantic-ai-harness[codemode]`, `pydantic-monty` 1.1.0, which is Monty v1.1.0; sparktea has v1.0.1):
  - the `run_code` tool takes `code` and `restart`, and REPL state persists between calls;
  - stock limits are 30 s, 256 MiB and 1,000 suspensions, with `max_retries=3`;
  - there's no recursion-limit setting;
  - its short description doesn't list Monty's gaps, and it adds no system instruction.
- **Workspaces:** core pydantic-ai has `pydantic_ai.capabilities.LocalWorkspace`. pydantic-ai-go's `ai.NewLocalWorkspace` is a port of it, with the same "isolates nothing" model and the same pass-through of `PATH`, `HOME`, `LANG`, `LC_ALL` and `LC_CTYPE`. pydantic-ai-go has no Shell or Bubblewrap equivalent.
- **Harness `Shell`:**
  - needs a workspace, and a run without one fails;
  - tools: `run_command`, plus `start_command`, `check_command` and `stop_command`;
  - output is labelled `[stdout]` / `[stderr]`, with `[exit code: N]` on failure;
  - `default_timeout` is 30 s; there are allow/deny lists for commands.

  A non-zero exit comes back as a normal result, not a retry. A denied command is a `ModelRetry`.
- **Harness `BubblewrapSandbox(LocalWorkspace(...))`:** commands see the host filesystem read-only, a private `/tmp`, and no network (enforced with seccomp). They can write only to the working directory. bwrap 0.8.0 is installed here, and an unprivileged namespace test passed.
- **Not used:** `mcp-run-python`, the pre-Monty option (Pyodide in Deno), has moved out of pydantic-ai into its own repo. It's an MCP server with a `run_python_code` tool, and it needs Deno.

Decisions:
- **Python execution configurations:**
  - stock harness Monty CodeMode;
  - Monty CodeMode with sparktea's prompt, limits and retry budget;
  - a local CPython `run_code` tool matched to sparktea's CPython backend;
  - harness Shell on a `LocalWorkspace`, as an upstream alternative;
  - harness Shell inside `BubblewrapSandbox`, as its isolated variant.
- **Monty:** use `pydantic-monty` 1.1.0.
- **Run conditions:** run one model at a time. Use the same case concurrency for paired Go/Python comparisons and record it with the result. Do not compare cost without also reporting cache-read and cache-write tokens, because the existing Anthropic runs had different prompt-cache behavior.

## Changes

### 1. Shared cases file: `monty_cases.json` (new, repo root)

```json
{"suffixes": {
   "run_code": " Use the run_code tool to compute it. End your reply with one line of the form \"ANSWER: <value>\", ...",
   "shell":    " Use the run_command tool to run Python 3 (python3) to compute it. End your reply with one line of the form \"ANSWER: <value>\", ..."},
 "cases": [{"name": "basic_arith", "expected": "338350", "prompt": "What is the sum ..."}, ...]}
```

- The extracted name, expected value and prompt strings match the 23 entries in `monty_codegen.sh`. The `run_code` suffix is the current `SUFFIX`. The `shell` suffix differs only in which tool to use.
- **`monty_codegen.sh`:**
  - load with `jq`: `jq -r '.cases[] | [.name,.expected,.prompt] | @tsv'` and `.suffixes.run_code`;
  - add `-c file`, defaulting to `monty_cases.json`;
  - fail clearly if `jq` is missing.
- Validate unique case names, required string fields, and the absence of tabs or newlines before using the TSV bridge; `jq @tsv` escapes those characters rather than round-tripping them through Bash `read`.
- Its scoring and `.result` format stay the same.

### 2. Single source for sparktea's prompt text (`codemode/`)

- Move the Monty and CPython `run_code` descriptions and the Monty instruction into `codemode/run_code_description.txt`, `codemode/run_code_python_description.txt` and `codemode/monty_instructions.txt`.
- The relevant `codemode` Go source files embed them with `//go:embed`. Behavior is unchanged.
- Avoid an accidental final newline in the model-facing strings: either store the files without one or trim it explicitly.
- Add a test that the embedded text equals the exact strings sent before.
- The Python matched backends read the same files relative to the repository, not the caller's working directory.

### 3. Python runner: `pyrunner/` (new uv project)

**`pyrunner/pyproject.toml`:**
- Python ≥ 3.11.
- Dependencies pinned: `pydantic-ai==2.55.0`, `pydantic-ai-harness[codemode]==0.55.0`, `pydantic-monty==1.1.0`, and a tested `logfire` version.
- Commit `uv.lock`, and git-ignore `pyrunner/.venv`.

**`pyrunner/monty_codegen.py`:**

- **Usage:**

  ```
  uv run --project pyrunner pyrunner/monty_codegen.py -m provider:model -b BACKEND [-j 4] [-o dir] [-c monty_cases.json] [--stall-timeout 120] [--case-timeout 600] [-l] [case ...]
  ```

  `-m` accepts the sparktea model strings needed by this comparison: `anthropic:` and `openrouter:` pass straight through. `google:` maps to `google-gla:` and `openai:` to `openai-responses:` for incidental use. The default comes from `$SPARKTEA_TEST_MODEL`.
- Resolve the default cases file and shared prompt files from the script/repository location. Document how an explicit relative `-c` path is resolved.
- **Every backend:** build a fresh `Agent(model, instructions=["Answer clearly and concisely."])` and fresh capabilities per case. Shell and CPython cases also get a fresh scratch directory that's deleted afterwards. Per-case construction prevents state or workspace leakage across concurrent cases.

| `-b` backend | Capabilities | Model tool, suffix |
|---|---|---|
| `monty-stock` | `CodeMode()` with the harness defaults, including `max_retries=3` | `run_code` |
| `monty-sparktea` | `CodeMode(max_retries=20, resource_limits={'max_duration_secs': 5, 'max_memory': 64 << 20})`, the Monty instruction appended, and a `prepare_tools` hook that swaps in sparktea's `run_code` description | `run_code` |
| `cpython-sparktea` | a custom stateless `run_code` tool with `max_retries=20`, sparktea's CPython description, `python3 -I`, trailing-expression evaluation, a 5 s timeout, 512 MiB address-space limit and the same restricted environment | `run_code` |
| `shell` | `LocalWorkspace(scratch, env={'HOME': scratch})` and `Shell(allowed_commands=['python3', 'python'], default_timeout=5, denied_env_patterns=LLM_API_KEY_ENV_PATTERNS, tools=['run_command'])` | `run_command` |
| `shell-bwrap` | `BubblewrapSandbox(LocalWorkspace(scratch, env=...))` and the same `Shell(...)` | `run_command` |

- **Notes on these settings:**
  - `monty-sparktea` and `cpython-sparktea` use 20 retries to match sparktea. `monty-stock` deliberately retains the upstream limit of 3 and may abort cases that eventually pass under the matched configuration.
  - `monty-sparktea` is prompt/limits matched, not identical: Python CodeMode still exposes `restart`, keeps REPL state between calls, and uses Monty 1.1.0 rather than sparktea's Monty revision.
  - `cpython-sparktea` is the direct comparison with sparktea's CPython backend. Reuse `codemode/python_runner.py` rather than independently reimplementing its trailing-expression and error behavior.
  - The `python3` allowlist keeps the benchmark on Python. Per the harness docs it's a guardrail, not a boundary: Python can still spawn anything.
  - `LocalWorkspace` alone isolates nothing.
  - `shell-bwrap` prevents writes outside the scratch directory and cuts off the network, but readable host files remain a confidentiality consideration.
  - The 5 s timeout matches sparktea's.
- **Execution:**
  - Run the cases with an `asyncio.Semaphore(jobs)`.
  - Consume the agent's event stream rather than relying only on the final `AgentRunResult`. This preserves partial tool calls and messages when a provider error, retry exhaustion or timeout ends the case.
  - Implement a 120 s inactivity watchdog reset by agent/model events, matching sparktea. Keep `--case-timeout` (default 600 s) as a separate hard safety ceiling; it is not a substitute for the inactivity watchdog.
  - Catch failures per case so one failed asyncio task cannot abort the remaining cases or prevent the summary.
- **Per-case files**, the same as the shell script's, so the existing scoring and analysis carry over:
  - `NAME.stdout`: the final output.
  - `NAME.stderr`, recorded in event/message order even if the run fails:
    - `[tool call] TOOL {args json}`;
    - `[tool result] TOOL {content json}`;
    - `[tool error] TOOL <message>`;
    - then a `usage:` line in sparktea's format.

    What counts as a tool error and how it is rendered must be deterministic:
    - for `run_code`: a `RetryPromptPart`;
    - for `run_command`: a `RetryPromptPart`, a result containing a non-zero `[exit code: N]`, or `[Command timed out`.

    For classifying errors, reuse the shell runner's prefix rules and also take the last `XxxError: ...` line of a Python traceback. This covers Monty retry messages that are not full tracebacks. Decide explicitly whether a non-zero Shell result produces both `[tool result]` and a synthetic `[tool error]`; the call/error counts must reconcile with the file.
  - `NAME.result`: the same tab-separated fields (name, status, calls, errors, secs, error_types, answer). It uses the same `ANSWER:` extraction and the same PASS / WRONG / NOCODE / ERROR rules, with "calls" counting the backend's execution tool.
- Always write `.stdout`, `.stderr` and `.result`, including for provider errors, retry exhaustion and timeouts.
- **Summary table:** the same as the shell script's, including its model, backend and logs lines. Also print jobs/concurrency. Exit status 0 only if every selected case passed, and 1 if no cases matched.
- **Logfire:**
  - Configure Logfire with `service_name=os.getenv("OTEL_SERVICE_NAME", "sparktea-runner")`, `send_to_logfire="if-token-present"`, and `logfire.instrument_pydantic_ai()`.
  - Honor `OTEL_SERVICE_NAME`, `LOGFIRE_TOKEN`, `LOGFIRE_ENDPOINT` and `LOGFIRE_SEND_CONTENT` consistently with sparktea. The Python runner sends content by default; `LOGFIRE_SEND_CONTENT=0` opts out. Keep `sparktea-runner` as the distinct default service so Go and Python data cannot be combined accidentally.
  - Open one root span per case named `sparktea turn`, so the existing duration query still applies. Give it:
    - `sparktea.provider`, `sparktea.model`, `sparktea.code_mode=true` and `sparktea.code_backend`;
    - `sparktea.implementation=python` and `sparktea.case`;
    - Logfire tags containing the broad backend family (`monty`, `cpython` or `shell`), the selected runner backend, and `pydantic-ai`. This keeps the Monty/CPython filters aligned with sparktea while retaining per-configuration filters.
  - Use `sparktea.code_backend=monty` for `monty-sparktea`, `cpython` for `cpython-sparktea`, and the literal backend name for the other configurations. Service name and `sparktea.implementation` distinguish these from Go spans.
  - Run `agent` inside that span so Pydantic AI's `invoke_agent`, `chat <model>` and `execute_tool ...` spans are descendants in the same trace.
  - `chat` spans are the authoritative source for requests, input/output/reasoning/cache tokens and `operation.cost`. Cost may be absent when the provider supplies insufficient usage or the model is unknown to the pricing table; report that rather than treating it as zero.
  - The matched CPython tool emits a `python.run` span below `execute_tool run_code`, with `python.exit_code`, `python.timed_out`, `python.exec_ms` and `python.code_bytes`. Verify whether harness CodeMode emits a `monty.run` child; add a wrapper span if needed so interpreter timing is queryable in the same shape as sparktea.
  - `LOGFIRE_SEND_CONTENT=0` must remove prompts, generated code and tool results while retaining trace structure, usage and cost.

### 4. Docs

- **README** ("Scripting" section):
  - the cases file and `-c`;
  - running `pyrunner`, with prerequisites (`uv` and `jq`; `bwrap` with user namespaces for `shell-bwrap`);
  - a table of the five Python backends, marking the two matched configurations and the three upstream/alternative configurations, with an isolation note for each.
- **`CODE-MODE.md`:** a "Python pydantic-ai comparison" section covering:
  - harness `CodeMode` against sparktea's Monty backend: limits, retries, description, REPL state and `restart`, Monty v1.1.0 vs. v1.0.1;
  - `LocalWorkspace` in both libraries;
  - the matched Python CPython tool against sparktea's CPython backend;
  - Shell and Bubblewrap having no direct pydantic-ai-go counterpart. A bwrap-wrapped `WorkspaceBackend` would be the Go equivalent, as a possible follow-up for sparktea's CPython backend.

## Critical files

- `monty_codegen.sh`: cases move out; `jq` loading and `-c` added.
- `monty_cases.json` (new).
- `codemode/codemode.go`, `codemode/python.go`, plus `codemode/run_code_description.txt`, `codemode/run_code_python_description.txt` and `codemode/monty_instructions.txt` (new), and tests in `codemode/codemode_test.go`.
- `pyrunner/pyproject.toml`, `pyrunner/uv.lock` and `pyrunner/monty_codegen.py` (new); `.gitignore`.
- `README.md` and `CODE-MODE.md`.

Things to reuse:
- the scoring and summary logic in `monty_codegen.sh` (`run_case`), ported to Python;
- sparktea's agent instruction (`newAgentFor` in `cmd/sparktea`);
- the stderr line format that `monty_codegen.sh` already parses.

## Verification

1. `go vet ./...` and `go test ./...` pass, including the new embedded-text equality test.
2. `./monty_codegen.sh -l` and `uv run --project pyrunner pyrunner/monty_codegen.py -l` print the same 23 cases.
3. `./monty_codegen.sh -m openrouter:z-ai/glm-5.3-flash sha256 enum generator` still works, with an unchanged output format.
4. Offline tests with a scripted/test model cover PASS, WRONG, NOCODE, retry, non-zero shell exit, provider exception, retry exhaustion, inactivity timeout and hard timeout. Every path writes all three per-case files, and recorded call/error counts reconcile with their stderr lines.
5. Live, cheap: GLM 5.3 Flash on `sha256 enum generator match`, once with each of the five Python backends. Check:
   - the `.result` and `.stderr` files and the summary;
   - in Logfire: one `sparktea turn` trace per case under service `sparktea-runner`, with broad and selected-backend tags, descendant agent/tool/interpreter spans, usage and `operation.cost`;
   - for `monty-sparktea`, sparktea's description in `gen_ai.tool.definitions`.
6. Reconcile telemetry for the live sample:
   - count root spans and compare their summed duration with local case durations;
   - sum `operation.cost`, requests and every token category from `chat` spans;
   - count `execute_tool` spans and compare with `.result` calls;
   - verify failed tool/interpreter spans have error status and every child belongs to the correct case trace.
7. (Optional privacy check) Repeat one case with `LOGFIRE_SEND_CONTENT=0`; prompts, generated code and results are absent, while usage, cost and trace structure remain. The default sends content.
8. `shell-bwrap` isolation, with a one-off case or a direct call:
   - writing outside the scratch directory fails;
   - opening a network socket fails;
   - `os.environ` contains no `OPENROUTER_API_KEY`.

   With `shell`, the network works, which confirms the difference between the two. Also document which host paths remain readable under Bubblewrap.
9. The full comparison is a separate execution step: run the selected OpenRouter and current Anthropic models one at a time, with paired Go/Python runs using the same concurrency, and write the resulting report into `results/`. Report cache behavior alongside cost and keep Shell/Bubblewrap results separate from the matched Monty/CPython table.
