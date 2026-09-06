package main

import (
	"strings"
	"testing"
)

func TestParseScript(t *testing.T) {
	src := `
# a comment, and a blank line above are both ignored
Work through this step by step: what's 17% of 340?

/search on
Thanks, that's helpful.
/search off
/code on
/model anthropic:claude-opus-5
/clear
One more, on a fresh history.
`
	steps, err := parseScript(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []scriptStep{
		{kind: "prompt", arg: "Work through this step by step: what's 17% of 340?"},
		{kind: "search", arg: "on"},
		{kind: "prompt", arg: "Thanks, that's helpful."},
		{kind: "search", arg: "off"},
		{kind: "code", arg: "on"},
		{kind: "model", arg: "anthropic:claude-opus-5"},
		{kind: "clear"},
		{kind: "prompt", arg: "One more, on a fresh history."},
	}
	if len(steps) != len(want) {
		t.Fatalf("parseScript() = %d steps, want %d: %+v", len(steps), len(want), steps)
	}
	for i, w := range want {
		if steps[i] != w {
			t.Errorf("step %d = %+v, want %+v", i, steps[i], w)
		}
	}
}

func TestParseScriptErrors(t *testing.T) {
	for name, src := range map[string]string{
		"/model no arg":     "/model",
		"/model extra arg":  "/model a b",
		"/search no arg":    "/search",
		"/search bad value": "/search maybe",
		"/code bad value":   "/code sometimes",
		"/clear with arg":   "/clear now",
		"unknown command":   "/frobnicate",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseScript(strings.NewReader(src)); err == nil {
				t.Errorf("parseScript(%q) succeeded, want an error", src)
			}
		})
	}
}

func TestPromptAndScriptMutuallyExclusive(t *testing.T) {
	if _, err := parseCLIFlags([]string{"-prompt", "hi", "-script", "steps.txt"}); err == nil {
		t.Error("-prompt and -script together succeeded, want an error")
	}
}
