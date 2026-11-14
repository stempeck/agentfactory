package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

const wfDeadline = 3 * time.Second

func wfWindowsDir(root string) string { return filepath.Join(config.StatuslineDir(root), "windows") }

func wfRecordPath(root, profile string) string {
	return filepath.Join(wfWindowsDir(root), profile+".json")
}

func wfClaimPath(root, profile string) string {
	return filepath.Join(wfWindowsDir(root), profile+".claim")
}

func wfFactPath(root, agent string) string {
	return filepath.Join(wfWindowsDir(root), "launched", agent+".json")
}

func wfWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func wfMkfifo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatalf("mkfifo %s: %v", path, err)
	}
}

func wfSymlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func wfRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

// wfWithin runs fn under a deadline. A reader that opens a FIFO blocks until a writer appears, so
// the failure mode under test is a hang, which only a deadline turns into a verdict.
func wfWithin(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(wfDeadline):
		t.Fatalf("%s did not return within %v: it blocked on the planted file", what, wfDeadline)
	}
}

func wfFact() windowLaunchFact {
	return windowLaunchFact{
		Profile:        "fable-5-2",
		ModelID:        "claude-fable-5-2",
		Declared:       1000000,
		HarnessVersion: "2.1.322",
		LaunchedAt:     time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
	}
}

