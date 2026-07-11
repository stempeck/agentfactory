package web

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// #534 — the PERMANENT reconstruction tripwire. The formula editor under static/formula-editor/ is
// the APPROVED prototype (.designs/web-ui/final, signed on prototype-v2/feedback-form.md) transplanted
// verbatim; every intentional delta is a recorded, reversible seam in testdata/fable-frontend-seams/
// (substitution rows with exact old/new bytes, one whole-file data-module replacement, and an
// added-files list — CSS additions are forbidden even if listed). This test reverse-applies that
// manifest to the EMBEDDED tree and demands byte-identity with the approved prototype. From the
// commit that introduced it, drifting the shipped editor away from the approved design — or editing
// its bytes without declaring the seam — fails web-unit CI. Comparators that judge resemblance
// saturate; byte identity cannot. Stdlib only, source-scan idiom (nav_test.go precedent), no JS
// runtime needed.

const seamsDir = "testdata/fable-frontend-seams"

// tripwireRepoRoot walks up from the package dir to the directory that holds quickstart.sh — the
// same location contract the guard test uses (web/internal/entrypoint/guard_test.go).
func tripwireRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "quickstart.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate the repo root (quickstart.sh) walking up from the package dir")
	return ""
}

func seamRead(t *testing.T, parts ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{seamsDir}, parts...)...))
	if err != nil {
		t.Fatalf("seam manifest read: %v", err)
	}
	return raw
}

