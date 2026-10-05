package cmd

import (
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// p724cComposerAFLaunchKeys covers one key of every af launch family plus the git config channel and the
// Anthropic API key.
var p724cComposerAFLaunchKeys = []string{
	"GIT_AUTHOR_NAME", "GIT_CONFIG_COUNT", "GIT_CONFIG_PARAMETERS", "AF_COAUTHOR_NAME", "AF_BUILD_HOST",
	config.EnvEffortLevel, "ANTHROPIC_API_KEY",
}

func p724cReservedReport(integration, key string) string {
	return fmt.Sprintf("integration %s: env key %q is reserved for af; not exported", integration, key)
}

// The emitter skips these keys silently, so the composer is where a skipped duplicate gets reported; it
// also keeps them out of the unset universe, or an old record naming the effort level would unset an
// operator's own value on every launch.
func TestPR724_T6_FillIntegrationContributionsReportsAFLaunchKeys(t *testing.T) {
	var env []config.EnvVar
	for _, k := range p724cComposerAFLaunchKeys {
		env = append(env, config.EnvVar{Key: k, Value: "mallory"})
	}
	env = append(env, config.EnvVar{Key: "ACME_TOKEN", Value: "t"})
	var c session.LaunchContributions

	reports := fillIntegrationContributions([]config.IntegrationBinding{{Name: "acme-int", Env: env}},
		append(slices.Clone(p724cComposerAFLaunchKeys), "ACME_TOKEN"), &c)

	var exported []string
	for _, ev := range c.IntegrationEnv {
		exported = append(exported, ev.Key)
	}
	if !slices.Equal(exported, []string{"ACME_TOKEN"}) {
		t.Errorf("exported integration keys = %q, want only ACME_TOKEN", exported)
	}
	for _, k := range p724cComposerAFLaunchKeys {
		if slices.Contains(c.IntegrationKeyUniverse, k) {
			t.Errorf("IntegrationKeyUniverse carries af launch key %s", k)
		}
		if want := p724cReservedReport("acme-int", k); !slices.Contains(reports, want) {
			t.Errorf("no report %q; reports %q", want, reports)
		}
	}
}

// The af-owned report wins over the model-ownership report, so a key gets one text whether or not a
// profile also carries it.
func TestPR724_T6_ComposerReportsAFLaunchKeyEvenWhenAProfileOwnsIt(t *testing.T) {
	c := session.LaunchContributions{ModelKeyUniverse: []string{"ANTHROPIC_API_KEY", config.EnvEffortLevel, "ANTHROPIC_MODEL"}}
	bound := []config.IntegrationBinding{{Name: "acme-int", Env: []config.EnvVar{
		{Key: config.EnvEffortLevel, Value: "low"}, {Key: "ANTHROPIC_API_KEY", Value: "mallory"},
	}}}

	reports := fillIntegrationContributions(bound, nil, &c)

	want := []string{p724cReservedReport("acme-int", config.EnvEffortLevel), p724cReservedReport("acme-int", "ANTHROPIC_API_KEY")}
	if !slices.Equal(reports, want) {
		t.Errorf("reports = %q\nwant      %q", reports, want)
	}
}

// The thread's own scenario on every launch path: an installed integration carrying an af launch key never
// changes what the launched process sees for it, and the launch output names the key.
func TestPR724_T6_InstalledIntegrationAFLaunchKeyNeverReachesProcess(t *testing.T) {
	for key, val := range map[string]string{
		"GIT_AUTHOR_NAME":       "mallory",
		"GIT_CONFIG_COUNT":      "0",
		"GIT_CONFIG_PARAMETERS": "'core.hooksPath'='/evil'",
		"AF_COAUTHOR_NAME":      "mallory",
		"AF_BUILD_HOST":         "evil.example",
		config.EnvEffortLevel:   "low",
		"ANTHROPIC_API_KEY":     "mallory-key",
	} {
		t.Run(key, func(t *testing.T) {
			fx := newK14Fixture(t)
			recordFactoryIntegration(t, fx.root, "fix-ture", key, val)
			fake, _ := setupHermeticSessions(t)

			u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)
			k14AssertParity(t, u.line, s.line, r.line)
			for name, l := range map[string]k14Launch{"up": u, "sling": s, "respawn": r} {
				if l.line == "" {
					t.Errorf("%s: nothing launched (err=%v)\n%s", name, l.err, l.output)
					continue
				}
				if got, ok := fx.exec(t, l.line).env[key]; ok && got == val {
					t.Errorf("%s: the launched process saw %s=%q from the integration manifest", name, key, got)
				}
				// The temp dir path embeds the subtest name, so a bare substring match would always pass.
				if !regexp.MustCompile(`(?m)^warning: .*\b` + key + `\b`).MatchString(l.output) {
					t.Errorf("%s: the af launch key %s was not reported on the launch output:\n%s", name, key, l.output)
				}
			}
		})
	}
}
