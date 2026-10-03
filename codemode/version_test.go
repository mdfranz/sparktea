package codemode

import "testing"

func TestCompactModuleVersion(t *testing.T) {
	tests := map[string]string{
		"v0.0.17":                               "v0.0.17",
		"v0.0.17-0.20261002155701-5325fd2318ec": "5325fd2",
		"v0.0.0-20260905152908-2a387f1a0338":    "2a387f1",
		"(devel)":                               "devel",
		"":                                      "devel",
	}
	for input, want := range tests {
		if got := compactModuleVersion(input); got != want {
			t.Errorf("compactModuleVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestVersionSummary(t *testing.T) {
	if got := VersionSummary(); got == "gomonty unknown · monty "+MontyVersion {
		t.Fatalf("VersionSummary() could not find the linked gomonty module: %q", got)
	}
}
