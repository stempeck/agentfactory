package worktree

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/templates"
)

// Meta holds metadata for a git worktree managed by agentfactory.
type Meta struct {
	ID           string   `json:"id"`
	Owner        string   `json:"owner"`
	Branch       string   `json:"branch"`
	Path         string   `json:"path"`           // relative to factory root
	Agents       []string `json:"agents"`
	CreatedAt    string   `json:"created_at"`
	ParentBranch string   `json:"parent_branch"`
}

type CreateOpts struct {
	MaxWorktrees int
}

var statfsFunc = func(path string, buf *syscall.Statfs_t) error {
	return syscall.Statfs(path, buf)
}

var readMemInfoFunc = func() (uint64, error) {
	return readAvailableMemoryMB()
}

var stderrWriter io.Writer = os.Stderr

var worktreeSymlinks = []string{
	filepath.Join(".claude", "skills"),
	".runtime",
	filepath.Join(".agentfactory", "AGENTS.md"),
}

// Contains reports whether candidate lies within the boundary directory. It is
// the load-bearing primitive of the #386 worktree-containment interlock
// (design-doc.md CMP-1 / Phase 1): the function that decides "in bounds vs
// escape" for an agent's working directory.
//
// It is deliberately PURE — it reads no environment, never calls os.Exit, and
// imports nothing from the higher cmd command layer — so the core decision
// cannot disarm itself under test (ADR-004/ADR-005). The boundary is supplied by
// the caller; it is never re-derived from process state here.
//
// Both paths are made absolute (filepath.Abs) and canonicalized
// (filepath.EvalSymlinks); candidate is in-bounds iff filepath.Rel yields a
// relative path with no leading "..". Two deliberate wrinkles:
//
//   - The three worktreeSymlinks (.claude/skills, .runtime,
//     .agentfactory/AGENTS.md) intentionally resolve OUT of the worktree to the
//     shared factory root (that is what EnsureWorktreeLinks creates). A candidate
//     reached THROUGH one of them is EXPECTED in-bounds, so the allowlist is
//     checked on the pre-EvalSymlinks (lexical) path, BEFORE resolution — after
//     resolution the path legitimately points outside the boundary (Gap 3).
//
//   - candidate may not exist yet (a `cd` destination or a not-yet-written
//     file). EvalSymlinks errors on a missing path, so canonicalize resolves the
//     deepest EXISTING ancestor and re-appends the missing tail rather than
//     collapsing to a false "out of bounds" (which would mis-flag legitimate new
//     writes).
//
// An empty boundary or candidate is rejected with an error: resolving "" would
// silently mean the current working directory, turning the boundary into hidden
// process state — exactly what a containment primitive must not depend on.
func Contains(boundary, candidate string) (bool, error) {
	if boundary == "" {
		return false, fmt.Errorf("worktree.Contains: boundary must not be empty")
	}
	if candidate == "" {
		return false, fmt.Errorf("worktree.Contains: candidate must not be empty")
	}

	boundaryAbs, err := filepath.Abs(boundary)
	if err != nil {
		return false, fmt.Errorf("worktree.Contains: resolving boundary %q: %w", boundary, err)
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false, fmt.Errorf("worktree.Contains: resolving candidate %q: %w", candidate, err)
	}

	// Allowlist: a candidate reached THROUGH one of the worktreeSymlinks is
	// EXPECTED in-bounds even though EvalSymlinks would resolve it outside the
	// boundary. Compare the pre-resolution (lexical) paths, before EvalSymlinks.
	for _, link := range worktreeSymlinks {
		if withinLexical(filepath.Join(boundaryAbs, link), candidateAbs) {
			return true, nil
		}
	}

	boundaryCanon, err := canonicalizePath(boundaryAbs)
	if err != nil {
		return false, fmt.Errorf("worktree.Contains: canonicalizing boundary %q: %w", boundary, err)
	}
	candidateCanon, err := canonicalizePath(candidateAbs)
	if err != nil {
		return false, fmt.Errorf("worktree.Contains: canonicalizing candidate %q: %w", candidate, err)
	}

	return withinLexical(boundaryCanon, candidateCanon), nil
}

// withinLexical reports whether candidate is at or under base using a purely
// lexical filepath.Rel check: in-bounds iff Rel succeeds and the result is
// neither ".." nor a "../"-prefixed path. filepath.Rel can succeed with a
// ".."-prefixed result across different roots/volumes, so the prefix is checked
// explicitly rather than relying on err alone. Both arguments must already be
// absolute (and ideally canonicalized).
func withinLexical(base, candidate string) bool {
	rel, err := filepath.Rel(base, candidate)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	return true
}

// canonicalizePath resolves symlinks in absPath. If absPath (or an ancestor)
// does not exist yet — a `cd` destination or a Write target — it resolves the
// deepest existing ancestor and re-appends the missing tail, so a
// not-yet-created path is canonicalized by where it WOULD live instead of
// erroring. Resolving the existing ancestor (rather than using the raw lexical
// path) keeps candidate and boundary in the same resolution domain, avoiding
// spurious escapes on platforms that symlink temp roots (e.g. macOS
// /var -> /private/var, or a symlinked $TMPDIR).
func canonicalizePath(absPath string) (string, error) {
	resolved, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}

	missing := ""
	dir := absPath
	for {
		parent := filepath.Dir(dir)
		missing = filepath.Join(filepath.Base(dir), missing)
		if parent == dir {
			// Reached the volume root without finding an existing ancestor; fall
			// back to the lexical absolute path.
			return absPath, nil
		}
		resolvedParent, err := filepath.EvalSymlinks(parent)
		if err == nil {
			return filepath.Join(resolvedParent, missing), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		dir = parent
	}
}

