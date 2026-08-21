package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDefaultElements_AllEightCanonicalOrder pins the default byte-exactly to the canonical
// roster. The two lists collapsed in issue #600: "elapsed" and "daily" used to be withheld from
// the default because both move on an idle re-render and would reset the watchdog's silence
// hash, but the watchdog now strips sentinel-marked statusline lines before hashing
// (internal/cmd/watchdog.go), so no element can mask a hung agent and none needs excluding.
func TestDefaultElements_AllEightCanonicalOrder(t *testing.T) {
	want := []string{"model", "dir", "branch", "diff", "elapsed", "context", "session", "daily"}

	if got := DefaultStatuslineElements(); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultStatuslineElements() = %v, want %v", got, want)
	}

	cfg, err := LoadStatuslineConfig(t.TempDir()) // absent file ⇒ the real default
	if err != nil {
		t.Fatalf("LoadStatuslineConfig on an absent file: %v", err)
	}
	if !reflect.DeepEqual(cfg.Elements, want) {
		t.Errorf("absent-file default = %v, want %v", cfg.Elements, want)
	}

	// The default IS the canonical roster — this is what makes the two-list split gone rather
	// than merely synchronized.
	if !reflect.DeepEqual(DefaultStatuslineElements(), StatuslineElementNames()) {
		t.Errorf("default %v and canonical roster %v have diverged again",
			DefaultStatuslineElements(), StatuslineElementNames())
	}

	// Same defensive-copy contract as StatuslineElementNames: a caller mutating the result must
	// not be able to corrupt the package-level list.
	DefaultStatuslineElements()[0] = "pwned"
	if DefaultStatuslineElements()[0] != "model" {
		t.Error("DefaultStatuslineElements returns the package slice, not a defensive copy")
	}
}

// TestStatuslineConfig_ColorAbsentMeansOn pins the absent⇒ON rule. Color is the one field whose
// zero value is the WRONG answer: a plain bool would default a fresh factory to color OFF, which
// is why it is a pointer (api.md A2.1). ColorEnabled is the single home of that rule, so without
// this test an inverted implementation would be invisible — nothing else in the module consumes
// it yet.
func TestStatuslineConfig_ColorAbsentMeansOn(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name  string
		color *bool
		want  bool
	}{
		{"absent key means ON", nil, true},
		{"explicit true", &on, true},
		{"explicit false", &off, false},
	} {
		cfg := &StatuslineConfig{Elements: DefaultStatuslineElements(), Color: tc.color}
		if got := cfg.ColorEnabled(); got != tc.want {
			t.Errorf("%s: ColorEnabled() = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A fresh factory must render WITH color — the AC-1 requirement the pointer exists to satisfy.
	loaded, err := LoadStatuslineConfig(t.TempDir())
	if err != nil {
		t.Fatalf("LoadStatuslineConfig on an absent file: %v", err)
	}
	if !loaded.ColorEnabled() {
		t.Error("a factory with no statusline.json must default to color ON")
	}

	// An explicit false must SURVIVE a save/load round-trip as a non-nil false. This is what
	// `omitempty` on a pointer buys: nil is elided (so existing files are byte-unchanged) while an
	// explicit false is still written. A plain bool, or omitempty on a value type, would drop the
	// operator's opt-out on the way to disk and silently re-enable color.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := SaveStatuslineConfig(StatuslineConfigPath(dir), &StatuslineConfig{
		Elements: []string{"model"}, Color: &off,
	}); err != nil {
		t.Fatalf("SaveStatuslineConfig: %v", err)
	}
	back, err := LoadStatuslineConfig(dir)
	if err != nil {
		t.Fatalf("LoadStatuslineConfig after save: %v", err)
	}
	if back.Color == nil {
		t.Fatal("an explicit color:false was dropped on the way to disk; the operator's opt-out must persist")
	}
	if back.ColorEnabled() {
		t.Error("color:false round-tripped as enabled")
	}

	// The converse: an absent color must not be materialized into the file by a save, or every
	// write would bake in a value the operator never chose.
	if err := SaveStatuslineConfig(StatuslineConfigPath(dir), &StatuslineConfig{Elements: []string{"model"}}); err != nil {
		t.Fatalf("SaveStatuslineConfig without color: %v", err)
	}
	raw, err := os.ReadFile(StatuslineConfigPath(dir))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(raw), "color") {
		t.Errorf("a nil color must be omitted from the written file, got %s", raw)
	}
}

