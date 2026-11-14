package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/session"
)

// launchReports carries what a launch site echoes or persists about the composition, as opposed
// to what the launch line carries.
type launchReports struct {
	ModelName           string
	Integrations        []string
	DroppedIntegrations []string
	IntegrationMails    []integrationMail
	WindowFact          *windowLaunchFact
}

type launchGates struct {
	SkipFitness    bool
	AcceptWindow   bool
	ReportCoverage bool   // a fresh launch the operator is watching; false on a respawn
	HarnessVersion string // set by launchContributions from windowHarnessVersion(root); the resolver hands it to windowGate and reads no version itself
}

// mailIntegrations sends the composer's reports; a launch site calls it only once its launch has happened,
// so a launch that failed announces nothing about bindings it never ran with.
func (r launchReports) mailIntegrations(root string, warn io.Writer) {
	for _, m := range r.IntegrationMails {
		if err := reportIntegration(root, m.name, m.condition, m.subject, m.body); err != nil {
			fmt.Fprintf(warn, "warning: %v\n", err)
		}
	}
}

// launchContributions is the one producer of config-derived launch values. af up, af sling and
// every recycle call it, so a value wired here reaches every launch path at once (#715: a value
// wired per site had to be remembered at each of them, and was not).
//
// A respawn passes cliModel "" and gates.ReportCoverage false: it re-resolves the full precedence chain
// (the --model marker AND a durable models.json.agents default must both survive a recycle), and
// a broken models.json warns and falls through instead of refusing the recycle.
func launchContributions(ctx context.Context, root, agentName, agentDir string, entry config.AgentEntry,
	cliModel string, gates launchGates, warn io.Writer) (session.LaunchContributions, launchReports, error) {
	var c session.LaunchContributions
	detectWindowsFn(root, windowDetectInput{Site: windowAtLaunch, Now: time.Now(), Warn: warn})
	version := windowHarnessVersion(root)
	gates.HarnessVersion = version
	modelName, env, err := resolveModelEnvForSession(root, agentName, agentDir, cliModel, entry.Model, gates, warn)
	if err != nil {
		return c, launchReports{}, err
	}
	if len(env) > 0 {
		nextStep, formula := nextReadyStep(ctx, root, agentDir)
		c.ModelEnv = withEffortLevel(root, agentDir, env, nextStep, formula)
	}
	fact := buildWindowLaunchFact(root, modelName, env, version)
	if fact != nil {
		fact.AcceptWindow = windowLaunchAccepted(root, agentName, *fact, gates)
	}
	if fact == nil && version == "" && !windowPass.versionWarned {
		if profile, ok := windowProfileOf(root, modelName); ok && !config.ForeignModelProfile(profile) && windowDeclared(profile[config.EnvMaxContextTokens]) > 0 {
			windowPass.versionWarned = true
			fmt.Fprintf(warn, "warning: claude --version returned nothing; no window launch fact recorded for profile %q, so the window gate is skipped\n", modelName)
		}
	}
	// Wired whether or not a profile resolved: a launch that resolves none is exactly the case
	// that must still clear the keys a previous profile left on a reused session (#602).
	c.ModelKeyUniverse = launchModelKeyUniverse(root)
	c.TelemetryEnv = telemetryLaunchEnv(root, agentDir, agentName, modelName, warn)
	c.GitAuthorName, c.GitAuthorEmail, c.GitHooksDir, c.CoauthorName, c.CoauthorEmail = wireGitIdentity(root, agentDir)
	bh, err := config.LoadBuildHostConfig(config.BuildHostConfigPath(root))
	if err != nil {
		fmt.Fprintf(warn, "warning: ignoring build-host.json (%v); launching %s without build-host env\n", err, agentName)
	} else {
		c.BuildHost = bh
	}
	reports, dropped, mails := composeIntegrations(root, agentDir, entry, &c)
	for _, r := range reports {
		fmt.Fprintf(warn, "warning: %s: %s\n", agentName, r)
	}
	return c, launchReports{ModelName: modelName, Integrations: reports, DroppedIntegrations: dropped, IntegrationMails: mails, WindowFact: fact}, nil
}

