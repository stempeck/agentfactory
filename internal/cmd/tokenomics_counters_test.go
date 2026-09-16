package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #678 K9's status half. The efficiency objective is only worth having if an operator can see it
// working, and the failure this file exists to make impossible is the "dark actuator": a factory
// that has learned enough to act, is configured to act, and never acts — reported by a status
// surface that prints a policy and no evidence, which reads exactly like a factory that is working.
//
// The counters answer the three questions in order: could it have acted (eligible), did it
// (fired), and when it did not, what did it do instead (the by-action legs). `dark` is the verdict
// over those three, and it is deliberately NOT an inert reason: inert means a gate is off, and a
// dark actuator's gates are all on.

const countersTestFormula = "counters"

// countersFactory is setupTestFactoryForPrime's roster rather than setupTokenomicsFactory's bare
// factory.json, because the counters come off the SAME roster walk the intervention tail uses: a
// factory with no agents.json has no logs to walk, and every counter would read zero for a reason
// that has nothing to do with what fired.
//
// learned_min_runs is pinned to 1 rather than inherited. The dark floor is that value times three,
// so a fixture on the shipped default of 2 would need six trusted keys to say anything about three.
func countersFactory(t *testing.T, block string) string {
	t.Helper()
	root := setupTestFactoryForPrime(t)
	t.Chdir(root)
	lightTheChain(t, root)
	asOperator(t)
	writeStartupTokenomics(t, root, block)
	return root
}

const countersDefaultBlock = `{"enabled":"on","learned_min_runs":1}`

func seedCountersDigest(t *testing.T, root string, keys ...tokenomics.DigestKey) {
	t.Helper()
	d := tokenomics.NewDigest()
	for i, k := range keys {
		d.Put(k, tokenomics.Aggregate{Runs: 3 + i, MedianPeakCtxTokens: int64(90_000 + i)})
	}
	dir := config.TelemetryDir(root)
	if err := os.MkdirAll(telemetry.LearnedDigestDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir digest dir: %v", err)
	}
	if err := tokenomics.SaveDigest(telemetry.LearnedDigestPath(dir, countersTestFormula), d); err != nil {
		t.Fatalf("SaveDigest: %v", err)
	}
}

// seedCountersDigestN seeds n distinct join-eligible keys, which is the only input `eligible` has.
func seedCountersDigestN(t *testing.T, root string, n int) {
	t.Helper()
	keys := make([]tokenomics.DigestKey, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, tokenomics.DigestKey{
			Formula: countersTestFormula, StepID: "s-" + string(rune('a'+i)), Model: "lmstudio",
		})
	}
	seedCountersDigest(t, root, keys...)
}

func seedCountersIntervention(t *testing.T, root, mechanism, action, objective, step string) {
	t.Helper()
	err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
		TS: "2026-09-09T10:00:00.000Z", Agent: "manager", Formula: countersTestFormula,
		InstanceID: "af-678-1", StepID: step,
		Mechanism: mechanism, Action: action, Objective: objective,
	})
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
}

// countersStatusJSON restores --json immediately rather than through t.Cleanup, which is what
// enableTokenomicsJSON does. Several sub-tests below assert on BOTH surfaces of one factory, and a
// flag that stayed set until the sub-test ended would route the human call through the JSON path
// and fail every human assertion with a payload that is perfectly correct.
func countersStatusJSON(t *testing.T) tokenomicsStatusJSON {
	t.Helper()
	if err := tokenomicsCmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set --json: %v", err)
	}
	out, err := runTokenomicsArgs(t)
	if resetErr := tokenomicsCmd.Flags().Set("json", "false"); resetErr != nil {
		t.Fatalf("restore --json: %v", resetErr)
	}
	if err != nil {
		t.Fatalf("tokenomics --json: %v", err)
	}
	var dto tokenomicsStatusJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return dto
}

func counterFor(t *testing.T, dto tokenomicsStatusJSON, mechanism string) tokenomicsCounterJSON {
	t.Helper()
	for _, c := range dto.Counters {
		if c.Mechanism == mechanism {
			return c
		}
	}
	t.Fatalf("no counter for mechanism %q in %+v", mechanism, dto.Counters)
	return tokenomicsCounterJSON{}
}