func wfFactBytes(t *testing.T, f windowLaunchFact) []byte {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wfID(version string) windowIdentity {
	return windowIdentity{Profile: "fable-5-2", ModelID: "claude-fable-5-2", HarnessVersion: version, Declared: 1000000}
}

// wfRawEntries decodes the record file as plain JSON, so a test reads what is on disk rather than
// what readWindowRecord chooses to return.
func wfRawEntries(t *testing.T, root, profile string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(wfRecordPath(root, profile))
	if err != nil {
		t.Fatalf("record file of %q: %v", profile, err)
	}
	var f struct {
		Identities map[string]map[string]any `json:"identities"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("record file of %q does not decode: %v", profile, err)
	}
	return f.Identities
}

func wfEntryJSON(t *testing.T, root, profile, key string) string {
	t.Helper()
	e, ok := wfRawEntries(t, root, profile)[key]
	if !ok {
		t.Fatalf("record file of %q holds no entry %q", profile, key)
	}
	data, _ := json.Marshal(e)
	return string(data)
}

func wfPlantRecordFile(t *testing.T, root, profile string, entries map[string]windowRecord) {
	t.Helper()
	data, err := json.Marshal(windowRecordFile{Profile: profile, Identities: entries})
	if err != nil {
		t.Fatal(err)
	}
	wfWrite(t, wfRecordPath(root, profile), data)
}

func wfSetFigure(agent string, applied int64, launchedAt, now time.Time) func(*windowRecord, bool) bool {
	return func(rec *windowRecord, _ bool) bool {
		if rec.Agents == nil {
			rec.Agents = map[string]agentWindow{}
		}
		rec.Agents[agent] = agentWindow{Applied: applied, LaunchedAt: launchedAt, LastSeen: now}
		return true
	}
}

func wfUpdate(t *testing.T, root string, id windowIdentity, now time.Time, mutate func(*windowRecord, bool) bool) {
	t.Helper()
	if err := updateWindowRecord(root, id, now, mutate); err != nil {
		t.Fatalf("updateWindowRecord(%q): %v", id.key(), err)
	}
}

func TestWindowFacts_RefusesPathNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  bool
	}{
		{"profile name", "fable-5-2", true},
		{"dotted name", "opus.5", true},
		{"printable non-ASCII rune", "modèle", true},
		{"at the field cap", strings.Repeat("a", windowFieldMaxBytes), true},
		{"empty", "", false},
		{"over the field cap", strings.Repeat("a", windowFieldMaxBytes+1), false},
		{"slash", "a/b", false},
		{"backslash", `a\b`, false},
		{"parent", "..", false},
		{"leading dot", ".hidden", false},
		{"control rune", "fable\x1b[31m", false},
		{"newline", "fable\n5", false},
		{"non-printable rune", "fable\u200b5", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validWindowName(tc.input); got != tc.want {
				t.Errorf("validWindowName(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}

	t.Run("a refused profile writes nothing and reads nothing", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		wfWrite(t, filepath.Join(config.StatuslineDir(root), "escape.json"), []byte(`{"profile":"escape","identities":{}}`))
		before := wfRead(t, filepath.Join(config.StatuslineDir(root), "escape.json"))
		for _, profile := range []string{"../escape", "fable\x07", "fable\u200b"} {
			id := windowIdentity{Profile: profile, ModelID: "claude-x", HarnessVersion: "2.1.322", Declared: 1000000}
			called := false
			err := updateWindowRecord(root, id, now, func(rec *windowRecord, _ bool) bool { called = true; return true })
			if err == nil {
				t.Errorf("updateWindowRecord for profile %q returned no error", profile)
			}
			if called {
				t.Errorf("updateWindowRecord for profile %q handed mutate an entry", profile)
			}
			if _, ok := readWindowRecord(root, id, now); ok {
				t.Errorf("readWindowRecord for profile %q returned an entry", profile)
			}
			if _, ok := claimWindowRecord(root, profile); ok {
				t.Errorf("claimWindowRecord for profile %q took a claim", profile)
			}
		}
		if after := wfRead(t, filepath.Join(config.StatuslineDir(root), "escape.json")); !bytes.Equal(before, after) {
			t.Errorf("a refused profile name reached a file outside windows/: %s", after)
		}
		// The positive control, so the refusals above are not a writer that refuses everything.
		wfUpdate(t, root, wfID("2.1.322"), now, wfSetFigure("manager", 200000, now, now))
		if _, ok := readWindowRecord(root, wfID("2.1.322"), now); !ok {
			t.Error("a valid profile name was not written and read back")
		}
	})

	t.Run("a refused agent name reads no launch fact", func(t *testing.T) {
		root := t.TempDir()
		fact := wfFactBytes(t, wfFact())
		wfWrite(t, filepath.Join(wfWindowsDir(root), "evil.json"), fact)
		wfWrite(t, wfFactPath(root, "has space"), fact)
		wfWrite(t, wfFactPath(root, "manager"), fact)
		for _, agent := range []string{"../evil", "has space", ".manager", ""} {
			if _, ok := readWindowLaunchFact(root, agent); ok {
				t.Errorf("readWindowLaunchFact(%q) returned a fact", agent)
			}
		}
		if _, ok := readWindowLaunchFact(root, "manager"); !ok {
			t.Error("readWindowLaunchFact(\"manager\") of a valid fact returned nothing")
		}
	})

	t.Run("a launch fact naming a refused profile is absent", func(t *testing.T) {
		root := t.TempDir()
		for agent, profile := range map[string]string{"one": "../x", "two": "fable\x1b", "three": "fable\u200b"} {
			f := wfFact()
			f.Profile = profile
			wfWrite(t, wfFactPath(root, agent), wfFactBytes(t, f))
			if _, ok := readWindowLaunchFact(root, agent); ok {
				t.Errorf("a launch fact naming profile %q was read", profile)
			}
		}
	})
}

func TestWindowFacts_EmptyHarnessVersionNeverMatches(t *testing.T) {
	if got := wfID("").key(); got != "" {
		t.Errorf("key() of an identity with no harness version = %q, want \"\"", got)
	}
	if got, want := wfID("2.1.322").key(), "2.1.322|1000000|claude-fable-5-2"; got != want {
		t.Errorf("key() = %q, want %q", got, want)
	}
	if got, want := (windowIdentity{Profile: "p", HarnessVersion: "2.1.322", Declared: 0}).key(), "2.1.322|0|"; got != want {
		t.Errorf("key() of a model-less, undeclared identity = %q, want %q", got, want)
	}

	root := t.TempDir()
	now := time.Now()
	wfPlantRecordFile(t, root, "fable-5-2", map[string]windowRecord{
		"":                          {ModelID: "claude-fable-5-2", Declared: 1000000, Applied: 200000, LastSeen: now, Source: "session"},
		"|1000000|claude-fable-5-2": {ModelID: "claude-fable-5-2", Declared: 1000000, Applied: 200000, LastSeen: now, Source: "session"},
	})
	if _, ok := readWindowRecord(root, wfID(""), now); ok {
		t.Error("readWindowRecord matched an identity with no harness version")
	}
	before := wfRead(t, wfRecordPath(root, "fable-5-2"))
	called := false
	_ = updateWindowRecord(root, wfID(""), now, func(rec *windowRecord, _ bool) bool { called = true; return true })
	if called {
		t.Error("updateWindowRecord handed mutate an entry for an identity with no harness version")
	}
	if after := wfRead(t, wfRecordPath(root, "fable-5-2")); !bytes.Equal(before, after) {
		t.Errorf("updateWindowRecord with no harness version rewrote the file:\n%s", after)
	}

	if f := buildWindowLaunchFact(root, "fable-5-2", nil, ""); f != nil {
		t.Errorf("buildWindowLaunchFact with an empty version = %+v, want nil", *f)
	}
	wfWrite(t, config.ModelsConfigPath(root), []byte(`{"models":{"fable-5-2":{"ANTHROPIC_MODEL":"claude-fable-5-2","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"1000000"}}}`))
	env := []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-fable-5-2"}, {Key: config.EnvMaxContextTokens, Value: "1000000"}}
	if f := buildWindowLaunchFact(root, "fable-5-2", env, ""); f != nil {
		t.Errorf("buildWindowLaunchFact with an empty version on a declaring profile = %+v, want nil", *f)
	}
	f := buildWindowLaunchFact(root, "fable-5-2", env, "2.1.322")
	if f == nil {
		t.Fatal("buildWindowLaunchFact with a version on a declaring profile = nil, want a fact (the control of the nil rows)")
	}
	if got := f.identity().key(); got != "2.1.322|1000000|claude-fable-5-2" {
		t.Errorf("the built fact's key = %q", got)
	}
}

func TestHarnessVersionToken(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"2.1.322 (Claude Code)", "2.1.322"},
		{"2.1.322", "2.1.322"},
		{"  2.1.322   (Claude Code)  ", "2.1.322"},
		{"1.0.0-beta+build.7", "1.0.0-beta+build.7"},
		{strings.Repeat("9", 32), strings.Repeat("9", 32)},
		{strings.Repeat("9", 33), ""},
		{"", ""},
		{"   ", ""},
		{"2.1.322|x", ""},
		{"2.1.322\x1b[31m", ""},
		{"2.1.322/../x", ""},
		{"(Claude Code)", ""},
		{"2.1.3é2", ""},
	} {
		if got := harnessVersionToken(tc.raw); got != tc.want {
			t.Errorf("harnessVersionToken(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	for _, tc := range []struct{ out, want string }{
		{"2.1.322 (Claude Code)\n", "2.1.322"},
		{"update available\n2.1.322 (Claude Code)\n", "2.1.322"},
		{"", ""},
		{"2.1.322\x07 (Claude Code)", ""},
	} {
		if got := harnessVersionOf(tc.out); got != tc.want {
			t.Errorf("harnessVersionOf(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

func TestWindowFacts_OversizedOrMalformedIsAbsent(t *testing.T) {
	now := time.Now()
	validRecord := func(t *testing.T) []byte {
		data, err := json.Marshal(windowRecordFile{Profile: "fable-5-2", Identities: map[string]windowRecord{
			wfID("2.1.322").key(): {ModelID: "claude-fable-5-2", HarnessVersion: "2.1.322", Declared: 1000000, Applied: 200000,
				Agents:   map[string]agentWindow{"manager": {Applied: 200000, LaunchedAt: now, LastSeen: now}},
				LastSeen: now, FirstSeen: now, Sessions: 1, Source: "session"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	padded := func(data []byte, size int) []byte {
		out := append([]byte{}, data...)
		return append(out, bytes.Repeat([]byte(" "), size-len(out))...)
	}

	type kind struct {
		name    string
		path    func(root string) string
		valid   func(t *testing.T) []byte
		maxSize int
		read    func(root string) bool
	}
	kinds := []kind{
		{
			name:    "launch fact",
			path:    func(root string) string { return wfFactPath(root, "manager") },
			valid:   func(t *testing.T) []byte { return wfFactBytes(t, wfFact()) },
			maxSize: windowFileMaxBytes,
			read: func(root string) bool {
				_, ok := readWindowLaunchFact(root, "manager")
				return ok
			},
		},
		{
			name:    "record file",
			path:    func(root string) string { return wfRecordPath(root, "fable-5-2") },
			valid:   validRecord,
			maxSize: windowFileMaxBytes,
			read: func(root string) bool {
				_, ok := readWindowRecord(root, wfID("2.1.322"), now)
				return ok
			},
		},
		{
			name:    "session id",
			path:    func(root string) string { return filepath.Join(root, "agent", ".runtime", "session_id") },
			valid:   func(*testing.T) []byte { return []byte("4f1c2b7e-session") },
			maxSize: windowFieldMaxBytes,
			read: func(root string) bool {
				_, _, ok := readCurrentSessionID(filepath.Join(root, "agent"))
				return ok
			},
		},
	}
	for _, k := range kinds {
		t.Run(k.name+" that is valid is read", func(t *testing.T) {
			root := t.TempDir()
			wfWrite(t, k.path(root), k.valid(t))
			if !k.read(root) {
				t.Errorf("a valid %s was not read", k.name)
			}
		})
		t.Run(k.name+" at its cap is read", func(t *testing.T) {
			root := t.TempDir()
			wfWrite(t, k.path(root), padded(k.valid(t), k.maxSize))
			if !k.read(root) {
				t.Errorf("a %s of exactly %d bytes was not read", k.name, k.maxSize)
			}
		})
		t.Run(k.name+" oversized", func(t *testing.T) {
			root := t.TempDir()
			wfWrite(t, k.path(root), padded(k.valid(t), k.maxSize+1))
			if k.read(root) {
				t.Errorf("a %s of %d bytes was read", k.name, k.maxSize+1)
			}
		})
		t.Run(k.name+" malformed", func(t *testing.T) {
			root := t.TempDir()
			bad := []byte("{not json")
			if k.name == "session id" {
				bad = []byte(" \n")
			}
			wfWrite(t, k.path(root), bad)
			if k.read(root) {
				t.Errorf("a malformed %s was read", k.name)
			}
		})
		t.Run(k.name+" FIFO", func(t *testing.T) {
			root := t.TempDir()
			wfMkfifo(t, k.path(root))
			var ok bool
			wfWithin(t, "reading a FIFO "+k.name, func() { ok = k.read(root) })
			if ok {
				t.Errorf("a FIFO %s was read", k.name)
			}
		})
		t.Run(k.name+" symlink to a regular file", func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "elsewhere")
			wfWrite(t, target, k.valid(t))
			wfSymlink(t, target, k.path(root))
			if k.read(root) {
				t.Errorf("a %s reached through a symlink was read", k.name)
			}
		})
	}

	claimRows := []struct {
		name  string
		plant func(t *testing.T, claim string)
	}{
		{"claim FIFO", func(t *testing.T, claim string) { wfMkfifo(t, claim) }},
		{"claim symlink to /dev/zero", func(t *testing.T, claim string) { wfSymlink(t, "/dev/zero", claim) }},
	}
	for _, row := range claimRows {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			row.plant(t, wfClaimPath(root, "fable-5-2"))
			var err error
			wfWithin(t, "updateWindowRecord under a "+row.name, func() {
				err = updateWindowRecord(root, wfID("2.1.322"), now, wfSetFigure("manager", 200000, now, now))
			})
			if err != nil {
				t.Fatalf("updateWindowRecord under a %s: %v (the planted claim must be judged stale at once)", row.name, err)
			}
			if _, ok := readWindowRecord(root, wfID("2.1.322"), now); !ok {
				t.Errorf("updateWindowRecord under a %s did not write its entry", row.name)
			}
			if info, lerr := os.Lstat(wfClaimPath(root, "fable-5-2")); lerr == nil && !info.Mode().IsRegular() {
				t.Errorf("the planted %s is still the claim after the write (mode %v)", row.name, info.Mode())
			}
		})
	}

	t.Run("listing names the facts of a directory", func(t *testing.T) {
		root := t.TempDir()
		for _, n := range []string{"manager.json", "supervisor.json", "manager.json.123.tmp"} {
			wfWrite(t, filepath.Join(wfWindowsDir(root), "launched", n), wfFactBytes(t, wfFact()))
		}
		got := listWindowFacts(root)
		sort.Strings(got)
		if fmt.Sprint(got) != "[manager supervisor]" {
			t.Errorf("listWindowFacts = %v, want [manager supervisor]", got)
		}
	})
	t.Run("listing FIFO", func(t *testing.T) {
		root := t.TempDir()
		wfMkfifo(t, filepath.Join(wfWindowsDir(root), "launched"))
		var got []string
		wfWithin(t, "listWindowFacts over a FIFO", func() { got = listWindowFacts(root) })
		if len(got) != 0 {
			t.Errorf("listWindowFacts over a FIFO = %v, want none", got)
		}
	})
	t.Run("listing symlink to a directory", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "real-launched")
		wfWrite(t, filepath.Join(target, "manager.json"), wfFactBytes(t, wfFact()))
		wfSymlink(t, target, filepath.Join(wfWindowsDir(root), "launched"))
		if got := listWindowFacts(root); len(got) != 0 {
			t.Errorf("listWindowFacts through a symlinked directory = %v, want none", got)
		}
	})
}

var wfWindowAllowedRefs = map[string]bool{
	"os.Remove": true, "os.Rename": true, "os.MkdirAll": true, "os.IsNotExist": true, "os.IsExist": true,
	"os.ErrNotExist": true, "os.ErrExist": true, "fs.ErrNotExist": true, "fs.ErrExist": true,
	"os.FileInfo": true, "os.DirEntry": true, "io.Writer": true,
}

var wfWindowGuardedPkgs = map[string]string{"os": "os", "io": "io", "io/ioutil": "ioutil", "io/fs": "fs", "syscall": "syscall"}

// wfScanWindowFile returns every violation of the open rule in one parsed file, and which of the two
// open functions it declares with the Lstat each must hold.
func wfScanWindowFile(name string, f *ast.File) (violations []string, declares map[string]bool) {
	declares = map[string]bool{}
	imports := map[string]string{}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		short, guarded := wfWindowGuardedPkgs[path]
		if !guarded {
			continue
		}
		local := short
		if imp.Name != nil {
			local = imp.Name.Name
		}
		if local == "_" || local == "." {
			if local == "." {
				violations = append(violations, fmt.Sprintf("%s: dot-import of %s", name, path))
			}
			continue
		}
		imports[local] = short
	}
	scan := func(owner string, n ast.Node) {
		var lstatAt, readDirAt token.Pos
		ast.Inspect(n, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Ident:
				if x.Name == "readHeldSlot" {
					violations = append(violations, fmt.Sprintf("%s: %s references readHeldSlot, the precedent's unguarded reader", name, wfOwner(owner)))
				}
			case *ast.SelectorExpr:
				id, ok := x.X.(*ast.Ident)
				if !ok {
					return true
				}
				short, ok := imports[id.Name]
				if !ok {
					return true
				}
				ref := short + "." + x.Sel.Name
				if ref == "os.Lstat" && lstatAt == token.NoPos {
					lstatAt = x.Pos()
				}
				if ref == "os.ReadDir" && readDirAt == token.NoPos {
					readDirAt = x.Pos()
				}
				if owner != "readWindowFile" && owner != "listWindowFacts" && !wfWindowAllowedRefs[ref] {
					violations = append(violations, fmt.Sprintf("%s: %s references %s outside readWindowFile and listWindowFacts", name, wfOwner(owner), ref))
				}
				return false
			}
			return true
		})
		switch owner {
		case "readWindowFile":
			declares[owner] = true
			if lstatAt == token.NoPos {
				violations = append(violations, fmt.Sprintf("%s: readWindowFile holds no os.Lstat call", name))
			}
		case "listWindowFacts":
			declares[owner] = true
			if lstatAt == token.NoPos || (readDirAt != token.NoPos && lstatAt > readDirAt) {
				violations = append(violations, fmt.Sprintf("%s: listWindowFacts holds no os.Lstat before its os.ReadDir", name))
			}
		}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			owner := d.Name.Name
			if d.Recv != nil {
				owner = "method " + owner
			}
			scan(owner, d)
		case *ast.GenDecl:
			if d.Tok != token.IMPORT {
				scan("", d)
			}
		}
	}
	return violations, declares
}

func wfOwner(owner string) string {
	if owner == "" {
		return "a package-level declaration"
	}
	return owner
}

func TestWindowFacts_OpenOnlyThroughReadWindowFile(t *testing.T) {
	t.Run("the scanner catches each planted offender", func(t *testing.T) {
		for _, tc := range []struct{ name, src, want string }{
			{"os.ReadFile as a value", "package cmd\nimport \"os\"\nvar readFile = os.ReadFile\n", "os.ReadFile"},
			{"aliased os.Open", "package cmd\nimport xos \"os\"\nfunc f() { xos.Open(\"p\") }\n", "os.Open"},
			{"io.ReadAll elsewhere", "package cmd\nimport \"io\"\nfunc f(r io.Reader) { io.ReadAll(r) }\n", "io.ReadAll"},
			{"ioutil.ReadFile", "package cmd\nimport \"io/ioutil\"\nfunc f() { ioutil.ReadFile(\"p\") }\n", "ioutil.ReadFile"},
			{"os.ReadDir outside the lister", "package cmd\nimport \"os\"\nfunc f() { os.ReadDir(\"p\") }\n", "os.ReadDir"},
			{"os.OpenFile", "package cmd\nimport \"os\"\nfunc f() { os.OpenFile(\"p\", 0, 0) }\n", "os.OpenFile"},
			{"syscall.Open", "package cmd\nimport \"syscall\"\nfunc f() { syscall.Open(\"p\", 0, 0) }\n", "syscall.Open"},
			{"readHeldSlot", "package cmd\nfunc f() { readHeldSlot(\"p\") }\n", "readHeldSlot"},
			{"readWindowFile without Lstat", "package cmd\nimport \"os\"\nfunc readWindowFile(p string, n int64) { os.Open(p) }\n", "readWindowFile holds no os.Lstat"},
			{"listWindowFacts reads before its Lstat", "package cmd\nimport \"os\"\nfunc listWindowFacts(r string) { os.ReadDir(r); os.Lstat(r) }\n", "listWindowFacts holds no os.Lstat before its os.ReadDir"},
		} {
			f, err := parser.ParseFile(token.NewFileSet(), "planted.go", tc.src, 0)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			v, _ := wfScanWindowFile("planted.go", f)
			if !strings.Contains(strings.Join(v, "\n"), tc.want) {
				t.Errorf("%s: scanner reported %q, want a violation naming %q", tc.name, v, tc.want)
			}
		}
		clean := "package cmd\nimport (\n\"io\"\n\"os\"\n)\nfunc g(w io.Writer, i os.FileInfo) { os.Remove(\"p\"); _ = os.IsNotExist(nil) }\n" +
			"func readWindowFile(p string) { os.Lstat(p); os.Open(p) }\nfunc listWindowFacts(r string) { os.Lstat(r); os.ReadDir(r) }\n"
		f, err := parser.ParseFile(token.NewFileSet(), "clean.go", clean, 0)
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := wfScanWindowFile("clean.go", f); len(v) != 0 {
			t.Errorf("scanner flagged allowed references: %q", v)
		}
	})

	files, err := filepath.Glob("window_*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var violations []string
	declared := map[string]bool{}
	scanned := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		v, d := wfScanWindowFile(name, f)
		violations = append(violations, v...)
		for k := range d {
			declared[k] = true
		}
	}
	if scanned == 0 {
		t.Fatal("no non-test window_*.go file in internal/cmd: the window files are not written yet")
	}
	for _, fn := range []string{"readWindowFile", "listWindowFacts"} {
		if !declared[fn] {
			t.Errorf("no window_*.go file declares %s", fn)
		}
	}
	for _, v := range violations {
		t.Error(v)
	}
}

func TestWindowFacts_IdentitiesKeptApart(t *testing.T) {
	base := wfID("2.1.308")
	variants := map[string]windowIdentity{
		"harness version": wfID("2.1.322"),
		"model id":        {Profile: base.Profile, ModelID: "claude-fable-5-3", HarnessVersion: base.HarnessVersion, Declared: base.Declared},
		"declaration":     {Profile: base.Profile, ModelID: base.ModelID, HarnessVersion: base.HarnessVersion, Declared: 500000},
	}
	for name, other := range variants {
		t.Run("differing in "+name, func(t *testing.T) {
			root := t.TempDir()
			now := time.Now().UTC().Truncate(time.Second)
			if base.key() == "" || base.key() == other.key() {
				t.Fatalf("keys %q and %q must be distinct and non-empty", base.key(), other.key())
			}
			wfUpdate(t, root, base, now, func(rec *windowRecord, exists bool) bool {
				wfSetFigure("first", 200000, now, now)(rec, exists)
				rec.Sessions = 3
				rec.NotifiedFor = []windowPair{{Declared: base.Declared, Applied: 200000}}
				return true
			})
			baseEntry := wfEntryJSON(t, root, base.Profile, base.key())

			var handed windowRecord
			var handedExists bool
			wfUpdate(t, root, other, now.Add(time.Minute), func(rec *windowRecord, exists bool) bool {
				handed, handedExists = *rec, exists
				return wfSetFigure("second", 262144, now, now.Add(time.Minute))(rec, exists)
			})
			if handedExists || handed.Sessions != 0 || len(handed.Agents) != 0 {
				t.Errorf("updating %q was handed the entry of %q: exists=%v %+v", other.key(), base.key(), handedExists, handed)
			}
			if got := len(wfRawEntries(t, root, base.Profile)); got != 2 {
				t.Errorf("%s.json holds %d entries, want 2", base.Profile, got)
			}
			if got := wfEntryJSON(t, root, base.Profile, base.key()); got != baseEntry {
				t.Errorf("writing %q changed the entry of %q:\nbefore %s\nafter  %s", other.key(), base.key(), baseEntry, got)
			}
			a, ok := readWindowRecord(root, base, now.Add(time.Minute))
			if !ok || a.Applied != 200000 || a.Sessions != 3 {
				t.Errorf("readWindowRecord(%q) = %+v, %v; want its own applied 200000 and sessions 3", base.key(), a, ok)
			}
			b, ok := readWindowRecord(root, other, now.Add(time.Minute))
			if !ok || b.Applied != 262144 || len(b.NotifiedFor) != 0 {
				t.Errorf("readWindowRecord(%q) = %+v, %v; want its own applied 262144 and no notified_for", other.key(), b, ok)
			}
			if _, inOther := b.Agents["first"]; inOther {
				t.Errorf("the entry of %q holds the figure of %q's agent", other.key(), base.key())
			}
		})
	}

	t.Run("an entry whose own fields do not reproduce its key is absent", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		good := windowRecord{ModelID: base.ModelID, HarnessVersion: base.HarnessVersion, Declared: base.Declared, Applied: 200000, LastSeen: now, Source: "session",
			Agents: map[string]agentWindow{"first": {Applied: 200000, LaunchedAt: now, LastSeen: now}}}
		for field, bad := range map[string]windowRecord{
			"harness_version": func() windowRecord { r := good; r.HarnessVersion = "2.1.999"; return r }(),
			"model_id":        func() windowRecord { r := good; r.ModelID = "claude-other"; return r }(),
			"declared":        func() windowRecord { r := good; r.Declared = 200000; return r }(),
			"model_id over the cap": func() windowRecord {
				r := good
				r.ModelID = strings.Repeat("m", windowFieldMaxBytes+1)
				return r
			}(),
		} {
			id := base
			if field == "model_id over the cap" {
				id.ModelID = bad.ModelID
			}
			wfPlantRecordFile(t, root, base.Profile, map[string]windowRecord{id.key(): bad})
			if _, ok := readWindowRecord(root, id, now); ok {
				t.Errorf("an entry whose %s does not match its key was read", field)
			}
		}
		wfPlantRecordFile(t, root, base.Profile, map[string]windowRecord{base.key(): good})
		if _, ok := readWindowRecord(root, base, now); !ok {
			t.Error("the matching entry (the control) was not read")
		}
	})
}

func TestWindowFacts_AgentFiguresKeptApart(t *testing.T) {
	id := wfID("2.1.308")
	for _, order := range [][]string{{"x", "y"}, {"y", "x"}} {
		t.Run("written "+strings.Join(order, " then "), func(t *testing.T) {
			root := t.TempDir()
			now := time.Now()
			launched := map[string]time.Time{"x": now.Add(-time.Hour), "y": now.Add(-2 * time.Hour)}
			figure := map[string]int64{"x": 200000, "y": 262144}
			for _, agent := range order {
				wfUpdate(t, root, id, now, wfSetFigure(agent, figure[agent], launched[agent], now))
			}
			rec, ok := readWindowRecord(root, id, now)
			if !ok {
				t.Fatal("no entry after two figures were written")
			}
			if rec.Applied != 200000 || rec.Source != "session" {
				t.Errorf("Applied, Source = %d, %q; want 200000, session", rec.Applied, rec.Source)
			}
			for agent, want := range figure {
				got, ok := rec.launchFigure(agent, launched[agent])
				if !ok || got != want {
					t.Errorf("launchFigure(%q, its launch) = %d, %v; want %d", agent, got, ok, want)
				}
				if got, ok := rec.launchFigure(agent, launched[agent].In(time.FixedZone("UTC+3", 3*3600))); !ok || got != want {
					t.Errorf("launchFigure(%q) at the same instant in another location = %d, %v; want %d (time.Equal, not ==)", agent, got, ok, want)
				}
				if _, ok := rec.launchFigure(agent, launched[agent].Add(time.Second)); ok {
					t.Errorf("launchFigure(%q) of another launch returned a figure", agent)
				}
			}
			if _, ok := rec.launchFigure("x", launched["y"]); ok {
				t.Error("launchFigure(\"x\") at y's launch time returned a figure")
			}
			if _, ok := rec.launchFigure("z", launched["x"]); ok {
				t.Error("launchFigure of an agent with no figure returned one")
			}
		})
	}

	t.Run("a stale agent figure is dropped and a fresh one survives its own write", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		wfUpdate(t, root, id, now, func(rec *windowRecord, _ bool) bool {
			rec.Agents = map[string]agentWindow{
				"old": {Applied: 150000, LaunchedAt: now.Add(-40 * 24 * time.Hour), LastSeen: now.Add(-windowEntryPruneAfter - time.Hour)},
				"x":   {Applied: 200000, LaunchedAt: now.Add(-time.Hour), LastSeen: now},
			}
			return true
		})
		rec, ok := readWindowRecord(root, id, now)
		if !ok {
			t.Fatal("no entry after the write")
		}
		if _, kept := rec.Agents["old"]; kept {
			t.Error("a figure older than windowEntryPruneAfter survived the write")
		}
		if _, kept := rec.Agents["x"]; !kept {
			t.Error("a figure written with last_seen = now was dropped by the write that created it")
		}
		if rec.Applied != 200000 {
			t.Errorf("Applied = %d, want 200000 (the dropped figure must not hold the minimum)", rec.Applied)
		}
	})

	t.Run("an agent filed under another identity leaves the first entry", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now().UTC().Truncate(time.Second)
		first := now.Add(-2 * time.Hour)
		wfUpdate(t, root, id, now, func(rec *windowRecord, exists bool) bool {
			wfSetFigure("x", 200000, first, now)(rec, exists)
			wfSetFigure("y", 262144, first, now)(rec, exists)
			rec.Sessions = 2
			rec.NotifiedFor = []windowPair{{Declared: 1000000, Applied: 200000}, {Declared: 1000000, Applied: 262144}}
			return true
		})
		before, ok := readWindowRecord(root, id, now)
		if !ok {
			t.Fatal("no first entry")
		}
		second := wfID("2.1.322")
		later := now.Add(time.Hour)
		wfUpdate(t, root, second, later, wfSetFigure("x", 1000000, now.Add(30*time.Minute), later))

		after, ok := readWindowRecord(root, id, later)
		if !ok {
			t.Fatal("the first entry is gone")
		}
		if _, held := after.Agents["x"]; held {
			t.Error("the first entry still holds x's figure of its earlier launch")
		}
		if after.Applied != 262144 {
			t.Errorf("the first entry's Applied = %d, want 262144 (recomputed without x)", after.Applied)
		}
		if after.Sessions != before.Sessions || fmt.Sprint(after.NotifiedFor) != fmt.Sprint(before.NotifiedFor) ||
			!after.FirstSeen.Equal(before.FirstSeen) || !after.LastSeen.Equal(before.LastSeen) ||
			after.ModelID != before.ModelID || after.Declared != before.Declared || after.HarnessVersion != before.HarnessVersion {
			t.Errorf("the first entry's other fields changed:\nbefore %+v\nafter  %+v", before, after)
		}
		if y := after.Agents["y"]; y.Applied != 262144 {
			t.Errorf("y's figure in the first entry = %+v, want 262144 kept", y)
		}
		got, ok := readWindowRecord(root, second, later)
		if !ok {
			t.Fatal("the second entry is missing")
		}
		if fig, ok := got.launchFigure("x", now.Add(30*time.Minute)); !ok || fig != 1000000 {
			t.Errorf("the second entry's figure of x = %d, %v; want 1000000", fig, ok)
		}
	})
}

func TestWindowLaunchFact_UndoRestoresOnlyItsOwnWrite(t *testing.T) {
	const agent = "manager"
	errStart := errors.New("session start failed")
	newFact := func() windowLaunchFact {
		f := wfFact()
		f.HarnessVersion = "2.1.330"
		f.LaunchedAt = time.Time{}
		return f
	}

	t.Run("failed start restores the previous fact byte for byte", func(t *testing.T) {
		root := t.TempDir()
		prev := wfFactBytes(t, wfFact())
		wfWrite(t, wfFactPath(root, agent), prev)
		f := newFact()
		started := false
		before := time.Now()
		err := launchReports{WindowFact: &f}.startWithWindowLaunchFact(root, agent, true, &bytes.Buffer{}, func() error {
			started = true
			got, ok := readWindowLaunchFact(root, agent)
			if !ok || got.HarnessVersion != "2.1.330" {
				t.Errorf("at start the fact on disk is %+v, %v; want the new launch's fact persisted before the start", got, ok)
			}
			if got.LaunchedAt.Before(before) || got.LaunchedAt.After(time.Now()) {
				t.Errorf("the persisted launched_at %v was not stamped at the write", got.LaunchedAt)
			}
			return errStart
		})
		if !started {
			t.Fatal("startWithWindowLaunchFact never ran start")
		}
		if err != errStart {
			t.Errorf("startWithWindowLaunchFact returned %v, want the start's own error unchanged", err)
		}
		if got := wfRead(t, wfFactPath(root, agent)); !bytes.Equal(got, prev) {
			t.Errorf("after the failed start the fact is\n%s\nwant the previous bytes\n%s", got, prev)
		}
	})

	t.Run("failed start with no previous fact leaves none", func(t *testing.T) {
		root := t.TempDir()
		f := newFact()
		started := false
		err := launchReports{WindowFact: &f}.startWithWindowLaunchFact(root, agent, true, &bytes.Buffer{}, func() error {
			started = true
			if _, ok := readWindowLaunchFact(root, agent); !ok {
				t.Error("at start no fact was persisted")
			}
			return errStart
		})
		if !started || err != errStart {
			t.Fatalf("start ran = %v, err = %v; want the start run and its error returned", started, err)
		}
		if _, lerr := os.Lstat(wfFactPath(root, agent)); !os.IsNotExist(lerr) {
			t.Errorf("after the failed start a fact is left: Lstat err = %v", lerr)
		}
	})

	t.Run("fact of a concurrent launch is left as it is", func(t *testing.T) {
		root := t.TempDir()
		wfWrite(t, wfFactPath(root, agent), wfFactBytes(t, wfFact()))
		concurrent := wfFact()
		concurrent.HarnessVersion = "2.1.400"
		concurrentBytes := wfFactBytes(t, concurrent)
		f := newFact()
		started := false
		err := launchReports{WindowFact: &f}.startWithWindowLaunchFact(root, agent, true, &bytes.Buffer{}, func() error {
			started = true
			wfWrite(t, wfFactPath(root, agent), concurrentBytes)
			return errStart
		})
		if !started || err != errStart {
			t.Fatalf("start ran = %v, err = %v", started, err)
		}
		if got := wfRead(t, wfFactPath(root, agent)); !bytes.Equal(got, concurrentBytes) {
			t.Errorf("the undo overwrote a concurrent launch's fact:\n%s\nwant\n%s", got, concurrentBytes)
		}
	})

	t.Run("previous fact failing the open rule is removed and not restored", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "elsewhere.json")
		targetBytes := wfFactBytes(t, wfFact())
		wfWrite(t, target, targetBytes)
		wfSymlink(t, target, wfFactPath(root, agent))
		f := newFact()
		started := false
		err := launchReports{WindowFact: &f}.startWithWindowLaunchFact(root, agent, true, &bytes.Buffer{}, func() error {
			started = true
			got, ok := readWindowLaunchFact(root, agent)
			if !ok || got.HarnessVersion != "2.1.330" {
				t.Errorf("at start the fact is %+v, %v; want the new fact in place of the symlink", got, ok)
			}
			return errStart
		})
		if !started || err != errStart {
			t.Fatalf("start ran = %v, err = %v", started, err)
		}
		if _, lerr := os.Lstat(wfFactPath(root, agent)); !os.IsNotExist(lerr) {
			t.Errorf("after the failed start the path holds something (Lstat err %v); the symlink must not be restored", lerr)
		}
		if got := wfRead(t, target); !bytes.Equal(got, targetBytes) {
			t.Errorf("the symlink's target was written: %s", got)
		}
	})

	t.Run("successful start keeps the new fact", func(t *testing.T) {
		root := t.TempDir()
		wfWrite(t, wfFactPath(root, agent), wfFactBytes(t, wfFact()))
		f := newFact()
		started := false
		err := launchReports{WindowFact: &f}.startWithWindowLaunchFact(root, agent, true, &bytes.Buffer{}, func() error { started = true; return nil })
		if !started || err != nil {
			t.Fatalf("start ran = %v, err = %v", started, err)
		}
		if got, ok := readWindowLaunchFact(root, agent); !ok || got.HarnessVersion != "2.1.330" || got.LaunchedAt.IsZero() {
			t.Errorf("after a successful start the fact is %+v, %v; want the new stamped fact", got, ok)
		}
	})

	t.Run("persist false touches no fact", func(t *testing.T) {
		root := t.TempDir()
		prev := wfFactBytes(t, wfFact())
		wfWrite(t, wfFactPath(root, agent), prev)
		f := newFact()
		started := false
		err := launchReports{WindowFact: &f}.startWithWindowLaunchFact(root, agent, false, &bytes.Buffer{}, func() error {
			started = true
			if got := wfRead(t, wfFactPath(root, agent)); !bytes.Equal(got, prev) {
				t.Error("persist=false wrote a fact before the start")
			}
			return errStart
		})
		if !started || err != errStart {
			t.Fatalf("start ran = %v, err = %v", started, err)
		}
		if got := wfRead(t, wfFactPath(root, agent)); !bytes.Equal(got, prev) {
			t.Error("persist=false changed the fact")
		}
	})
}

