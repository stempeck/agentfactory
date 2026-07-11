package cmd

import (
	"testing"
)

// TestContainmentRoutingRoot_RoutesBySessionIdentity pins the #519 review follow-up
// (unresolved thread 7b, containment.go:411): a containment notification is routed by
// SESSION IDENTITY (the launcher-baked AF_ROOT / home factory), never by the offending
// boundary cwd that tripped the alarm. Routing by the stray cwd would deliver the
// stray-agent warning into — or, under the post-#519 cross-check, be refused by — the
// very wrong factory it is warning about, silently losing the alarm when it matters most.
func TestContainmentRoutingRoot_RoutesBySessionIdentity(t *testing.T) {
	fx := buildNestedFactoryFixture(t)

	t.Run("AF_ROOT set: routes to the home factory, not the stray boundary", func(t *testing.T) {
		t.Setenv("AF_ROOT", fx.outer)
		// The boundary is the nested clone the agent strayed into.
		got, err := containmentRoutingRoot(fx.clone)
		if err != nil {
			t.Fatalf("containmentRoutingRoot: %v", err)
		}
		if got != fx.outer {
			t.Errorf("routing root = %q, want the AF_ROOT home factory %q (must NOT route by the stray boundary %q)", got, fx.outer, fx.clone)
		}
	})

	t.Run("AF_ROOT unset: falls back to the passed boundary", func(t *testing.T) {
		t.Setenv("AF_ROOT", "")
		got, err := containmentRoutingRoot(fx.clone)
		if err != nil {
			t.Fatalf("containmentRoutingRoot: %v", err)
		}
		if got != fx.clone {
			t.Errorf("with AF_ROOT unset, routing root = %q, want the boundary fallback %q", got, fx.clone)
		}
	})
}