func TestTokenomicsStatusCounters(t *testing.T) {
	t.Run("three trusted keys and nothing fired reads dark", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		// Three is the floor: the fixture pins learned_min_runs to 1, and the dark verdict
		// needs eligible >= learned_min_runs x 3 before it will call a silent mechanism dark
		// rather than under-informed.
		seedCountersDigestN(t, root, 3)

		dto := countersStatusJSON(t)
		if len(dto.Counters) != len(tokenomics.Mechanisms()) {
			t.Fatalf("counters = %d rows, want one per mechanism (%d)",
				len(dto.Counters), len(tokenomics.Mechanisms()))
		}
		for _, c := range dto.Counters {
			if c.Eligible != 3 {
				t.Errorf("%s eligible = %d, want 3 — eligible is the trusted-key count every "+
					"mechanism can join on", c.Mechanism, c.Eligible)
			}
			if c.Fired != 0 {
				t.Errorf("%s fired = %d on a factory where nothing fired", c.Mechanism, c.Fired)
			}
			if !c.Dark {
				t.Errorf("%s dark = false with %d eligible keys and zero firings; a dark actuator "+
					"reported as healthy is the exact failure this counter exists to catch", c.Mechanism, c.Eligible)
			}
		}

		out, err := runTokenomicsArgs(t, "status")
		if err != nil {
			t.Fatalf("af tokenomics status: %v", err)
		}
		if !strings.Contains(out, "dark") {
			t.Errorf("the human status never prints `dark`:\n%s", out)
		}
	})

	t.Run("below the dark floor a silent mechanism is under-informed, not dark", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		seedCountersDigestN(t, root, 2)

		for _, c := range countersStatusJSON(t).Counters {
			if c.Dark {
				t.Errorf("%s is called dark on 2 eligible keys; below the floor a mechanism that "+
					"has not fired has simply not had the evidence to fire on", c.Mechanism)
			}
		}
	})

	t.Run("a mechanism that fired is not dark, and only that one", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		seedCountersDigestN(t, root, 3)
		seedCountersIntervention(t, root, string(tokenomics.MechanismEffort),
			telemetry.ActionReduceEffort, telemetry.ObjectiveEfficiency, "bd-1")

		dto := countersStatusJSON(t)
		effort := counterFor(t, dto, string(tokenomics.MechanismEffort))
		if effort.Fired != 1 {
			t.Errorf("effort fired = %d, want 1", effort.Fired)
		}
		if effort.Dark {
			t.Error("effort is dark after it fired")
		}
		if effort.ReduceEffort != 1 {
			t.Errorf("effort reduce_effort = %d, want 1 — the by-action legs are the "+
				"declined-by-reason breakdown, over the closed action vocabulary", effort.ReduceEffort)
		}
		if effort.FiredForEfficiency != 1 || effort.FiredForCapacity != 0 {
			t.Errorf("effort objective split = efficiency %d / capacity %d, want 1 / 0; the same "+
				"mechanism fires for either reason and without the split the two arms of #678's "+
				"experiment are one series",
				effort.FiredForEfficiency, effort.FiredForCapacity)
		}
		if thrift := counterFor(t, dto, string(tokenomics.MechanismThrift)); !thrift.Dark {
			t.Error("thrift stopped being dark because a DIFFERENT mechanism fired")
		}
	})

	t.Run("an observe record is a decline, not a firing", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		seedCountersDigestN(t, root, 3)
		seedCountersIntervention(t, root, string(tokenomics.MechanismDispatch),
			telemetry.ActionObserve, telemetry.ObjectiveCapacity, "bd-2")

		dispatch := counterFor(t, countersStatusJSON(t), string(tokenomics.MechanismDispatch))
		if dispatch.Observe != 1 {
			t.Errorf("dispatch observe = %d, want 1", dispatch.Observe)
		}
		if dispatch.Fired != 0 {
			t.Errorf("dispatch fired = %d; an observe record is the gate admitting a launch it "+
				"could not judge — nothing was done to the session, so counting it as a firing "+
				"would report a broken gate as a working one", dispatch.Fired)
		}
		if !dispatch.Dark {
			t.Error("dispatch is not dark though it acted on nothing; declining is not acting")
		}
	})

	t.Run("a refusal IS a firing", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		seedCountersDigestN(t, root, 3)
		seedCountersIntervention(t, root, string(tokenomics.MechanismDispatch),
			telemetry.ActionRefuse, telemetry.ObjectiveCapacity, "bd-3")

		dispatch := counterFor(t, countersStatusJSON(t), string(tokenomics.MechanismDispatch))
		if dispatch.Fired != 1 || dispatch.Refuse != 1 {
			t.Errorf("dispatch fired/refuse = %d/%d, want 1/1 — a refusal changed what happened "+
				"to the launch, which is what firing means", dispatch.Fired, dispatch.Refuse)
		}
		if dispatch.Dark {
			t.Error("dispatch is dark after refusing a launch")
		}
	})

	t.Run("the objective block names the efficiency operands", func(t *testing.T) {
		countersFactory(t, `{"enabled":"on","efficiency":"on",`+
			`"efficiency_effort_level":"medium","efficiency_thinking_share_pct":80,`+
			`"efficiency_repeat_read_floor":1,"efficiency_max_relaunches":6}`)

		dto := countersStatusJSON(t)
		e := dto.Policy.Efficiency
		if !e.On {
			t.Error("policy.efficiency.on = false though startup.json sets efficiency=on")
		}
		for _, tc := range []struct {
			name string
			got  int
			want int
		}{
			{"thinking_share_pct", e.ThinkingSharePct, 80},
			{"repeat_read_floor", e.RepeatReadFloor, 1},
			{"max_relaunches", e.MaxRelaunches, 6},
		} {
			if tc.got != tc.want {
				t.Errorf("policy.efficiency.%s = %d, want %d", tc.name, tc.got, tc.want)
			}
		}
		if e.EffortLevel != "medium" {
			t.Errorf("policy.efficiency.effort_level = %q, want %q", e.EffortLevel, "medium")
		}

		out, err := runTokenomicsArgs(t, "status")
		if err != nil {
			t.Fatalf("af tokenomics status: %v", err)
		}
		for _, want := range []string{
			"objective: efficiency=on", "effort_level=medium", "thinking_share_pct=80",
			"repeat_read_floor=1", "max_relaunches=6", "capacity:",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the human objective block does not carry %q:\n%s", want, out)
			}
		}
	})

	t.Run("the umbrella being off is reported on the efficiency leg too", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		writeGate(t, tokenomicsGateFile(root), "off")

		if countersStatusJSON(t).Policy.Efficiency.On {
			t.Error("policy.efficiency.on = true with the umbrella off; the umbrella decides " +
				"absolutely, and an objective block that outlived it would be the surface " +
				"disagreeing with the resolver")
		}
	})

	t.Run("the measurement self-test says whether this session's figures are readable", func(t *testing.T) {
		countersFactory(t, countersDefaultBlock)

		dto := countersStatusJSON(t)
		if dto.Measurement.UnavailableBecause == "" {
			t.Error("measurement.unavailable_because is empty in a workspace with no transcript " +
				"marker; a self-test that reports nothing about an unreachable transcript is the " +
				"reason Gap 22 exists — every generation figure would be silently absent")
		}
		if dto.Measurement.TranscriptReachable || dto.Measurement.CarriesUsage || dto.Measurement.CarriesThinking {
			t.Errorf("measurement = %+v claims a readable transcript where none exists", dto.Measurement)
		}

		out, err := runTokenomicsArgs(t, "status")
		if err != nil {
			t.Fatalf("af tokenomics status: %v", err)
		}
		if !strings.Contains(out, "measurement:") {
			t.Errorf("the human status has no measurement self-test line:\n%s", out)
		}
		// D-17. TestTokenomicsStatusReportsAConfigDarkChain asserts the ABSENCE of `self-test: live`
		// on a factory whose chain is dark, so a measurement line that borrowed that prefix would
		// turn its negative assertion into a false failure on a factory that IS inert. The line
		// asserted on is the measurement one, not the whole surface: the liveness self-test above
		// says `self-test: live` legitimately, and this fixture's chain is lit.
		measurement := lineContaining(t, out, "measurement:")
		if strings.Contains(measurement, "self-test") {
			t.Errorf("the measurement line %q borrows the liveness self-test's prefix, which "+
				"another test asserts the absence of on a dark factory", measurement)
		}
	})

	t.Run("a reachable transcript carrying usage and thinking is reported as such", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)

		runtimeDir := root + "/.runtime"
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			t.Fatalf("mkdir .runtime: %v", err)
		}
		transcript := runtimeDir + "/session.jsonl"
		// The NESTED shape the host actually emits (#679 T3): output_tokens_details.thinking_tokens.
		line := `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,` +
			`"output_tokens":20,"output_tokens_details":{"thinking_tokens":5}}}}` + "\n"
		if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if err := os.WriteFile(runtimeDir+"/transcript_path",
			[]byte("sess-1\t"+transcript+"\n"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		dto := countersStatusJSON(t)
		if !dto.Measurement.TranscriptReachable {
			t.Fatalf("measurement = %+v; the marker names a file that exists", dto.Measurement)
		}
		if !dto.Measurement.CarriesUsage {
			t.Error("measurement.carries_usage = false on a transcript whose record has a usage object")
		}
		if !dto.Measurement.CarriesThinking {
			t.Error("measurement.carries_thinking = false though the usage object nests " +
				"thinking_tokens; that field is what separates an exact thinking count from an " +
				"estimate, and a host generation that omits it makes the band's think figure " +
				"incomparable across runs")
		}
		if dto.Measurement.UnavailableBecause != "" {
			t.Errorf("unavailable_because = %q on a readable transcript",
				dto.Measurement.UnavailableBecause)
		}
	})

	t.Run("the counters degrade with a reason rather than printing bare zeros", func(t *testing.T) {
		countersFactory(t, countersDefaultBlock)

		dto := countersStatusJSON(t)
		if dto.CountersUnavailableBecause == "" {
			t.Error("a cold factory reports eligible=0 with no reason, which reads identically " +
				"to a factory that has learned nothing to act on and one whose digest could not " +
				"be read")
		}
		for _, c := range dto.Counters {
			if c.Dark {
				t.Errorf("%s is called dark on a cold start; with no learned data there was "+
					"never an opportunity to fire", c.Mechanism)
			}
		}
	})

	// D-18. The dark verdict belongs to the counters, and TestTokenomicsStatusReportsAConfigDarkChain
	// asserts inert_because holds EXACTLY the causes it darkened — a dark actuator appended there
	// would break a test about an entirely different layer, and would also be wrong: inert means a
	// gate is off, and this factory's gates are all on.
	t.Run("dark is not an inert reason", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		seedCountersDigestN(t, root, 3)

		for _, reason := range countersStatusJSON(t).InertBecause {
			if strings.Contains(reason, "dark") {
				t.Errorf("inert_because carries %q; a dark actuator is not an inert gate", reason)
			}
		}
	})

	// D-19. TestTokenomicsStatusCarriesProvenance counts INDENTED human lines containing "cli" and
	// pins the count, so the word "declined" — which contains that substring — can never appear on
	// one. The by-action legs say what the mechanism did instead, which is more precise anyway.
	t.Run("no indented counter line contains the provenance guard's substring", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)
		seedCountersDigestN(t, root, 3)
		seedCountersIntervention(t, root, string(tokenomics.MechanismEffort),
			telemetry.ActionObserve, telemetry.ObjectiveEfficiency, "bd-4")

		out, err := runTokenomicsArgs(t, "status")
		if err != nil {
			t.Fatalf("af tokenomics status: %v", err)
		}
		for _, line := range strings.Split(out, "\n") {
			if !strings.HasPrefix(line, "  ") || !strings.Contains(line, "cli") {
				continue
			}
			// The provenance tail legitimately carries "cli" (the actor is a CLI caller); a
			// counter line must not.
			if strings.Contains(line, "eligible") || strings.Contains(line, "fired") {
				t.Errorf("counter line %q contains the substring \"cli\", which "+
					"TestTokenomicsStatusCarriesProvenance counts and pins", line)
			}
		}
	})
}

