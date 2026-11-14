package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

type windowRefusal struct {
	Profile string
	Text    string // full refusal the first time an identity is refused in this process, its one-line Summary after
}

func (r *windowRefusal) Error() string { return r.Text }

// windowGateNoticed holds the identities whose full refusal this process has printed. `af up` refuses
// one agent per loop iteration, so agents 2..N of a divergent identity would otherwise each repeat the
// block; they get the self-contained Summary line instead.
var windowGateNoticed = map[string]bool{}

// resetWindowGateNotices clears the dedupe for a test binary, which runs every case in one process.
// Mirrors resetModelCoverageWarnings.
func resetWindowGateNotices() { windowGateNoticed = map[string]bool{} }

// harnessProbeFailed holds the identities whose launch probe failed in this process, so `af up` of
// seven agents probes once and a version mismatch is not re-probed on every watched launch.
var harnessProbeFailed = map[string]bool{}

func resetHarnessProbeFailures() { harnessProbeFailed = map[string]bool{} }

func windowShortfall(rec windowRecord) bool { return 0 < rec.Applied && rec.Applied < rec.Declared }

func windowGate(root, agentName, profileName string, profile map[string]string, gates launchGates, warn io.Writer) error {
	if config.ForeignModelProfile(profile) {
		return nil
	}
	declared := windowDeclared(profile[config.EnvMaxContextTokens])
	if declared <= 0 {
		return nil
	}
	if gates.HarnessVersion == "" {
		return nil
	}
	id := windowIdentity{Profile: profileName, ModelID: profile["ANTHROPIC_MODEL"], HarnessVersion: gates.HarnessVersion, Declared: declared}
	now := time.Now()
	notice := id.Profile + "|" + id.key()
	rec, ok := readWindowRecord(root, id, now)
	if gates.ReportCoverage && (!ok || rec.Applied == 0) && !harnessProbeFailed[notice] {
		rec, ok = probeWindowAtLaunch(root, id, profile, now, warn)
	}
	if !ok || !windowShortfall(rec) {
		return nil
	}
	text := formatWindowText(root, agentName, rec, rec.Applied)
	first := !windowGateNoticed[notice]
	windowGateNoticed[notice] = true
	shown := text.Summary
	if first {
		shown = text.Refusal
	}
	switch {
	case !gates.ReportCoverage:
		fmt.Fprintf(warn, "warning: %s\n", shown)
		return nil
	case gates.AcceptWindow:
		fmt.Fprintf(warn, "warning: --accept-window: launching agent %q on profile %q despite a recorded window of %d (declared %d)\n", agentName, profileName, rec.Applied, rec.Declared)
		if first {
			fmt.Fprintf(warn, "%s\n", text.Refusal)
		}
		return nil
	}
	mailWindowRefusal(root, agentName, id, rec, now)
	return &windowRefusal{Profile: profileName, Text: shown}
}

func probeWindowAtLaunch(root string, id windowIdentity, profile map[string]string, now time.Time, warn io.Writer) (windowRecord, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), harnessProbeLaunchTimeout)
	defer cancel()
	p, err := probeHarnessWindowFn(ctx, profile)
	if err == nil {
		var rec windowRecord
		rec, err = writeProbeRecord(root, id, p, now)
		if err == nil {
			return rec, true
		}
		// A measured shortfall refuses whether or not it could be stored.
		if !errors.Is(err, errProbeVersionMismatch) {
			fmt.Fprintf(warn, "warning: window record for profile %q could not be written (%v)\n", id.Profile, err)
			return rec, true
		}
	}
	fmt.Fprintf(warn, "warning: window probe for profile %q did not complete (%v); launching without a measurement\n", id.Profile, err)
	harnessProbeFailed[id.Profile+"|"+id.key()] = true
	return windowRecord{}, false
}

// mailWindowRefusal tells the operator once per refused (declared, applied) pair: a refused launch
// writes no launch fact, so no detector run would ever announce a shortfall the probe measured.
func mailWindowRefusal(root, agentName string, id windowIdentity, refused windowRecord, now time.Time) {
	pair := windowPair{Declared: refused.Declared, Applied: refused.Applied}
	send := false
	_ = updateWindowRecord(root, id, now, func(rec *windowRecord, exists bool) bool {
		if !exists || hasWindowPair(rec.NotifiedFor, pair.Declared, pair.Applied) {
			return false
		}
		rec.NotifiedFor = append(rec.NotifiedFor, pair)
		send = true
		return true
	})
	if send {
		sendWindowMail(root, agentName, formatWindowText(root, agentName, refused, refused.Applied))
	}
}

// precheckWindowGate gives the dispatcher path the verdict its launch would take before the --reset
// branch stops a live agent. It reads no .runtime/model_override marker, so the launch-time gate stays
// authoritative.
func precheckWindowGate(root, agentName, cliModel, legacyModel string, warn io.Writer) error {
	detectWindowsFn(root, windowDetectInput{Site: windowAtLaunch, Now: time.Now(), Warn: warn})
	cfg, err := config.LoadModelsConfig(root)
	if err != nil {
		return nil
	}
	name, _, ok, err := config.ResolveModelEnv(cfg, agentName, cliModel, "", legacyModel)
	if err != nil || !ok {
		return nil
	}
	return windowGate(root, agentName, name, cfg.Models[name], launchGates{ReportCoverage: true, HarnessVersion: windowHarnessVersion(root)}, warn)
}