// EnsureWorktreeLinks creates symlinks from a worktree to factory-root resources.
// Symlink failures are non-fatal — warnings are emitted to stderr.
func EnsureWorktreeLinks(factoryRoot, worktreePath string) error {
	for _, relPath := range worktreeSymlinks {
		source := filepath.Join(worktreePath, relPath)
		target := filepath.Join(factoryRoot, relPath)

		if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
			fmt.Fprintf(stderrWriter, "warning: creating parent dir for %s: %v\n", relPath, err)
			continue
		}

		fi, err := os.Lstat(source)
		if err == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				existing, _ := os.Readlink(source)
				if existing == target {
					continue
				}
				os.Remove(source)
			} else if fi.IsDir() && relPath == filepath.Join(".claude", "skills") {
				merged, err := mergeSkillsDir(target, source)
				if err != nil {
					fmt.Fprintf(stderrWriter, "warning: merging factory skills into %s: %v\n", source, err)
				}
				if merged > 0 {
					fmt.Fprintf(stderrWriter, "info: merged %d factory skill(s) into %s\n", merged, source)
				}
				continue
			} else {
				fmt.Fprintf(stderrWriter, "warning: %s exists as real file/dir, skipping symlink\n", source)
				continue
			}
		}

		if err := os.Symlink(target, source); err != nil {
			fmt.Fprintf(stderrWriter, "warning: symlink %s -> %s: %v\n", source, target, err)
		}
	}
	return nil
}

func mergeSkillsDir(factorySkillsDir, worktreeSkillsDir string) (int, error) {
	entries, err := os.ReadDir(factorySkillsDir)
	if err != nil {
		return 0, fmt.Errorf("reading factory skills dir: %w", err)
	}
	merged := 0
	for _, entry := range entries {
		destPath := filepath.Join(worktreeSkillsDir, entry.Name())
		if _, err := os.Stat(destPath); err == nil {
			continue
		}
		srcPath := filepath.Join(factorySkillsDir, entry.Name())
		srcInfo, err := os.Lstat(srcPath)
		if err != nil {
			fmt.Fprintf(stderrWriter, "warning: lstat %s: %v\n", srcPath, err)
			continue
		}
		if srcInfo.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(stderrWriter, "warning: skipping symlink %s in factory skills\n", srcPath)
			continue
		}
		if srcInfo.IsDir() {
			if err := copyDir(srcPath, destPath); err != nil {
				fmt.Fprintf(stderrWriter, "warning: copying skill %s: %v\n", entry.Name(), err)
				continue
			}
		} else {
			if err := copyFile(srcPath, destPath); err != nil {
				fmt.Fprintf(stderrWriter, "warning: copying skill file %s: %v\n", entry.Name(), err)
				continue
			}
		}
		merged++
	}
	return merged, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}

func readAvailableMemoryMB() (uint64, error) {
	switch runtime.GOOS {
	case "linux":
		return readLinuxMemAvailable()
	case "darwin":
		return readDarwinMemAvailable()
	default:
		return 0, fmt.Errorf("unsupported platform for memory check: %s", runtime.GOOS)
	}
}

func readLinuxMemAvailable() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("reading /proc/meminfo: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("unexpected MemAvailable format: %s", line)
			}
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parsing MemAvailable: %w", err)
			}
			return kb / 1024, nil
		}
	}
	return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
}

func readDarwinMemAvailable() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("running vm_stat: %w", err)
	}

	var pageSize uint64 = 4096
	var freePages, inactivePages uint64

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "page size of") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "size" && i+2 < len(parts) {
					if ps, err := strconv.ParseUint(parts[i+2], 10, 64); err == nil {
						pageSize = ps
					}
					break
				}
			}
		} else if strings.HasPrefix(line, "Pages free:") {
			freePages = parseVMStatValue(line)
		} else if strings.HasPrefix(line, "Pages inactive:") {
			inactivePages = parseVMStatValue(line)
		}
	}

	return (freePages + inactivePages) * pageSize / (1024 * 1024), nil
}

func parseVMStatValue(line string) uint64 {
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return 0
	}
	val := strings.TrimSuffix(parts[len(parts)-1], ".")
	n, _ := strconv.ParseUint(val, 10, 64)
	return n
}

func checkResources(factoryRoot string) error {
	var stat syscall.Statfs_t
	if err := statfsFunc(factoryRoot, &stat); err != nil {
		return fmt.Errorf("checking disk space: %w", err)
	}

	availBytes := stat.Bavail * uint64(stat.Bsize)
	totalBytes := stat.Blocks * uint64(stat.Bsize)
	availGB := availBytes / (1024 * 1024 * 1024)
	var pct uint64
	if totalBytes > 0 {
		pct = (availBytes * 100) / totalBytes
	}

	if availGB < 2 || pct < 10 {
		return fmt.Errorf("insufficient disk to create worktree: %dGB available (%d%% of total). Free space with: af down <agent> --reset", availGB, pct)
	}

	memMB, err := readMemInfoFunc()
	if err != nil {
		return nil
	}
	if memMB < 1024 {
		return fmt.Errorf("insufficient memory to create worktree: %dMB available, 1024MB required. Free memory with: af down <agent> --reset", memMB)
	}

	return nil
}

// GenerateID returns a new unique worktree ID: "wt-" + 6 hex chars.
func GenerateID() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return "wt-" + hex.EncodeToString(b)
}

// BranchName returns the branch name for a worktree: "af/{agent}-{id-suffix}".
func BranchName(agentName, wtID string) string {
	suffix := strings.TrimPrefix(wtID, "wt-")
	return "af/" + agentName + "-" + suffix
}

// WorktreesDir returns the path to the worktrees directory.
func WorktreesDir(factoryRoot string) string {
	return filepath.Join(factoryRoot, ".agentfactory", "worktrees")
}

