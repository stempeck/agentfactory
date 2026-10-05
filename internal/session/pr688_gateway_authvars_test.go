package session

import (
	"reflect"
	"testing"
)

// TestGatewayUpstreamAuthVarsIsCanonicalFive closes the session-side pin for PR #688 F15.
// afGatewayUpstreamAuthVars (session.go) must stay byte-identical — same names, same order — to
// the config denylist config.afGatewayUpstreamKeys, which internal/config/models_gateway_test.go
// already pins to this literal. No prior session test asserted the slice itself against the five
// names, so an ADD/REORDER to it went uncaught (the twin-literal drift). ADR-004 forbids importing
// the unexported config list across the package boundary, so this holds an independent literal copy.
func TestGatewayUpstreamAuthVarsIsCanonicalFive(t *testing.T) {
	want := []string{
		"OPENAI_API_KEY",
		"CHATGPT_TOKEN_DIR",
		"CHATGPT_AUTH_FILE",
		"CHATGPT_API_BASE",
		"CODEX_HOME",
	}
	if !reflect.DeepEqual(afGatewayUpstreamAuthVars, want) {
		t.Errorf("afGatewayUpstreamAuthVars drifted from the canonical five (names and order):\n got: %#v\nwant: %#v", afGatewayUpstreamAuthVars, want)
	}
}
