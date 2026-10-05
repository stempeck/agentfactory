//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/templates"
)

// ---- PR #539 increment: list classification, T15, respawn guard -------------

// g5ListRows runs `af plugin list --json` and indexes every formula row by its file.
func g5ListRows(t *testing.T) (map[string]pluginListFormulaJSON, string) {
	t.Helper()
	out, err := runPlugin(t, "list", map[string]string{"json": "true"})
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}
	var got []pluginListJSON
	if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); e != nil {
		t.Fatalf("list --json is not an array (%v):\n%s", e, out)
	}
	rows := map[string]pluginListFormulaJSON{}
	for _, p := range got {
		for _, f := range p.Formulas {
			rows[f.File] = f
		}
	}
	return rows, out
}

// PR #539 T3: an embedded built-in identity that no agents.json entry and no plugin record claims
// is refused — including through the unrecorded-identical-store-copy (H1 heal) exit.
func TestPluginListRefusesEmbeddedIdentitySquat(t *testing.T) {
	for _, stem := range []string{"manager", "supervisor"} {
		t.Run(stem, func(t *testing.T) {
			dir := setupFactoryDir(t)
			writeAgentsJSON(t, dir, `{"agents":{}}`)
			writePluginFixture(t, dir, "acme", map[string]string{stem + ".formula.toml": validPluginFormula(stem)})
			g2EnterFactory(t, dir)
			rows, out := g5ListRows(t)
			if got := rows[stem+".formula.toml"].Status; got != "collides-with-embedded-agent" {
				t.Errorf("%s squat status = %q, want collides-with-embedded-agent:\n%s", stem, got, out)
			}
		})
	}
	for _, stem := range []string{"manager", "supervisor"} {
		t.Run(stem+"_unrecorded_identical_store_copy", func(t *testing.T) {
			dir := setupFactoryDir(t)
			writeAgentsJSON(t, dir, `{"agents":{}}`)
			g2StoreCleanFormula(t, dir, stem)
			writePluginFixture(t, dir, "acme", map[string]string{stem + ".formula.toml": validPluginFormula(stem)})
			g2EnterFactory(t, dir)
			rows, out := g5ListRows(t)
			if got := rows[stem+".formula.toml"].Status; got != "collides-with-embedded-agent" {
				t.Errorf("%s via an unrecorded identical store copy = %q, want collides-with-embedded-agent:\n%s", stem, got, out)
			}
		})
	}
}

// PR #539 T3 (blind review 1): factories on one host share one af binary, so a plugin a sibling
// factory installed is embedded here without being recorded or registered here — it must stay
// installable. A shipped role with shippedStems cleared is exactly that state, with a real embed.
func TestPluginListAdmitsSiblingFactoryEmbeddedStem(t *testing.T) {
	const stem = "investigate"
	if !templates.New().HasRole(stem) {
		t.Fatalf("precondition: %s must be embedded in this binary", stem)
	}
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{}}`)
	writePluginFixture(t, dir, "acme", map[string]string{stem + ".formula.toml": validPluginFormula(stem)})
	u, err := buildCollisionUniverse(dir)
	if err != nil {
		t.Fatal(err)
	}
	delete(u.shippedStems, stem)
	pi, err := enumeratePlugin(dir, "acme", u)
	if err != nil {
		t.Fatal(err)
	}
	if got := pi.Formulas[0]; got.Status != pluginStatusOK {
		t.Errorf("embedded-elsewhere stem %s = %q (%s), want ok", stem, got.Status, got.Detail)
	}
}

// PR #539 T3: builtinIdentities is the set agent-gen-all.sh's orphan pass skips, and each one is a
// role this binary embeds without shipping a formula for it.
func TestBuiltinIdentitiesParity(t *testing.T) {
	script, err := os.ReadFile(filepath.Join(findModuleRoot(t), "agent-gen-all.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`case "\$tmpl_name" in ([a-z|-]+)\) continue`).FindSubmatch(script)
	if m == nil {
		t.Fatal("agent-gen-all.sh has no built-in template skip line; the scan matches nothing, so it guards nothing")
	}
	skipped := strings.Split(string(m[1]), "|")
	slices.Sort(skipped)
	if got := slices.Sorted(maps.Keys(builtinIdentities)); !slices.Equal(got, skipped) {
		t.Errorf("builtinIdentities = %v, agent-gen-all.sh skips %v", got, skipped)
	}
	tmpl := templates.New()
	for stem := range builtinIdentities {
		if !tmpl.HasRole(stem) {
			t.Errorf("built-in identity %q is not embedded", stem)
		}
		if _, err := formulasFS.ReadFile("install_formulas/" + stem + ".formula.toml"); err == nil {
			t.Errorf("built-in identity %q ships a formula, so the shipped-formula class already owns it", stem)
		}
	}
}

// PR #539 T3 protective: every installed plugin agent is embedded after the rebuild, so the
// embedded-identity class must never refuse a plugin's re-install of its own recorded stem.
func TestPluginListOwnEmbeddedStemStaysOK(t *testing.T) {
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{"supervisor":{"type":"autonomous","description":"x","formula":"supervisor"}}}`)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"supervisor": g2StoreCleanFormula(t, dir, "supervisor")}})
	writePluginFixture(t, dir, "acme", map[string]string{"supervisor.formula.toml": validPluginFormula("supervisor")})
	g2EnterFactory(t, dir)
	rows, out := g5ListRows(t)
	if got := rows["supervisor.formula.toml"].Status; got != pluginStatusOK {
		t.Errorf("own recorded embedded stem = %q, want ok:\n%s", got, out)
	}
}

