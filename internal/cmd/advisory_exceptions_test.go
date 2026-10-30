package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// advisoryException is one entry of .github/advisory-exceptions.json: a known advisory the
// advisory audit is told to accept, with the evidence that the affected path is unused.
type advisoryException struct {
	ID        string    `json:"id"`
	Aliases   *[]string `json:"aliases"`
	Package   string    `json:"package"`
	Version   string    `json:"version"`
	Reason    string    `json:"reason"`
	Evidence  string    `json:"evidence"`
	Reviewed  string    `json:"reviewed"`
	RecheckBy string    `json:"recheck_by"`
}

const advisoryRegisterPath = ".github/advisory-exceptions.json"

func decodeAdvisoryExceptions(raw []byte) ([]advisoryException, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var entries []advisoryException
	if err := dec.Decode(&entries); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the register array")
	}
	return entries, nil
}

func parseISODate(s string) (time.Time, bool) {
	d, err := time.Parse("2006-01-02", s)
	return d, err == nil && d.Format("2006-01-02") == s
}

// validateAdvisoryExceptions never compares against today's date: a recheck_by that has passed
// is the audit's job to report (with its injectable clock), not a reason for every later commit
// of this repo to fail its unit tests.
func validateAdvisoryExceptions(raw []byte) []string {
	entries, err := decodeAdvisoryExceptions(raw)
	if err != nil {
		return []string{"decoding the register: " + err.Error()}
	}
	if len(entries) == 0 {
		return []string{"the register is empty"}
	}
	var out []string
	seen := map[string]string{}
	claim := func(label, name string) {
		if prev, dup := seen[name]; dup {
			out = append(out, fmt.Sprintf("%s: %q already used by %s", label, name, prev))
			return
		}
		seen[name] = label
	}
	for i, e := range entries {
		label := fmt.Sprintf("entry %d (%s)", i, e.ID)
		for field, v := range map[string]string{"id": e.ID, "package": e.Package, "version": e.Version, "reason": e.Reason, "evidence": e.Evidence, "reviewed": e.Reviewed, "recheck_by": e.RecheckBy} {
			if strings.TrimSpace(v) == "" {
				out = append(out, fmt.Sprintf("%s: %s is empty", label, field))
			}
		}
		if e.Aliases == nil {
			out = append(out, label+": aliases is missing (write [] when the advisory has no alias)")
		} else {
			for _, a := range *e.Aliases {
				if strings.TrimSpace(a) == "" {
					out = append(out, label+": empty alias")
					continue
				}
				claim(label, a)
			}
		}
		if e.ID != "" {
			claim(label, e.ID)
		}
		reviewed, okR := parseISODate(e.Reviewed)
		recheck, okC := parseISODate(e.RecheckBy)
		if !okR {
			out = append(out, fmt.Sprintf("%s: reviewed %q is not an ISO date (YYYY-MM-DD)", label, e.Reviewed))
		}
		if !okC {
			out = append(out, fmt.Sprintf("%s: recheck_by %q is not an ISO date (YYYY-MM-DD)", label, e.RecheckBy))
		}
		if okR && okC {
			if recheck.Before(reviewed) {
				out = append(out, fmt.Sprintf("%s: recheck_by %s is before reviewed %s", label, e.RecheckBy, e.Reviewed))
			}
			if recheck.After(reviewed.AddDate(0, 0, 30)) {
				out = append(out, fmt.Sprintf("%s: recheck_by %s is more than 30 days after reviewed %s", label, e.RecheckBy, e.Reviewed))
			}
		}
	}
	return out
}

func TestAdvisoryExceptionsSchema(t *testing.T) {
	entry := func(id, aliases, reviewed, recheck string) string {
		return fmt.Sprintf(`{"id":%q,"aliases":%s,"package":"p","version":"1","reason":"r","evidence":"e","reviewed":%q,"recheck_by":%q}`, id, aliases, reviewed, recheck)
	}
	t.Run("each malformed register is rejected", func(t *testing.T) {
		for name, raw := range map[string]string{
			"unknown field":         `[{"id":"X","aliases":[],"package":"p","version":"1","reason":"r","evidence":"e","reviewed":"2026-10-27","recheck_by":"2026-11-26","severity":"high"}]`,
			"empty reason":          `[{"id":"X","aliases":[],"package":"p","version":"1","reason":" ","evidence":"e","reviewed":"2026-10-27","recheck_by":"2026-11-26"}]`,
			"missing aliases":       `[{"id":"X","package":"p","version":"1","reason":"r","evidence":"e","reviewed":"2026-10-27","recheck_by":"2026-11-26"}]`,
			"slash date":            "[" + entry("X", "[]", "2026/10/27", "2026-11-26") + "]",
			"recheck 31 days later": "[" + entry("X", "[]", "2026-10-27", "2026-11-27") + "]",
			"recheck before review": "[" + entry("X", "[]", "2026-10-27", "2026-10-26") + "]",
			"duplicate id":          "[" + entry("X", "[]", "2026-10-27", "2026-11-26") + "," + entry("X", "[]", "2026-10-27", "2026-11-26") + "]",
			"alias equals an id":    "[" + entry("X", `["Y"]`, "2026-10-27", "2026-11-26") + "," + entry("Y", "[]", "2026-10-27", "2026-11-26") + "]",
			"empty register":        "[]",
		} {
			if errs := validateAdvisoryExceptions([]byte(raw)); len(errs) == 0 {
				t.Errorf("%s: accepted %s", name, raw)
			}
		}
	})

	t.Run("a long-past recheck_by is not a schema error", func(t *testing.T) {
		raw := "[" + entry("X", `["CVE-1"]`, "2020-01-01", "2020-01-31") + "]"
		if errs := validateAdvisoryExceptions([]byte(raw)); len(errs) != 0 {
			t.Errorf("a valid but expired entry was rejected: %v", errs)
		}
	})

	for _, e := range validateAdvisoryExceptions([]byte(readRepoFile(t, advisoryRegisterPath))) {
		t.Errorf("%s: %s", advisoryRegisterPath, e)
	}
}
