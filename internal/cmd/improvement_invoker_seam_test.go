package cmd

import (
	"errors"
	"testing"
)

// TestRunImprovement_StatusPath_RefusesNestedCloneCapture (#528) proves runImprovement's
// no-arg status path now resolves its root via resolveInvokerRoot instead of a direct
// config.FindFactoryRoot call. Before the fix, an env-less shell inside a nested clone
// (the #519 K5 scenario) silently resolved the clone; after the fix it must refuse,
// mirroring af quality (quality.go:34), which improvement.go's own doc comment says
// this command mirrors on the file-toggle side.
func TestRunImprovement_StatusPath_RefusesNestedCloneCapture(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	t.Chdir(fx.clone)

	err := runImprovement(improvementCmd, nil)
	if err == nil {
		t.Fatal("expected enclosing refusal from inside the nested clone, got success")
	}
	var enc *enclosingRootError
	if !errors.As(err, &enc) {
		t.Fatalf("expected *enclosingRootError, got %T: %v", err, err)
	}
}

// TestRunImprovement_OnOff_RefusesNestedCloneCapture (#528) is the state-writing twin:
// "af improvement on/off" run from inside a nested clone with no AF_ROOT must refuse
// rather than silently flip the wrong (nested) factory's hook file.
func TestRunImprovement_OnOff_RefusesNestedCloneCapture(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	t.Chdir(fx.clone)

	err := runImprovement(improvementCmd, []string{"on"})
	if err == nil {
		t.Fatal("expected enclosing refusal from inside the nested clone, got success")
	}
	var enc *enclosingRootError
	if !errors.As(err, &enc) {
		t.Fatalf("expected *enclosingRootError, got %T: %v", err, err)
	}
}

// TestRunImprovementComplete_RefusesNestedCloneCapture (#528) proves runImprovementComplete
// (inherently state-writing: it validates, mails a verdict, and tears down) now routes
// through resolveInvokerRoot instead of a direct config.FindFactoryRoot call.
func TestRunImprovementComplete_RefusesNestedCloneCapture(t *testing.T) {
	fx := buildNestedFactoryFixture(t)
	t.Chdir(fx.clone)
	t.Cleanup(func() {
		_ = improvementCompleteCmd.Flags().Set("reap", "false")
		_ = improvementCompleteCmd.Flags().Set("dir", "")
	})

	err := runImprovementComplete(improvementCompleteCmd, nil)
	if err == nil {
		t.Fatal("expected enclosing refusal from inside the nested clone, got success")
	}
	var enc *enclosingRootError
	if !errors.As(err, &enc) {
		t.Fatalf("expected *enclosingRootError, got %T: %v", err, err)
	}
}
