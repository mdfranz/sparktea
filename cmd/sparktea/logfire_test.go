package main

import (
	"os"
	"testing"
)

func TestLogfireSendContent(t *testing.T) {
	t.Run("enabled by default", func(t *testing.T) {
		t.Setenv("LOGFIRE_SEND_CONTENT", "temporary")
		if err := os.Unsetenv("LOGFIRE_SEND_CONTENT"); err != nil {
			t.Fatal(err)
		}
		if !logfireSendContent() {
			t.Fatal("logfireSendContent() = false, want true")
		}
	})

	t.Run("explicitly disabled", func(t *testing.T) {
		t.Setenv("LOGFIRE_SEND_CONTENT", "0")
		if logfireSendContent() {
			t.Fatal("logfireSendContent() = true, want false")
		}
	})
}