// PR #539 T4: an owned stem whose incoming bytes differ from the recorded hash is `changed`, with the
// old and new sha256 in its detail — whether or not the store copy is present.
func TestPluginListReportsChangedFormula(t *testing.T) {
	incoming := "# upstream change\n" + validPluginFormula("acme-triage")
	newSHA := hashHex([]byte(incoming))
	for name, storePresent := range map[string]bool{"store_copy_present": true, "store_copy_absent": false} {
		t.Run(name, func(t *testing.T) {
			dir := setupFactoryDir(t)
			oldSHA := hashHex([]byte(validPluginFormula("acme-triage")))
			if storePresent {
				g2StoreCleanFormula(t, dir, "acme-triage")
			}
			g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"acme-triage": oldSHA}})
			writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": incoming})
			g2EnterFactory(t, dir)
			rows, out := g5ListRows(t)
			row := rows["acme-triage.formula.toml"]
			if row.Status != "changed" {
				t.Errorf("status = %q, want changed:\n%s", row.Status, out)
			}
			if !strings.Contains(row.Detail, oldSHA) || !strings.Contains(row.Detail, newSHA) {
				t.Errorf("detail must carry the old (%s) and new (%s) sha256; got %q", oldSHA, newSHA, row.Detail)
			}
		})
	}
}

// PR #539 T4 protective: an owned stem whose bytes match the record stays ok.
func TestPluginListUnchangedOwnedIsOK(t *testing.T) {
	dir := setupFactoryDir(t)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage")}})
	writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": validPluginFormula("acme-triage")})
	g2EnterFactory(t, dir)
	rows, out := g5ListRows(t)
	if got := rows["acme-triage.formula.toml"].Status; got != pluginStatusOK {
		t.Errorf("unchanged owned stem = %q, want ok:\n%s", got, out)
	}
}

