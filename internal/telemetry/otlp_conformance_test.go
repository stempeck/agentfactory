package telemetry

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This file is the phase's conformance gate, and it was written BEFORE otlp.go existed.
// The ordering is the point: an encoder written first, with fixtures generated afterwards
// from its own output, produces a suite that compares the encoder to itself. That suite goes
// green while proving nothing, which is precisely how the previous attempt at this wire
// format shipped unproven. The validator below derives its rules from a pinned upstream
// release (testdata/OTLP_PROTO_VERSION, testdata/otlp-schema.json) and is itself proven in
// both directions — it must accept the genuine upstream payload and reject a corpus of
// single-deviation mutations of it.

const (
	testdataDir  = "testdata"
	manifestFile = "testdata/manifest.json"
	schemaFile   = "testdata/otlp-schema.json"
	versionFile  = "testdata/OTLP_PROTO_VERSION"
)

// otlpSchema is the pinned per-message contract, transcribed from the release's .proto files
// into testdata/otlp-schema.json. It lives in a data file rather than in Go so that re-pinning
// a newer release is a visible data change instead of a silent code edit.
type otlpSchema struct {
	Messages map[string]struct {
		Keys []string `json:"keys"`
	} `json:"messages"`
	HexIDFields         map[string]json.RawMessage `json:"hex_id_fields"`
	DecimalStringFields []string                   `json:"decimal_string_fields"`
	JSONNumberFields    []string                   `json:"json_number_fields"`
	Enums               map[string]struct {
		Min            int      `json:"min"`
		Max            int      `json:"max"`
		NamesForbidden []string `json:"names_forbidden"`
	} `json:"enums"`
	HTTP struct {
		ContentType       string `json:"content_type"`
		DefaultTracesPath string `json:"default_traces_path"`
	} `json:"http"`

	hexWidths map[string]int
	decimal   map[string]bool
	number    map[string]bool
}

type fixtureEntry struct {
	File       string `json:"file"`
	Verdict    string `json:"verdict"`
	Provenance string `json:"provenance"`
	Rule       string `json:"rule"`
	SHA256     string `json:"sha256"`
	Bytes      int    `json:"bytes"`
}

type fixtureManifest struct {
	Fixtures []fixtureEntry `json:"fixtures"`
}

// childMessage maps a (parent message, key) pair to the message type of the value. A key
// absent from this map is a leaf as far as the walker is concerned. Derived from the same
// pinned .proto files as otlp-schema.json.
var childMessage = map[string]string{
	"ExportTraceServiceRequest.resourceSpans": "ResourceSpans",
	"ResourceSpans.resource":                  "Resource",
	"ResourceSpans.scopeSpans":                "ScopeSpans",
	"Resource.attributes":                     "KeyValue",
	"ScopeSpans.scope":                        "InstrumentationScope",
	"ScopeSpans.spans":                        "Span",
	"InstrumentationScope.attributes":         "KeyValue",
	"Span.attributes":                         "KeyValue",
	"Span.events":                             "Event",
	"Span.links":                              "Link",
	"Span.status":                             "Status",
	"Event.attributes":                        "KeyValue",
	"Link.attributes":                         "KeyValue",
	"KeyValue.value":                          "AnyValue",
	"AnyValue.arrayValue":                     "ArrayValue",
	"AnyValue.kvlistValue":                    "KeyValueList",
	"ArrayValue.values":                       "AnyValue",
	"KeyValueList.values":                     "KeyValue",
}

var (
	hexOnly     = regexp.MustCompile(`^[0-9a-fA-F]+$`)
	lowerHex    = regexp.MustCompile(`^[0-9a-f]+$`)
	digitsOnly  = regexp.MustCompile(`^[0-9]+$`)
	releaseTag  = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	camelCaseRE = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)
)

// violation names the rule that was broken and where. The rule name is what the manifest
// records per reject fixture, so a validator that catches the wrong thing for the right
// reason still fails.
type violation struct {
	Rule string
	At   string
	Msg  string
}

func (v violation) String() string { return fmt.Sprintf("%s at %s: %s", v.Rule, v.At, v.Msg) }

func loadOTLPSchema(t *testing.T) *otlpSchema {
	t.Helper()
	raw, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatalf("reading %s: %v", schemaFile, err)
	}
	var s otlpSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("parsing %s: %v", schemaFile, err)
	}
	if len(s.Messages) == 0 {
		t.Fatalf("%s declares no messages — the validator would accept anything", schemaFile)
	}
	s.hexWidths = map[string]int{}
	for k, v := range s.HexIDFields {
		var w int
		if json.Unmarshal(v, &w) == nil {
			s.hexWidths[k] = w
		}
	}
	if len(s.hexWidths) == 0 {
		t.Fatalf("%s declares no hex id fields", schemaFile)
	}
	s.decimal = map[string]bool{}
	for _, f := range s.DecimalStringFields {
		s.decimal[f] = true
	}
	s.number = map[string]bool{}
	for _, f := range s.JSONNumberFields {
		s.number[f] = true
	}
	return &s
}