// metaPath returns the path to a worktree's meta.json file.
func metaPath(factoryRoot, worktreeID string) string {
	return filepath.Join(WorktreesDir(factoryRoot), worktreeID+".meta.json")
}

// WriteMeta writes meta to {factoryRoot}/.agentfactory/worktrees/{meta.ID}.meta.json.
func WriteMeta(factoryRoot string, meta *Meta) error {
	dir := WorktreesDir(factoryRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating worktrees dir: %w", err)
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling meta: %w", err)
	}
	if err := fsutil.WriteFileAtomic(metaPath(factoryRoot, meta.ID), data, 0o644); err != nil {
		return fmt.Errorf("writing meta: %w", err)
	}
	return nil
}

// UpdateMeta overwrites an existing meta file. Semantic alias for WriteMeta.
func UpdateMeta(factoryRoot string, meta *Meta) error {
	return WriteMeta(factoryRoot, meta)
}

// ReadMeta reads and parses the meta file for a worktree.
func ReadMeta(factoryRoot, worktreeID string) (*Meta, error) {
	data, err := os.ReadFile(metaPath(factoryRoot, worktreeID))
	if err != nil {
		return nil, fmt.Errorf("reading meta: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("parsing meta: %w", err)
	}
	return &meta, nil
}

// Create creates a new git worktree, writes the .factory-root redirect,
// and creates the .agentfactory/ structure in the worktree.
// Returns the worktree root path and the Meta.
func Create(factoryRoot, agentName string, opts CreateOpts) (string, *Meta, error) {
	wtID := GenerateID()
	branch := BranchName(agentName, wtID)
	relPath := filepath.Join(".agentfactory", "worktrees", wtID)
	absPath := filepath.Join(factoryRoot, relPath)

	// Acquire creation lock to serialize concurrent Create calls
	lockPath := filepath.Join(factoryRoot, ".agentfactory", "worktrees", ".creation-lock")
	lk := lock.NewWithPath(lockPath)
	if err := lk.Acquire(fmt.Sprintf("pid-%d", os.Getpid())); err != nil {
		return "", nil, fmt.Errorf("acquiring creation lock: %w", err)
	}
	defer lk.Release()

	// Check max worktrees cap
	if opts.MaxWorktrees > 0 {
		count, err := countActiveWorktrees(factoryRoot)
		if err != nil {
			return "", nil, fmt.Errorf("counting worktrees: %w", err)
		}
		if count >= opts.MaxWorktrees {
			return "", nil, fmt.Errorf("cannot create worktree: %d/%d active worktrees at capacity. Free with: af down <agent> --reset", count, opts.MaxWorktrees)
		}
	}

	// Pre-flight resource check
	if err := checkResources(factoryRoot); err != nil {
		return "", nil, err
	}

	// Determine parent branch
	parentCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	parentCmd.Dir = factoryRoot
	parentOut, err := parentCmd.Output()
	if err != nil {
		return "", nil, fmt.Errorf("detecting parent branch: %w", err)
	}
	parentBranch := strings.TrimSpace(string(parentOut))

	// Ensure worktrees directory exists
	if err := os.MkdirAll(WorktreesDir(factoryRoot), 0o755); err != nil {
		return "", nil, fmt.Errorf("creating worktrees dir: %w", err)
	}

	// Create the git worktree
	cmd := exec.Command("git", "worktree", "add", "--quiet", "-b", branch, absPath)
	cmd.Dir = factoryRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("git worktree add: %w\n%s", err, out)
	}

	// Write .factory-root redirect file in the worktree
	wtAfDir := filepath.Join(absPath, ".agentfactory")
	if err := os.MkdirAll(wtAfDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("creating worktree .agentfactory: %w", err)
	}
	redirectPath := filepath.Join(wtAfDir, ".factory-root")
	if err := os.WriteFile(redirectPath, []byte(factoryRoot+"\n"), 0o644); err != nil {
		return "", nil, fmt.Errorf("writing redirect file: %w", err)
	}

	// Create agents directory in worktree
	wtAgentsDir := filepath.Join(wtAfDir, "agents")
	if err := os.MkdirAll(wtAgentsDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("creating worktree agents dir: %w", err)
	}

	EnsureWorktreeLinks(factoryRoot, absPath)

	// Write meta
	meta := &Meta{
		ID:           wtID,
		Owner:        agentName,
		Branch:       branch,
		Path:         relPath,
		Agents:       []string{agentName},
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		ParentBranch: parentBranch,
	}
	if err := WriteMeta(factoryRoot, meta); err != nil {
		return "", nil, fmt.Errorf("writing meta: %w", err)
	}

	return absPath, meta, nil
}