// PR #539 T4/T9: a recorded stem the clone no longer ships is listed `removed`, with the
// agent-gen --delete remediation and no sha256 (there are no incoming bytes).
func TestPluginListReportsRemovedFormula(t *testing.T) {
	dir := setupFactoryDir(t)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {
		"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage"),
		"acme-old":    g2StoreCleanFormula(t, dir, "acme-old"),
	}})
	writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": validPluginFormula("acme-triage")})
	g2EnterFactory(t, dir)
	rows, out := g5ListRows(t)
	row, ok := rows["acme-old.formula.toml"]
	if !ok {
		t.Fatalf("no row for the recorded-but-dropped acme-old:\n%s", out)
	}
	if row.Status != "removed" || row.Agent != "acme-old" || row.SHA256 != "" {
		t.Errorf("dropped stem row = %+v, want status removed, agent acme-old, no sha256", row)
	}
	if !strings.Contains(row.Detail, "af formula agent-gen acme-old --delete") {
		t.Errorf("removed detail must carry the remediation; got %q", row.Detail)
	}
	if got := rows["acme-triage.formula.toml"].Status; got != pluginStatusOK {
		t.Errorf("the surviving stem = %q, want ok", got)
	}
	human, err := runPlugin(t, "list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(human, "[removed]") {
		t.Errorf("human list must show the [removed] row:\n%s", human)
	}
}

// PR #539 T6: a formula over the size cap is a parse-error naming the cap, and install refuses it.
func TestPluginListSizeCapIsParseError(t *testing.T) {
	dir := setupFactoryDir(t)
	big := validPluginFormula("acme-big") + "# " + strings.Repeat("x", maxPluginFormulaBytes) + "\n"
	writePluginFixture(t, dir, "acme", map[string]string{"acme-big.formula.toml": big})
	stubInstallPipeline(t)
	g3OKVerify(t)
	g2EnterFactory(t, dir)
	rows, out := g5ListRows(t)
	row := rows["acme-big.formula.toml"]
	if row.Status != pluginStatusParseError {
		t.Errorf("oversized formula status = %q, want parse-error:\n%s", row.Status, out)
	}
	if want := fmt.Sprintf("exceeds cap %d", maxPluginFormulaBytes); !strings.Contains(row.Detail, want) {
		t.Errorf("detail must name the cap (%q); got %q", want, row.Detail)
	}
	if _, err := runPlugin(t, "install", nil, "acme"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("install must refuse the oversized formula, got: %v", err)
	}
}

// PR #539 T6: a nested formula is listed as an out-of-contract row, not silently dropped.
func TestPluginListReportsNestedOutOfContract(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		"nested/deep.formula.toml": validPluginFormula("deep"),
	})
	g2EnterFactory(t, dir)
	rows, out := g5ListRows(t)
	row, ok := rows[filepath.Join("nested", "deep.formula.toml")]
	if !ok {
		t.Fatalf("no row for nested/deep.formula.toml:\n%s", out)
	}
	if row.Status != pluginStatusOutOfContract {
		t.Errorf("nested row status = %q, want out-of-contract", row.Status)
	}
}

// PR #539 T7: a `formula` field that differs from the file stem is `name-mismatch`, naming both.
func TestPluginListFormulaFieldMismatchesStem(t *testing.T) {
	dir := setupFactoryDir(t)
	fields := map[string]string{
		"mismatch.formula.toml":   "investigate",
		"acme-scout.formula.toml": "acme-triage",
	}
	files := map[string]string{}
	for file, field := range fields {
		files[file] = validPluginFormula(field)
	}
	writePluginFixture(t, dir, "acme", files)
	g2EnterFactory(t, dir)
	rows, out := g5ListRows(t)
	for file, field := range fields {
		row := rows[file]
		stem := strings.TrimSuffix(file, ".formula.toml")
		if row.Status != "name-mismatch" {
			t.Errorf("%s status = %q, want name-mismatch:\n%s", file, row.Status, out)
		}
		if !strings.Contains(row.Detail, field) || !strings.Contains(row.Detail, stem) {
			t.Errorf("%s detail must name the field %q and the stem %q; got %q", file, field, stem, row.Detail)
		}
	}
}

