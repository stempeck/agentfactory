package config

import "testing"

// TestPluginsConfigOwnsAgent pins the derived-ownership contract the K14 runtime
// refusal rides on: a plugin owns role template <stem> iff it owns formula
// <stem>.formula.toml, matched with the suffix normalized off BOTH the query and the
// stored key. Also pins AC-6 dormancy: an absent/empty/nil manifest owns nothing.
func TestPluginsConfigOwnsAgent(t *testing.T) {
	m := &PluginsConfig{Plugins: map[string]PluginEntry{
		"acme": {Formulas: map[string]PluginFormula{"acme-triage": {SHA256: "x"}}},
		"beta": {Formulas: map[string]PluginFormula{"beta-flow": {SHA256: "y"}}},
	}}

	if p, ok := m.OwnsAgent("acme-triage"); !ok || p != "acme" {
		t.Errorf(`OwnsAgent("acme-triage") = %q,%v; want "acme",true`, p, ok)
	}
	// A caller may pass the full filename — the suffix must normalize.
	if p, ok := m.OwnsAgent("acme-triage.formula.toml"); !ok || p != "acme" {
		t.Errorf(`OwnsAgent("acme-triage.formula.toml") = %q,%v; want "acme",true`, p, ok)
	}
	if p, ok := m.OwnsAgent("beta-flow"); !ok || p != "beta" {
		t.Errorf(`OwnsAgent("beta-flow") = %q,%v; want "beta",true`, p, ok)
	}
	if _, ok := m.OwnsAgent("unrelated"); ok {
		t.Error(`OwnsAgent("unrelated") must be false`)
	}

	// The stored key may itself carry the suffix — normalization is two-sided.
	m2 := &PluginsConfig{Plugins: map[string]PluginEntry{
		"acme": {Formulas: map[string]PluginFormula{"acme-triage.formula.toml": {}}},
	}}
	if p, ok := m2.OwnsAgent("acme-triage"); !ok || p != "acme" {
		t.Errorf("stored-with-suffix key did not normalize: %q,%v", p, ok)
	}

	// Dormancy: an absent manifest (the AC-6 default) owns nothing.
	empty, err := LoadPluginsConfig(PluginsConfigPath(t.TempDir()))
	if err != nil {
		t.Fatalf("LoadPluginsConfig(absent): %v", err)
	}
	if _, ok := empty.OwnsAgent("acme-triage"); ok {
		t.Error("empty manifest must own nothing (AC-6 dormancy)")
	}
	var nilc *PluginsConfig
	if _, ok := nilc.OwnsAgent("acme-triage"); ok {
		t.Error("nil receiver must own nothing")
	}
}