// SetupAgent creates an agent workspace inside an existing worktree.
// Renders CLAUDE.md via templates and generates settings.json from embedded template.
func SetupAgent(factoryRoot, worktreePath, agentName string, isOwner bool) (string, error) {
	// Load agent config from factory root
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(factoryRoot))
	if err != nil {
		return "", fmt.Errorf("loading agent config: %w", err)
	}
	agentEntry, ok := agents.Agents[agentName]
	if !ok {
		return "", fmt.Errorf("agent %q not found in agents.json", agentName)
	}

	// Create agent directory in worktree
	agentDir := filepath.Join(worktreePath, ".agentfactory", "agents", agentName)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", fmt.Errorf("creating agent dir: %w", err)
	}

	// Render CLAUDE.md via templates (not copy)
	tmpl := templates.New()
	templateRole := agentName
	if !tmpl.HasRole(templateRole) {
		templateRole = agentEntry.Type
		if templateRole == "interactive" {
			templateRole = "manager"
		} else if templateRole == "autonomous" {
			templateRole = "supervisor"
		}
	}

	data := templates.RoleData{
		Role:        agentName,
		Description: agentEntry.Description,
		RootDir:     factoryRoot,
		WorkDir:     agentDir,
	}
	claudeContent, err := tmpl.RenderRole(templateRole, data)
	if err != nil {
		return "", fmt.Errorf("rendering CLAUDE.md: %w", err)
	}
	claudeMDPath := filepath.Join(agentDir, "CLAUDE.md")
	if err := os.WriteFile(claudeMDPath, []byte(claudeContent), 0o644); err != nil {
		return "", fmt.Errorf("writing CLAUDE.md: %w", err)
	}

	// Generate settings.json via claude.EnsureSettings (not copy)
	roleType := claude.RoleTypeFor(agentName, agents)
	if err := claude.EnsureSettings(agentDir, roleType); err != nil {
		return "", fmt.Errorf("generating settings.json: %w", err)
	}

	// Create .runtime directory
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", fmt.Errorf("creating .runtime dir: %w", err)
	}

	// Write worktree_id file
	wtID := filepath.Base(worktreePath)
	if err := os.WriteFile(filepath.Join(runtimeDir, "worktree_id"), []byte(wtID+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("writing worktree_id: %w", err)
	}

	// Write worktree_owner file if this agent owns the worktree
	if isOwner {
		if err := os.WriteFile(filepath.Join(runtimeDir, "worktree_owner"), []byte("true\n"), 0o644); err != nil {
			return "", fmt.Errorf("writing worktree_owner: %w", err)
		}
	}

	return agentDir, nil
}

// unlinkBeforeRemove removes worktree symlinks so git worktree remove
// does not treat them as untracked modifications.
func unlinkBeforeRemove(worktreePath string) {
	for _, relPath := range worktreeSymlinks {
		p := filepath.Join(worktreePath, relPath)
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(p)
		}
	}
}

// cleanupMergedSkills removes factory skills that were merge-copied into the
// worktree at creation, WITHOUT touching skills the worktree's branch committed
// itself. Provenance comes from git: a skill directory tracked in the worktree's
// index is branch content and is preserved; an untracked directory whose name
// matches a factory skill is a merge copy and is removed. If the git invocation
// fails (e.g. git unavailable, or the path is not a git repository), nothing is
// removed (ADR-017: when in doubt, don't delete). In production the worktree is
// always a real git worktree, so the query resolves against its own index.
func cleanupMergedSkills(factoryRoot, worktreePath string) {
	skillsRel := filepath.Join(".claude", "skills")
	wtSkillsDir := filepath.Join(worktreePath, skillsRel)
	fi, err := os.Lstat(wtSkillsDir)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return
	}
	factorySkillsDir := filepath.Join(factoryRoot, skillsRel)
	entries, err := os.ReadDir(factorySkillsDir)
	if err != nil {
		return
	}
	tracked, ok := trackedSkillDirs(worktreePath)
	if !ok {
		return // provenance unknown — preserve everything (ADR-017)
	}
	for _, entry := range entries {
		if tracked[entry.Name()] {
			continue // branch-committed skill — preserve
		}
		os.RemoveAll(filepath.Join(wtSkillsDir, entry.Name()))
	}
}

// trackedSkillDirs returns the set of top-level directory names under
// .claude/skills tracked in the worktree's git index. ok is false if the git
// invocation fails (e.g. git unavailable, or worktreePath is not a git
// repository), in which case the caller must not delete anything.
func trackedSkillDirs(worktreePath string) (map[string]bool, bool) {
	cmd := exec.Command("git", "ls-files", "-z", "--", ".claude/skills")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	// git ls-files always emits forward-slash paths regardless of OS, so the
	// literal "/" prefix and split are correct (do not swap for filepath.Separator).
	const prefix = ".claude/skills/"
	dirs := make(map[string]bool)
	for _, p := range strings.Split(string(out), "\x00") {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := p[len(prefix):]
		name := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name = rest[:i]
		}
		if name != "" {
			dirs[name] = true
		}
	}
	return dirs, true
}

// AbsWorktreePath returns the absolute on-disk path of a worktree from its Meta.
// meta.Path is normally relative to the factory root, but a relocated worktree
// may carry an absolute path (issue #392 K1/R4); in that case it is returned
// verbatim so the join does not silently corrupt it. (filepath.Join already
// happens to ignore factoryRoot for an absolute meta.Path, but that is
// accidental — this helper makes the relocation-safety explicit and testable.)
func AbsWorktreePath(factoryRoot string, meta *Meta) string {
	if filepath.IsAbs(meta.Path) {
		return meta.Path
	}
	return filepath.Join(factoryRoot, meta.Path)
}

// HasUnfinishedFormula reports whether any agent registered on the worktree has
// an in-flight formula — a non-empty .runtime/hooked_formula pointer. It guards
// the two destructive worktree paths (GC and the default `af down`) so a worktree
// mid-formula for the owner OR any co-tenant in meta.Agents is preserved (issue
// #392 K6/D4, fixing the Gap-5 blast radius where GC's owner-only session check
// strands co-tenants' work). The worktree path is resolved once via
// AbsWorktreePath so relocated worktrees (Phase 1 K1/R4) are still found.
//
// Missing agent dir / missing .runtime dir / missing file are all non-fatal: that
// agent simply contributes false. Only a pointer that is non-empty after
// strings.TrimSpace protects, mirroring readHookedFormulaID (prime.go) so a
// whitespace-only pointer is treated as empty/removable.
func HasUnfinishedFormula(factoryRoot string, meta *Meta) bool {
	wtPath := AbsWorktreePath(factoryRoot, meta)
	for _, agent := range meta.Agents {
		data, err := os.ReadFile(filepath.Join(wtPath, ".agentfactory", "agents", agent, ".runtime", "hooked_formula"))
		if err != nil {
			continue // missing dir/file → no in-flight formula for this agent
		}
		if strings.TrimSpace(string(data)) != "" {
			return true
		}
	}
	return false
}

