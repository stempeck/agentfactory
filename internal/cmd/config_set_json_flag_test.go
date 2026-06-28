package cmd

import "testing"

// TestConfigSet_NoJSONFlag locks in the T4 fix: the dead `--json` flag — registered on
// `config dispatch set` and `config startup set` but never read by their handlers,
// which unconditionally decode JSON from stdin — is removed. A flag whose value is
// silently ignored misrepresents the CLI contract.
func TestConfigSet_NoJSONFlag(t *testing.T) {
	if f := configDispatchSetCmd.Flags().Lookup("json"); f != nil {
		t.Errorf("`config dispatch set` must not register a --json flag (it is never read)")
	}
	if f := configStartupSetCmd.Flags().Lookup("json"); f != nil {
		t.Errorf("`config startup set` must not register a --json flag (it is never read)")
	}
}
