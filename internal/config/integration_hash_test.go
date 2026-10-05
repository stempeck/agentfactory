package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
)

// ihFile is one fixture file: a slash-separated relpath, its content and the mode to force on it.
type ihFile struct {
	rel     string
	content string
	mode    os.FileMode
}

// ihTree writes files under a fresh dir and FORCES each mode with os.Chmod (the process umask
// would otherwise strip group-write bits and make a mode fixture vacuous). Parent dirs get dirMode.
func ihTree(t *testing.T, files []ihFile, dirMode os.FileMode) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f.rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f.content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, f.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != root {
			return os.Chmod(p, dirMode)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return root
}

func ihSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ihStatMode asserts the precondition that makes a mode fixture non-vacuous.
func ihStatMode(t *testing.T, root, rel string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != want {
		t.Fatalf("fixture precondition: %s has mode %04o, want %04o (the umask test is vacuous unless the modes really differ)", rel, got, want)
	}
}

// ihHash calls the hash and fails the test on error (never dereferences a nil map).
func ihHash(t *testing.T, root string, declared []string) (string, map[string]string) {
	t.Helper()
	sum, files, err := IntegrationContentHash(root, declared)
	if err != nil {
		t.Fatalf("IntegrationContentHash(%s, %v) error: %v", root, declared, err)
	}
	if sum == "" {
		t.Fatalf("IntegrationContentHash(%s, %v) returned an empty sum", root, declared)
	}
	return sum, files
}

var ihDeclared = []string{"claude-plugin", "af/install.sh"}

func ihBaseFiles(fileMode, execMode os.FileMode) []ihFile {
	return []ihFile{
		{"claude-plugin/.claude-plugin/plugin.json", `{"name":"acme-guard"}` + "\n", fileMode},
		{"claude-plugin/bin/acme-tool", "#!/bin/sh\necho tool\n", execMode},
		{"claude-plugin/hooks/hooks.json", "{}\n", fileMode},
		{"af/install.sh", "#!/bin/sh\necho install\n", execMode},
	}
}

// TestIntegrationContentHash_StableAcrossUmask pins H3-13 / spec L263: the per-file <mode> is
// normalized to 0755 (any exec bit) or 0644, so a umask-002 clone (0664/0775) and a umask-077
// clone (0600/0700) hash exactly like a umask-022 one (0644/0755).
func TestIntegrationContentHash_StableAcrossUmask(t *testing.T) {
	ref := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
	group := ihTree(t, ihBaseFiles(0o664, 0o775), 0o775)
	private := ihTree(t, ihBaseFiles(0o600, 0o700), 0o700)

	ihStatMode(t, ref, "af/install.sh", 0o755)
	ihStatMode(t, ref, "claude-plugin/hooks/hooks.json", 0o644)
	ihStatMode(t, group, "af/install.sh", 0o775)
	ihStatMode(t, group, "claude-plugin/hooks/hooks.json", 0o664)
	ihStatMode(t, private, "af/install.sh", 0o700)
	ihStatMode(t, private, "claude-plugin/hooks/hooks.json", 0o600)

	refSum, refFiles := ihHash(t, ref, ihDeclared)
	for name, root := range map[string]string{"umask_002": group, "umask_077": private} {
		sum, files := ihHash(t, root, ihDeclared)
		if sum != refSum {
			t.Errorf("%s tree hashes %s, umask-022 tree hashes %s: modes must be normalized to 0644/0755", name, sum, refSum)
		}
		if !reflect.DeepEqual(files, refFiles) {
			t.Errorf("%s per-file map differs from the umask-022 one:\n got %v\nwant %v", name, files, refFiles)
		}
	}
}

