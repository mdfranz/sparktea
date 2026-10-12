package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	ai "github.com/Kludex/pydantic-ai-go/ai"
	"github.com/Kludex/pydantic-ai-go/ai/models/fakes"
)

func withStallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := stallTimeout
	stallTimeout = d
	t.Cleanup(func() { stallTimeout = old })
}

// A model that never answers is cut off after stallTimeout with a stall
// error, not left to hang until the connection resets.
func TestRunTurnStallWatchdogCancelsSilentModel(t *testing.T) {
	withStallTimeout(t, 100*time.Millisecond)
	model := fakes.NewFunctionModel(func(ctx context.Context, _ []ai.ModelMessage, _ ai.ModelRequestParams) (*ai.ModelResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	agent := ai.NewAgent[struct{}, string](model)

	start := time.Now()
	_, err := runTurn(context.Background(), agent, modelOption{}, "q", nil, nil, "test")
	if !errors.Is(err, errStalled) {
		t.Fatalf("err = %v, want errStalled", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %s to give up", elapsed)
	}
}

// The timeout is per gap between events, not per turn: a turn longer than
// stallTimeout overall succeeds as long as events keep arriving.
func TestRunTurnStallWatchdogAllowsSlowSteadyTurn(t *testing.T) {
	withStallTimeout(t, 300*time.Millisecond)
	var requests atomic.Int32
	model := fakes.NewFunctionModel(func(context.Context, []ai.ModelMessage, ai.ModelRequestParams) (*ai.ModelResponse, error) {
		time.Sleep(150 * time.Millisecond)
		if requests.Add(1) == 1 {
			return &ai.ModelResponse{Parts: []ai.ResponsePart{
				ai.ToolCallPart{ToolName: "slow", Args: []byte(`{}`), ToolCallID: "c1"},
			}}, nil
		}
		return &ai.ModelResponse{Parts: []ai.ResponsePart{ai.TextPart{Content: "done"}}}, nil
	})
	agent := ai.NewAgent[struct{}, string](model)
	ai.AddSimpleTool(agent, "slow", func(context.Context, struct{}) (int, error) {
		time.Sleep(150 * time.Millisecond)
		return 1, nil
	})

	start := time.Now()
	if _, err := runTurn(context.Background(), agent, modelOption{}, "q", nil, nil, "test"); err != nil {
		t.Fatalf("runTurn: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("turn took %s; the test needs it to outlast stallTimeout", elapsed)
	}
}

func TestStallTimeoutFromEnv(t *testing.T) {
	cases := map[string]time.Duration{
		"":    defaultStallTimeout,
		"3m":  3 * time.Minute,
		"0":   0,
		"bad": defaultStallTimeout,
		"-1s": defaultStallTimeout,
	}
	for in, want := range cases {
		if got := stallTimeoutFromEnv(in); got != want {
			t.Errorf("stallTimeoutFromEnv(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestStallWatchdogDisabled(t *testing.T) {
	withStallTimeout(t, 0)
	w := newStallWatchdog(context.Background())
	defer w.stop()
	w.reset()
	select {
	case <-w.Context().Done():
		t.Fatal("disabled watchdog cancelled the turn")
	case <-time.After(50 * time.Millisecond):
	}
}