// windowLaunchAccepted records an acceptance, not the flag: a blanket af up --accept-window marks only
// the launches it let through a recorded shortfall. A respawn carries no flag, so it keeps the previous
// launch's acceptance while the identity is unchanged.
func windowLaunchAccepted(root, agent string, fact windowLaunchFact, gates launchGates) bool {
	if gates.ReportCoverage {
		if !gates.AcceptWindow {
			return false
		}
		rec, ok := readWindowRecord(root, fact.identity(), time.Now())
		return ok && windowShortfall(rec)
	}
	prev, ok := readWindowLaunchFact(root, agent)
	return ok && prev.AcceptWindow && prev.Profile == fact.Profile && prev.identity().key() == fact.identity().key()
}

func windowProfileOf(root, modelName string) (map[string]string, bool) {
	if modelName == "" {
		return nil, false
	}
	cfg, err := config.LoadModelsConfig(root)
	if err != nil {
		return nil, false
	}
	profile, ok := cfg.Models[modelName]
	return profile, ok
}

// buildWindowLaunchFact leaves launched_at unset: persistWindowLaunchFact stamps it right before the
// start, so the replaced process has the shortest possible window to bind an observation to it.
func buildWindowLaunchFact(root, modelName string, env []config.EnvVar, version string) *windowLaunchFact {
	if version == "" {
		return nil
	}
	profile, ok := windowProfileOf(root, modelName)
	if !ok {
		return nil
	}
	f := &windowLaunchFact{
		Profile:        modelName,
		ModelID:        modelEnvValue(env, "ANTHROPIC_MODEL"),
		Declared:       windowDeclared(modelEnvValue(env, config.EnvMaxContextTokens)),
		HarnessVersion: version,
	}
	if config.ForeignModelProfile(profile) {
		f.Class = "foreign"
	}
	return f
}

// persistWindowLaunchFact removes the previous fact before writing, so a failed write leaves no
// older fact standing in for this launch. Its undo restores what was there only while the file
// still holds what this call left (the releaseReclaimGuard content check), so a concurrent launch's
// fact is never overwritten.
func (r launchReports) persistWindowLaunchFact(root, agent string, warn io.Writer) (undo func()) {
	path, ok := windowLaunchFactPath(root, agent)
	if !ok {
		return func() {}
	}
	prev, _, hadPrev := readWindowFile(path, windowFileMaxBytes)
	_ = os.Remove(path)
	var mine []byte
	if r.WindowFact != nil {
		fact := *r.WindowFact
		fact.LaunchedAt = time.Now()
		data, err := json.Marshal(fact)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(path), 0o755)
		}
		if err == nil {
			err = fsutil.WriteFileAtomic(path, data, 0o644)
		}
		if err != nil {
			fmt.Fprintf(warn, "warning: recording the window launch fact of %s: %v\n", agent, err)
		} else {
			mine = data
		}
	}
	return func() {
		if mine == nil {
			if _, err := os.Lstat(path); err == nil {
				return
			}
		} else if cur, _, present := readWindowFile(path, windowFileMaxBytes); !present || !bytes.Equal(cur, mine) {
			return
		}
		if hadPrev {
			_ = fsutil.WriteFileAtomic(path, prev, 0o644)
		} else {
			_ = os.Remove(path)
		}
	}
}

// startWithWindowLaunchFact is the only way a launch site reaches its start, so none can persist a
// fact without the undo: the fact must be on disk before the session it describes can report, and a
// start that fails because the session is already running must leave that session's fact as it was.
func (r launchReports) startWithWindowLaunchFact(root, agent string, persist bool, warn io.Writer, start func() error) error {
	if !persist {
		return start()
	}
	undo := r.persistWindowLaunchFact(root, agent, warn)
	err := start()
	if err != nil {
		undo()
	}
	return err
}