func TestWindowFacts_WriterStampsLastSeen(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	unobserved := wfID("2.1.301")
	probe := wfID("2.1.302")
	refusal := wfID("2.1.303")
	wfUpdate(t, root, unobserved, now, func(rec *windowRecord, _ bool) bool { rec.UnobservedNotified = true; return true })
	wfUpdate(t, root, probe, now, func(rec *windowRecord, _ bool) bool { rec.Probed = 200000; return true })
	wfUpdate(t, root, refusal, now, func(rec *windowRecord, _ bool) bool {
		rec.NotifiedFor = append(rec.NotifiedFor, windowPair{Declared: 1000000, Applied: 200000})
		return true
	})
	stamp := now.Format(time.RFC3339)
	for _, id := range []windowIdentity{unobserved, probe, refusal} {
		e := wfRawEntries(t, root, id.Profile)[id.key()]
		if e == nil {
			t.Fatalf("no entry %q was written", id.key())
		}
		if !strings.HasPrefix(fmt.Sprint(e["last_seen"]), stamp[:19]) || !strings.HasPrefix(fmt.Sprint(e["first_seen"]), stamp[:19]) {
			t.Errorf("entry %q: last_seen %v, first_seen %v; want both stamped %s by the writer", id.key(), e["last_seen"], e["first_seen"], stamp)
		}
	}
	if e := wfRawEntries(t, root, probe.Profile)[probe.key()]; e != nil && (e["source"] != "probe" || fmt.Sprint(e["applied"]) != "200000") {
		t.Errorf("the probe row's source, applied = %v, %v; want probe, 200000 (recomputed by the writer)", e["source"], e["applied"])
	}

	later := now.Add(time.Hour)
	other := wfID("2.1.304")
	wfUpdate(t, root, other, later, wfSetFigure("manager", 200000, later, later))
	entries := wfRawEntries(t, root, other.Profile)
	for _, id := range []windowIdentity{unobserved, probe, refusal} {
		if entries[id.key()] == nil {
			t.Errorf("the write of another identity pruned entry %q", id.key())
		}
	}
	if e := entries[unobserved.key()]; e != nil && e["unobserved_notified"] != true {
		t.Errorf("unobserved_notified did not survive the write of another identity: %v", e)
	}

	wfUpdate(t, root, probe, later, func(rec *windowRecord, exists bool) bool {
		if !exists {
			t.Error("the probe row was not handed to its next mutate as existing")
		}
		return true
	})
	got, ok := readWindowRecord(root, probe, later)
	if !ok || !got.FirstSeen.Equal(now) || !got.LastSeen.Equal(later) {
		t.Errorf("after a rewrite: first_seen %v, last_seen %v, %v; want %v kept and %v stamped", got.FirstSeen, got.LastSeen, ok, now, later)
	}
}

