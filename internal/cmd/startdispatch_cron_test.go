package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A dispatch.json carrying crons and an unparseable `every` is MISCONFIGURED, not unconfigured.
// This is the end-to-end half of the issue #610 error-class contract (design-doc.md:249,
// cross-review CRITICAL-1 item iii): TestDispatchCron_Rejections asserts the cause (no cron error
// wraps ErrMissingField); this asserts the consequence at the only site that reads that sentinel.
//
// If validateCrons ever regains the ErrMissingField wrapping that its sibling validateWorkflows uses
// at dispatch.go:214, one bad schedule would print "dispatch.json not configured" and silently kill
// the whole dispatcher — items and crons both. Mirrors TestStartDispatch_InvalidTypeWarns.
func TestStartDispatch_CronValidationErrorWarns(t *testing.T) {
	root := t.TempDir()
	writeDispatchJSON(t, root, `{"crons":[{"name":"weekly-pm","agent":"product-manager","every":"1w"}]}`)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := startDispatch(cmd, root, fake); err != nil {
		t.Fatalf("a bad cron schedule must not abort af up, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("an invalid cron config must not launch a session; ops=%v", fake.ops)
	}
	combined := out.String() + errBuf.String()
	if !strings.Contains(combined, "warning") {
		t.Errorf("a cron validation error must surface a warning, got %q", combined)
	}
	if strings.Contains(combined, "not configured") {
		t.Errorf("a cron validation error must NOT be reported as the friendly 'not configured' skip "+
			"— that would silently kill the whole dispatcher on one bad schedule; got %q", combined)
	}
	if !strings.Contains(combined, "1w") {
		t.Errorf("the warning must name the offending value so the operator can fix it, got %q", combined)
	}
}

// The relaxation's positive consequence: a crons-only factory is CONFIGURED, so af up must actually
// launch the dispatcher rather than friendly-skipping it. Without this, the whole feature is
// unreachable no matter how well the config validates.
func TestStartDispatch_CronsOnlyConfigLaunches(t *testing.T) {
	root := t.TempDir()
	writeDispatchJSON(t, root, `{"crons":[{"name":"patrol-wake","agent":"financial-patrol","every":"4h"}]}`)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := startDispatch(cmd, root, fake); err != nil {
		t.Fatalf("a crons-only config must not error at af up, got: %v", err)
	}
	combined := out.String() + errBuf.String()
	if strings.Contains(combined, "not configured") {
		t.Errorf("a crons-only factory IS configured and must not be friendly-skipped; got %q", combined)
	}
	if !opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("a crons-only config must launch the dispatcher session; ops=%v", fake.ops)
	}
}