func TestStatuslineConfig_RoundTripAndWhitelist(t *testing.T) {
	// Absent file ⇒ populated default + nil error (mirrors startup.go, NOT telemetry.go's
	// empty-struct default). The default is the full canonical roster; the byte-exact pin and
	// the reason the two-list split is gone live in TestDefaultElements_AllEightCanonicalOrder.
	dir := t.TempDir()
	cfg, err := LoadStatuslineConfig(dir)
	if err != nil {
		t.Fatalf("LoadStatuslineConfig on an absent file: %v", err)
	}
	want := []string{"model", "dir", "branch", "diff", "elapsed", "context", "session", "daily"}
	if !reflect.DeepEqual(cfg.Elements, want) {
		t.Errorf("absent-file default = %v, want %v", cfg.Elements, want)
	}

	// Round-trip preserves element ORDER — Elements is an ordered list, so a length-only
	// check would miss an ordering regression.
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rt := &StatuslineConfig{Elements: []string{"model", "dir"}}
	if err := SaveStatuslineConfig(StatuslineConfigPath(dir), rt); err != nil {
		t.Fatalf("SaveStatuslineConfig: %v", err)
	}
	loaded, err := LoadStatuslineConfig(dir)
	if err != nil {
		t.Fatalf("LoadStatuslineConfig after save: %v", err)
	}
	if !reflect.DeepEqual(loaded.Elements, []string{"model", "dir"}) {
		t.Errorf("round-trip Elements = %v, want [model dir]", loaded.Elements)
	}

	// An empty (non-nil) element list is a VALID config that renders nothing (data.md:189).
	if err := SaveStatuslineConfig(StatuslineConfigPath(dir), &StatuslineConfig{Elements: []string{}}); err != nil {
		t.Errorf("an empty element list must be accepted (renders nothing); got %v", err)
	}

	// An unknown element is rejected: the error wraps ErrInvalidType, names the bad
	// element, and lists all 8 valid names.
	err = SaveStatuslineConfig(StatuslineConfigPath(dir), &StatuslineConfig{Elements: []string{"kost"}})
	if err == nil {
		t.Fatal("an unknown element must be rejected")
	}
	if !errors.Is(err, ErrInvalidType) {
		t.Errorf("unknown-element error must wrap ErrInvalidType; got %v", err)
	}
	if !strings.Contains(err.Error(), "kost") {
		t.Errorf("error must name the unknown element %q; got %v", "kost", err)
	}
	for _, name := range []string{"model", "dir", "branch", "diff", "elapsed", "context", "session", "daily"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error must list the valid name %q; got %v", name, err)
		}
	}
}

func TestSaveStatuslineConfig_Atomic(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &StatuslineConfig{Elements: []string{"model", "dir", "branch"}}
	// Writes even though the file does not pre-exist (the absent-file-⇒-defaults invariant
	// lives in Load, not Save).
	if err := SaveStatuslineConfig(StatuslineConfigPath(dir), cfg); err != nil {
		t.Fatalf("SaveStatuslineConfig: %v", err)
	}
	assertNoTempResidue(t, afDir)

	loaded, err := LoadStatuslineConfig(dir)
	if err != nil {
		t.Fatalf("LoadStatuslineConfig: %v", err)
	}
	if !reflect.DeepEqual(loaded.Elements, []string{"model", "dir", "branch"}) {
		t.Errorf("round-trip mismatch: loaded=%+v", loaded)
	}

	// An unknown element is rejected before any write.
	bad := &StatuslineConfig{Elements: []string{"nope"}}
	if err := SaveStatuslineConfig(StatuslineConfigPath(dir), bad); err == nil {
		t.Error("SaveStatuslineConfig accepted an unknown element")
	}
}