// Remove removes a worktree: git worktree remove, delete meta file,
// delete branch. Returns error if worktree has uncommitted changes.
func Remove(factoryRoot string, meta *Meta) error {
	absPath := AbsWorktreePath(factoryRoot, meta)

	unlinkBeforeRemove(absPath)
	cleanupMergedSkills(factoryRoot, absPath)

	// git worktree remove (does NOT force — fails on uncommitted changes)
	cmd := exec.Command("git", "worktree", "remove", absPath)
	cmd.Dir = factoryRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, out)
	}

	// Delete meta file
	mp := metaPath(factoryRoot, meta.ID)
	if err := os.Remove(mp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing meta file: %w", err)
	}

	// Delete branch
	branchCmd := exec.Command("git", "branch", "-D", meta.Branch)
	branchCmd.Dir = factoryRoot
	if out, err := branchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deleting branch %s: %w\n%s", meta.Branch, err, out)
	}

	return nil
}

// ForceRemove removes a worktree forcefully: git worktree remove --force,
// delete meta file, delete branch. Falls back to os.RemoveAll + git worktree
// prune if force-remove fails (file locks, NFS, permissions).
func ForceRemove(factoryRoot string, meta *Meta) error {
	absPath := AbsWorktreePath(factoryRoot, meta)
	unlinkBeforeRemove(absPath)
	cleanupMergedSkills(factoryRoot, absPath)
	cmd := exec.Command("git", "worktree", "remove", "--force", absPath)
	cmd.Dir = factoryRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		if rmErr := os.RemoveAll(absPath); rmErr != nil {
			return fmt.Errorf("git worktree remove --force failed: %w\n%s\nfallback os.RemoveAll also failed: %v", err, out, rmErr)
		}
		pruneCmd := exec.Command("git", "worktree", "prune")
		pruneCmd.Dir = factoryRoot
		pruneCmd.CombinedOutput() // best-effort
	}
	mp := metaPath(factoryRoot, meta.ID)
	if err := os.Remove(mp); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing meta file: %w", err)
	}
	branchCmd := exec.Command("git", "branch", "-D", meta.Branch)
	branchCmd.Dir = factoryRoot
	if out, err := branchCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deleting branch %s: %w\n%s", meta.Branch, err, out)
	}
	return nil
}

// RemoveAgent removes an agent from a worktree's meta.agents list.
// Returns the updated meta and whether the list is now empty.
func RemoveAgent(factoryRoot, worktreeID, agentName string) (*Meta, bool, error) {
	meta, err := ReadMeta(factoryRoot, worktreeID)
	if err != nil {
		return nil, false, fmt.Errorf("reading meta for RemoveAgent: %w", err)
	}

	filtered := make([]string, 0, len(meta.Agents))
	for _, a := range meta.Agents {
		if a != agentName {
			filtered = append(filtered, a)
		}
	}
	meta.Agents = filtered

	if err := WriteMeta(factoryRoot, meta); err != nil {
		return nil, false, fmt.Errorf("writing updated meta: %w", err)
	}

	return meta, len(meta.Agents) == 0, nil
}

// GC scans .agentfactory/worktrees/ for stale worktrees (owner tmux
// session not running) and removes them. Returns count of removed worktrees.
func GC(factoryRoot string) (int, error) {
	dir := WorktreesDir(factoryRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading worktrees dir: %w", err)
	}

	removed := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		wtID := strings.TrimSuffix(entry.Name(), ".meta.json")
		meta, err := ReadMeta(factoryRoot, wtID)
		if err != nil {
			continue
		}

		// Check if owner's tmux session is running
		checkCmd := exec.Command("tmux", "has-session", "-t", "=af-"+meta.Owner)
		if checkCmd.Run() == nil {
			// Session is running, skip
			continue
		}

		// Preserve a worktree with an in-flight formula for any of its agents
		// (owner or co-tenant), even when the owner's session is dead — e.g. after
		// a reboot. meta is read fresh and un-mutated here, so the guard sees the
		// full Agents list (issue #392 K6/D4).
		if HasUnfinishedFormula(factoryRoot, meta) {
			continue
		}

		// Session not running — remove worktree
		if err := ForceRemove(factoryRoot, meta); err != nil {
			continue
		}
		removed++
	}

	return removed, nil
}

// Outcome describes how ResolveOrCreate resolved a worktree for an agent. It
// replaces the former `created bool` 3rd return so reattach / recover / create
// are distinguishable for downstream callers (issue #392 K3).
type Outcome int

const (
	// Reattached: the agent was bound to an already-existing worktree — via the
	// creator's env var, the creator's meta, the agent's own meta, or (when the
	// sidecar was missing/relocated) git's worktree registry. Also the zero
	// value, returned on error paths where the Outcome is not meaningful.
	Reattached Outcome = iota
	// Recovered: an in-flight binding was reconstructed from a durable source.
	// Introduced here for Phase 2's reuse; ResolveOrCreate emits only
	// Reattached / Created in Phase 1.
	Recovered
	// Created: a brand-new worktree was created for a genuinely-new agent.
	Created
)

// IsCreated reports whether a new worktree was created (as opposed to
// reattached or recovered). It is the drop-in replacement for the former
// `created bool` and keeps caller churn minimal.
func (o Outcome) IsCreated() bool { return o == Created }

