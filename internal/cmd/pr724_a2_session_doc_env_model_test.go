//go:build !integration

package cmd

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const p724a2SessionDoc = "docs/architecture/subsystems/session.md"

func p724a2SessionDocFlat(t *testing.T) string {
	t.Helper()
	return p724a2Flat(repoFile(t, p724a2SessionDoc))
}

// Config-derived env is exported on the launch line only; tmux carries just the identity vars.
func TestPR724_BODY1_SessionDocDropsAllEnvWritesTwice(t *testing.T) {
	const stale = "All env writes happen twice"
	if strings.Contains(p724a2SessionDocFlat(t), stale) {
		t.Errorf("%s still claims %q; since the tmux twin was deleted, config-derived env is written once, on the launch line", p724a2SessionDoc, stale)
	}
}

func TestPR724_BODY1_SessionDocDropsFourOrSixVarCount(t *testing.T) {
	const stale = "four (or six)"
	if strings.Contains(p724a2SessionDocFlat(t), stale) {
		t.Errorf("%s still says Start sets %q vars; it writes three identity vars to tmux, five with a worktree", p724a2SessionDoc, stale)
	}
}

func TestPR724_BODY1_SessionDocSaysConfigEnvNeverWrittenToTmux(t *testing.T) {
	const want = "never written to tmux"
	if !strings.Contains(strings.ToLower(p724a2SessionDocFlat(t)), want) {
		t.Errorf("%s does not state that config-derived env is %s (launch line only)", p724a2SessionDoc, want)
	}
}

func TestPR724_BODY1_SessionDocNamesAFActor(t *testing.T) {
	if !strings.Contains(p724a2SessionDocFlat(t), "`AF_ACTOR`") {
		t.Errorf("%s does not name `AF_ACTOR`, one of the identity vars session.Manager writes", p724a2SessionDoc)
	}
}

func TestPR724_BODY1_SessionDocDropsAllFourVarsWrittenTwice(t *testing.T) {
	const stale = "All four env vars are written twice"
	if strings.Contains(p724a2SessionDocFlat(t), stale) {
		t.Errorf("%s still says %q; Start writes three identity vars (five with a worktree) twice and config env once", p724a2SessionDoc, stale)
	}
}

func TestPR724_BODY1_SessionDocFactoryRootSeamNamesAFActor(t *testing.T) {
	const marker = "Factory-root env contract"
	for _, line := range strings.Split(repoFile(t, p724a2SessionDoc), "\n") {
		if strings.Contains(line, marker) {
			if !strings.Contains(line, "`AF_ACTOR`") {
				t.Errorf("%s %q seam does not name `AF_ACTOR` among the identity vars session.Manager writes: %q", p724a2SessionDoc, marker, line)
			}
			return
		}
	}
	t.Errorf("%s has no %q seam row", p724a2SessionDoc, marker)
}

var p724a2SessionAnchor = regexp.MustCompile(`session\.go:(\d+)`)

// Before the one-emitter rewrite (6cda6a56) Start wrote env through these session.go blocks: tmux
// SetEnvironment, inline exports, shellQuote. No line of the current file sits there, so a page line
// citing them still describes the deleted tmux twin.
var p724a2PreOneEmitterEnvBlocks = [][2]int{{114, 122}, {158, 164}, {176, 178}}

func TestPR724_BODY1_SessionDocCitesNoPreOneEmitterEnvAnchors(t *testing.T) {
	for i, line := range strings.Split(repoFile(t, p724a2SessionDoc), "\n") {
		var stale []int
		for _, m := range p724a2SessionAnchor.FindAllStringSubmatch(line, -1) {
			n, _ := strconv.Atoi(m[1])
			for _, b := range p724a2PreOneEmitterEnvBlocks {
				if b[0] <= n && n <= b[1] {
					stale = append(stale, n)
				}
			}
		}
		if stale != nil {
			t.Errorf("%s:%d cites pre-one-emitter env-write anchors session.go:%v", p724a2SessionDoc, i+1, stale)
		}
	}
}