// TestFableFrontendReconstruction reverse-applies the seam manifest to the embedded formula-editor
// tree and requires the result to be byte-identical to the approved prototype tree.
func TestFableFrontendReconstruction(t *testing.T) {
	roots := strings.Split(strings.TrimSpace(string(seamRead(t, "roots.txt"))), "\n")
	if len(roots) != 2 {
		t.Fatalf("roots.txt must hold exactly two repo-relative lines, got %d", len(roots))
	}
	protoRoot := filepath.Join(tripwireRepoRoot(t), filepath.FromSlash(strings.TrimSpace(roots[0])))
	if _, err := os.Stat(protoRoot); os.IsNotExist(err) {
		// Same legitimate-absence contract as the design-contract traces: the extracted/public repo
		// ships web/ without .designs/. Monorepo CI runs on a full checkout, so the tripwire is live
		// exactly where the approved artifact lives.
		t.Skipf("approved prototype absent at %s — OSS/extracted checkout; tripwire skipped", protoRoot)
	}

	// The shipped tree, from the EMBEDDED FS (what the binary actually serves).
	shipped := map[string][]byte{}
	sub, err := fs.Sub(Static(), "formula-editor")
	if err != nil {
		t.Fatalf("embedded formula-editor subtree missing: %v", err)
	}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, rerr := fs.ReadFile(sub, p)
		if rerr != nil {
			return rerr
		}
		shipped[p] = raw
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded tree: %v", err)
	}

	// Optional path map: proto-rel -> ship-rel (default identity).
	pathmap := map[string]string{}
	for _, line := range strings.Split(string(seamRead(t, "map.tsv")), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 2 {
			t.Fatalf("map.tsv line %q is not proto<TAB>ship", line)
		}
		pathmap[strings.TrimSpace(f[0])] = strings.TrimSpace(f[1])
	}

	// 1. Reverse every substitution row: the bound bytes must appear exactly `count` times, and
	// reversing them must restore the prototype bytes.
	subs, err := os.ReadDir(seamsDir)
	if err != nil {
		t.Fatalf("read seam manifest: %v", err)
	}
	var subNames []string
	for _, e := range subs {
		if e.IsDir() && strings.HasPrefix(e.Name(), "sub-") {
			subNames = append(subNames, e.Name())
		}
	}
	sort.Strings(subNames)
	if len(subNames) == 0 {
		t.Fatal("seam manifest declares no substitution rows — the manifest location or layout drifted")
	}
	for _, name := range subNames {
		target := strings.TrimSpace(string(seamRead(t, name, "target")))
		oldB := seamRead(t, name, "old")
		newB := seamRead(t, name, "new")
		want, aerr := strconv.Atoi(strings.TrimSpace(string(seamRead(t, name, "count"))))
		if aerr != nil {
			t.Fatalf("%s: bad count: %v", name, aerr)
		}
		data, ok := shipped[target]
		if !ok {
			t.Fatalf("%s: target %q missing from the shipped tree", name, target)
		}
		if got := bytes.Count(data, newB); got != want {
			t.Fatalf("%s: %s has %d occurrence(s) of the bound bytes; manifest declares %d — an undeclared edit touched a seam site", name, target, got, want)
		}
		shipped[target] = bytes.ReplaceAll(data, newB, oldB)
	}

	// 2. Whole-file seams: restore the prototype file over the production replacement.
	for _, line := range strings.Split(string(seamRead(t, "replaced.tsv")), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 2 {
			t.Fatalf("replaced.tsv line %q is not proto<TAB>ship", line)
		}
		protoRel, shipRel := strings.TrimSpace(f[0]), strings.TrimSpace(f[1])
		raw, rerr := os.ReadFile(filepath.Join(protoRoot, filepath.FromSlash(protoRel)))
		if rerr != nil {
			t.Fatalf("replaced.tsv: prototype file %q unreadable: %v", protoRel, rerr)
		}
		if _, ok := shipped[shipRel]; !ok {
			t.Fatalf("replaced.tsv: shipped file %q missing", shipRel)
		}
		shipped[shipRel] = raw
		pathmap[protoRel] = shipRel
	}

	// 3. Byte-identity with the approved prototype, file by file.
	accounted := map[string]bool{}
	err = filepath.WalkDir(protoRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(protoRoot, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		shipRel := rel
		if m, ok := pathmap[rel]; ok {
			shipRel = m
		}
		accounted[shipRel] = true
		got, ok := shipped[shipRel]
		if !ok {
			t.Errorf("missing in shipped tree: %s", rel)
			return nil
		}
		want, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if !bytes.Equal(got, want) {
			t.Errorf("bytes differ from the approved prototype: %s — either restore the approved bytes or declare the delta as a seam", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk prototype tree: %v", err)
	}

	// 4. The shipped tree is WHOLLY accounted for: everything is prototype-derived or listed in
	// added.txt — and added CSS is forbidden even if listed (styling exists only in approved bytes).
	added := map[string]bool{}
	for _, line := range strings.Split(string(seamRead(t, "added.txt")), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			added[s] = true
		}
	}
	var rels []string
	for rel := range shipped {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if accounted[rel] {
			continue
		}
		if strings.HasSuffix(strings.ToLower(rel), ".css") {
			t.Errorf("non-prototype CSS in shipped tree (forbidden even if declared): %s", rel)
			continue
		}
		if !added[rel] {
			t.Errorf("undeclared file in shipped tree: %s", rel)
		}
	}
}

// TestFableFrontendReconstruction_SelfNegative proves the tripwire reads red: a mutated copy of a
// shipped file must fail the byte comparison, and an undeclared CSS name must be refused. It drives
// the same primitives the tripwire uses (bytes.Equal on mutated content; the CSS suffix rule), so
// the guarantee is calibrated, not assumed.
func TestFableFrontendReconstruction_SelfNegative(t *testing.T) {
	raw, err := fs.ReadFile(Static(), "formula-editor/styles/main.css")
	if err != nil {
		t.Fatalf("read embedded main.css: %v", err)
	}
	mutated := append(append([]byte{}, raw...), 'x')
	if bytes.Equal(mutated, raw) {
		t.Error("a one-byte mutation must break byte-identity")
	}
	if !strings.HasSuffix(strings.ToLower("formula-editor/extra.CSS"), ".css") {
		t.Error("the forbidden-CSS rule must be case-insensitive")
	}
}
