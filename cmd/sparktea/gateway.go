package main

import (
	"net/http"
	"os"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/propagation"
)

// Pydantic AI (Logfire) Gateway support. When a gateway key is set, the
// OpenAI and Anthropic catalog entries route through the gateway instead of
// calling the provider directly. This mirrors pydantic-ai's
// providers/gateway.py without any pydantic-ai-go changes: the gateway is just
// a custom base URL, a bearer token, and a traceparent header.

const (
	gatewayKeyEnv     = "PYDANTIC_AI_GATEWAY_API_KEY"
	gatewayKeyEnvAlt  = "PAIG_API_KEY"
	gatewayBaseEnv    = "PYDANTIC_AI_GATEWAY_BASE_URL"
	gatewayBaseEnvAlt = "PAIG_BASE_URL"
)

// gatewayKeyPattern matches pylf_v<version>_<region>_<secret>; the region
// selects the gateway host.
var gatewayKeyPattern = regexp.MustCompile(`^pylf_v[0-9]+_([a-z]+)_[a-zA-Z0-9_-]+$`)

// gatewayAPIKey returns the gateway key, or "" when the gateway is not in use.
func gatewayAPIKey() string {
	if key := os.Getenv(gatewayKeyEnv); key != "" {
		return key
	}
	return os.Getenv(gatewayKeyEnvAlt)
}

// gatewayActive reports whether a gateway key is set and a base URL can be
// resolved for it.
func gatewayActive() bool {
	key := gatewayAPIKey()
	return key != "" && gatewayBaseURL(key) != ""
}

// gatewayRoutes are the route segments sparktea knows. A base URL that already
// ends in one (e.g. .../proxy/google-vertex) is trimmed back to the proxy root.
var gatewayRoutes = []string{"openai", "anthropic", "google-vertex", "groq", "bedrock"}

// gatewayBaseURL returns the gateway proxy root without a trailing slash. An
// explicit base URL env var wins; otherwise the host comes from the region
// encoded in the key. It returns "" when neither is available.
func gatewayBaseURL(key string) string {
	base := os.Getenv(gatewayBaseEnv)
	if base == "" {
		base = os.Getenv(gatewayBaseEnvAlt)
	}
	if base != "" {
		base = strings.TrimRight(base, "/")
		for _, route := range gatewayRoutes {
			base = strings.TrimSuffix(base, "/"+route)
		}
		return base
	}
	m := gatewayKeyPattern.FindStringSubmatch(key)
	if m == nil {
		return ""
	}
	if strings.HasPrefix(m[1], "staging") {
		return "https://gateway.pydantic.info/proxy"
	}
	return "https://gateway-" + m[1] + ".pydantic.dev/proxy"
}

// gatewayRoute returns the URL for one upstream route, e.g. "openai".
func gatewayRoute(base, route string) string {
	return base + "/" + route
}

// gatewayTransport authenticates requests with the gateway key and injects the
// W3C traceparent header so gateway spans join sparktea's trace.
type gatewayTransport struct {
	key  string
	base http.RoundTripper
}

func (t gatewayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	propagation.TraceContext{}.Inject(req.Context(), propagation.HeaderCarrier(req.Header))
	// The Anthropic and Google adapters send their own key headers; the
	// gateway wants a bearer token.
	req.Header.Del("x-api-key")
	req.Header.Del("x-goog-api-key")
	req.Header.Set("Authorization", "Bearer "+t.key)
	return t.base.RoundTrip(req)
}

func gatewayHTTPClient(key string) *http.Client {
	return &http.Client{Transport: gatewayTransport{key: key, base: http.DefaultTransport}}
}
