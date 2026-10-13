#!/usr/bin/env python3
"""Run the shared Code Mode benchmark through Python pydantic-ai."""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import re
import resource
import shutil
import sys
import tempfile
import time
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Any

os.environ.setdefault("PYDANTIC_AI_NO_BANNER", "1")

import logfire
from logfire import AdvancedOptions
from pydantic_ai import (
    Agent,
    AgentRunResultEvent,
    FunctionToolCallEvent,
    FunctionToolResultEvent,
    ModelRetry,
    PartDeltaEvent,
    PartStartEvent,
    TextPart,
    WrapperToolset,
)
from pydantic_ai.capabilities import LocalWorkspace
from pydantic_ai.messages import RetryPromptPart, ToolReturnPart
from pydantic_ai_harness import BubblewrapSandbox, CodeMode, Shell
from pydantic_ai_harness.shell import LLM_API_KEY_ENV_PATTERNS

ROOT = Path(__file__).resolve().parents[1]
CASES_DEFAULT = ROOT / "monty_cases.json"
PYTHON_RUNNER = ROOT / "codemode" / "python_runner.py"
MONTY_DESCRIPTION = ROOT / "codemode" / "run_code_description.txt"
PYTHON_DESCRIPTION = ROOT / "codemode" / "run_code_python_description.txt"
MONTY_INSTRUCTIONS = ROOT / "codemode" / "monty_instructions.txt"
RUN_CODE_TIMEOUT = 5.0


def get_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("-m", "--model", default=os.getenv("SPARKTEA_TEST_MODEL"))
    parser.add_argument(
        "-b",
        "--backend",
        default="monty-sparktea",
        choices=("monty-stock", "monty-sparktea", "cpython-sparktea", "shell", "shell-bwrap"),
    )
    parser.add_argument("-j", "--jobs", type=int, default=4)
    parser.add_argument("-o", "--outdir")
    parser.add_argument("-c", "--cases", default=str(CASES_DEFAULT))
    parser.add_argument("--stall-timeout", type=float, default=120.0)
    parser.add_argument("--case-timeout", type=float, default=600.0)
    parser.add_argument("-l", "--list", action="store_true")
    parser.add_argument("case", nargs="*")
    args = parser.parse_args()
    if args.jobs < 1:
        parser.error("-j/--jobs must be at least 1")
    if args.stall_timeout <= 0 or args.case_timeout <= 0:
        parser.error("timeouts must be greater than zero")
    return args


def load_cases(path: str) -> tuple[list[dict[str, str]], dict[str, str]]:
    case_path = Path(path)
    if not case_path.is_absolute():
        case_path = ROOT / case_path
    try:
        document = json.loads(case_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"cannot read cases file {case_path}: {exc}") from exc
    suffixes = document.get("suffixes")
    cases = document.get("cases")
    if not isinstance(suffixes, dict) or not isinstance(cases, list):
        raise ValueError("cases file must contain suffixes and cases")
    for key in ("run_code", "shell"):
        if not isinstance(suffixes.get(key), str) or not suffixes[key]:
            raise ValueError(f"cases file has no {key!r} suffix")
    names: set[str] = set()
    for item in cases:
        if not isinstance(item, dict):
            raise ValueError("each case must be an object")
        for key in ("name", "expected", "prompt"):
            value = item.get(key)
            if not isinstance(value, str) or not value or any(c in value for c in "\t\r\n"):
                raise ValueError(f"each case needs a non-empty {key!r} without tabs/newlines")
        name = item["name"]
        if not re.fullmatch(r"[A-Za-z0-9_-]+", name) or name in names:
            raise ValueError(f"invalid or duplicate case name: {name!r}")
        names.add(name)
    return cases, suffixes


def selected(name: str, filters: list[str]) -> bool:
    return not filters or any(part in name for part in filters)


def model_name(name: str) -> str:
    if ":" not in name:
        raise ValueError("model must use provider:model form")
    provider, model = name.split(":", 1)
    provider = {"google": "google-gla", "openai": "openai-responses"}.get(provider, provider)
    return f"{provider}:{model}"


def provider_model(name: str) -> tuple[str, str]:
    normalized = model_name(name)
    return tuple(normalized.split(":", 1))  # type: ignore[return-value]


