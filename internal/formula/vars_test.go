package formula

import (
	"os"
	"testing"
)

func TestExpandTemplateVars_KnownVars(t *testing.T) {
	ctx := map[string]string{"name": "Alice", "place": "Wonderland"}
	got := ExpandTemplateVars("Hello {{name}}, welcome to {{place}}", ctx)
	want := "Hello Alice, welcome to Wonderland"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTemplateVars_UnknownVarsLeftAsIs(t *testing.T) {
	ctx := map[string]string{"name": "Bob"}
	got := ExpandTemplateVars("Hello {{name}}, your ID is {{id}}", ctx)
	want := "Hello Bob, your ID is {{id}}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTemplateVars_NilContext(t *testing.T) {
	got := ExpandTemplateVars("Hello {{name}}", nil)
	want := "Hello {{name}}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandTemplateVars_EmptyText(t *testing.T) {
	got := ExpandTemplateVars("", map[string]string{"x": "y"})
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestExpandTemplateVars_NoPlaceholders(t *testing.T) {
	got := ExpandTemplateVars("no placeholders here", map[string]string{"x": "y"})
	want := "no placeholders here"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveVars_CLISource(t *testing.T) {
	vars := map[string]Var{
		"issue": {Source: "cli", Required: true},
	}
	ctx := ResolveContext{CLIArgs: map[string]string{"issue": "BUG-123"}}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["issue"] != "BUG-123" {
		t.Errorf("got %q, want %q", got["issue"], "BUG-123")
	}
}

func TestResolveVars_EnvSource(t *testing.T) {
	t.Setenv("TEST_FORMULA_VAR", "from-env")

	vars := map[string]Var{
		"TEST_FORMULA_VAR": {Source: "env", Required: true},
	}
	ctx := ResolveContext{EnvLookup: os.Getenv}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["TEST_FORMULA_VAR"] != "from-env" {
		t.Errorf("got %q, want %q", got["TEST_FORMULA_VAR"], "from-env")
	}
}

func TestResolveVars_LiteralSource(t *testing.T) {
	vars := map[string]Var{
		"greeting": {Source: "literal", Default: "hello"},
	}
	ctx := ResolveContext{}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["greeting"] != "hello" {
		t.Errorf("got %q, want %q", got["greeting"], "hello")
	}
}

func TestResolveVars_MissingRequired(t *testing.T) {
	vars := map[string]Var{
		"missing": {Source: "cli", Required: true},
	}
	ctx := ResolveContext{CLIArgs: map[string]string{}}
	_, err := ResolveVars(vars, ctx)
	if err == nil {
		t.Fatal("expected error for missing required variable")
	}
}

func TestResolveVars_HookBeadSource(t *testing.T) {
	vars := map[string]Var{
		"issue": {Source: "hook_bead", Required: true},
	}
	ctx := ResolveContext{HookedBeadID: "bd-42"}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["issue"] != "bd-42" {
		t.Errorf("got %q, want %q", got["issue"], "bd-42")
	}
}

func TestResolveVars_BeadTitleSource(t *testing.T) {
	vars := map[string]Var{
		"title": {Source: "bead_title", Required: true},
	}
	ctx := ResolveContext{BeadTitle: "Fix login bug"}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["title"] != "Fix login bug" {
		t.Errorf("got %q, want %q", got["title"], "Fix login bug")
	}
}

func TestResolveVars_BeadDescriptionSource(t *testing.T) {
	vars := map[string]Var{
		"desc": {Source: "bead_description", Required: true},
	}
	ctx := ResolveContext{BeadDescription: "Users cannot log in after password reset"}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["desc"] != "Users cannot log in after password reset" {
		t.Errorf("got %q, want %q", got["desc"], "Users cannot log in after password reset")
	}
}

func TestResolveVars_CLIOverridesBeadSource(t *testing.T) {
	vars := map[string]Var{
		"issue": {Source: "hook_bead", Required: true},
	}
	ctx := ResolveContext{
		CLIArgs:      map[string]string{"issue": "cli-override"},
		HookedBeadID: "bd-42",
	}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["issue"] != "cli-override" {
		t.Errorf("got %q, want %q — CLI should override bead source", got["issue"], "cli-override")
	}
}

func TestResolveVars_BeadSourceMissingRequired(t *testing.T) {
	sources := []string{"hook_bead", "bead_title", "bead_description"}
	for _, src := range sources {
		vars := map[string]Var{
			"x": {Source: src, Required: true},
		}
		ctx := ResolveContext{}
		_, err := ResolveVars(vars, ctx)
		if err == nil {
			t.Errorf("expected error for missing required bead source %q", src)
		}
	}
}

func TestResolveVars_BeadSourceDefault(t *testing.T) {
	vars := map[string]Var{
		"issue": {Source: "hook_bead", Default: "default-bead"},
	}
	ctx := ResolveContext{} // no bead context
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["issue"] != "default-bead" {
		t.Errorf("got %q, want %q", got["issue"], "default-bead")
	}
}

func TestResolveVars_CLIOverridesEnvSource(t *testing.T) {
	t.Setenv("MY_ENV_VAR", "from-env")

	vars := map[string]Var{
		"MY_ENV_VAR": {Source: "env", Required: true},
	}
	ctx := ResolveContext{CLIArgs: map[string]string{"MY_ENV_VAR": "from-cli"}, EnvLookup: os.Getenv}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["MY_ENV_VAR"] != "from-cli" {
		t.Errorf("got %q, want %q — CLI should override env source", got["MY_ENV_VAR"], "from-cli")
	}
}

func TestResolveVars_UnknownSourceError(t *testing.T) {
	vars := map[string]Var{
		"x": {Source: "magic"},
	}
	ctx := ResolveContext{}
	_, err := ResolveVars(vars, ctx)
	if err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestResolveVars_EmptySource(t *testing.T) {
	vars := map[string]Var{
		"placeholder": {Source: "", Default: ""},
		"with_default": {Source: "", Default: "hello"},
	}
	result, err := ResolveVars(vars, ResolveContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["placeholder"] != "" {
		t.Errorf("placeholder = %q, want empty", result["placeholder"])
	}
	if result["with_default"] != "hello" {
		t.Errorf("with_default = %q, want hello", result["with_default"])
	}
}

func TestResolveVars_DefaultFallback(t *testing.T) {
	vars := map[string]Var{
		"opt": {Source: "cli", Default: "fallback"},
	}
	ctx := ResolveContext{CLIArgs: map[string]string{}}
	got, err := ResolveVars(vars, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["opt"] != "fallback" {
		t.Errorf("got %q, want %q", got["opt"], "fallback")
	}
}

func TestMergeInputsToVars_Basic(t *testing.T) {
	inputs := map[string]Input{
		"analyst_name": {Description: "Name of analyst agent", Required: true},
	}
	vars := map[string]Var{
		"issue": {Source: "hook_bead", Required: true},
	}
	merged, err := MergeInputsToVars(inputs, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	v, ok := merged["analyst_name"]
	if !ok {
		t.Fatal("expected analyst_name in merged vars")
	}
	if v.Source != "cli" {
		t.Errorf("source = %q, want %q", v.Source, "cli")
	}
	if _, ok := merged["issue"]; !ok {
		t.Fatal("expected original var 'issue' to be preserved")
	}
}

func TestMergeInputsToVars_PreservesRequired(t *testing.T) {
	inputs := map[string]Input{
		"name": {Required: true},
	}
	merged, err := MergeInputsToVars(inputs, map[string]Var{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !merged["name"].Required {
		t.Error("expected Required=true to be preserved")
	}
}

func TestMergeInputsToVars_PreservesDefault(t *testing.T) {
	inputs := map[string]Input{
		"interval": {Default: "10m"},
	}
	merged, err := MergeInputsToVars(inputs, map[string]Var{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged["interval"].Default != "10m" {
		t.Errorf("default = %q, want %q", merged["interval"].Default, "10m")
	}
}

func TestMergeInputsToVars_CollisionError(t *testing.T) {
	inputs := map[string]Input{
		"issue": {Description: "from inputs"},
	}
	vars := map[string]Var{
		"issue": {Source: "hook_bead"},
	}
	_, err := MergeInputsToVars(inputs, vars)
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !contains(err.Error(), "collides") {
		t.Errorf("error %q should contain 'collides'", err.Error())
	}
}

func TestMergeInputsToVars_EmptyInputs(t *testing.T) {
	vars := map[string]Var{
		"x": {Source: "cli"},
	}
	merged, err := MergeInputsToVars(map[string]Input{}, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(merged) != 1 || merged["x"].Source != "cli" {
		t.Errorf("expected original vars unchanged, got %v", merged)
	}
}

func TestMergeInputsToVars_NilInputs(t *testing.T) {
	vars := map[string]Var{
		"x": {Source: "cli"},
	}
	merged, err := MergeInputsToVars(nil, vars)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(merged) != 1 {
		t.Errorf("expected original vars unchanged, got %v", merged)
	}
}

func TestResolveVars_DeferredSourceSkipped(t *testing.T) {
	vars := map[string]Var{
		"issue_id":  {Source: "deferred", Default: ""},
		"greeting":  {Source: "literal", Default: "hello"},
	}
	got, err := ResolveVars(vars, ResolveContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := got["issue_id"]; exists {
		t.Error("deferred var should not appear in resolved map")
	}
	if got["greeting"] != "hello" {
		t.Errorf("greeting = %q, want %q", got["greeting"], "hello")
	}
}

func TestExpandTemplateVars_DeferredVarSurvives(t *testing.T) {
	// Deferred vars are excluded from the resolved map, so ExpandTemplateVars
	// treats them as unknown and leaves them as-is.
	ctx := map[string]string{"name": "Alice"}
	got := ExpandTemplateVars("Hello {{name}}, issue {{issue_id}}", ctx)
	want := "Hello Alice, issue {{issue_id}}"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