// decodeLoose decodes with UseNumber so the JSON string-vs-number distinction survives, which
// is the whole point of rules 2 and 5. It deliberately does NOT decode through the emission
// structs: the spec requires receivers to accept 64-bit fields as either numbers or strings,
// and Go's string struct-tag option rejects the number form, so reusing the emission types
// here would import an asymmetry the spec forbids.
func decodeLoose(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// validateOTLP walks a decoded OTLP/JSON trace request and reports every rule it breaks.
// It is intentionally case-INSENSITIVE about hex ids: the spec calls the encoding
// case-insensitive, so an uppercase upstream payload and this project's lowercase output are
// both conformant. Producer-side lowercase is asserted separately, on our own output only.
func validateOTLP(s *otlpSchema, root any) []violation {
	var vs []violation
	walkOTLP(s, "ExportTraceServiceRequest", "$", root, &vs)
	if obj, ok := root.(map[string]any); ok {
		spans, present := obj["resourceSpans"]
		if !present {
			vs = append(vs, violation{"root-shape", "$", "no resourceSpans key"})
		} else if arr, isArr := spans.([]any); !isArr || len(arr) == 0 {
			vs = append(vs, violation{"root-shape", "$.resourceSpans", "not a non-empty array"})
		}
	} else {
		vs = append(vs, violation{"root-shape", "$", "payload is not a JSON object"})
	}
	return vs
}

func walkOTLP(s *otlpSchema, msg, at string, node any, vs *[]violation) {
	switch v := node.(type) {
	case nil:
		*vs = append(*vs, violation{"no-null-values", at, "null value; an absent field must be omitted, never emitted as null"})
	case []any:
		for i, item := range v {
			walkOTLP(s, msg, fmt.Sprintf("%s[%d]", at, i), item, vs)
		}
	case map[string]any:
		allowed := map[string]bool{}
		known := false
		if def, ok := s.Messages[msg]; ok {
			known = true
			for _, k := range def.Keys {
				allowed[k] = true
			}
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			kat := at + "." + k
			if !camelCaseRE.MatchString(k) {
				*vs = append(*vs, violation{"lower-camel-case-keys", kat,
					fmt.Sprintf("key %q is not lowerCamelCase; original proto field names are not valid JSON keys", k)})
			}
			if known && !allowed[k] {
				*vs = append(*vs, violation{"message-key-allowlist", kat,
					fmt.Sprintf("key %q is not a field of %s in the pinned release", k, msg)})
			}
			checkLeaf(s, k, kat, v[k], vs)
			if child, ok := childMessage[msg+"."+k]; ok {
				walkOTLP(s, child, kat, v[k], vs)
			}
		}
	}
}

// checkLeaf applies the field-level encoding rules. Enum handling runs before the generic
// number check so that an enum emitted as its name is reported as an enum violation rather
// than as a generic type error — the manifest pins which rule each reject fixture proves.
func checkLeaf(s *otlpSchema, key, at string, val any, vs *[]violation) {
	if val == nil {
		return // reported by walkOTLP
	}
	if width, isID := s.hexWidths[key]; isID {
		str, ok := val.(string)
		if !ok {
			*vs = append(*vs, violation{"hex-id-encoding", at, "id is not a JSON string"})
			return
		}
		if !hexOnly.MatchString(str) {
			*vs = append(*vs, violation{"hex-id-encoding", at,
				fmt.Sprintf("id %q is not hex-encoded; OTLP/JSON does not base64 trace and span ids", str)})
			return
		}
		if len(str) != width*2 {
			*vs = append(*vs, violation{"hex-id-width", at,
				fmt.Sprintf("id is %d hex chars, want %d (%d bytes)", len(str), width*2, width)})
		}
		return
	}
	if enum, isEnum := s.Enums[key]; isEnum {
		num, ok := val.(json.Number)
		if !ok {
			*vs = append(*vs, violation{"integer-enums", at,
				fmt.Sprintf("enum %q is %T; enum name strings must not be used", key, val)})
			return
		}
		n, err := num.Int64()
		if err != nil {
			*vs = append(*vs, violation{"integer-enums", at, "enum is not an integer"})
			return
		}
		if n < int64(enum.Min) || n > int64(enum.Max) {
			*vs = append(*vs, violation{"integer-enums", at,
				fmt.Sprintf("enum value %d outside the pinned range %d..%d", n, enum.Min, enum.Max)})
		}
		return
	}
	if s.decimal[key] {
		str, ok := val.(string)
		if !ok {
			*vs = append(*vs, violation{"sixty-four-bit-decimal-string", at,
				fmt.Sprintf("64-bit field %q is %T; proto3 JSON encodes 64-bit integers as decimal strings", key, val)})
			return
		}
		if !digitsOnly.MatchString(str) {
			*vs = append(*vs, violation{"sixty-four-bit-decimal-string", at,
				fmt.Sprintf("64-bit field %q is not a decimal string: %q", key, str)})
		}
		return
	}
	if s.number[key] {
		if _, ok := val.(json.Number); !ok {
			*vs = append(*vs, violation{"thirty-two-bit-json-number", at,
				fmt.Sprintf("32-bit field %q is %T; it must be a JSON number, not a string", key, val)})
		}
	}
}

func loadFixtureManifest(t *testing.T) fixtureManifest {
	t.Helper()
	raw, err := os.ReadFile(manifestFile)
	if err != nil {
		t.Fatalf("reading %s: %v", manifestFile, err)
	}
	var m fixtureManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing %s: %v", manifestFile, err)
	}
	// An empty corpus would let every assertion below pass vacuously — the exact way a
	// fixture set becomes decorative. Copied from internal/formula/parity_test.go.
	if len(m.Fixtures) == 0 {
		t.Fatalf("%s lists no fixtures", manifestFile)
	}
	return m
}

