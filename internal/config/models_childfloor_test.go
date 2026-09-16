package config

import "testing"

// TestBackendChildFloorTokens_DefaultAndNeverZero pins F1 (r3906601296) / BAD-4 (r3906161527): the
// child-floor accessor is default-NOT-inert. It returns the operator's declared value when a positive
// decimal is set, otherwise the conservative ~50000 default — "absent → default, never zero". This is
// the mirror of BackendPoolTokens (models.go:345-350) except the pool has NO default (a pool is an
// operator fact or absent) while the floor's absence means "use the default", so an inert floor would
// be a proportionality knob the doctrine forbids.
//
// RED today (compile-time): BackendChildFloorTokens does not exist yet, so the config test package will
// not build (`undefined: BackendChildFloorTokens`). That build failure is the RED; Phase 6 adds the
// accessor and the constant it mirrors.
func TestBackendChildFloorTokens_DefaultAndNeverZero(t *testing.T) {
	const def = int64(50000)
	if got := BackendChildFloorTokens(nil); got != def {
		t.Errorf("absent profile -> %d, want default %d", got, def)
	}
	if got := BackendChildFloorTokens(map[string]string{}); got != def {
		t.Errorf("empty profile -> %d, want default %d", got, def)
	}
	if got := BackendChildFloorTokens(map[string]string{"AF_BACKEND_CHILD_FLOOR_TOKENS": ""}); got != def {
		t.Errorf("empty value -> %d, want default %d", got, def)
	}
	if got := BackendChildFloorTokens(map[string]string{"AF_BACKEND_CHILD_FLOOR_TOKENS": "70000"}); got != 70000 {
		t.Errorf("operator override -> %d, want 70000", got)
	}
	// Defense-in-depth: even a hand-edited "0" (which validateModelsConfig already rejects at load, per
	// T4) must fall back to the default, never disable the floor.
	if got := BackendChildFloorTokens(map[string]string{"AF_BACKEND_CHILD_FLOOR_TOKENS": "0"}); got != def {
		t.Errorf(`"0" -> %d, want default %d (never zero)`, got, def)
	}
}

// TestParallelSubagentsDisabled pins the #672 hard-cap flag: only the exact value "1" enables it, so a
// typo or any other value fails safe toward the existing arithmetic path rather than silently disabling
// parallelism.
func TestParallelSubagentsDisabled(t *testing.T) {
	if ParallelSubagentsDisabled(nil) {
		t.Error("absent profile -> enabled, want disabled")
	}
	if ParallelSubagentsDisabled(map[string]string{}) {
		t.Error("empty profile -> enabled, want disabled")
	}
	if !ParallelSubagentsDisabled(map[string]string{"AF_DISABLE_PARALLEL_SUBAGENTS": "1"}) {
		t.Error(`"1" -> not enabled, want enabled (the hard cap must engage)`)
	}
	for _, v := range []string{"", "0", "true", "yes", "2"} {
		if ParallelSubagentsDisabled(map[string]string{"AF_DISABLE_PARALLEL_SUBAGENTS": v}) {
			t.Errorf("%q -> enabled, want disabled (only \"1\" enables; fail safe toward existing behavior)", v)
		}
	}
}