func TestWindowFacts_ClaimSerialisesReadModifyWrite(t *testing.T) {
	t.Run("two writers of different identities keep both entries", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, id := range []windowIdentity{wfID("2.1.308"), wfID("2.1.322")} {
			wg.Add(1)
			go func(i int, id windowIdentity) {
				defer wg.Done()
				errs[i] = updateWindowRecord(root, id, now, func(rec *windowRecord, exists bool) bool {
					time.Sleep(50 * time.Millisecond)
					return wfSetFigure(fmt.Sprintf("agent%d", i), 200000, now, now)(rec, exists)
				})
			}(i, id)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("writer %d: %v", i, err)
			}
		}
		for _, id := range []windowIdentity{wfID("2.1.308"), wfID("2.1.322")} {
			if _, ok := readWindowRecord(root, id, now); !ok {
				t.Errorf("entry %q was lost to the concurrent write", id.key())
			}
		}
	})

	t.Run("two writers of one identity send once", func(t *testing.T) {
		root := t.TempDir()
		now := time.Now()
		pair := windowPair{Declared: 1000000, Applied: 200000}
		var sends atomic.Int32
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = updateWindowRecord(root, wfID("2.1.322"), now, func(rec *windowRecord, exists bool) bool {
					for _, p := range rec.NotifiedFor {
						if p == pair {
							return false
						}
					}
					sends.Add(1)
					time.Sleep(100 * time.Millisecond)
					rec.NotifiedFor = append(rec.NotifiedFor, pair)
					return wfSetFigure("manager", 200000, now, now)(rec, exists)
				})
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("writer %d: %v", i, err)
			}
		}
		if got := sends.Load(); got != 1 {
			t.Errorf("the send ran %d times, want exactly 1: the second writer must re-read the mark under the claim", got)
		}
	})

	t.Run("a stale claim is reclaimed by exactly one of two reclaimers", func(t *testing.T) {
		root := t.TempDir()
		claim := wfClaimPath(root, "fable-5-2")
		wfWrite(t, claim, []byte("dead-holder 2026-11-12T00:00:00Z\n"))
		old := time.Now().Add(-windowClaimTTL - time.Minute)
		if err := os.Chtimes(claim, old, old); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		type result struct {
			release func()
			ok      bool
		}
		results := make([]result, 2)
		for i := range results {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				release, ok := claimWindowRecord(root, "fable-5-2")
				results[i] = result{release, ok}
			}(i)
		}
		close(start)
		wg.Wait()
		won := 0
		for _, r := range results {
			if r.ok {
				won++
			}
		}
		for _, r := range results {
			if r.ok && r.release != nil {
				r.release()
			}
		}
		if won != 1 {
			t.Errorf("%d reclaimers took the stale claim, want exactly 1", won)
		}
	})

	t.Run("a reclaimed holder does not remove the new holder's claim", func(t *testing.T) {
		root := t.TempDir()
		claim := wfClaimPath(root, "fable-5-2")
		releaseA, ok := claimWindowRecord(root, "fable-5-2")
		if !ok || releaseA == nil {
			t.Fatal("the first claim was not taken")
		}
		old := time.Now().Add(-windowClaimTTL - time.Minute)
		if err := os.Chtimes(claim, old, old); err != nil {
			t.Fatal(err)
		}
		releaseB, ok := claimWindowRecord(root, "fable-5-2")
		if !ok || releaseB == nil {
			t.Fatal("a claim older than windowClaimTTL was not reclaimed")
		}
		held := wfRead(t, claim)
		releaseA()
		got, err := os.ReadFile(claim)
		if err != nil || !bytes.Equal(got, held) {
			t.Errorf("the reclaimed holder's release removed or changed the new holder's claim: %q, %v", got, err)
		}
		releaseB()
		if _, err := os.Lstat(claim); !os.IsNotExist(err) {
			t.Errorf("the holder's own release left its claim: %v", err)
		}
	})

	t.Run("a fresh claim held by another is waited for and then refused", func(t *testing.T) {
		root := t.TempDir()
		release, ok := claimWindowRecord(root, "fable-5-2")
		if !ok || release == nil {
			t.Fatal("the first claim was not taken")
		}
		defer release()
		began := time.Now()
		called := false
		err := updateWindowRecord(root, wfID("2.1.322"), time.Now(), func(*windowRecord, bool) bool { called = true; return true })
		if err == nil || called {
			t.Errorf("updateWindowRecord under a held claim: err %v, mutate called %v; want an error and no mutate", err, called)
		}
		if waited := time.Since(began); waited < windowClaimWait/2 {
			t.Errorf("updateWindowRecord gave up after %v, want about windowClaimWait (%v)", waited, windowClaimWait)
		}
	})
}

