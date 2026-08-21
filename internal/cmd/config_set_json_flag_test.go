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
	if f := configStatuslineSetCmd.Flags().Lookup("json"); f != nil {
		t.Errorf("`config statusline set` must not register a --json flag (it is never read)")
	}
	if f := configModelsSetCmd.Flags().Lookup("json"); f != nil {
		t.Errorf("`config models set` must not register a --json flag (it is never read)")
	}
	// Added with the messaging setter (issue #620 Phase 1). The rule is easiest to break by
	// copy-paste, which is exactly how the new setter was written.
	if f := configMessagingSetCmd.Flags().Lookup("json"); f != nil {
		t.Errorf("`config messaging set` must not register a --json flag (it is never read)")
	}
}