// String renders the Outcome as a stable, human-readable label. It backs the
// `af up` run-summary breadcrumb (issue #392 K8), which records each agent's
// resolution outcome for unattended bulk starts.
func (o Outcome) String() string {
	switch o {
	case Recovered:
		return "Recovered"
	case Created:
		return "Created"
	default:
		return "Reattached"
	}
}

// ResolveOrCreate returns the worktree a newly-starting agent should use.
//
// Decision order:
//  1. If creatorEnvWT != "", inherit that worktree. creatorEnvWTID is used
//     as the ID; Outcome=Reattached. No disk I/O needed — the env var was set
//     by a trusted source (session.Manager at a prior start).
//  2. Else if creatorAgent != "", look up the creator's worktree via
//     FindByOwner. If found, the new agent inherits. Outcome=Reattached.
//  3. Else run FindByOwner for newAgentName itself (self-adoption). C-7
//     backstop for restart-after-crash when GC may not have cleaned up
//     the prior meta.
//  4. Else rediscover via git's worktree registry (FindByGitRegistry) — the
//     durable authority — when the *.meta.json sidecar is missing or the
//     worktree was relocated (issue #392). A hit self-heals the sidecar;
//     Outcome=Reattached.
//  5. Else run GC (best-effort), then Create a new worktree owned by
//     newAgentName. Outcome=Created.
//
// creatorAgent "" is a valid input meaning "no identifiable creator" — falls
// through to branch 3. At @cli (workspace shell with no AF_ROLE), this is the
// expected case.
//
// Returned path is absolute; id is "wt-<6hex>". Caller is responsible for
// calling SetupAgent(root, path, newAgentName, outcome.IsCreated()) and, if
// launching a tmux session, session.Manager.SetWorktree(path, id).
func ResolveOrCreate(factoryRoot, newAgentName, creatorAgent, creatorEnvWT, creatorEnvWTID string, opts CreateOpts) (string, string, Outcome, error) {
	if creatorEnvWT != "" {
		return creatorEnvWT, creatorEnvWTID, Reattached, nil
	}
	if creatorAgent != "" {
		meta, err := FindByOwner(factoryRoot, creatorAgent)
		if err != nil {
			return "", "", Reattached, fmt.Errorf("FindByOwner(%q): %w", creatorAgent, err)
		}
		if meta != nil {
			// CMP-1 choke-point ownership guard (issue #401): only adopt a
			// candidate whose absolute path lives under the factory's worktrees
			// dir. A Contains error means "can't prove ownership" ⇒ refuse. On
			// refusal, WARN and fall through to the next step (ultimately Create)
			// — never an error: up.go treats a ResolveOrCreate error as "skip this
			// agent," which would strand it (AC-4(ii)).
			if in, cerr := Contains(WorktreesDir(factoryRoot), AbsWorktreePath(factoryRoot, meta)); cerr != nil || !in {
				fmt.Fprintf(stderrWriter, "warning: ResolveOrCreate: creator worktree for agent %q at %s is not factory-owned; not adopting\n", creatorAgent, AbsWorktreePath(factoryRoot, meta))
			} else {
				return AbsWorktreePath(factoryRoot, meta), meta.ID, Reattached, nil
			}
		}
	}
	if selfMeta, err := FindByOwner(factoryRoot, newAgentName); err != nil {
		return "", "", Reattached, fmt.Errorf("FindByOwner(self=%q): %w", newAgentName, err)
	} else if selfMeta != nil {
		if in, cerr := Contains(WorktreesDir(factoryRoot), AbsWorktreePath(factoryRoot, selfMeta)); cerr != nil || !in {
			fmt.Fprintf(stderrWriter, "warning: ResolveOrCreate: self worktree for agent %q at %s is not factory-owned; not adopting\n", newAgentName, AbsWorktreePath(factoryRoot, selfMeta))
		} else {
			return AbsWorktreePath(factoryRoot, selfMeta), selfMeta.ID, Reattached, nil
		}
	}
	// 5th fallback: git's worktree registry is the durable authority that
	// survives a lost/relocated sidecar (issue #392). Runs only after the meta
	// fast paths above, preserving the C-5 meta-present behavior.
	if regMeta, err := FindByGitRegistry(factoryRoot, newAgentName); err != nil {
		return "", "", Reattached, fmt.Errorf("FindByGitRegistry(self=%q): %w", newAgentName, err)
	} else if regMeta != nil {
		if in, cerr := Contains(WorktreesDir(factoryRoot), AbsWorktreePath(factoryRoot, regMeta)); cerr != nil || !in {
			fmt.Fprintf(stderrWriter, "warning: ResolveOrCreate: registry worktree for agent %q at %s is not factory-owned; not adopting\n", newAgentName, AbsWorktreePath(factoryRoot, regMeta))
		} else {
			return AbsWorktreePath(factoryRoot, regMeta), regMeta.ID, Reattached, nil
		}
	}
	_, _ = GC(factoryRoot)
	wtPath, meta, err := Create(factoryRoot, newAgentName, opts)
	if err != nil {
		return "", "", Reattached, fmt.Errorf("Create(%q): %w", newAgentName, err)
	}
	return wtPath, meta.ID, Created, nil
}

// FindByOwner scans .agentfactory/worktrees/*.meta.json for a worktree
// owned by the given agent. Returns nil if no active worktree found.
func FindByOwner(factoryRoot, agentName string) (*Meta, error) {
	dir := WorktreesDir(factoryRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading worktrees dir: %w", err)
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		wtID := strings.TrimSuffix(entry.Name(), ".meta.json")
		meta, err := ReadMeta(factoryRoot, wtID)
		if err != nil {
			continue
		}
		if meta.Owner == agentName {
			return meta, nil
		}
	}

	return nil, nil
}