func TestWindowFacts_PrunesByLastSeen(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	oldID, newID := wfID("2.1.308"), wfID("2.1.322")

	t.Run("a write drops another identity past the horizon and never its own", func(t *testing.T) {
		root := t.TempDir()
		wfUpdate(t, root, oldID, t0, wfSetFigure("x", 200000, t0, t0))
		later := t0.Add(windowEntryPruneAfter + time.Hour)
		wfUpdate(t, root, newID, later, wfSetFigure("y", 200000, later, later))
		entries := wfRawEntries(t, root, oldID.Profile)
		if _, kept := entries[oldID.key()]; kept {
			t.Error("an entry older than windowEntryPruneAfter survived another identity's write")
		}
		if _, kept := entries[newID.key()]; !kept {
			t.Error("the writer's own entry is missing")
		}

		wfPlantRecordFile(t, root, newID.Profile, map[string]windowRecord{
			newID.key(): {ModelID: newID.ModelID, HarnessVersion: newID.HarnessVersion, Declared: newID.Declared, Applied: 200000, LastSeen: t0, Source: "session",
				Agents: map[string]agentWindow{"y": {Applied: 200000, LaunchedAt: t0, LastSeen: t0}}},
		})
		wfUpdate(t, root, newID, later, wfSetFigure("y", 200000, later, later))
		if _, ok := readWindowRecord(root, newID, later); !ok {
			t.Error("the writer's own entry past the horizon was not rewritten by its write")
		}
	})

	t.Run("with no write a read past the horizon is absent", func(t *testing.T) {
		root := t.TempDir()
		wfUpdate(t, root, oldID, t0, wfSetFigure("x", 200000, t0, t0))
		if _, ok := readWindowRecord(root, oldID, t0.Add(windowEntryPruneAfter-time.Hour)); !ok {
			t.Error("an entry inside the horizon was not read")
		}
		if _, ok := readWindowRecord(root, oldID, t0.Add(windowEntryPruneAfter+time.Hour)); ok {
			t.Error("an entry past the horizon was read")
		}
		if _, still := wfRawEntries(t, root, oldID.Profile)[oldID.key()]; !still {
			t.Error("the read removed the entry from the file; it must only read")
		}
	})
}

