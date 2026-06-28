package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDispatchStatus_JSON_ErrorState locks in the T5 fix: `af dispatch status --json`
// must honor the documented "always exit 0, branch on .state" contract even on an early
// infra failure (no factory root). It must emit a {"state":"error",...} envelope through
// the cobra output seam and return nil, mirroring the agents-list / formula-show siblings.
func TestDispatchStatus_JSON_ErrorState(t *testing.T) {
	t.Chdir(t.TempDir()) // no .agentfactory ⇒ FindFactoryRoot fails

	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	_ = cmd.Flags().Set("json", "true")
	var buf strings.Builder
	cmd.SetOut(&buf)

	runErr := runDispatchStatus(cmd, nil)
	if runErr != nil {
		t.Fatalf("runDispatchStatus --json must return nil on infra failure (errors go in the envelope), got %v", runErr)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &env); err != nil {
		t.Fatalf("output is not a JSON object: %q (%v)", buf.String(), err)
	}
	if env["state"] != "error" || env["error"] == "" {
		t.Errorf("want {state:error, error:<msg>}, got %q", buf.String())
	}
}

// TestRunDispatch_NoInlineCrossValidationDuplicate locks in BODY-1: runDispatch must
// delegate cross-validation to config.ValidateDispatchConfig rather than carry a
// duplicated inline notify/mapping check that can (and already does) drift from the
// shared validator. The notify "not found" error string must live only in the config
// package, not be duplicated in cmd/dispatch.go.
func TestRunDispatch_NoInlineCrossValidationDuplicate(t *testing.T) {
	src, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatalf("read dispatch.go: %v", err)
	}
	if strings.Contains(string(src), `notify_on_complete agent %q not found in agents.json`) {
		t.Errorf("internal/cmd/dispatch.go still contains the inline notify cross-validation; replace it with config.ValidateDispatchConfig")
	}
	if !strings.Contains(string(src), "config.ValidateDispatchConfig(dispatchCfg, agentsCfg)") {
		t.Errorf("runDispatch should delegate to config.ValidateDispatchConfig(dispatchCfg, agentsCfg)")
	}
}
