package cmd

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// TestUsingAgentfactoryDoc_ImprovementHookReviewFixes pins the PR #516 review-thread
// corrections to USING_AGENTFACTORY.md so the removed content cannot silently return,
// mirroring the existing stale-SSH-forwarding and formula-deletion doc guards.
func TestUsingAgentfactoryDoc_ImprovementHookReviewFixes(t *testing.T) {
	data, err := os.ReadFile("../../USING_AGENTFACTORY.md")
	if err != nil {
		t.Fatalf("reading USING_AGENTFACTORY.md: %v", err)
	}
	content := string(data)

	// The fire condition must not claim the WORK_DONE mail "was delivered": the fire gate
	// in sendWorkDoneAndCleanup never reads mailErr (proven by the characterization test
	// below), and the Edge rules, the improvement CLI help, and the design doc all omit any
	// delivery clause.
	if strings.Contains(content, "was delivered") {
		t.Error(`USING_AGENTFACTORY.md still claims the improvement hook fires only when its WORK_DONE mail "was delivered"; the fire gate never checks mail delivery`)
	}

	// The guide uses the ADR-0xx convention, not bare issue-tracker citations.
	if strings.Contains(content, "(issue #483)") {
		t.Error(`USING_AGENTFACTORY.md still contains the bare "(issue #483)" parenthetical`)
	}
}

// TestDone_ImprovementHook_WorkDoneMailFails_StillFires pins the ground truth behind the
// doc correction above: the improvement hook's fire gate does not depend on WORK_DONE mail
// delivery. With a failing sendWorkDoneMail the marker must still be written — an accidental
// `&& mailErr == nil` added to the fire gate would fail here, and only here.
func TestDone_ImprovementHook_WorkDoneMailFails_StillFires(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
		return fmt.Errorf("WORK_DONE mail delivery failed")
	}
	defer func() { sendWorkDoneMail = origMail }()

	_, stderr := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	m := readMarker(t, improvementPendingFile(root, "alpha"))
	if m.InstanceID != instanceID {
		t.Errorf("marker.instance_id = %q, want %q (hook must fire regardless of WORK_DONE mail delivery)", m.InstanceID, instanceID)
	}
	if !strings.Contains(stderr, "WORK_DONE mail") {
		t.Errorf("stderr must surface the WORK_DONE mail failure, not swallow it:\n%s", stderr)
	}
}