// isWorktreeSuffix reports whether s is a valid worktree-id suffix: exactly the
// 6 lowercase-hex chars GenerateID appends after "wt-". Used to reject branches
// like "af/<other-agent>-..." that merely share an agent-name prefix.
func isWorktreeSuffix(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// isAdoptableFactoryWorktree reports whether a reattach candidate is a trustworthy
// factory-owned worktree worth self-healing a sidecar for (issue #401 AC-2/AC-3). Three
// legs, short-circuiting on the first failure with a human-readable reason the caller
// surfaces as a WARN before falling through to Create:
//   - containment: wtPath is under WorktreesDir(factoryRoot) (the AC-1 boundary; the
//     owner-marker is satisfied structurally here, never by reading the
//     attacker-controllable worktree_owner file — security D5-2 REJECTED). A Contains
//     resolution error means "can't prove ownership" ⇒ refuse (err != nil || !in).
//   - identity: the on-disk basename equals the branch-derived id (the recover-2e45ca
//     case synthesizes wt-2e45ca yet sits in a dir named recover-2e45ca — they disagree).
//   - base non-divergence: the branch is built on the factory's current base (the
//     Phase-1 isBaseNonDivergent helper, directional via merge-base --is-ancestor).
//
// Pass the ABSOLUTE wtPath from the git registry, not the relative-or-absolute
// meta.Path: feeding a relocation-aware path to Contains would defeat containment.
func isAdoptableFactoryWorktree(factoryRoot, wtPath, wtID, branch string) (ok bool, reason string) {
	if in, err := Contains(WorktreesDir(factoryRoot), wtPath); err != nil || !in {
		return false, fmt.Sprintf("candidate worktree at %s is not under %s (not factory-owned)", wtPath, WorktreesDir(factoryRoot))
	}
	if filepath.Base(wtPath) != wtID {
		return false, fmt.Sprintf("candidate worktree has inconsistent identity (path basename %q != id %q)", filepath.Base(wtPath), wtID)
	}
	if !isBaseNonDivergent(factoryRoot, branch) {
		return false, fmt.Sprintf("candidate branch %s is not based on the current default branch (diverged)", branch)
	}
	return true, ""
}

// FindByGitRegistry rediscovers an agent's worktree from git's durable worktree
// registry when the *.meta.json sidecar is missing or the worktree was
// relocated (issue #392 K2). It parses `git worktree list --porcelain`, matches
// the agent's `af/<agent>-<suffix>` branch (BranchName semantics, :280-283),
// validates the resolved path on disk, and — on a single valid match —
// self-heals the sidecar via WriteMeta.
//
// Meta.Agents is reconstructed as the owner plus every per-agent dir under
// {wtPath}/.agentfactory/agents/ whose .runtime/worktree_id equals this
// worktree's id (issue #392 A2/R11/HIGH-1) — never narrowed to [self], so idle
// branch-less co-tenants are preserved while lingering deregistered dirs (stale
// worktree_id) are excluded. ParentBranch is recovered from the branch
// merge-base. WARN (never silently narrow) if either is unrecoverable.
//
// Returns (nil, nil) — so ResolveOrCreate falls through to Create — when there
// is no match, more than one match (ambiguous: warns, R3/Gap-4), or the single
// match's path is stale/pruned (validated before any self-heal, R5/L4).
func FindByGitRegistry(factoryRoot, agentName string) (*Meta, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = factoryRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git worktree list --porcelain: %w", err)
	}

	branchPrefix := "af/" + agentName + "-"
	type registryEntry struct{ path, branch string }
	var matches []registryEntry
	var curPath, curBranch string
	flush := func() {
		if curPath != "" && strings.HasPrefix(curBranch, branchPrefix) &&
			isWorktreeSuffix(strings.TrimPrefix(curBranch, branchPrefix)) {
			matches = append(matches, registryEntry{curPath, curBranch})
		}
		curPath, curBranch = "", ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			curPath = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch "):
			curBranch = strings.TrimPrefix(strings.TrimPrefix(line, "branch "), "refs/heads/")
		}
	}
	flush() // final block (porcelain ends with a blank line, but be defensive)
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scanning git worktree list: %w", err)
	}

	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		// fall through to self-heal
	default:
		fmt.Fprintf(stderrWriter, "warning: FindByGitRegistry: %d worktrees match branch %s* for agent %q — cannot auto-reattach without guessing; resolve manually\n", len(matches), branchPrefix, agentName)
		return nil, nil
	}

	wtPath := matches[0].path
	branch := matches[0].branch
	wtID := "wt-" + strings.TrimPrefix(branch, branchPrefix)

	// Validate the path exists on disk AND is a live git worktree before
	// reattach — a pruned/ghost registry entry must fall through to Create
	// rather than self-heal a dangling sidecar (R5/L4).
	if _, statErr := os.Stat(wtPath); statErr != nil {
		return nil, nil
	}
	revCmd := exec.Command("git", "rev-parse", "--git-dir")
	revCmd.Dir = wtPath
	if revErr := revCmd.Run(); revErr != nil {
		return nil, nil
	}

	// Trust gate (issue #401 R2): the two gates above prove the candidate is live, not
	// that it is OURS and current-base. Refuse to self-heal a factory-owned sidecar for
	// an out-of-root, inconsistent-identity, or divergent-base candidate — once a bad
	// meta persists, every later FindByOwner reattach perpetuates the bad binding, so the
	// check MUST precede WriteMeta. Pass the absolute wtPath from the registry to Contains.
	if ok, reason := isAdoptableFactoryWorktree(factoryRoot, wtPath, wtID, branch); !ok {
		fmt.Fprintf(stderrWriter, "warning: FindByGitRegistry: %s for agent %q; not adopting — a fresh worktree will be created\n", reason, agentName)
		return nil, nil
	}

	meta := &Meta{
		ID:           wtID,
		Owner:        agentName,
		Branch:       branch,
		Path:         relocationAwarePath(factoryRoot, wtPath),
		Agents:       reconstructAgents(factoryRoot, wtPath, wtID, agentName),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
		ParentBranch: recoverParentBranch(factoryRoot, branch, agentName),
	}
	if err := WriteMeta(factoryRoot, meta); err != nil {
		return nil, fmt.Errorf("self-healing meta for %s: %w", wtID, err)
	}
	return meta, nil
}

