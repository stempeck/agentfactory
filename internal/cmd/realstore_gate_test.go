package cmd

import "testing"

// realStoreGateAction is the decision of the real-store dependency gate used by
// the integration suite's requirePython3WithServerDeps. It is the PURE decision
// core — extracted from the live *testing.T so it can be unit-tested directly
// under `make test`, mirroring tmuxisolation.checkCIOnly (ciguard.go), the
// repo's established "pure predicate + thin impure actor" idiom. The integration
// helper (//go:build integration) maps the action onto t.Skipf / t.Fatalf.
type realStoreGateAction int

const (
	// realStoreProceed: the real Python MCP store deps are present — run the test.
	realStoreProceed realStoreGateAction = iota
	// realStoreSkip: deps missing and the CI signal is unset — friendly skip so
	// dev machines without python3/aiohttp/sqlalchemy are unaffected (local default).
	realStoreSkip
	// realStoreFatal: deps missing while AF_REQUIRE_REAL_STORE=1 (the gating
	// integration lane) — hard-fail so the real-store gate can never silently
	// no-op → green under CI Python/venv drift (issue #458 Gap-4 / THREAD-1).
	realStoreFatal
)

// realStoreGateDecision is the pure decision: given whether the gating lane
// requires the real store (AF_REQUIRE_REAL_STORE=1, captured before the env
// wipe) and whether a dependency probe failed, return the action.
//
//   - deps present            -> Proceed (the CI signal is irrelevant)
//   - deps missing, !require   -> Skip   (friendly local skip)
//   - deps missing,  require   -> Fatal  (no silent green in the gating lane)
func realStoreGateDecision(requireRealStore, depMissing bool) realStoreGateAction {
	if !depMissing {
		return realStoreProceed
	}
	if requireRealStore {
		return realStoreFatal
	}
	return realStoreSkip
}

func TestRealStoreGateDecision(t *testing.T) {
	cases := []struct {
		name             string
		requireRealStore bool
		depMissing       bool
		want             realStoreGateAction
	}{
		{"deps present, CI signal set -> proceed", true, false, realStoreProceed},
		{"deps present, CI signal unset -> proceed", false, false, realStoreProceed},
		{"deps missing, CI signal unset -> friendly skip", false, true, realStoreSkip},
		{"deps missing, CI signal set -> hard fatal (the fix)", true, true, realStoreFatal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := realStoreGateDecision(tc.requireRealStore, tc.depMissing)
			if got != tc.want {
				t.Errorf("realStoreGateDecision(requireRealStore=%v, depMissing=%v) = %d, want %d",
					tc.requireRealStore, tc.depMissing, got, tc.want)
			}
		})
	}
}
