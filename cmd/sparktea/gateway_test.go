package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func clearGatewayEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{gatewayKeyEnv, gatewayKeyEnvAlt, gatewayBaseEnv, gatewayBaseEnvAlt} {
		t.Setenv(k, "")
	}
}

func TestGatewayBaseURL(t *testing.T) {
	clearGatewayEnv(t)
	tests := []struct {
		name, key, base, want string
	}{
		{"us region", "pylf_v1_us_abc-DEF_123", "", "https://gateway-us.pydantic.dev/proxy"},
		{"eu region", "pylf_v2_eu_abc", "", "https://gateway-eu.pydantic.dev/proxy"},
		{"staging", "pylf_v1_staging_abc", "", "https://gateway.pydantic.info/proxy"},
		{"override trims slash", "pylf_v1_us_abc", "https://example.test/proxy/", "https://example.test/proxy"},
		{"unknown key shape", "sk-nope", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(gatewayBaseEnv, tt.base)
			if got := gatewayBaseURL(tt.key); got != tt.want {
				t.Fatalf("gatewayBaseURL(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestGatewayActive(t *testing.T) {
	clearGatewayEnv(t)
	if gatewayActive() {
		t.Fatal("active with no key")
	}
	t.Setenv(gatewayKeyEnvAlt, "sk-nope")
	if gatewayActive() {
		t.Fatal("active with a key whose base URL cannot be resolved")
	}
	t.Setenv(gatewayKeyEnvAlt, "pylf_v1_us_abc")
	if !gatewayActive() {
		t.Fatal("inactive with a regional key")
	}
	t.Setenv(gatewayKeyEnv, "pylf_v1_eu_xyz")
	if got := gatewayAPIKey(); got != "pylf_v1_eu_xyz" {
		t.Fatalf("PYDANTIC_AI_GATEWAY_API_KEY should win over PAIG_API_KEY, got %q", got)
	}
}

func TestGatewayTransportHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3},
		SpanID:     trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", "should-be-removed")

	resp, err := gatewayHTTPClient("pylf_v1_us_abc").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if v := got.Get("Authorization"); v != "Bearer pylf_v1_us_abc" {
		t.Fatalf("Authorization = %q", v)
	}
	if v := got.Get("x-api-key"); v != "" {
		t.Fatalf("x-api-key not removed: %q", v)
	}
	if v := got.Get("traceparent"); v == "" {
		t.Fatal("traceparent not injected")
	}
	if req.Header.Get("x-api-key") == "" {
		t.Fatal("caller's request was mutated")
	}
}

func TestGatewayModelRouting(t *testing.T) {
	clearGatewayEnv(t)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	openaiOpt := modelOption{"GPT", providerOpenAI, "gpt-5.6-sol"}
	anthropicOpt := modelOption{"Claude", providerAnthropic, "claude-sonnet-5-5"}
	if openaiOpt.apiKeyPresent() || anthropicOpt.apiKeyPresent() {
		t.Fatal("models offered with no keys")
	}
	t.Setenv(gatewayKeyEnv, "pylf_v1_us_abc")
	if !openaiOpt.apiKeyPresent() || !anthropicOpt.apiKeyPresent() {
		t.Fatal("gateway key should enable OpenAI and Anthropic models")
	}
	type identity interface {
		ProviderName() string
		ProviderURL() string
	}
	oai, ok := openaiOpt.newModel().(identity)
	if !ok {
		t.Fatal("OpenAI model has no provider identity")
	}
	if got := oai.ProviderName(); got != "openai" {
		t.Fatalf("OpenAI provider name = %q, want openai", got)
	}
	if got := oai.ProviderURL(); got != "https://gateway-us.pydantic.dev/proxy/openai" {
		t.Fatalf("OpenAI URL = %q", got)
	}
	ant, ok := anthropicOpt.newModel().(identity)
	if !ok {
		t.Fatal("Anthropic model has no provider identity")
	}
	if got := ant.ProviderURL(); got != "https://gateway-us.pydantic.dev/proxy/anthropic/v1" {
		t.Fatalf("Anthropic URL = %q", got)
	}
}