// PR #539 T15: a plugin with no top-level formula is refused before anything is staged or recorded —
// including a recorded plugin whose clone dropped every top-level formula.
func TestPluginInstallRefusesNoTopLevelFormula(t *testing.T) {
	for name, recorded := range map[string]bool{"nested_only": false, "recorded_stems_all_dropped": true} {
		t.Run(name, func(t *testing.T) {
			dir := setupFactoryDir(t)
			if recorded {
				g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage")}})
			}
			manifestBefore, _ := os.ReadFile(config.PluginsConfigPath(dir))
			writePluginFixture(t, dir, "acme", map[string]string{"nested/deep.formula.toml": validPluginFormula("deep")})
			stubInstallPipeline(t)
			g3OKVerify(t)
			g2EnterFactory(t, dir)
			_, err := runPlugin(t, "install", nil, "acme")
			if err == nil || !strings.Contains(err.Error(), "contains no *.formula.toml files at its top level") {
				t.Fatalf("want the no-top-level refusal, got: %v", err)
			}
			if _, serr := os.Stat(filepath.Join(config.FormulasDir(dir), "deep.formula.toml")); serr == nil {
				t.Error("the nested formula was staged")
			}
			if after, _ := os.ReadFile(config.PluginsConfigPath(dir)); !bytes.Equal(manifestBefore, after) {
				t.Errorf("plugins.json changed on a refused install:\n before=%s\n after=%s", manifestBefore, after)
			}
		})
	}
	// R3 (IMPLREADME Phase 2 B3, L542-543): the zero-formula refusal applies only when no
	// af-integration.toml is present; a manifest-only plugin routes to the integration branch.
	t.Run("manifest_present_zero_formulas", func(t *testing.T) {
		dir := setupFactoryDir(t)
		intBCleanupWritable(t, dir)
		writePluginFixture(t, dir, "acme", map[string]string{
			config.IntegrationManifestFile:             "name = \"acme\"\ndescription = \"acme integration\"\n\n[claude]\nplugins = [\"claude-plugin\"]\n",
			"claude-plugin/.claude-plugin/plugin.json": `{"name":"acme-plugin"}` + "\n",
			"claude-plugin/skills/acme/SKILL.md":       "---\nname: acme\ndescription: acme skill\n---\nbody\n",
		})
		stubInstallPipeline(t)
		g3OKVerify(t)
		t.Setenv("AF_ROLE", "")
		t.Setenv("TMUX", "")
		t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
		g2EnterFactory(t, dir)
		_, err := runPlugin(t, "install", nil, "acme")
		if err != nil && strings.Contains(err.Error(), "contains no *.formula.toml files at its top level") {
			t.Fatalf("a plugin with af-integration.toml must not get the zero-formula refusal; got: %v", err)
		}
		if err != nil {
			t.Fatalf("a manifest-only plugin installs through the integration branch; got: %v", err)
		}
		cfg, lerr := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
		if lerr != nil {
			t.Fatalf("load plugins.json: %v", lerr)
		}
		if cfg.Plugins["acme"].Integration == nil {
			t.Errorf("the manifest-only plugin was not recorded as an integration: %+v", cfg.Plugins["acme"])
		}
	})
}

// PR #539 BODY-1: every recycle route (handoff, compact-handoff, done, watchdog, recovery) reaches
// the pane through respawnSession, so its K14 guard refuses before any identity or tmux effect and
// the refusal is still recorded in the K6 recovery log.
func TestPluginRuntimeRefusalRespawn(t *testing.T) {
	cases := map[string]struct {
		factory func(t *testing.T) string
		agent   string
		entry   config.AgentEntry
		wantErr string
	}{
		"plugin_owned_unembedded": {
			factory: k14Factory,
			agent:   "acme-triage",
			entry:   config.AgentEntry{Type: "autonomous", Formula: "acme-triage"},
			wantErr: `owned by plugin "acme"`,
		},
		"hand_authored_with_corrupt_manifest": {
			factory: func(t *testing.T) string {
				dir := setupFactoryDir(t)
				writeAgentsJSON(t, dir, `{"agents":{"myops":{"type":"autonomous","description":"hand-authored"}}}`)
				g2WriteManifestBytes(t, dir, g2ConflictManifest)
				return dir
			},
			agent:   "myops",
			entry:   config.AgentEntry{Type: "autonomous"},
			wantErr: "cannot be loaded",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := tc.factory(t)
			agentDir := filepath.Join(dir, ".agentfactory", "agents", tc.agent)
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			mock := &mockTmux{}
			err := respawnSession(RespawnOptions{
				FactoryRoot:  dir,
				AgentName:    tc.agent,
				AgentEntry:   tc.entry,
				PaneID:       "%5",
				AgentWorkDir: agentDir,
				Trigger:      triggerSelfHandoff,
				Tx:           mock,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("respawnSession must refuse with %q, got: %v", tc.wantErr, err)
			}
			if len(mock.clearHistoryCalls)+len(mock.respawnPaneCalls) != 0 {
				t.Errorf("a refused respawn touched tmux: clearHistory=%v respawnPane=%v", mock.clearHistoryCalls, mock.respawnPaneCalls)
			}
			if _, serr := os.Stat(filepath.Join(agentDir, "CLAUDE.md")); serr == nil {
				t.Error("a refused respawn rewrote the agent's identity (CLAUDE.md)")
			}
			lines := readRecoveryLogLines(t, dir)
			if len(lines) != 1 || lines[0].Agent != tc.agent || lines[0].Outcome != outcomeRespawnFailed {
				t.Errorf("the refusal must be logged once as %s for %s; got %+v", outcomeRespawnFailed, tc.agent, lines)
			}
		})
	}
}

