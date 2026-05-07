package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestQuickstartSyncsBeforeBuild(t *testing.T) {
	data, err := os.ReadFile("../../quickstart.sh")
	if err != nil {
		t.Fatalf("read quickstart.sh: %v", err)
	}
	body := string(data)

	syncIdx := strings.Index(body, "make sync-formulas")
	if syncIdx == -1 {
		t.Fatal("quickstart.sh does not contain 'make sync-formulas'")
	}

	buildIdx := strings.Index(body, "make build")
	if buildIdx == -1 {
		t.Fatal("quickstart.sh does not contain 'make build'")
	}

	if syncIdx >= buildIdx {
		t.Errorf("quickstart.sh: 'make sync-formulas' (offset %d) must appear before 'make build' (offset %d)", syncIdx, buildIdx)
	}
}

func TestAgentGenAllSyncsBeforeLoop(t *testing.T) {
	data, err := os.ReadFile("../../agent-gen-all.sh")
	if err != nil {
		t.Fatalf("read agent-gen-all.sh: %v", err)
	}
	body := string(data)

	syncIdx := strings.Index(body, "syncing formulas from source")
	if syncIdx == -1 {
		t.Fatal("agent-gen-all.sh does not contain a formula sync block ('syncing formulas from source')")
	}

	regenIdx := strings.Index(body, "af formula agent-gen")
	if regenIdx == -1 {
		t.Fatal("agent-gen-all.sh does not contain the formula regeneration call ('af formula agent-gen')")
	}

	if syncIdx >= regenIdx {
		t.Errorf("agent-gen-all.sh: sync block (offset %d) must appear before regeneration (offset %d)", syncIdx, regenIdx)
	}

	if !strings.Contains(body, "removed orphan:") {
		t.Error("agent-gen-all.sh sync block does not remove orphan formulas (missing orphan removal loop)")
	}
}

func TestSyncFormulasIncrementalCopy(t *testing.T) {
	data, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	body := string(data)

	targetIdx := strings.Index(body, "sync-formulas:")
	if targetIdx == -1 {
		t.Fatal("Makefile does not contain 'sync-formulas:' target")
	}

	targetBody := body[targetIdx:]
	nextTarget := strings.Index(targetBody[1:], "\n\n")
	if nextTarget > 0 {
		targetBody = targetBody[:nextTarget+1]
	}

	if !strings.Contains(targetBody, "basename") {
		t.Error("sync-formulas target does not use incremental per-file copy (missing 'basename')")
	}
}