// TestThinkingProbeReadsNested pins that probeTranscriptUsageShape reports carries_thinking from the
// NESTED thinking count the host actually writes — usage.output_tokens_details.thinking_tokens
// (telemetry_generation.go:92-94) — and from nothing else. A transcript nesting the count is reported as
// carrying thinking; a transcript carrying only a FLAT top-level usage.thinking_tokens (a shape the host
// never emits) is NOT; and a usage object with no thinking figure at all is NOT.
func TestThinkingProbeReadsNested(t *testing.T) {
	t.Run("a nested thinking_tokens is reported as carried", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)

		runtimeDir := root + "/.runtime"
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			t.Fatalf("mkdir .runtime: %v", err)
		}
		transcript := runtimeDir + "/session.jsonl"
		line := `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,` +
			`"output_tokens":20,"output_tokens_details":{"thinking_tokens":5}}}}` + "\n"
		if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if err := os.WriteFile(runtimeDir+"/transcript_path",
			[]byte("sess-1\t"+transcript+"\n"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		dto := countersStatusJSON(t)
		if !dto.Measurement.TranscriptReachable {
			t.Fatalf("measurement = %+v; the marker names a file that exists", dto.Measurement)
		}
		if !dto.Measurement.CarriesUsage {
			t.Error("measurement.carries_usage = false on a transcript whose record has a usage object")
		}
		if !dto.Measurement.CarriesThinking {
			t.Error("measurement.carries_thinking = false though the usage object nests thinking_tokens " +
				"under output_tokens_details, the shape the probe reads")
		}
	})

	// A flat-only transcript: a top-level usage.thinking_tokens and NO output_tokens_details. The host
	// writes the count only under output_tokens_details.thinking_tokens, so a flat-only transcript is not
	// a thinking-bearing transcript — reporting it as one would credit the counter with a shape nothing
	// produces. This pins the flat-only case to carries_thinking=false.
	t.Run("a flat-only thinking_tokens is not reported as carried", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)

		runtimeDir := root + "/.runtime"
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			t.Fatalf("mkdir .runtime: %v", err)
		}
		transcript := runtimeDir + "/session.jsonl"
		// The FLAT shape only: a top-level thinking_tokens and NO output_tokens_details.
		line := `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,` +
			`"output_tokens":20,"thinking_tokens":5}}}` + "\n"
		if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if err := os.WriteFile(runtimeDir+"/transcript_path",
			[]byte("sess-1\t"+transcript+"\n"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		dto := countersStatusJSON(t)
		if !dto.Measurement.CarriesUsage {
			t.Error("measurement.carries_usage = false on a transcript whose record has a usage object")
		}
		if dto.Measurement.CarriesThinking {
			t.Error("measurement.carries_thinking = true for a FLAT top-level thinking_tokens; the host " +
				"emits only the nested output_tokens_details shape, so a flat-only transcript carries no " +
				"host thinking count")
		}
	})

	t.Run("a usage object with no thinking figure is reported as not carried", func(t *testing.T) {
		root := countersFactory(t, countersDefaultBlock)

		runtimeDir := root + "/.runtime"
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			t.Fatalf("mkdir .runtime: %v", err)
		}
		transcript := runtimeDir + "/session.jsonl"
		line := `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10,` +
			`"output_tokens":20}}}` + "\n"
		if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		if err := os.WriteFile(runtimeDir+"/transcript_path",
			[]byte("sess-1\t"+transcript+"\n"), 0o644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		dto := countersStatusJSON(t)
		if !dto.Measurement.CarriesUsage {
			t.Error("measurement.carries_usage = false on a transcript whose record has a usage object")
		}
		if dto.Measurement.CarriesThinking {
			t.Error("measurement.carries_thinking = true though the usage object carries no thinking " +
				"figure at all, flat or nested")
		}
	})
}