func TestOTLPConformanceAgainstPinnedFixtures(t *testing.T) {
	// This test fails against: a validator that accepts everything (the upstream fixture is
	// the only accept case, so an always-accept validator survives it — but the reject corpus
	// kills it); a validator that rejects everything (the upstream case kills it); an encoder
	// that base64s ids, snake_cases keys, emits 64-bit nanos as numbers, or names its enums;
	// and a fixture set that was quietly regenerated from our own encoder (the recorded
	// sha256 of the upstream bytes is re-verified here).
	schema := loadOTLPSchema(t)
	manifest := loadFixtureManifest(t)

	t.Run("the pinned release is recorded and well formed", func(t *testing.T) {
		raw, err := os.ReadFile(versionFile)
		if err != nil {
			t.Fatalf("reading %s: %v", versionFile, err)
		}
		lines := strings.Fields(strings.TrimSpace(string(raw)))
		if len(lines) == 0 {
			t.Fatalf("%s is empty", versionFile)
		}
		if !releaseTag.MatchString(lines[0]) {
			t.Errorf("%s line 1 = %q, want a release tag like v1.2.3", versionFile, lines[0])
		}
	})

	t.Run("every fixture on disk has a manifest entry", func(t *testing.T) {
		listed := map[string]bool{}
		for _, f := range manifest.Fixtures {
			listed[f.File] = true
		}
		var found int
		err := filepath.WalkDir(testdataDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(p) != ".json" {
				return err
			}
			rel := filepath.ToSlash(strings.TrimPrefix(p, testdataDir+string(filepath.Separator)))
			if rel == "manifest.json" || rel == "otlp-schema.json" {
				return nil
			}
			found++
			if !listed[rel] {
				t.Errorf("fixture %s has no manifest entry — an unrecorded fixture proves nothing", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", testdataDir, err)
		}
		if found == 0 {
			t.Fatal("no fixtures found on disk")
		}
	})

	t.Run("upstream bytes are unmodified", func(t *testing.T) {
		var checked int
		for _, f := range manifest.Fixtures {
			if f.Provenance != "upstream" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(testdataDir, filepath.FromSlash(f.File)))
			if err != nil {
				t.Fatalf("reading %s: %v", f.File, err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != f.SHA256 {
				t.Errorf("%s sha256 = %s, manifest records %s — the upstream fixture has been edited, "+
					"which is how a conformance corpus silently becomes a self-portrait of the encoder", f.File, got, f.SHA256)
			}
			if f.Bytes != 0 && len(raw) != f.Bytes {
				t.Errorf("%s is %d bytes, manifest records %d", f.File, len(raw), f.Bytes)
			}
			checked++
		}
		if checked == 0 {
			t.Fatal("no upstream-provenance fixture in the manifest; the corpus has no anchor to the spec")
		}
	})

	t.Run("the validator accepts the genuine upstream payload", func(t *testing.T) {
		var checked int
		for _, f := range manifest.Fixtures {
			if f.Verdict != "accept" {
				continue
			}
			checked++
			raw, err := os.ReadFile(filepath.Join(testdataDir, filepath.FromSlash(f.File)))
			if err != nil {
				t.Fatalf("reading %s: %v", f.File, err)
			}
			node, err := decodeLoose(raw)
			if err != nil {
				t.Fatalf("decoding %s: %v", f.File, err)
			}
			if vs := validateOTLP(schema, node); len(vs) != 0 {
				t.Errorf("%s must validate clean; got %d violations:\n%s", f.File, len(vs), formatViolations(vs))
			}
		}
		if checked == 0 {
			t.Fatal("no accept fixture — a validator that rejects everything would pass this suite")
		}
	})

	t.Run("the validator rejects each single-rule deviation", func(t *testing.T) {
		var checked int
		for _, f := range manifest.Fixtures {
			if f.Verdict != "reject" {
				continue
			}
			checked++
			f := f
			t.Run(f.File, func(t *testing.T) {
				raw, err := os.ReadFile(filepath.Join(testdataDir, filepath.FromSlash(f.File)))
				if err != nil {
					t.Fatalf("reading %s: %v", f.File, err)
				}
				node, err := decodeLoose(raw)
				if err != nil {
					t.Fatalf("decoding %s: %v", f.File, err)
				}
				vs := validateOTLP(schema, node)
				if len(vs) == 0 {
					t.Fatalf("%s must be rejected but validated clean", f.File)
				}
				if f.Rule == "" {
					return
				}
				for _, v := range vs {
					if v.Rule == f.Rule {
						return
					}
				}
				t.Errorf("%s was rejected, but not for rule %q; got:\n%s", f.File, f.Rule, formatViolations(vs))
			})
		}
		if checked == 0 {
			t.Fatal("no reject fixture — a validator that accepts everything would pass this suite")
		}
	})

	t.Run("the encoder's own payload conforms", func(t *testing.T) {
		payload, err := EncodeOTLPTraces(conformanceEvents())
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		node, err := decodeLoose(payload)
		if err != nil {
			t.Fatalf("emitted payload is not valid JSON: %v", err)
		}
		if vs := validateOTLP(schema, node); len(vs) != 0 {
			t.Errorf("emitted payload breaks %d rules:\n%s\npayload: %s", len(vs), formatViolations(vs), payload)
		}
	})

	t.Run("emitted ids are lowercase hex and are not base64", func(t *testing.T) {
		payload, err := EncodeOTLPTraces(conformanceEvents())
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		node, err := decodeLoose(payload)
		if err != nil {
			t.Fatalf("decoding emitted payload: %v", err)
		}
		ids := collectIDs(node)
		if len(ids) == 0 {
			t.Fatal("emitted payload carries no trace or span id")
		}
		for at, id := range ids {
			// Lowercase is this project's own convention, asserted only on our output. The
			// spec's encoding is case-insensitive, so the uppercase upstream fixture is
			// equally conformant and must not be "corrected" to match this.
			if !lowerHex.MatchString(id) {
				t.Errorf("%s = %q, want lowercase hex", at, id)
			}
			decoded, err := hex.DecodeString(id)
			if err != nil {
				t.Errorf("%s = %q is not decodable hex: %v", at, id, err)
				continue
			}
			if allZero(decoded) {
				t.Errorf("%s is all zero bytes", at)
			}
			// The explicit base64 negative. A Go []byte field marshals to base64 by default,
			// and that default is exactly the defect this corpus exists to catch, so the
			// suite states it as its own assertion rather than trusting the hex regexp.
			if b64 := base64.StdEncoding.EncodeToString(decoded); id == b64 {
				t.Errorf("%s is base64-encoded", at)
			}
			if strings.ContainsAny(id, "+/=") {
				t.Errorf("%s = %q contains base64 alphabet characters", at, id)
			}
		}
	})

	t.Run("no emitted key anywhere contains an underscore", func(t *testing.T) {
		payload, err := EncodeOTLPTraces(conformanceEvents())
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		node, err := decodeLoose(payload)
		if err != nil {
			t.Fatalf("decoding emitted payload: %v", err)
		}
		for _, k := range collectKeys(node) {
			if strings.Contains(k, "_") {
				t.Errorf("emitted key %q is snake_case; proto field names are not valid JSON keys", k)
			}
		}
	})

	t.Run("the exporter's HTTP contract matches the pinned schema", func(t *testing.T) {
		// The pinned release states both of these as requirements, and export.go states them
		// again as constants. Without this assertion the two copies could diverge — re-pinning
		// a release that moved the default path would update the fixture and leave the
		// exporter posting to the old one, with nothing failing.
		if contentTypeJSON != schema.HTTP.ContentType {
			t.Errorf("export content type = %q, pinned schema says %q", contentTypeJSON, schema.HTTP.ContentType)
		}
		if defaultTracesPath != schema.HTTP.DefaultTracesPath {
			t.Errorf("default traces path = %q, pinned schema says %q", defaultTracesPath, schema.HTTP.DefaultTracesPath)
		}
	})

	t.Run("no emitted enum is ever spelled as its name", func(t *testing.T) {
		// The pinned schema carries the forbidden spellings; asserting against them keeps the
		// fixture's own data load-bearing rather than decorative.
		payload, err := EncodeOTLPTraces(conformanceEvents())
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		var checked int
		for field, enum := range schema.Enums {
			for _, name := range enum.NamesForbidden {
				checked++
				if bytes.Contains(payload, []byte(`"`+name+`"`)) {
					t.Errorf("payload spells enum %q as %q; only integer enum values are legal", field, name)
				}
			}
		}
		if checked == 0 {
			t.Fatal("the pinned schema lists no forbidden enum spellings; this assertion is vacuous")
		}
	})

	t.Run("the af identity attributes ride on the resource", func(t *testing.T) {
		payload, err := EncodeOTLPTraces(conformanceEvents())
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		var doc struct {
			ResourceSpans []struct {
				Resource struct {
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							StringValue *string `json:"stringValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"resource"`
			} `json:"resourceSpans"`
		}
		if err := json.Unmarshal(payload, &doc); err != nil {
			t.Fatalf("decoding emitted payload: %v", err)
		}
		if len(doc.ResourceSpans) == 0 {
			t.Fatal("no resourceSpans emitted")
		}
		got := map[string]string{}
		for _, a := range doc.ResourceSpans[0].Resource.Attributes {
			if a.Value.StringValue == nil {
				t.Errorf("resource attribute %q has no stringValue", a.Key)
				continue
			}
			got[a.Key] = *a.Value.StringValue
		}
		for _, want := range []string{"service.name", "af.agent", "af.worktree_id", "af.formula_instance"} {
			if got[want] == "" {
				t.Errorf("resource attribute %q missing or empty; attributes are a KeyValue list, not a flat object", want)
			}
		}
	})
}

// conformanceEvents is the af-side input the encoder is exercised with: one closed step plus
// a second step in the same formula instance, which is the shape a bounded drain actually
// carries.
func conformanceEvents() []StepEvent {
	return []StepEvent{
		{
			V: SchemaVersion, Event: EventStepEnd, TS: "2026-07-22T18:31:10.500Z",
			Agent: "design-v7", WorktreeID: "wt-03478d", Formula: "design-v7",
			InstanceID: "af-329-instance", StepID: "phase2-analysis", StepSeq: 7,
			StepTitle: "Phase 2: analysis", SessionID: "5f2c1d90-0000-4000-8000-000000000001",
			Model: "fable-5", ModelSource: ModelSourceModelsJSON, Verb: "done", VerbMS: 77,
			DurationMS: 6388, Status: StatusClosed,
		},
		{
			V: SchemaVersion, Event: EventStepEnd, TS: "2026-07-22T18:31:25.000Z",
			Agent: "design-v7", WorktreeID: "wt-03478d", Formula: "design-v7",
			InstanceID: "af-329-instance", StepID: "phase3-synthesis", StepSeq: 8,
			StepTitle: "Phase 3: synthesis", SessionID: "5f2c1d90-0000-4000-8000-000000000001",
			Model: "fable-5", ModelSource: ModelSourceOverride, Verb: "done", VerbMS: 81,
			DurationMS: 10000, Status: StatusSkipped,
		},
	}
}

func formatViolations(vs []violation) string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, "  - "+v.String())
	}
	return strings.Join(out, "\n")
}

func collectIDs(node any) map[string]string {
	out := map[string]string{}
	var walk func(at string, n any)
	walk = func(at string, n any) {
		switch v := n.(type) {
		case []any:
			for i, item := range v {
				walk(fmt.Sprintf("%s[%d]", at, i), item)
			}
		case map[string]any:
			for k, val := range v {
				at := at + "." + k
				if k == "traceId" || k == "spanId" || k == "parentSpanId" {
					if s, ok := val.(string); ok {
						out[at] = s
					}
				}
				walk(at, val)
			}
		}
	}
	walk("$", node)
	return out
}

func collectKeys(node any) []string {
	seen := map[string]bool{}
	var walk func(n any)
	walk = func(n any) {
		switch v := n.(type) {
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for k, val := range v {
				seen[k] = true
				walk(val)
			}
		}
	}
	walk(node)
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