func TestWindowFacts_InvalidEntryHandedToMutateAsAbsent(t *testing.T) {
	id := wfID("2.1.308")
	now := time.Now().UTC().Truncate(time.Second)
	for name, bad := range map[string]windowRecord{
		"fields not reproducing the key": {ModelID: id.ModelID, HarnessVersion: "2.1.999", Declared: id.Declared, Applied: 200000, Sessions: 7, LastSeen: now, Source: "session",
			Agents: map[string]agentWindow{"ghost": {Applied: 150000, LaunchedAt: now, LastSeen: now}}},
		"past the prune horizon": {ModelID: id.ModelID, HarnessVersion: id.HarnessVersion, Declared: id.Declared, Applied: 200000, Sessions: 7, LastSeen: now.Add(-windowEntryPruneAfter - time.Hour), Source: "session",
			Agents: map[string]agentWindow{"ghost": {Applied: 150000, LaunchedAt: now, LastSeen: now}}},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			wfPlantRecordFile(t, root, id.Profile, map[string]windowRecord{id.key(): bad})
			called := false
			wfUpdate(t, root, id, now, func(rec *windowRecord, exists bool) bool {
				called = true
				if exists || rec.Sessions != 0 || len(rec.Agents) != 0 || rec.Applied != 0 {
					t.Errorf("mutate was handed exists=%v %+v; want the zero entry and exists false", exists, *rec)
				}
				return wfSetFigure("manager", 262144, now, now)(rec, exists)
			})
			if !called {
				t.Fatal("mutate was never called")
			}
			rec, ok := readWindowRecord(root, id, now)
			if !ok {
				t.Fatal("the replaced entry does not read back")
			}
			if rec.Sessions == 7 || rec.Applied != 262144 || rec.HarnessVersion != id.HarnessVersion {
				t.Errorf("the bad entry was not replaced: %+v", rec)
			}
			if _, ghost := rec.Agents["ghost"]; ghost {
				t.Error("the bad entry's figures survived its replacement")
			}
		})
	}
}

