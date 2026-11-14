package cmd

import (
	"fmt"
	"strings"
	"testing"
)

const gatewayMasterKeyLine = "master_key: os.environ/LITELLM_MASTER_KEY"

const gatewayNoDBConsequence = "the .github/advisory-exceptions.json entries for GHSA-7hp6-4w63-5g45, GHSA-g5ff-637f-6q2m and GHSA-2rwg-2wh4-7c4f " +
	"assume the LiteLLM gateway has no user/key database; re-evaluate them before the gateway config gains one"

// The register entries for GHSA-7hp6-4w63-5g45, GHSA-g5ff-637f-6q2m and GHSA-2rwg-2wh4-7c4f rest on
// one fact: the gateway has no user or key store, so no non-admin principal can exist. The gateway
// runs either a config quickstart.sh writes or a checked-in one that quickstart.sh left in place,
// so those are where a database would first appear.
func gatewayConfigNoDBViolations(quickstart string) []string {
	var out []string
	if n := strings.Count(strings.ToLower(quickstart), "database_url"); n != 0 {
		out = append(out, fmt.Sprintf("quickstart.sh mentions database_url %d time(s): %s", n, gatewayNoDBConsequence))
	}
	if n := strings.Count(quickstart, gatewayMasterKeyLine); n != 2 {
		out = append(out, fmt.Sprintf("quickstart.sh has %d %q lines, want 2 (the api-key config and subscription_yaml_body): %s", n, gatewayMasterKeyLine, gatewayNoDBConsequence))
	}
	return out
}

func gatewayCheckedInConfigNoDBViolations(path, config string) []string {
	if n := strings.Count(strings.ToLower(config), "database_url"); n != 0 {
		return []string{fmt.Sprintf("%s mentions database_url %d time(s): %s", path, n, gatewayNoDBConsequence)}
	}
	return nil
}

func TestGatewayConfigHasNoUserDatabase(t *testing.T) {
	src := readQuickstartSh(t)
	ids := []string{"GHSA-7hp6-4w63-5g45", "GHSA-g5ff-637f-6q2m", "GHSA-2rwg-2wh4-7c4f"}

	t.Run("each way the evidence can break is caught", func(t *testing.T) {
		withoutOne := strings.Replace(src, gatewayMasterKeyLine, "", 1)
		arms := map[string][]string{
			"yaml database_url":      gatewayConfigNoDBViolations(src + "\n  database_url: os.environ/DATABASE_URL\n"),
			"DATABASE_URL export":    gatewayConfigNoDBViolations(src + "\nDATABASE_URL=postgres://x litellm --config .agentfactory/litellm.yaml\n"),
			"a master_key line lost": gatewayConfigNoDBViolations(withoutOne),
			"a third master_key":     gatewayConfigNoDBViolations(src + "\n" + gatewayMasterKeyLine + "\n"),
		}
		for _, f := range checkedInLitellmFiles {
			path := ".agentfactory/" + f
			v := gatewayCheckedInConfigNoDBViolations(path, readRepoFile(t, path)+"\ngeneral_settings:\n  database_url: os.environ/DATABASE_URL\n")
			if joined := strings.Join(v, "\n"); !strings.Contains(joined, path) {
				t.Errorf("seed database_url in %s: violation missing or does not name the file: %q", path, joined)
			}
			arms["seed database_url in "+path] = v
		}
		for name, v := range arms {
			joined := strings.Join(v, "\n")
			for _, id := range ids {
				if !strings.Contains(joined, id) {
					t.Errorf("%s: violation missing or does not name %s: %q", name, id, joined)
				}
			}
		}
	})

	violations := gatewayConfigNoDBViolations(src)
	for _, f := range checkedInLitellmFiles {
		path := ".agentfactory/" + f
		violations = append(violations, gatewayCheckedInConfigNoDBViolations(path, readRepoFile(t, path))...)
	}
	for _, v := range violations {
		t.Error(v)
	}
}