// relocationAwarePath returns the value to store in Meta.Path: relative to the
// factory root when the worktree lives inside it (matching Create), or the
// absolute path when it was relocated outside (issue #392 K1/R4).
func relocationAwarePath(factoryRoot, wtPath string) string {
	rel, err := filepath.Rel(factoryRoot, wtPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return wtPath // relocated outside factory root → absolute
	}
	return rel
}

// reconstructAgents rebuilds Meta.Agents for a self-healed worktree: the owner
// plus every agent whose per-agent dir under {wtPath}/.agentfactory/agents/
// carries a .runtime/worktree_id equal to this worktree's id. The worktree_id
// filter is the disambiguator (issue #392 A2): it restores branch-less,
// pointer-less co-tenants (SetupAgent writes a dir for owner and co-tenant
// alike, :420/:432) while excluding lingering deregistered dirs — RemoveAgent
// edits meta.Agents but never deletes the dir (:632-651), so a deregistered
// tenant's dir lingers with a stale/absent worktree_id. Never narrows to [self].
func reconstructAgents(factoryRoot, wtPath, wtID, owner string) []string {
	agents := []string{owner}
	seen := map[string]bool{owner: true}
	agentsDir := filepath.Join(wtPath, ".agentfactory", "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(stderrWriter, "warning: FindByGitRegistry: reading agent dirs in %s: %v (Meta.Agents may be incomplete)\n", agentsDir, err)
		}
		return agents
	}
	for _, e := range entries {
		if !e.IsDir() || seen[e.Name()] {
			continue
		}
		idData, err := os.ReadFile(filepath.Join(agentsDir, e.Name(), ".runtime", "worktree_id"))
		if err != nil {
			continue // no pointer → not a current tenant of this worktree
		}
		if strings.TrimSpace(string(idData)) != wtID {
			continue // stale/mismatched → lingering deregistered dir (A2)
		}
		agents = append(agents, e.Name())
		seen[e.Name()] = true
	}
	return agents
}

// recoverParentBranch best-effort recovers the branch a worktree was forked
// from: the factory root's current branch, confirmed (via git merge-base) to be
// an ancestor of the worktree branch. Returns "" and WARNs if unrecoverable —
// never silently fabricates (issue #392 R11/H2). ParentBranch has no production
// readers today, so recovery is correct-by-construction.
func recoverParentBranch(factoryRoot, branch, agentName string) string {
	headCmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	headCmd.Dir = factoryRoot
	headOut, err := headCmd.Output()
	if err != nil {
		fmt.Fprintf(stderrWriter, "warning: FindByGitRegistry: cannot determine parent branch for agent %q: %v\n", agentName, err)
		return ""
	}
	parent := strings.TrimSpace(string(headOut))
	mbCmd := exec.Command("git", "merge-base", parent, branch)
	mbCmd.Dir = factoryRoot
	if err := mbCmd.Run(); err != nil {
		fmt.Fprintf(stderrWriter, "warning: FindByGitRegistry: parent branch for agent %q unrecoverable from merge-base(%s, %s): %v\n", agentName, parent, branch, err)
		return ""
	}
	return parent
}

// isBaseNonDivergent reports whether candidateBranch is built on top of the
// factory root's current base (exit 0) vs diverged from it (exit 1). On an
// unresolvable base (e.g. detached HEAD -> literal "HEAD") it returns false so
// the caller surfaces a WARN and falls through to Create (the safe default;
// never guess a base branch). base = factory HEAD, resolved in-layer with the
// same idiom recoverParentBranch uses. Unlike recoverParentBranch's bare
// merge-base (which only proves shared ancestry), --is-ancestor is directional:
// it accepts a candidate that is merely ahead of base and rejects one that has
// diverged (issue #401 AC-2).
func isBaseNonDivergent(factoryRoot, candidateBranch string) bool {
	head := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	head.Dir = factoryRoot
	out, err := head.Output()
	base := strings.TrimSpace(string(out))
	if err != nil || base == "" || base == "HEAD" {
		return false // detached/unresolvable => not adoptable
	}
	anc := exec.Command("git", "merge-base", "--is-ancestor", base, candidateBranch)
	anc.Dir = factoryRoot
	return anc.Run() == nil // exit 0 => base is ancestor of candidate => adoptable
}

// FindByAgent scans .agentfactory/worktrees/*.meta.json for a worktree
// where agentName appears in meta.Agents or as meta.Owner.
// Returns nil if no matching worktree found.
func FindByAgent(factoryRoot, agentName string) (*Meta, error) {
	dir := WorktreesDir(factoryRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading worktrees dir: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		wtID := strings.TrimSuffix(entry.Name(), ".meta.json")
		meta, err := ReadMeta(factoryRoot, wtID)
		if err != nil {
			continue
		}
		for _, a := range meta.Agents {
			if a == agentName {
				return meta, nil
			}
		}
		if meta.Owner == agentName {
			return meta, nil
		}
	}
	return nil, nil
}

func countActiveWorktrees(factoryRoot string) (int, error) {
	dir := WorktreesDir(factoryRoot)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading worktrees dir: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".meta.json") {
			count++
		}
	}
	return count, nil
}
