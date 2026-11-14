package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const advisoryLoopsDoc = "USING_ADVISORYLOOPS.md"

var guideCitationRe = regexp.MustCompile("`?(USING_[A-Z_]+\\.md)`?,?\\s+\"([^\"]+)\"")

type guideCitation struct{ file, heading string }

func guideCitations(doc string) []guideCitation {
	var cites []guideCitation
	for _, m := range guideCitationRe.FindAllStringSubmatch(strings.Join(strings.Fields(doc), " "), -1) {
		cites = append(cites, guideCitation{file: m[1], heading: m[2]})
	}
	return cites
}

func guideHasHeading(t *testing.T, file, heading string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), file))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	inFence := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if !inFence && isATXHeading(line) && strings.TrimSpace(strings.TrimLeft(line, "#")) == heading {
			return true
		}
	}
	return false
}

func TestAdvisoryLoopsGuide(t *testing.T) {
	doc := g4ReadRepoDoc(t, advisoryLoopsDoc)

	t.Run("carries the healing loop under the heading its citations name", func(t *testing.T) {
		if !regexp.MustCompile(`(?m)^## Advisory healing loop$`).MatchString(doc) {
			t.Errorf("%s has no `## Advisory healing loop` heading", advisoryLoopsDoc)
		}
	})

	t.Run("names no particular agentfactory repository", func(t *testing.T) {
		flat := strings.ToLower(strings.Join(strings.Fields(doc), " "))
		for _, banned := range []string{"stempeck/", "agentfactory-pro", "public repository"} {
			if strings.Contains(flat, banned) {
				t.Errorf("%s says %q: agentfactory is agentfactory, whichever repository it comes from", advisoryLoopsDoc, banned)
			}
		}
	})

	t.Run("Stalls says a pull request closed after completion is not seen again", func(t *testing.T) {
		stalls := regexp.MustCompile(`(?s)\*\*Stalls\.\*\*.*?(\n\n|$)`).FindString(doc)
		if stalls == "" {
			t.Fatalf("%s has no **Stalls.** paragraph", advisoryLoopsDoc)
		}
		flat := strings.Join(strings.Fields(stalls), " ")
		if !strings.Contains(flat, "closed after `WORKFLOW_COMPLETE` is not seen again") {
			t.Errorf("the Stalls paragraph promises a stall for every closed pull request; one closed after `WORKFLOW_COMPLETE` never stalls:\n%s", stalls)
		}
		if !regexp.MustCompile(`(?i)\bclos(e|ing) (the|its) issue\b`).MatchString(flat) {
			t.Errorf("the Stalls paragraph does not say closing the issue is what files a fresh one:\n%s", stalls)
		}
	})
}

func TestTrustBoundariesGuideCitationsResolve(t *testing.T) {
	const path = "docs/architecture/trust-boundaries.md"
	cites := guideCitations(dcReadRepoFile(t, path))
	if len(cites) == 0 {
		t.Fatalf("%s cites no guide heading, so a renamed or moved heading would go unnoticed", path)
	}
	for _, c := range cites {
		if !guideHasHeading(t, c.file, c.heading) {
			t.Errorf("%s cites %s %q, which carries no such heading", path, c.file, c.heading)
		}
	}
}