// PR #539 BODY-1 protective: an embedded agent respawns normally even with a manifest present.
func TestPluginRuntimeRefusalRespawnSparesEmbeddedAgent(t *testing.T) {
	dir := k14Factory(t)
	mock := &mockTmux{}
	err := respawnSession(RespawnOptions{
		FactoryRoot:  dir,
		AgentName:    "supervisor",
		AgentEntry:   config.AgentEntry{Type: "autonomous"},
		PaneID:       "%5",
		AgentWorkDir: t.TempDir(),
		Tx:           mock,
	})
	if err != nil {
		t.Fatalf("an embedded agent must respawn: %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Errorf("RespawnPane calls = %d, want 1", len(mock.respawnPaneCalls))
	}
}

// ---- PR #539 increment: install admits/refuses the new list statuses ---------

// PR #539 T3: a plugin that ships an embedded built-in identity no one registers is refused
// before any write.
func TestPluginInstallRefusesEmbeddedIdentitySquat(t *testing.T) {
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{}}`)
	writePluginFixture(t, dir, "acme", map[string]string{"manager.formula.toml": validPluginFormula("manager")})
	s := g1StubCountingPipeline(t)
	g2EnterFactory(t, dir)
	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	_, err := runPlugin(t, "install", nil, "acme")

	if err == nil || !strings.Contains(err.Error(), `"manager"`) || !strings.Contains(err.Error(), "embedded") {
		t.Fatalf("install must refuse the embedded built-in identity manager, got: %v", err)
	}
	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
}

// PR #539 T4 protective: `changed` is still installable — the update lands and is re-recorded.
func TestPluginInstallAcceptsChangedFormula(t *testing.T) {
	dir := setupFactoryDir(t)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage")}})
	incoming := "# upstream change\n" + validPluginFormula("acme-triage")
	writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": incoming})
	stubInstallPipeline(t)
	g3OKVerify(t)
	g2EnterFactory(t, dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("a changed formula must install: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(config.FormulasDir(dir), "acme-triage.formula.toml")); string(got) != incoming {
		t.Errorf("store copy was not updated to the incoming bytes:\n%s", got)
	}
	m, err := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Plugins["acme"].Formulas["acme-triage"].SHA256; got != hashHex([]byte(incoming)) {
		t.Errorf("manifest sha256 = %s, want the incoming %s", got, hashHex([]byte(incoming)))
	}
}

// PR #539 T7: a formula field that differs from the file stem is refused before any write.
func TestPluginInstallRefusesFormulaFieldMismatch(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{"acme-scout.formula.toml": validPluginFormula("acme-triage")})
	s := g1StubCountingPipeline(t)
	g2EnterFactory(t, dir)
	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	_, err := runPlugin(t, "install", nil, "acme")

	if err == nil || !strings.Contains(err.Error(), "acme-scout") || !strings.Contains(err.Error(), "acme-triage") {
		t.Fatalf("install must refuse naming the stem acme-scout and the field acme-triage, got: %v", err)
	}
	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
}

// PR #539 T9: a re-install whose clone dropped a recorded stem narrates it as removed, with the
// agent-gen --delete remediation, before the first write and again after verify — and never stages it.
func TestPluginReinstallNarratesDroppedStem(t *testing.T) {
	dir := setupFactoryDir(t)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {
		"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage"),
		"acme-old":    g2StoreCleanFormula(t, dir, "acme-old"),
	}})
	oldPath := filepath.Join(config.FormulasDir(dir), "acme-old.formula.toml")
	oldBefore, _ := os.ReadFile(oldPath)
	writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": validPluginFormula("acme-triage")})
	stubInstallPipeline(t)
	g3OKVerify(t)
	g2EnterFactory(t, dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("re-install: %v\n%s", err, out)
	}
	remediation := strings.Index(out, "af formula agent-gen acme-old --delete")
	if remediation < 0 || !strings.Contains(out, "[removed]") {
		t.Fatalf("re-install must narrate acme-old as [removed] with its --delete remediation:\n%s", out)
	}
	if staged := strings.Index(out, "staged "); staged >= 0 && remediation > staged {
		t.Errorf("the removed narration must precede the first write:\n%s", out)
	}
	if verified := strings.Index(out, "plugin install verified."); verified < 0 || strings.LastIndex(out, "af formula agent-gen acme-old --delete") < verified {
		t.Errorf("the --delete remedy must be repeated after verify, below the pipeline transcript:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "staged ") && strings.Contains(line, "acme-old") {
			t.Errorf("a removed stem was staged: %q", line)
		}
	}
	if after, _ := os.ReadFile(oldPath); !bytes.Equal(oldBefore, after) {
		t.Errorf("the verb rewrote the removed stem's store copy:\n before=%q\n after=%q", oldBefore, after)
	}
}

// PR #539 T14: in agent context a valid install is refused before its set is narrated.
func TestPluginInstallAgentContextRefusesBeforeNarration(t *testing.T) {
	dir := g1AcmeFactory(t)
	s := g1StubCountingPipeline(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	g2EnterFactory(t, dir)

	out, err := runPlugin(t, "install", nil, "acme")

	assertTeardownRefused(t, err, "af plugin install")
	if strings.Contains(out, "installing 1 plugin(s)") || strings.Contains(out, thirdPartyLabel) {
		t.Errorf("agent-context install narrated its set before the operator-gate refusal:\n%s", out)
	}
	s.assertNoneRan(t)
}

// PR #539 T14 protective (D12 option E): validation keeps precedence over the agent-context
// refusal, and a validation failure still narrates the set it refused.
func TestPluginInstallValidationPrecedesAgentContextRefusal(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{"9lives.formula.toml": validPluginFormula("9lives")})
	s := g1StubCountingPipeline(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	g2EnterFactory(t, dir)

	out, err := runPlugin(t, "install", nil, "acme")

	if err == nil || !strings.Contains(err.Error(), "is not a valid agent name") {
		t.Fatalf("want the invalid-name validation error ahead of the agent-context refusal, got: %v", err)
	}
	if !strings.Contains(out, "installing 1 plugin(s)") {
		t.Errorf("a validation failure must still narrate the refused set:\n%s", out)
	}
	s.assertNoneRan(t)
}

// PR #539 T14 protective: in operator context the set is narrated before the first write.
func TestPluginInstallNarrationPrecedesStaging(t *testing.T) {
	dir := g1AcmeFactory(t)
	stubInstallPipeline(t)
	g3OKVerify(t)
	g2EnterFactory(t, dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	narrated, staged := strings.Index(out, "installing 1 plugin(s)"), strings.Index(out, "staged ")
	if narrated < 0 || staged < 0 || narrated > staged {
		t.Errorf("the install set must be narrated before the first staged write:\n%s", out)
	}
}

// ---- PR #539 increment: verify, agents.json and K14 pins -------------------------

// PR #539 T5: a present-but-drifted store copy of an embedded, registered plugin agent fails verify.
func TestPluginVerifyReportsDriftedStoreCopy(t *testing.T) {
	dir := g2EmbeddedPluginFactory(t, false)
	drifted := "# drifted\n" + validPluginFormula("factoryworker")
	if err := os.WriteFile(filepath.Join(config.FormulasDir(dir), "factoryworker.formula.toml"), []byte(drifted), 0o644); err != nil {
		t.Fatal(err)
	}
	g2EnterFactory(t, dir)
	p, _, out, err := g2VerifyJSON(t, nil, "acme")
	if err == nil {
		t.Errorf("drifted verify must exit non-zero:\n%s", out)
	}
	if len(p.Results) != 1 {
		t.Fatalf("want exactly one row, got %d:\n%s", len(p.Results), out)
	}
	r := p.Results[0]
	if !r.Registered || !r.Embedded || r.HashClean == nil || *r.HashClean || r.OK {
		t.Errorf("want registered, embedded, hash_clean=false, ok=false; got %+v", r)
	}
	if p.OK || p.State != "fail" || !strings.Contains(r.Message, "drift") {
		t.Errorf("want payload ok=false state=fail and a drift message; got ok=%v state=%q message=%q", p.OK, p.State, r.Message)
	}
}

// PR #539 T13: a missing store copy is reported as missing with the re-install remedy, not as drift.
func TestPluginVerifyMissingStoreCopyIsNotDrift(t *testing.T) {
	dir := g2EmbeddedPluginFactory(t, false)
	if err := os.Remove(filepath.Join(config.FormulasDir(dir), "factoryworker.formula.toml")); err != nil {
		t.Fatal(err)
	}
	g2EnterFactory(t, dir)
	p, _, out, err := g2VerifyJSON(t, nil, "acme")
	if err == nil {
		t.Errorf("a missing store copy must fail verify:\n%s", out)
	}
	if len(p.Results) != 1 {
		t.Fatalf("want exactly one row, got %d:\n%s", len(p.Results), out)
	}
	r := p.Results[0]
	if r.OK || r.HashClean == nil || *r.HashClean {
		t.Errorf("want ok=false hash_clean=false; got %+v", r)
	}
	if !strings.Contains(r.Message, "missing") || !strings.Contains(r.Message, "af plugin install acme") || strings.Contains(r.Message, "drift") {
		t.Errorf("message must say missing and name `af plugin install acme`, never drift; got %q", r.Message)
	}
}

// PR #539 T7 (D7): a plugin row whose agents.json entry runs a different formula is not OK.
func TestPluginVerifyFlagsFormulaFieldMismatch(t *testing.T) {
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{"factoryworker":{"type":"autonomous","description":"x","formula":"investigate"}}}`)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"factoryworker": g2StoreCleanFormula(t, dir, "factoryworker")}})
	g2EnterFactory(t, dir)
	p, _, out, err := g2VerifyJSON(t, nil, "acme")
	if err == nil || p.OK {
		t.Errorf("verify must fail a plugin agent registered with another formula:\n%s", out)
	}
	if len(p.Results) != 1 {
		t.Fatalf("want exactly one row, got %d:\n%s", len(p.Results), out)
	}
	if r := p.Results[0]; r.OK || !strings.Contains(r.Message, "investigate") || !strings.Contains(r.Message, "factoryworker") {
		t.Errorf("row must be not-ok and name the formula field and the stem; got %+v", r)
	}
}

