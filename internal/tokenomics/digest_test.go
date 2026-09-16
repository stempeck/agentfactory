package tokenomics

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigestCodecRoundTrip(t *testing.T) {
	d := NewDigest()
	k1 := DigestKey{Formula: "design-v7", StepID: "phase-2", Model: "qwen3.8-27b"}
	k2 := DigestKey{Formula: "design-v7", StepID: "phase-2", Model: "fable-5"}
	d.Put(k1, Aggregate{Runs: 3, MedianPeakCtxTokens: 90_000, MaxPeakCtxTokens: 140_000, MedianCumTokensDelta: 42_000, UpdatedAt: "2026-08-30T00:00:00Z"})
	d.Put(k2, Aggregate{Runs: 9, MedianPeakCtxTokens: 30_000, MaxPeakCtxTokens: 41_000, MedianCumTokensDelta: 12_000, UpdatedAt: "2026-08-30T00:00:01Z"})

	data, err := EncodeDigest(d)
	if err != nil {
		t.Fatalf("EncodeDigest: %v", err)
	}
	back, err := DecodeDigest(data)
	if err != nil {
		t.Fatalf("DecodeDigest: %v", err)
	}
	if back.V != DigestVersion {
		t.Errorf("decoded V = %d, want %d", back.V, DigestVersion)
	}
	if got := Coverage(back); got != 2 {
		t.Errorf("Coverage = %d, want 2", got)
	}
	for _, k := range []DigestKey{k1, k2} {
		want, _ := d.Lookup(k)
		got, ok := back.Lookup(k)
		if !ok {
			t.Fatalf("key %v missing after a round trip", k)
		}
		if got != want {
			t.Errorf("key %v: got %+v, want %+v", k, got, want)
		}
	}

	// The two models differ only in the model leg of the key. A codec that dropped a leg would
	// collapse them and silently predict one backend's appetite for another.
	if a, _ := back.Lookup(k1); a.Runs == 9 {
		t.Error("the model leg of the key is not participating; two profiles collapsed onto one aggregate")
	}
}