// TestIntegrationContentHash_Rules pins the canonical-hash contract (spec L260-267; design L371).
// Every refusal row first proves the same tree WITHOUT the offending entry hashes, so a hash that
// refuses everything cannot pass a row.
func TestIntegrationContentHash_Rules(t *testing.T) {
	t.Run("canonical_line_format", func(t *testing.T) {
		files := append(ihBaseFiles(0o644, 0o755), ihFile{"notes.txt", "not declared\n", 0o644})
		root := ihTree(t, files, 0o755)
		sum, got := ihHash(t, root, ihDeclared)

		declaredFiles := ihBaseFiles(0o644, 0o755)
		sort.Slice(declaredFiles, func(i, j int) bool { return declaredFiles[i].rel < declaredFiles[j].rel })
		var lines strings.Builder
		wantKeys := make([]string, 0, len(declaredFiles))
		for _, f := range declaredFiles {
			mode := "0644"
			if f.mode&0o111 != 0 {
				mode = "0755"
			}
			fmt.Fprintf(&lines, "%s %s %s\n", f.rel, mode, ihSHA(f.content))
			wantKeys = append(wantKeys, f.rel)
		}
		if want := ihSHA(lines.String()); sum != want {
			t.Errorf("aggregate sum = %s, want sha256 over the sorted \"<relpath> <mode> <sha256>\\n\" lines = %s\nlines:\n%s", sum, want, lines.String())
		}
		var gotKeys []string
		for k := range got {
			gotKeys = append(gotKeys, k)
		}
		sort.Strings(gotKeys)
		if !reflect.DeepEqual(gotKeys, wantKeys) {
			t.Errorf("per-file map keys = %v, want exactly the declared files %v (notes.txt is not declared)", gotKeys, wantKeys)
		}
		for _, f := range declaredFiles {
			if !strings.Contains(got[f.rel], ihSHA(f.content)) {
				t.Errorf("per-file entry for %s = %q, want it to carry the content sha256 %s", f.rel, got[f.rel], ihSHA(f.content))
			}
		}
	})

	t.Run("exec_bit_participates", func(t *testing.T) {
		a := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
		flipped := ihBaseFiles(0o644, 0o755)
		flipped[3].mode = 0o644 // af/install.sh loses its exec bit
		b := ihTree(t, flipped, 0o755)
		sumA, _ := ihHash(t, a, ihDeclared)
		sumB, _ := ihHash(t, b, ihDeclared)
		if sumA == sumB {
			t.Errorf("clearing the exec bit on af/install.sh did not change the hash (%s): <mode> is 0755 when any exec bit is set, else 0644", sumA)
		}
	})

	t.Run("content_change_names_file", func(t *testing.T) {
		a := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
		edited := ihBaseFiles(0o644, 0o755)
		edited[2].content = "{\"edited\":true}\n"
		b := ihTree(t, edited, 0o755)
		sumA, filesA := ihHash(t, a, ihDeclared)
		sumB, filesB := ihHash(t, b, ihDeclared)
		if sumA == sumB {
			t.Fatalf("editing claude-plugin/hooks/hooks.json did not change the aggregate hash")
		}
		var differ []string
		for k := range filesA {
			if filesA[k] != filesB[k] {
				differ = append(differ, k)
			}
		}
		sort.Strings(differ)
		if !reflect.DeepEqual(differ, []string{"claude-plugin/hooks/hooks.json"}) {
			t.Errorf("per-file map differs at %v, want exactly [claude-plugin/hooks/hooks.json] (verify names the changed file)", differ)
		}
	})

	t.Run("creation_order_independent", func(t *testing.T) {
		fwd := ihBaseFiles(0o644, 0o755)
		rev := make([]ihFile, len(fwd))
		for i := range fwd {
			rev[len(fwd)-1-i] = fwd[i]
		}
		sumA, _ := ihHash(t, ihTree(t, fwd, 0o755), ihDeclared)
		sumB, _ := ihHash(t, ihTree(t, rev, 0o755), []string{"af/install.sh", "claude-plugin"})
		if sumA != sumB {
			t.Errorf("same content created in a different order / declared in a different order hashes differently: %s vs %s", sumA, sumB)
		}
	})

	t.Run("git_dir_excluded", func(t *testing.T) {
		clean := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
		withGit := append(ihBaseFiles(0o644, 0o755),
			ihFile{"claude-plugin/.git/config", "[core]\n", 0o644},
			ihFile{"claude-plugin/.git/HEAD", "ref: refs/heads/main\n", 0o644},
		)
		dirty := ihTree(t, withGit, 0o755)
		sumClean, _ := ihHash(t, clean, ihDeclared)
		sumDirty, files := ihHash(t, dirty, ihDeclared)
		if sumClean != sumDirty {
			t.Errorf(".git/ contents changed the hash (%s vs %s): .git/ is excluded", sumDirty, sumClean)
		}
		for k := range files {
			if strings.Contains("/"+k, "/.git/") {
				t.Errorf("per-file map carries %q: .git/ is excluded", k)
			}
		}
	})

	t.Run("upstream_only_under_declared_path", func(t *testing.T) {
		files := append(ihBaseFiles(0o644, 0o755),
			ihFile{".upstream/src/a.txt", "upstream a\n", 0o644},
			ihFile{".upstream/other/b.txt", "upstream b\n", 0o644},
		)
		root := ihTree(t, files, 0o755)
		_, notDeclared := ihHash(t, root, ihDeclared)
		for k := range notDeclared {
			if strings.HasPrefix(k, ".upstream/") {
				t.Errorf("per-file map carries %q although no declared path points into .upstream/", k)
			}
		}
		_, declared := ihHash(t, root, append(append([]string{}, ihDeclared...), ".upstream/src"))
		if _, ok := declared[".upstream/src/a.txt"]; !ok {
			t.Errorf("declared .upstream/src but .upstream/src/a.txt is not hashed: %v", declared)
		}
		if _, ok := declared[".upstream/other/b.txt"]; ok {
			t.Errorf(".upstream/other/b.txt is hashed although only .upstream/src is declared")
		}
		// The two roots whose walk meets the .upstream dir entry itself: "." must skip it,
		// ".upstream" must not.
		_, whole := ihHash(t, root, []string{"."})
		if _, ok := whole[ihBaseFiles(0o644, 0o755)[0].rel]; !ok {
			t.Errorf(`control: declared "." but the base content is not hashed: %v`, whole)
		}
		for k := range whole {
			if strings.HasPrefix(k, ".upstream/") {
				t.Errorf(`per-file map carries %q: declaring "." does not declare .upstream/`, k)
			}
		}
		_, upstream := ihHash(t, root, []string{".upstream"})
		for _, k := range []string{".upstream/src/a.txt", ".upstream/other/b.txt"} {
			if _, ok := upstream[k]; !ok {
				t.Errorf("declared .upstream but %s is not hashed: %v", k, upstream)
			}
		}
	})

	t.Run("symlink_refused", func(t *testing.T) {
		root := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
		ihHash(t, root, ihDeclared) // control: the tree without the symlink hashes
		if err := os.Symlink("/etc/passwd", filepath.Join(root, "claude-plugin", "hooks", "evil-link")); err != nil {
			t.Fatal(err)
		}
		if sum, _, err := IntegrationContentHash(root, ihDeclared); err == nil {
			t.Errorf("a symlink inside a declared dir must be refused; got sum %s", sum)
		} else if !strings.Contains(err.Error(), "evil-link") {
			t.Errorf("symlink refusal must name the entry evil-link: %v", err)
		}
	})

	t.Run("declared_root_symlink_refused", func(t *testing.T) {
		root := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
		ihHash(t, root, ihDeclared) // control
		outside := ihTree(t, []ihFile{{"x.json", "{}\n", 0o644}}, 0o755)
		if err := os.Symlink(outside, filepath.Join(root, "linked-plugin")); err != nil {
			t.Fatal(err)
		}
		if sum, _, err := IntegrationContentHash(root, append(append([]string{}, ihDeclared...), "linked-plugin")); err == nil {
			t.Errorf("a declared path that is itself a symlink must be refused; got sum %s", sum)
		} else if !strings.Contains(err.Error(), "linked-plugin") {
			t.Errorf("symlink refusal must name linked-plugin: %v", err)
		}
	})

	t.Run("fifo_refused", func(t *testing.T) {
		root := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
		ihHash(t, root, ihDeclared) // control
		if err := syscall.Mkfifo(filepath.Join(root, "claude-plugin", "hooks", "pipe"), 0o644); err != nil {
			t.Skipf("mkfifo unsupported here: %v", err)
		}
		if sum, _, err := IntegrationContentHash(root, ihDeclared); err == nil {
			t.Errorf("an irregular file (FIFO) inside a declared dir must be refused; got sum %s", sum)
		} else if !strings.Contains(err.Error(), "pipe") {
			t.Errorf("irregular-file refusal must name the entry pipe: %v", err)
		}
	})

	t.Run("file_cap_2000", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "bulk")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2000; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d", i)), []byte{byte(i)}, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		_, files := ihHash(t, root, []string{"bulk"}) // exactly at the cap: accepted
		if len(files) != 2000 {
			t.Errorf("at-cap tree hashed %d files, want 2000", len(files))
		}
		if err := os.WriteFile(filepath.Join(dir, "f2000"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if sum, _, err := IntegrationContentHash(root, []string{"bulk"}); err == nil {
			t.Errorf("2,001 declared files must be refused (cap 2,000); got sum %s", sum)
		}
	})

	t.Run("byte_cap_16MiB", func(t *testing.T) {
		const capBytes = 16 << 20
		root := t.TempDir()
		dir := filepath.Join(root, "bulk")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "big"), make([]byte, capBytes-10), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "small"), make([]byte, 10), 0o644); err != nil {
			t.Fatal(err)
		}
		ihHash(t, root, []string{"bulk"}) // exactly 16 MiB in total: accepted
		if err := os.WriteFile(filepath.Join(dir, "small"), make([]byte, 11), 0o644); err != nil {
			t.Fatal(err)
		}
		if sum, _, err := IntegrationContentHash(root, []string{"bulk"}); err == nil {
			t.Errorf("16 MiB + 1 byte of declared content must be refused (cap 16 MiB); got sum %s", sum)
		}
	})
}

