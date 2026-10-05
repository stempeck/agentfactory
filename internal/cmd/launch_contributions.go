package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// launchReports carries what a launch site echoes or persists about the composition, as opposed
// to what the launch line carries.
type launchReports struct {
	ModelName           string
	Integrations        []string
	DroppedIntegrations []string
	IntegrationMails    []integrationMail
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
// A respawn passes cliModel "" and reportCoverage false: it re-resolves the full precedence chain
// (the --model marker AND a durable models.json.agents default must both survive a recycle), and
// a broken models.json warns and falls through instead of refusing the recycle.
func launchContributions(ctx context.Context, root, agentName, agentDir string, entry config.AgentEntry,
	cliModel string, skipFitness, reportCoverage bool, warn io.Writer) (session.LaunchContributions, launchReports, error) {
	var c session.LaunchContributions
	modelName, env, err := resolveModelEnvForSession(root, agentName, agentDir, cliModel, entry.Model, skipFitness, reportCoverage, warn)
	if err != nil {
		return c, launchReports{}, err
	}
	if len(env) > 0 {
		nextStep, formula := nextReadyStep(ctx, root, agentDir)
		c.ModelEnv = withEffortLevel(root, agentDir, env, nextStep, formula)
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
	return c, launchReports{ModelName: modelName, Integrations: reports, DroppedIntegrations: dropped, IntegrationMails: mails}, nil
}