func TestWindowDeclared_OneSpelling(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"", 0},
		{"abc", 0},
		{"1e6", 0},
		{"-200000", 0},
		{"0", 0},
		{"9223372036854775808", 0},
		{"18446744073709551615", 0},
		{"1000000", 1000000},
		{"9223372036854775807", 9223372036854775807},
	} {
		if got := windowDeclared(tc.raw); got != tc.want {
			t.Errorf("windowDeclared(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}

	const profileJSON = `{"ANTHROPIC_MODEL":"claude-fable-5-2","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"1000000"}`
	root := setupConfigFactory(t)
	orig := claudeCodeVersionFn
	claudeCodeVersionFn = func() string { return "2.1.322 (Claude Code)" }
	t.Cleanup(func() { claudeCodeVersionFn = orig })
	wfWrite(t, config.ModelsConfigPath(root), []byte(`{"models":{"fable-5-2":`+profileJSON+`}}`))

	env := []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-fable-5-2"}, {Key: config.EnvMaxContextTokens, Value: "1000000"}}
	fact := buildWindowLaunchFact(root, "fable-5-2", env, harnessVersionOf(claudeCodeVersionFn()))
	if fact == nil {
		t.Fatal("buildWindowLaunchFact returned no fact for a declaring claude- profile")
	}
	if fact.Declared != 1000000 {
		t.Errorf("the launch fact's declared = %d, want 1000000", fact.Declared)
	}
	id := fact.identity()
	if id.key() != (windowIdentity{Profile: "fable-5-2", ModelID: "claude-fable-5-2", HarnessVersion: "2.1.322", Declared: windowDeclared("1000000")}).key() {
		t.Errorf("the launch fact's key %q is not the key windowDeclared spells", id.key())
	}

	// The probe row `af config models check` files for the same profile lands under the launch
	// fact's key only if the check spells declared the same way.
	resetWindowPass()
	t.Cleanup(resetWindowPass)
	origProbe := probeHarnessWindowFn
	probeHarnessWindowFn = func(context.Context, map[string]string) (harnessWindowProbe, error) {
		return harnessWindowProbe{ModelID: "claude-fable-5-2", ContextWindow: 1000000, HarnessVersion: "2.1.322"}, nil
	}
	t.Cleanup(func() { probeHarnessWindowFn = origProbe })
	if out, err := runModelsCmd(t, runConfigModelsCheck, "fable-5-2"); err != nil {
		t.Errorf("config models check fable-5-2 with an agreeing probe: %v\n%s", err, out)
	}
	if probe, ok := wdEntry(t, root, "fable-5-2", id.key()); !ok || probe.Source != "probe" || probe.Declared != 1000000 {
		t.Errorf("the probe row is not filed under the launch fact's key %q: entry %+v (present %v)", id.key(), probe, ok)
	}

	// The save-time line reads the record under its own identity of the same profile; it finds the
	// entry filed under the launch fact's key only if both spell declared the same way.
	now := time.Now()
	wfUpdate(t, root, id, now, wfSetFigure("manager", 200000, now, now))
	_, stderr, err := runConfigModelsSetSplit(t, `{"models":{"fable-5-2":`+profileJSON+`}}`)
	if err != nil {
		t.Fatalf("config models set: %v", err)
	}
	if !strings.Contains(stderr, "warning: predicted from the record, a watched launch would be refused: ") ||
		!strings.Contains(stderr, `profile "fable-5-2" declares a 1000000-token window`) {
		t.Errorf("the save-time line did not find the entry filed under the launch fact's key; stderr:\n%s", stderr)
	}
}
