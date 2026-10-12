import ast
import contextlib
import io
import json
import os
import resource
import sys
import time
import traceback

SCRIPT = "run_code.py"

limit = int(os.environ.get("SPARKTEA_MAX_MEMORY", "0"))
if limit:
    resource.setrlimit(resource.RLIMIT_AS, (limit, limit))


def plain(v):
    if v is None or isinstance(v, (bool, int, float, str)):
        return v
    if isinstance(v, (list, tuple, set, frozenset)):
        return [plain(x) for x in v]
    if isinstance(v, dict) and all(isinstance(k, str) for k in v):
        return {k: plain(x) for k, x in v.items()}
    return repr(v)


def fail(exc, output):
    te = traceback.TracebackException.from_exception(exc)
    te.stack = traceback.StackSummary.from_list(
        [f for f in te.stack if f.filename == SCRIPT])
    # Exception line first (as Monty reports it), then any location detail
    # (a SyntaxError's caret lines).
    lines = "".join(te.format_exception_only()).rstrip().split("\n")
    msg = "\n".join([lines[-1]] + lines[:-1])
    if te.stack:
        msg += "\nTraceback (most recent call last):\n" + "".join(te.stack.format()).rstrip()
    if output:
        msg += "\nOutput before the error:\n" + output.rstrip()
    sys.stderr.write(msg + "\n")
    sys.exit(1)


with open(SCRIPT, encoding="utf-8") as f:
    source = f.read()
try:
    tree = ast.parse(source, SCRIPT)
except (SyntaxError, ValueError) as e:  # ValueError: source contains null bytes
    fail(e, "")

last = None
if tree.body and isinstance(tree.body[-1], ast.Expr):
    last = ast.Expression(tree.body.pop().value)

scope = {"__name__": "__main__"}
out = io.StringIO()
start = time.perf_counter()
try:
    with contextlib.redirect_stdout(out):
        exec(compile(tree, SCRIPT, "exec"), scope)
        value = eval(compile(last, SCRIPT, "eval"), scope) if last else None
except BaseException as e:
    fail(e, out.getvalue())
exec_ms = (time.perf_counter() - start) * 1000

sys.stdout.write("\n" + json.dumps({"output": out.getvalue(), "value": plain(value), "exec_ms": exec_ms}) + "\n")
