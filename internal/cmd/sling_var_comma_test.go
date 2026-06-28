package cmd

import "testing"

// TestSling_VarValueWithCommaPreserved locks in the T2 fix: a --var value that
// contains a comma must survive as a SINGLE variable. Registered as a pflag
// StringSliceVar, the --var flag CSV-splits values, so `pr_uri=a,b` becomes
// ["pr_uri=a","b"] and parseCLIVars then rejects the "b" fragment (no '='), making
// `af sling` exit non-zero. Switching the flag to StringArrayVar preserves commas.
func TestSling_VarValueWithCommaPreserved(t *testing.T) {
	f := slingCmd.Flags().Lookup("var")
	if f == nil {
		t.Fatal("--var flag is not registered on slingCmd")
	}

	// Behavioural guard: drive the real flag and confirm the comma is preserved.
	origVars := slingVars
	origChanged := f.Changed
	slingVars = nil
	t.Cleanup(func() {
		slingVars = origVars
		f.Changed = origChanged
	})
	if err := f.Value.Set("pr_uri=a,b"); err != nil {
		t.Fatalf("setting --var pr_uri=a,b: %v", err)
	}
	vars, err := parseCLIVars(slingVars)
	if err != nil {
		t.Fatalf("comma in --var value was mangled by the flag parser (raw=%v): %v", slingVars, err)
	}
	if got := vars["pr_uri"]; got != "a,b" {
		t.Fatalf("--var pr_uri=a,b: got pr_uri=%q, want %q (raw slingVars=%v)", got, "a,b", slingVars)
	}

	// Structural guard: the flag must not be the CSV-splitting stringSlice type.
	if f.Value.Type() == "stringSlice" {
		t.Fatalf("--var is registered as stringSlice (CSV-splits values); use stringArray")
	}
}