def json_value(value: Any) -> Any:
    if isinstance(value, (str, int, float, bool)) or value is None:
        return value
    if isinstance(value, dict):
        return {str(k): json_value(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [json_value(v) for v in value]
    if hasattr(value, "model_dump"):
        return json_value(value.model_dump(mode="json"))
    return str(value)


def json_text(value: Any) -> str:
    return json.dumps(json_value(value), ensure_ascii=False, separators=(",", ":"))


def traceback_type(message: str) -> str:
    for line in reversed(message.splitlines()):
        found = re.search(r"\b([A-Za-z_][A-Za-z0-9]*(?:Error|Exception))(?::|$)", line.strip())
        if found:
            return found.group(1)
    prefixes = (
        ("TimeoutError:", "TimeoutError"),
        ("MemoryError:", "MemoryError"),
        ("NotImplementedError:", "NotImplementedError"),
        ("TypeError:", "TypeError"),
        ("ValueError:", "ValueError"),
    )
    for prefix, name in prefixes:
        if message.startswith(prefix):
            return name
    return "other"


def add_tool_call(stderr: list[str], name: str, args: Any) -> None:
    if isinstance(args, str):
        try:
            args = json.loads(args)
        except json.JSONDecodeError:
            pass
    stderr.append(f"[tool call] {name} {json_text(args)}")


def add_tool_result(stderr: list[str], name: str, content: Any) -> None:
    stderr.append(f"[tool result] {name} {json_text(content)}")


def _set_memory_limit() -> None:
    limit = 512 << 20
    resource.setrlimit(resource.RLIMIT_AS, (limit, limit))


async def cpython_run(code: str, scratch: Path) -> tuple[Any, str, float]:
    script_path = scratch / "run_code.py"
    script_path.write_text(code, encoding="utf-8")
    executable = shutil.which("python3") or "python3"
    env = {
        "PATH": os.getenv("PATH", "/usr/bin:/bin"),
        "HOME": str(scratch),
        "LANG": os.getenv("LANG", "C.UTF-8"),
        "LC_ALL": os.getenv("LC_ALL", "C.UTF-8"),
        "LC_CTYPE": os.getenv("LC_CTYPE", "C.UTF-8"),
        "SPARKTEA_MAX_MEMORY": str(512 << 20),
    }
    runner = PYTHON_RUNNER.read_text(encoding="utf-8")
    start = time.monotonic()
    process = await asyncio.create_subprocess_exec(
        executable,
        "-I",
        "-c",
        runner,
        cwd=scratch,
        env=env,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        preexec_fn=_set_memory_limit,
    )
    try:
        stdout, stderr = await asyncio.wait_for(process.communicate(), timeout=RUN_CODE_TIMEOUT)
    except TimeoutError as exc:
        process.kill()
        await process.communicate()
        raise ModelRetry(f"TimeoutError: script exceeded {RUN_CODE_TIMEOUT:g}s") from exc
    elapsed_ms = (time.monotonic() - start) * 1000
    if process.returncode != 0:
        raise ModelRetry(stderr.decode("utf-8", "replace").strip() or f"RuntimeError: python exited with status {process.returncode}")
    try:
        payload = json.loads(stdout.decode("utf-8", "replace").splitlines()[-1])
    except (IndexError, json.JSONDecodeError) as exc:
        raise ModelRetry("RuntimeError: unreadable result from python") from exc
    return payload.get("value"), payload.get("output", ""), float(payload.get("exec_ms", elapsed_ms))


@dataclass
class MontyDescriptionToolset(WrapperToolset[Any]):
    """Applies sparktea's description and records the sandbox execution span."""

    use_sparktea_description: bool = True

    async def get_tools(self, ctx: Any) -> dict[str, Any]:
        tools = await self.wrapped.get_tools(ctx)
        run_code = tools.get("run_code")
        if run_code is not None and self.use_sparktea_description:
            run_code.tool_def = replace(run_code.tool_def, description=MONTY_DESCRIPTION.read_text(encoding="utf-8"))
        return tools

    async def call_tool(self, name: str, tool_args: dict[str, Any], ctx: Any, tool: Any) -> Any:
        if name != "run_code":
            return await self.wrapped.call_tool(name, tool_args, ctx, tool)
        started = time.monotonic()
        code = tool_args.get("code", "")
        with logfire.span(
            "monty.run",
            attributes={"monty.code_bytes": len(code.encode()), "monty.script_name": "run_code.py"},
        ) as span:
            try:
                result = await self.wrapped.call_tool(name, tool_args, ctx, tool)
                span.set_attribute("monty.exit_code", 0)
                return result
            except BaseException as exc:
                span.set_attribute("monty.exit_code", 1)
                span.set_attribute("monty.error_type", traceback_type(str(exc)))
                raise
            finally:
                # The whole harness tool call, not interpreter time alone.
                span.set_attribute("monty.call_ms", (time.monotonic() - started) * 1000)


class SparkteaCodeMode(CodeMode):
    def get_wrapper_toolset(self, toolset: Any) -> Any:
        wrapped = super().get_wrapper_toolset(toolset)
        return MontyDescriptionToolset(wrapped=wrapped) if wrapped is not None else None


class InstrumentedStockCodeMode(CodeMode):
    def get_wrapper_toolset(self, toolset: Any) -> Any:
        wrapped = super().get_wrapper_toolset(toolset)
        return MontyDescriptionToolset(wrapped=wrapped, use_sparktea_description=False) if wrapped is not None else None


def make_agent(model: str, backend: str, scratch: Path | None) -> Agent[None, str]:
    capabilities: list[Any] = []
    instructions = ["Answer clearly and concisely."]
    if backend == "monty-stock":
        capabilities.append(InstrumentedStockCodeMode())
    elif backend == "monty-sparktea":
        instructions.append(MONTY_INSTRUCTIONS.read_text(encoding="utf-8"))
        capabilities.append(SparkteaCodeMode(
            max_retries=20,
            resource_limits={"max_duration_secs": 5, "max_memory": 64 << 20},
        ))
    elif backend == "cpython-sparktea":
        assert scratch is not None

        async def run_code(code: str) -> Any:
            started = time.monotonic()
            with logfire.span(
                "python.run",
                attributes={
                    "python.script_name": "run_code.py",
                    "python.interpreter": "python3",
                    "python.code_bytes": len(code.encode()),
                },
            ) as span:
                try:
                    value, output, exec_ms = await cpython_run(code, scratch)
                    span.set_attribute("python.exit_code", 0)
                    span.set_attribute("python.exec_ms", exec_ms)
                    if output and os.getenv("LOGFIRE_SEND_CONTENT") != "0":
                        span.set_attribute("python.output", output[:4096])
                    if output and value is not None:
                        return {"output": output, "result": value}
                    if output:
                        return {"output": output}
                    return value if value is not None else {}
                except ModelRetry as exc:
                    msg = str(exc)
                    span.set_attribute("python.exit_code", 1)
                    span.set_attribute("python.timed_out", msg.startswith("TimeoutError:"))
                    span.set_attribute("python.error_type", traceback_type(msg))
                    span.set_attribute("python.exec_ms", (time.monotonic() - started) * 1000)
                    raise

        run_code.__name__ = "run_code"
        run_code.__doc__ = PYTHON_DESCRIPTION.read_text(encoding="utf-8")
        return Agent(
            model,
            name="sparktea",
            instructions=instructions,
            tools=[run_code],
            retries=20,
            capabilities=capabilities,
        )
    elif backend in ("shell", "shell-bwrap"):
        assert scratch is not None
        workspace = LocalWorkspace(scratch, env={"HOME": str(scratch)})
        if backend == "shell-bwrap":
            capabilities.append(BubblewrapSandbox(workspace))
        else:
            capabilities.append(workspace)
        capabilities.append(
            Shell(
                allowed_commands=["python3", "python"],
                default_timeout=RUN_CODE_TIMEOUT,
                denied_env_patterns=LLM_API_KEY_ENV_PATTERNS,
                tools=["run_command"],
            )
        )
        # Shell raises ModelRetry for disallowed commands; match the 20
        # retries the other matched backends allow instead of the default 1.
        return Agent(model, name="sparktea", instructions=instructions, capabilities=capabilities, retries=20)
    return Agent(model, name="sparktea", instructions=instructions, capabilities=capabilities)


def configure_logfire() -> None:
    endpoint = os.getenv("LOGFIRE_ENDPOINT")
    if endpoint:
        endpoint = endpoint.rstrip("/")
        if not endpoint.startswith(("http://", "https://")):
            endpoint = "https://" + endpoint
    advanced = AdvancedOptions(base_url=endpoint)
    logfire.configure(
        service_name=os.getenv("OTEL_SERVICE_NAME", "sparktea-runner"),
        token=os.getenv("LOGFIRE_TOKEN"),
        send_to_logfire="if-token-present",
        console=False,
        advanced=advanced,
    )
    logfire.instrument_pydantic_ai(include_content=os.getenv("LOGFIRE_SEND_CONTENT") != "0")


def error_message(exc: BaseException) -> str:
    # The stall check raises TimeoutError with a message; the hard timeout
    # (asyncio.wait_for) raises one without.
    if isinstance(exc, TimeoutError) and not str(exc):
        return "TimeoutError: case hard timeout exceeded"
    return f"{type(exc).__name__}: {exc}"


async def execute_case(
    case: dict[str, str], model_arg: str, backend: str, suffixes: dict[str, str],
    outdir: Path, semaphore: asyncio.Semaphore, stall_timeout: float, case_timeout: float,
) -> dict[str, Any]:
    name = case["name"]
    started: float | None = None
    stderr: list[str] = []
    calls = errors = 0
    error_types: dict[str, int] = {}
    final_output = ""
    usage_data: dict[str, Any] = {}
    text_parts: dict[int, str] = {}
    failure: str | None = None
    provider, model_id = provider_model(model_arg)
    code_backend = {
        "monty-sparktea": "monty",
        "cpython-sparktea": "cpython",
    }.get(backend, backend)
    technology_tag = {
        "monty-stock": "monty",
        "monty-sparktea": "monty",
        "cpython-sparktea": "cpython",
        "shell": "shell",
        "shell-bwrap": "shell",
    }[backend]

    async def run() -> None:
        nonlocal calls, errors, final_output, failure
        temp_context = tempfile.TemporaryDirectory(prefix=f"sparktea-{name}-") if backend in ("cpython-sparktea", "shell", "shell-bwrap") else None
        scratch = Path(temp_context.name) if temp_context else None
        try:
            agent = make_agent(model_name(model_arg), backend, scratch)
            attrs = {
                "sparktea.provider": provider,
                "sparktea.model": model_id,
                "sparktea.code_mode": True,
                "sparktea.code_backend": code_backend,
                "sparktea.implementation": "python",
                "sparktea.case": name,
            }
            tags = list(dict.fromkeys([technology_tag, backend, "pydantic-ai"]))
            prompt_suffix = suffixes["shell"] if backend.startswith("shell") else suffixes["run_code"]
            events_context = agent.run_stream_events(case["prompt"] + prompt_suffix)
            # Use Logfire's tag API (not a `logfire.tags` span attribute) so
            # these show up in its tag filters and the records.tags column.
            # The hard case timeout is the asyncio.wait_for around run().
            with logfire.span("sparktea turn", _tags=tags, **attrs):
                async with events_context as events:
                    iterator = events.__aiter__()
                    while True:
                        try:
                            event = await asyncio.wait_for(anext(iterator), timeout=stall_timeout)
                        except StopAsyncIteration:
                            break
                        except TimeoutError as exc:
                            raise TimeoutError(f"model stream stalled for {stall_timeout:g}s") from exc
                        if isinstance(event, PartStartEvent) and isinstance(event.part, TextPart):
                            text_parts[event.index] = event.part.content
                        elif isinstance(event, PartDeltaEvent):
                            delta = getattr(event.delta, "content_delta", None)
                            if isinstance(delta, str):
                                text_parts[event.index] = text_parts.get(event.index, "") + delta
                        elif isinstance(event, FunctionToolCallEvent):
                            part = event.part
                            calls += 1
                            add_tool_call(stderr, part.tool_name, part.args)
                        elif isinstance(event, FunctionToolResultEvent):
                            part = event.part
                            tool = part.tool_name
                            if isinstance(part, RetryPromptPart):
                                message = str(part.content)
                                errors += 1
                                error_types[traceback_type(message)] = error_types.get(traceback_type(message), 0) + 1
                                stderr.append(f"[tool error] {tool} {message}")
                            elif isinstance(part, ToolReturnPart):
                                content = part.content
                                add_tool_result(stderr, tool, content)
                                if tool == "run_command":
                                    content_text = str(content)
                                    match = re.search(r"\[exit code: (\d+)\]", content_text)
                                    if match and int(match.group(1)) != 0:
                                        # Classify by the Python exception in the output,
                                        # like run_code errors.
                                        kind = traceback_type(content_text)
                                        if kind == "other":
                                            kind = "RuntimeError"
                                        message = f"{kind}: command exited with status {match.group(1)}"
                                        errors += 1
                                        error_types[kind] = error_types.get(kind, 0) + 1
                                        stderr.append(f"[tool error] {tool} {message}")
                                    if "[Command timed out" in content_text:
                                        message = "TimeoutError: command timed out"
                                        errors += 1
                                        error_types["TimeoutError"] = error_types.get("TimeoutError", 0) + 1
                                        stderr.append(f"[tool error] {tool} {message}")
                        elif isinstance(event, AgentRunResultEvent):
                            final_output = str(event.result.output)
                            usage = event.result.usage
                            usage_data.update({
                                "requests": usage.requests,
                                "input_tokens": usage.input_tokens,
                                "output_tokens": usage.output_tokens,
                                "cache_read_tokens": usage.cache_read_tokens,
                                "cache_write_tokens": usage.cache_write_tokens,
                            })
                    if events.result is not None:
                        final_output = str(events.result.output)
        finally:
            if temp_context is not None:
                temp_context.cleanup()

    try:
        async with semaphore:
            # Time the case from when it gets a job slot, as monty_codegen.sh does.
            started = time.monotonic()
            await asyncio.wait_for(run(), timeout=case_timeout)
    except BaseException as exc:
        failure = error_message(exc)
        stderr.append(f"[runner error] {failure}")
        if not final_output:
            final_output = "".join(text_parts[index] for index in sorted(text_parts))
    elapsed = time.monotonic() - started if started is not None else 0.0
    if failure:
        final_output = final_output or ""
    usage_line = (
        "usage: requests={requests} input_tokens={input_tokens} output_tokens={output_tokens} "
        "tool_calls={tool_calls} tool_errors={tool_errors} cost=unknown"
    ).format(
        requests=usage_data.get("requests", "unknown"),
        input_tokens=usage_data.get("input_tokens", "unknown"),
        output_tokens=usage_data.get("output_tokens", "unknown"),
        tool_calls=calls,
        tool_errors=errors,
    )
    usage_line += " cache_read_tokens={} cache_write_tokens={}".format(
        usage_data.get("cache_read_tokens", "unknown"),
        usage_data.get("cache_write_tokens", "unknown"),
    )
    stderr.append(usage_line)
    answer_match = re.findall(r"ANSWER:.*", final_output)
    answer = re.sub(r"[*`]", "", re.sub(r"^ANSWER:\s*", "", answer_match[-1]).strip()) if answer_match else "-"
    if failure:
        status = "ERROR"
    elif calls == 0:
        status = "NOCODE"
    elif re.sub(r"\s+", "", case["expected"]) in re.sub(r"\s+", "", answer):
        status = "PASS"
    else:
        status = "WRONG"
    (outdir / f"{name}.stdout").write_text(final_output + ("\n" if final_output else ""), encoding="utf-8")
    (outdir / f"{name}.stderr").write_text("\n".join(stderr) + "\n", encoding="utf-8")
    type_summary = ",".join(f"{key}×{count}" for key, count in sorted(error_types.items())) or "-"
    result = {
        "name": name, "status": status, "calls": calls, "errors": errors,
        "secs": str(int(elapsed)), "error_types": type_summary, "answer": answer,
    }
    (outdir / f"{name}.result").write_text(
        "\t".join(str(result[key]) for key in ("name", "status", "calls", "errors", "secs", "error_types", "answer")) + "\n",
        encoding="utf-8",
    )
    return result


async def main_async(args: argparse.Namespace) -> int:
    cases, suffixes = load_cases(args.cases)
    chosen = [case for case in cases if selected(case["name"], args.case)]
    if args.list:
        for case in chosen:
            print(f"{case['name']:<15} {case['prompt']}")
        return 0
    if not chosen:
        print(f"no cases matched: {' '.join(args.case)}", file=sys.stderr)
        return 1
    if not args.model:
        print("no model set: pass -m provider:model or set SPARKTEA_TEST_MODEL", file=sys.stderr)
        return 1
    try:
        model_name(args.model)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    outdir = Path(args.outdir) if args.outdir else Path(tempfile.mkdtemp(prefix="monty-codegen."))
    outdir.mkdir(parents=True, exist_ok=True)
    configure_logfire()
    print(f"model:   {args.model}")
    print(f"backend: {args.backend}")
    print(f"jobs:    {args.jobs}")
    print(f"logs:    {outdir}")
    print()
    semaphore = asyncio.Semaphore(args.jobs)
    results = await asyncio.gather(*(
        execute_case(case, args.model, args.backend, suffixes, outdir, semaphore, args.stall_timeout, args.case_timeout)
        for case in chosen
    ))
    print(f"{'CASE':<15} {'STATUS':<6} {'CALLS':>5} {'ERRORS':>6} {'SECS':>5}  {'ERROR TYPES':<28} ANSWER")
    passed = calls = errors = 0
    for result in results:
        print(f"{result['name']:<15} {result['status']:<6} {result['calls']:>5} {result['errors']:>6} {result['secs']:>5}  {result['error_types']:<28} {result['answer'][:50]}")
        passed += result["status"] == "PASS"
        calls += result["calls"]
        errors += result["errors"]
    print(f"\n{passed}/{len(results)} passed; {calls} execution calls, {errors} failed")
    return 0 if passed == len(results) else 1


def main() -> int:
    args = get_args()
    try:
        return asyncio.run(main_async(args))
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