// PR #539 T7 protective (D7): `agent-gen --name` legitimately registers a name running another
// formula, so non-plugin rows under --all never compare the formula field.
func TestPluginVerifyAllNonPluginRowIgnoresFormulaField(t *testing.T) {
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{"supervisor":{"type":"autonomous","description":"x","formula":"factoryworker"}}}`)
	g2EnterFactory(t, dir)
	p, _, out, err := g2VerifyJSON(t, map[string]string{"all": "true"})
	if err != nil || !p.OK {
		t.Errorf("a registered, embedded non-plugin agent must stay ok whatever its formula field: %v\n%s", err, out)
	}
}

// PR #539 T12 (D10): plugin names and --all are mutually exclusive, never silently merged.
func TestPluginVerifyNamesWithAll(t *testing.T) {
	dir := g2ClassWideFactory(t)
	g2EnterFactory(t, dir)
	p, _, out, err := g2VerifyJSON(t, map[string]string{"all": "true"}, "acme")
	if err == nil {
		t.Errorf("verify acme --all must exit non-zero:\n%s", out)
	}
	if p.State != "error" || !strings.Contains(p.Error, "mutually exclusive") || len(p.Results) != 0 {
		t.Errorf(`want {"state":"error"} naming the exclusivity and no rows; got state=%q error=%q rows=%d`, p.State, p.Error, len(p.Results))
	}
	hout, herr := runPlugin(t, "verify", map[string]string{"all": "true"}, "acme")
	if herr == nil || !strings.Contains(g2Combined(hout, herr), "mutually exclusive") {
		t.Errorf("human verify acme --all must refuse the combination; err=%v output:\n%s", herr, hout)
	}
}

// PR #539 T8: an agents.json that exists but cannot be loaded fails list and install closed,
// instead of emptying the manual- and formula-agent collision classes.
func TestPluginListAndInstallRefuseUnreadableAgentsJSON(t *testing.T) {
	dir := g1AcmeFactory(t)
	writeAgentsJSON(t, dir, g2ConflictAgents)
	s := g1StubCountingPipeline(t)
	g2EnterFactory(t, dir)

	lout, lerr := runPlugin(t, "list", nil)
	if lerr == nil || !strings.Contains(g2Combined(lout, lerr), "agents.json") {
		t.Errorf("human list must fail naming agents.json; err=%v output:\n%s", lerr, lout)
	}

	jout, jerr := runPlugin(t, "list", map[string]string{"json": "true"})
	var obj map[string]any
	if e := json.Unmarshal([]byte(strings.TrimSpace(jout)), &obj); e != nil || jerr != nil {
		t.Fatalf(`list --json must exit 0 with a {"state":"error"} object; err=%v decode=%v output:\n%s`, jerr, e, jout)
	}
	if msg, _ := obj["error"].(string); obj["state"] != "error" || !strings.Contains(msg, "agents.json") {
		t.Errorf(`want {"state":"error"} naming agents.json; got %s`, jout)
	}

	storeBefore := snapshotTree(t, config.StoreDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))
	_, ierr := runPlugin(t, "install", nil, "acme")
	if ierr == nil || !strings.Contains(ierr.Error(), "agents.json") {
		t.Errorf("install must refuse naming agents.json, got: %v", ierr)
	}
	g1AssertZeroWrites(t, dir, storeBefore, agentsBefore)
	s.assertNoneRan(t)
}

// PR #539 T8 protective (D8): a factory without agents.json has empty collision classes, not an error.
func TestPluginListToleratesAbsentAgentsJSON(t *testing.T) {
	dir := g1AcmeFactory(t)
	if err := os.Remove(config.AgentsConfigPath(dir)); err != nil {
		t.Fatal(err)
	}
	g2EnterFactory(t, dir)
	rows, out := g5ListRows(t)
	if got := rows["acme-triage.formula.toml"].Status; got != pluginStatusOK {
		t.Errorf("absent agents.json: status = %q, want ok:\n%s", got, out)
	}
}

// PR #539 T10 protective: the guide's blast-radius sentence — a corrupt plugins.json refuses
// hand-authored agents too, because ownership is unknowable.
func TestPluginRuntimeRefusalCorruptManifestRefusesHandAuthoredAgent(t *testing.T) {
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{"myops":{"type":"autonomous","description":"hand-authored"}}}`)
	g2WriteManifestBytes(t, dir, g2ConflictManifest)
	if err := refusePluginAgentWithoutTemplate(dir, "myops"); err == nil || !strings.Contains(err.Error(), "plugins.json") {
		t.Errorf("a hand-authored, un-embedded agent must be refused while plugins.json is corrupt; got: %v", err)
	}
}
