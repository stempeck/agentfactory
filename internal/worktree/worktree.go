package worktree

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

var worktreeSymlinks = []string{
	filepath.Join(".claude", "skills"),
	".runtime",
	"AGENTS.md",
}

// EnsureWorktreeLinks creates symlinks from a worktree to factory-root resources.
// Symlink failures are non-fatal — warnings are emitted to stderr.
func EnsureWorktreeLinks(factoryRoot, worktreePath string) error {
	for _, relPath := range worktreeSymlinks {
		source := filepath.Join(worktreePath, relPath)
		target := filepath.Join(factoryRoot, relPath)

		if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: creating parent dir for %s: %v\n", relPath, err)
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
			} else {
				fmt.Fprintf(os.Stderr, "warning: %s exists as real file/dir, skipping symlink\n", source)
				continue
			}
		}

		if err := os.Symlink(target, source); err != nil {
			fmt.Fprintf(os.Stderr, "warning: symlink %s -> %s: %v\n", source, target, err)
		}
	}
	return nil
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

// Remove removes a worktree: git worktree remove, delete meta file,
// delete branch. Returns error if worktree has uncommitted changes.
func Remove(factoryRoot string, meta *Meta) error {
	absPath := filepath.Join(factoryRoot, meta.Path)

	unlinkBeforeRemove(absPath)

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
	absPath := filepath.Join(factoryRoot, meta.Path)
	unlinkBeforeRemove(absPath)
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

		// Session not running — remove worktree
		if err := ForceRemove(factoryRoot, meta); err != nil {
			continue
		}
		removed++
	}

	return removed, nil
}

// ResolveOrCreate returns the worktree a newly-starting agent should use.
//
// Decision order:
//  1. If creatorEnvWT != "", inherit that worktree. creatorEnvWTID is used
//     as the ID; created=false. No disk I/O needed — the env var was set by
//     a trusted source (session.Manager at a prior start).
//  2. Else if creatorAgent != "", look up the creator's worktree via
//     FindByOwner. If found, the new agent inherits. created=false.
//  3. Else run FindByOwner for newAgentName itself (self-adoption). C-7
//     backstop for restart-after-crash when GC may not have cleaned up
//     the prior meta.
//  4. Else run GC (best-effort), then Create a new worktree owned by
//     newAgentName. created=true.
//
// creatorAgent "" is a valid input meaning "no identifiable creator" — falls
// through to branch 3. At @cli (workspace shell with no AF_ROLE), this is the
// expected case.
//
// Returned path is absolute; id is "wt-<6hex>". Caller is responsible for
// calling SetupAgent(root, path, newAgentName, created) and, if launching a
// tmux session, session.Manager.SetWorktree(path, id).
func ResolveOrCreate(factoryRoot, newAgentName, creatorAgent, creatorEnvWT, creatorEnvWTID string, opts CreateOpts) (string, string, bool, error) {
	if creatorEnvWT != "" {
		return creatorEnvWT, creatorEnvWTID, false, nil
	}
	if creatorAgent != "" {
		meta, err := FindByOwner(factoryRoot, creatorAgent)
		if err != nil {
			return "", "", false, fmt.Errorf("FindByOwner(%q): %w", creatorAgent, err)
		}
		if meta != nil {
			return filepath.Join(factoryRoot, meta.Path), meta.ID, false, nil
		}
	}
	if selfMeta, err := FindByOwner(factoryRoot, newAgentName); err != nil {
		return "", "", false, fmt.Errorf("FindByOwner(self=%q): %w", newAgentName, err)
	} else if selfMeta != nil {
		return filepath.Join(factoryRoot, selfMeta.Path), selfMeta.ID, false, nil
	}
	_, _ = GC(factoryRoot)
	wtPath, meta, err := Create(factoryRoot, newAgentName, opts)
	if err != nil {
		return "", "", false, fmt.Errorf("Create(%q): %w", newAgentName, err)
	}
	return wtPath, meta.ID, true, nil
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