// TestDigestEncodingIsDeterministic matters because the digest is a rebuildable cache written on a
// hot path: an encoder whose byte output varied between identical inputs would make every rebuild
// look like a change.
func TestDigestEncodingIsDeterministic(t *testing.T) {
	build := func() Digest {
		d := NewDigest()
		for _, f := range []string{"zeta", "alpha", "mu"} {
			d.Put(DigestKey{Formula: f, StepID: "s", Model: "m"}, Aggregate{Runs: 2, MedianPeakCtxTokens: 1})
		}
		return d
	}
	first, err := EncodeDigest(build())
	if err != nil {
		t.Fatalf("EncodeDigest: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := EncodeDigest(build())
		if err != nil {
			t.Fatalf("EncodeDigest: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("encoding %d differs from the first:\n%s\n%s", i+2, again, first)
		}
	}
}

func TestDigestLoadSaveInjectedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokenomics-digest.json")

	// An absent digest is the cold-start state, not an error: every factory starts here and the
	// predicate's answer for it is observation-only, which is a decision rather than a failure.
	empty, err := LoadDigest(path)
	if err != nil {
		t.Fatalf("LoadDigest(absent) = %v, want nil", err)
	}
	if Coverage(empty) != 0 {
		t.Errorf("an absent digest reported %d aggregates", Coverage(empty))
	}

	d := NewDigest()
	k := DigestKey{Formula: "design-v7", StepID: "phase-1", Model: "qwen3.8-27b"}
	d.Put(k, Aggregate{Runs: 4, MedianPeakCtxTokens: 77_000, UpdatedAt: "2026-08-30T00:00:00Z"})
	if err := SaveDigest(path, d); err != nil {
		t.Fatalf("SaveDigest: %v", err)
	}
	back, err := LoadDigest(path)
	if err != nil {
		t.Fatalf("LoadDigest: %v", err)
	}
	if got, ok := back.Lookup(k); !ok || got.Runs != 4 || got.MedianPeakCtxTokens != 77_000 {
		t.Errorf("round trip through the filesystem lost data: %+v (ok=%v)", got, ok)
	}

	t.Run("a corrupt digest is an error, not a silent empty", func(t *testing.T) {
		bad := filepath.Join(dir, "corrupt.json")
		if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadDigest(bad); err == nil {
			t.Error("LoadDigest(corrupt) = nil error; a cache that cannot be read must say so rather than masquerade as a cold start")
		}
	})
}

// TestPeakAppetiteForIsTheJoin ties the codec to the predicate: the digest's absolute-peak answer —
// what freshFits reads — is "how much has this (formula, step, model) historically needed", and the
// run count has to travel with the number so the predicate can apply its learned_min_runs floor.
// (The additive decision reads the MARGINAL via AppetiteFor; the two are split by thread T1.)
func TestPeakAppetiteForIsTheJoin(t *testing.T) {
	d := NewDigest()
	k := DigestKey{Formula: "design-v7", StepID: "phase-2", Model: "qwen3.8-27b"}
	d.Put(k, Aggregate{Runs: 6, MedianPeakCtxTokens: 88_000, MaxPeakCtxTokens: 120_000})

	got := d.PeakAppetiteFor(k)
	if !got.Known || got.Tokens != 88_000 || got.Runs != 6 {
		t.Errorf("PeakAppetiteFor(present) = %+v, want {88000 6 true}", got)
	}

	missing := d.PeakAppetiteFor(DigestKey{Formula: "never-recorded", StepID: "s", Model: "m"})
	if missing.Known {
		t.Errorf("PeakAppetiteFor(absent) = %+v, want an unknown appetite", missing)
	}

	// A recorded aggregate whose peak was never captured is not a zero-token prediction — it is
	// no prediction. Treating it as zero would admit every step that has ever been observed.
	noPeak := NewDigest()
	noPeak.Put(k, Aggregate{Runs: 6, MedianPeakCtxTokens: 0})
	if a := noPeak.PeakAppetiteFor(k); a.Known {
		t.Errorf("PeakAppetiteFor(runs recorded, no peak) = %+v, want an unknown appetite", a)
	}
}

// TestAppetiteFor_ReturnsMarginalNotAbsolutePeak is thread T1's AC-1: the appetite the additive
// predicate reads is the step's MARGINAL growth (peak − start), not its ABSOLUTE peak occupancy.
// An absolute peak already includes the step's own baseline; adding it to the current occupancy —
// which is that same baseline — counts the baseline twice, the double-count T1 exists to remove.
//
// The reviewer's own operands: a step whose session started at 103485 tokens and peaked at 108226
// grew by 4741 across the step. The appetite the additive predicate needs is that 4741, never the
// 108226 that reads the baseline in a second time.
func TestAppetiteFor_ReturnsMarginalNotAbsolutePeak(t *testing.T) {
	k := DigestKey{Formula: "design-v7", StepID: "phase-1", Model: "qwen3.8-27b"}
	d := NewDigest()
	d.Put(k, AggregateSamples(marginals(k, [2]int64{103_485, 108_226}), foldedAt))

	got := d.AppetiteFor(k)
	if !got.Known {
		t.Fatalf("AppetiteFor(marginal sample) = %+v, want a known appetite", got)
	}
	if got.Tokens != 4_741 {
		t.Errorf("AppetiteFor(...).Tokens = %d, want 4741 (108226 − 103485, the marginal growth)", got.Tokens)
	}
	// Named so a fix that left AppetiteFor returning the absolute peak cannot pass by coincidence:
	// 108226 is exactly the value that double-counts the baseline.
	if got.Tokens == 108_226 {
		t.Errorf("AppetiteFor(...).Tokens = %d — the absolute peak, which re-counts the step baseline the "+
			"current occupancy already carries", got.Tokens)
	}
}

// TestAppetiteFor_MarginalAbsentIsUnknown is thread T1's AC-5: when no marginal is on record — an
// old on-disk digest written before the field existed, or an always-fragmenting step — the additive
// appetite degrades to UNKNOWN. It does NOT fall back to the absolute peak, because a silent
// fallback would reintroduce the exact double-count T1 removes, invisibly, on precisely the cache
// entries no one is looking at.
func TestAppetiteFor_MarginalAbsentIsUnknown(t *testing.T) {
	k := DigestKey{Formula: "design-v7", StepID: "phase-2", Model: "qwen3.8-27b"}

	// The "old digest" shape: a peak on record, no marginal field.
	d := NewDigest()
	d.Put(k, Aggregate{Runs: 6, MedianPeakCtxTokens: 88_000, MaxPeakCtxTokens: 120_000})
	if a := d.AppetiteFor(k); a.Known {
		t.Errorf("AppetiteFor(peak-only aggregate) = %+v, want unknown — a missing marginal must not fall "+
			"back to the absolute peak", a)
	}

	// And through the wire: an encoded body that carries no median_marginal_ctx_tokens key decodes
	// with the field at zero (it is ,omitempty) and reports unknown, without erroring or panicking.
	data, err := EncodeDigest(d)
	if err != nil {
		t.Fatalf("EncodeDigest: %v", err)
	}
	if strings.Contains(string(data), "median_marginal_ctx_tokens") {
		t.Fatal("the fixture encoded a marginal key; it no longer stands in for an old digest that never had one")
	}
	back, err := DecodeDigest(data)
	if err != nil {
		t.Fatalf("DecodeDigest(old-shape body) = %v, want nil — an additive field must not break the codec", err)
	}
	if a := back.AppetiteFor(k); a.Known {
		t.Errorf("AppetiteFor(decoded old digest) = %+v, want unknown", a)
	}
}

func TestDigestKeyIsAJoinKey(t *testing.T) {
	a := DigestKey{Formula: "one", StepID: "step", Model: "model"}
	b := DigestKey{Formula: "two", StepID: "step", Model: "model"}
	if a.String() == b.String() {
		t.Error("two formulas share a key; the name is joining nothing")
	}
	// Every leg must be recoverable from the encoded form, or an operator cannot read the cache.
	if !strings.Contains(a.String(), "one") || !strings.Contains(a.String(), "step") || !strings.Contains(a.String(), "model") {
		t.Errorf("key %q does not carry all three legs", a.String())
	}
	// The encoding must be INJECTIVE: no two distinct triples may share a key, or the cache
	// answers for the wrong step. The pair below is the exact collision a plain join has — the
	// separator migrates out of one leg and into the next — and it is the whole reason the legs
	// are escaped.
	//
	// Note the StepID. Spelling this with an EMPTY StepID proves nothing: the empty leg
	// contributes an extra separator, so the two keys differ whether or not the escaping does
	// anything at all, and the assertion passes against an escapeKeyLeg that is the identity.
	left := DigestKey{Formula: "one" + digestKeySeparator + "step", StepID: "x", Model: "m"}
	right := DigestKey{Formula: "one", StepID: "step" + digestKeySeparator + "x", Model: "m"}
	if left.String() == right.String() {
		t.Errorf("two distinct triples collide on the key %q; the separator is forgeable from a leg's own content", left.String())
	}

	// And the escape byte must itself be escaped, or the forgery just moves one level down: once
	// the escape passes through raw, a separator PRECEDED by an escape is ambiguous between "an
	// escaped separator" and "a delimiter after a leg that ended in an escape", and the split stops
	// being unique. This is that ambiguity spelled as a collision — the two encode identically the
	// moment escapeKeyLeg stops doubling the escape byte.
	//
	// A pair that merely contains an escape proves nothing here, because a separator elsewhere in
	// one of the legs decides it under either implementation.
	escLeft := DigestKey{Formula: digestKeyEscape, StepID: digestKeyEscape, Model: digestKeySeparator}
	escRight := DigestKey{Formula: digestKeySeparator + digestKeyEscape, StepID: digestKeyEscape, Model: ""}
	if escLeft.String() == escRight.String() {
		t.Errorf("two distinct triples collide on the key %q; the escape byte is not itself escaped", escLeft.String())
	}
}

// TestDecodeDigestRejectsAnUnknownVersion pins what the version is FOR. Asserting only that a "v"
// key exists would leave a reader that never looks at it passing — and a reader that cannot tell
// which shape it is holding is exactly what the version was added to prevent.
func TestDecodeDigestRejectsAnUnknownVersion(t *testing.T) {
	if _, err := DecodeDigest([]byte(`{"v":99,"entries":{}}`)); err == nil {
		t.Error("DecodeDigest(v=99) = nil error; a digest from a shape this binary does not speak must be refused, not silently ranged over")
	}
	// The control: the version this binary does speak still decodes, so the rejection above is
	// not simply "DecodeDigest always errors".
	if _, err := DecodeDigest([]byte(`{"v":1,"entries":{}}`)); err != nil {
		t.Errorf("DecodeDigest(v=%d) = %v, want nil", DigestVersion, err)
	}
}

// TestSaveDigestWritesAtomically is structural, and deliberately so: the guarantee is about what a
// CONCURRENT reader sees, which a single-process test can only observe by winning a race it is
// equally free to lose — a flaky assertion is worse than an honest structural one. What is pinned
// here is that the write goes through the helper that provides the guarantee at all, because
// swapping it for os.WriteFile is otherwise completely invisible.
func TestSaveDigestWritesAtomically(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "digest.go", nil, 0)
	if err != nil {
		t.Fatalf("parse digest.go: %v", err)
	}
	var body *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "SaveDigest" {
			body = fn
		}
	}
	if body == nil {
		t.Fatal("no SaveDigest in digest.go; the guard proves nothing")
	}
	atomic, plain := false, false
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "WriteFileAtomic":
			atomic = true
		case "WriteFile":
			plain = true
		}
		return true
	})
	if !atomic {
		t.Error("SaveDigest does not call WriteFileAtomic; a reader in the same run can see a half-written cache")
	}
	if plain {
		t.Error("SaveDigest calls os.WriteFile directly, which is the non-atomic path")
	}
}

// TestDigestEnvelopeIsVersioned pins the house rule that every durable record carries a version,
// so a later phase can change the aggregate shape without a reader guessing which it is holding.
func TestDigestEnvelopeIsVersioned(t *testing.T) {
	data, err := EncodeDigest(NewDigest())
	if err != nil {
		t.Fatalf("EncodeDigest: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["v"]; !ok {
		t.Error(`encoded digest has no "v" key`)
	}
	if entries, ok := raw["entries"]; !ok {
		t.Error(`encoded digest has no "entries" key`)
	} else if string(entries) == "null" {
		t.Error(`"entries" marshalled to null; a consumer cannot range over it`)
	}
}
