package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

func TestMergepatrolFormula_LabelBasedDiscovery(t *testing.T) {
	f, err := formula.ParseFile("install_formulas/mergepatrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	var queueScan *formula.Step
	for i := range f.Steps {
		if f.Steps[i].ID == "queue-scan" {
			queueScan = &f.Steps[i]
			break
		}
	}
	if queueScan == nil {
		t.Fatal("queue-scan step not found in mergepatrol formula")
	}

	if !strings.Contains(queueScan.Description, "gh pr list") ||
		!strings.Contains(queueScan.Description, "merge_ready") {
		t.Error("queue-scan step must contain label-based PR discovery using 'gh pr list' with 'merge_ready' label")
	}
}

func TestMergepatrolFormula_LabelDeduplication(t *testing.T) {
	f, err := formula.ParseFile("install_formulas/mergepatrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	var queueScan *formula.Step
	for i := range f.Steps {
		if f.Steps[i].ID == "queue-scan" {
			queueScan = &f.Steps[i]
			break
		}
	}
	if queueScan == nil {
		t.Fatal("queue-scan step not found in mergepatrol formula")
	}

	desc := strings.ToLower(queueScan.Description)
	if !strings.Contains(desc, "dedup") && !strings.Contains(desc, "deduplicate") && !strings.Contains(desc, "already") {
		t.Error("queue-scan step must mention deduplication between mail-sourced and label-sourced items")
	}
}

func TestMergepatrolFormula_LabelRemovalAfterMerge(t *testing.T) {
	f, err := formula.ParseFile("install_formulas/mergepatrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	var mergePush *formula.Step
	for i := range f.Steps {
		if f.Steps[i].ID == "merge-push" {
			mergePush = &f.Steps[i]
			break
		}
	}
	if mergePush == nil {
		t.Fatal("merge-push step not found in mergepatrol formula")
	}

	if !strings.Contains(mergePush.Description, "remove-label") ||
		!strings.Contains(mergePush.Description, "merge_ready") {
		t.Error("merge-push step must remove the merge_ready label after successful merge")
	}
}

func TestMergepatrolFormula_BothDiscoveryPathsCoexist(t *testing.T) {
	f, err := formula.ParseFile("install_formulas/mergepatrol.formula.toml")
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}

	var queueScan *formula.Step
	for i := range f.Steps {
		if f.Steps[i].ID == "queue-scan" {
			queueScan = &f.Steps[i]
			break
		}
	}
	if queueScan == nil {
		t.Fatal("queue-scan step not found in mergepatrol formula")
	}

	if !strings.Contains(queueScan.Description, "MERGE_READY") {
		t.Error("queue-scan step must still reference MERGE_READY mail path (coexistence)")
	}
	if !strings.Contains(queueScan.Description, "merge_ready") {
		t.Error("queue-scan step must reference merge_ready label path (coexistence)")
	}
}

func TestMergepatrolTemplate_LabelDiscoveryDocumented(t *testing.T) {
	tmpl, err := os.ReadFile("../templates/roles/mergepatrol.md.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	content := string(tmpl)
	if !strings.Contains(content, "merge_ready") {
		t.Error("mergepatrol role template must document label-based discovery (merge_ready label)")
	}
	if !strings.Contains(content, "Label") || !strings.Contains(content, "label") {
		t.Error("mergepatrol role template must mention label-based PR discovery path")
	}
}
