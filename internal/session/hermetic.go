package session

// TmuxForTest exposes the unexported tmuxClient interface to external test
// helpers (notably internal/cmd) that must inject a recording fake via
// InstallHermeticForTest. Because it is a type alias, any value implementing the
// 11-method tmuxClient union is assignable to it from another package without
// that package having to name the unexported interface.
type TmuxForTest = tmuxClient

// InstallHermeticForTest redirects the two internal/session seams for hermetic
// tests and returns a restore closure that callers register with t.Cleanup:
//   - sessionPrefixFn -> a constant function returning prefix
//   - newManagerTmux  -> mk (only when mk != nil)
//
// It exists because internal/cmd cannot assign session's unexported seam vars
// directly, and an export_test.go would only widen visibility to session's own
// test build — not to internal/cmd. This is test-support code: no production
// path calls it, so it does not change production behavior.
func InstallHermeticForTest(prefix string, mk func() TmuxForTest) (restore func()) {
	origPrefix := sessionPrefixFn
	origManagerTmux := newManagerTmux

	sessionPrefixFn = func() string { return prefix }
	if mk != nil {
		newManagerTmux = func() tmuxClient { return mk() }
	}

	return func() {
		sessionPrefixFn = origPrefix
		newManagerTmux = origManagerTmux
	}
}
