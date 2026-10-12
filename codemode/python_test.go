package codemode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func runPythonRaw(t *testing.T, code string) (any, error) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	args, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	return New(WithBackend(BackendPython)).handleRunPython(context.Background(), args)
}

func runPython(t *testing.T, code string) any {
	t.Helper()
	result, err := runPythonRaw(t, code)
	if err != nil {
		t.Fatalf("handleRunPython returned an error for a script expected to succeed: %v", err)
	}
	return result
}

func TestParseBackend(t *testing.T) {
	for _, s := range []string{"monty", "python"} {
		if b, err := ParseBackend(s); err != nil || string(b) != s {
			t.Errorf("ParseBackend(%q) = %q, %v", s, b, err)
		}
	}
	if _, err := ParseBackend("cpython"); err == nil {
		t.Error("ParseBackend(\"cpython\") succeeded, want an error")
	}
}

func TestPythonBackendRegistersOwnDefinitionWithoutMontyInstruction(t *testing.T) {
	def := runCodePythonDefinition()
	if def.Name != ToolName {
		t.Fatalf("name = %q, want %q", def.Name, ToolName)
	}
	if strings.Contains(def.Description, "Monty") || !strings.Contains(def.Description, "CPython") {
		t.Fatalf("description should describe CPython, not Monty: %q", def.Description)
	}
	if !reflect.DeepEqual(def.Schema, runCodeDefinition().Schema) {
		t.Fatal("python run_code schema differs from Monty's; the model-facing tool must match")
	}
}

func TestRunPythonResultShapes(t *testing.T) {
	cases := []struct {
		code string
		want any
	}{
		{"40 + 2", json.Number("42")},
		{"2 ** 100", json.Number("1267650600228229401496703205376")},
		{"x = 1", map[string]any{}},
		{"print('hi')", map[string]any{"output": "hi\n"}},
		{"print('hi')\n[1, (2, 3)]", map[string]any{"output": "hi\n", "result": []any{json.Number("1"), []any{json.Number("2"), json.Number("3")}}}},
		{"{'a': None}", map[string]any{"a": nil}},
		{"b'\\x00'", "b'\\x00'"},
		{"False", false},
	}
	for _, tc := range cases {
		if got := runPython(t, tc.code); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q: got %#v, want %#v", tc.code, got, tc.want)
		}
	}
}

// The point of the backend: things Monty rejects run fine.
func TestRunPythonFullLanguageAndStdlib(t *testing.T) {
	code := `import hashlib, enum, statistics
from dataclasses import dataclass, field, asdict
class Color(enum.Enum):
    RED = 1
class Base:
    def area(self): return 0
class Sq(Base):
    @property
    def area(self): return 4
def gen():
    yield 1
@dataclass
class Item:
    qty: int = field(default=2)
match 3:
    case 3: m = "ok"
[hashlib.sha256(b"hello").hexdigest()[:8], Color.RED.value, statistics.median([1, 2, 3]), Sq().area, list(gen()), asdict(Item()), m, "é".encode("latin-1")[-1]]`
	want := []any{"2cf24dba", json.Number("1"), json.Number("2"), json.Number("4"), []any{json.Number("1")}, map[string]any{"qty": json.Number("2")}, "ok", json.Number("233")}
	if got := runPython(t, code); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestRunPythonErrorsAreRetries(t *testing.T) {
	cases := []struct{ code, prefix string }{
		{"def f(:\n  pass", "SyntaxError:"},
		{"print('before')\nraise ValueError('bad')", "ValueError: bad"},
		{"import numpy_does_not_exist", "ModuleNotFoundError:"},
		{"import time\ntime.sleep(10)", "TimeoutError:"},
		{"x = bytearray(1 << 30)", "MemoryError"},
	}
	for _, tc := range cases {
		result, err := runPythonRaw(t, tc.code)
		msg := checkRetry(t, result, err)
		if !strings.HasPrefix(msg, tc.prefix) {
			t.Errorf("%q: message %q, want prefix %q", tc.code, msg, tc.prefix)
		}
	}
	result, err := runPythonRaw(t, "print('before')\nraise ValueError('bad')")
	msg := checkRetry(t, result, err)
	for _, want := range []string{`File "run_code.py", line 2`, "Output before the error:\nbefore"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	if strings.Contains(msg, "<string>") {
		t.Errorf("message leaks runner frames: %q", msg)
	}
}

func TestRunPythonEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "secret")
	got := runPython(t, "import os\n[os.environ.get('OPENROUTER_API_KEY'), os.getcwd() == os.environ['HOME']]")
	if want := []any{nil, true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v (API key hidden, HOME is the scratch dir)", got, want)
	}
}

func TestRunPythonScratchDirectoryIsRemoved(t *testing.T) {
	dir, ok := runPython(t, "import os\nos.getcwd()").(string)
	if !ok {
		t.Fatal("expected a string result")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir %s still exists (err %v)", dir, err)
	}
}