// A declared root reached through a symlinked parent dir points the hash outside the tree even
// though its own last component is a real dir (blind review iteration 1, F4c).
func TestIntegrationContentHash_RefusesSymlinkedIntermediateDir(t *testing.T) {
	root := ihTree(t, ihBaseFiles(0o644, 0o755), 0o755)
	outside := ihTree(t, []ihFile{{"inner/.claude-plugin/plugin.json", `{"name":"outside"}` + "\n", 0o644}}, 0o755)
	if err := os.Symlink(outside, filepath.Join(root, "vendor")); err != nil {
		t.Fatal(err)
	}
	declared := append(append([]string{}, ihDeclared...), "vendor/inner")
	if sum, _, err := IntegrationContentHash(root, declared); err == nil {
		t.Errorf("a declared path under a symlinked dir must be refused; got sum %s", sum)
	} else if !strings.Contains(err.Error(), "vendor") {
		t.Errorf("the refusal must name the symlinked component vendor: %v", err)
	}
}

func TestPR724_T18_FileSHA256ExportedHelper(t *testing.T) {
	t.Run("digest_matches_sha256sum", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "external-write")
		if err := os.WriteFile(p, []byte("installed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := FileSHA256(p)
		if err != nil {
			t.Fatalf("FileSHA256: %v", err)
		}
		if want := "7d0698689b2d55cbce578d325da39bae00d260dc71c14a26909461903cc06ca6"; got != want {
			t.Errorf("FileSHA256 = %q, want the lowercase hex digest %q that every recorded external_write_hashes entry uses", got, want)
		}
	})
	t.Run("missing_path_is_not_exist", func(t *testing.T) {
		if _, err := FileSHA256(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("FileSHA256(missing) err = %v, want one wrapping fs.ErrNotExist", err)
		}
	})
}
