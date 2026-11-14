package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// auditCurlStub stands in for curl under the advisory audit. It routes on method and URL, serves
// canned answers from $CURL_STUB_DIR, logs every call, and refuses any option or URL it does not
// model, so a script that drifts from the modelled call shape fails loudly instead of reading an
// option's value as the URL.
const auditCurlStub = `D="$CURL_STUB_DIR"
method=""; url=""; out=""; hdr=""; wfmt=""; fail=0; include=0; data=""; hasdata=0; headers=""
take() {
  case "$1" in
    X|--request) method="$2" ;;
    o|--output) out="$2" ;;
    D|--dump-header) hdr="$2" ;;
    w|--write-out) wfmt="$2" ;;
    H|--header) headers="$headers$2"$'\n' ;;
    d|--data|--data-binary|--data-raw) hasdata=1
       case "$2" in @-) data="$(cat)" ;; @*) data="$(cat "${2#@}")" ;; *) data="$2" ;; esac ;;
    m|--max-time|--connect-timeout|--retry|--retry-delay|--retry-max-time|A|--user-agent) : ;;
    *) echo "curl stub: unhandled option $1" >&2; echo "UNHANDLED $1" >> "$D/calls.log"; exit 2 ;;
  esac
}
while [ $# -gt 0 ]; do
  a="$1"; shift
  case "$a" in
    --fail) fail=1 ;;
    --include) include=1 ;;
    --head) method=HEAD ;;
    --silent|--show-error|--location|-L|--globoff|-g) : ;;
    --*=*) take "${a%%=*}" "${a#*=}" ;;
    --request|--output|--dump-header|--write-out|--header|--data|--data-binary|--data-raw|--max-time|--connect-timeout|--retry|--retry-delay|--retry-max-time|--user-agent)
       take "$a" "$1"; shift ;;
    --*) echo "curl stub: unhandled option $a" >&2; echo "UNHANDLED $a" >> "$D/calls.log"; exit 2 ;;
    -?*) cl="${a#-}"
       while [ -n "$cl" ]; do
         c="${cl:0:1}"; cl="${cl:1}"
         case "$c" in
           f) fail=1 ;; i) include=1 ;; I) method=HEAD ;; s|S|L|g) : ;;
           X|o|D|w|H|d|m|A)
              if [ -n "$cl" ]; then take "$c" "$cl"; else take "$c" "$1"; shift; fi; cl="" ;;
           *) echo "curl stub: unhandled flag -$c" >&2; echo "UNHANDLED -$c" >> "$D/calls.log"; exit 2 ;;
         esac
       done ;;
    *) url="$a" ;;
  esac
done
[ -n "$method" ] || { [ "$hasdata" = 1 ] && method=POST || method=GET; }
auth=$(printf '%s' "$headers" | grep -i '^authorization:' | tr -d '\r')
printf '%s\t%s\t%s\n' "$method" "$url" "${auth:-noauth}" >> "$D/calls.log"
fault() { grep -qx "$1" "$D/faults" 2>/dev/null; }
code=200; link=""; body=""
case "$method $url" in
  "POST https://api.osv.dev/v1/querybatch")
    printf '%s' "$data" | jq -c '.queries[] | [.package.ecosystem, .package.name, .version]' >> "$D/queries.log"
    printf '%s' "$data" | jq '.queries | length' >> "$D/batches.log"
    # A fault that also broke the control query would be caught as "oracle silent", hiding whether
    # the lock batches' own error paths work.
    case "$data" in *'"version":"24.0"'*) fault() { false; } ;; esac
    if fault querybatch=transport; then echo "curl: (7) Failed to connect to api.osv.dev port 443: Couldn't connect to server" >&2; [ -n "$wfmt" ] && printf '000'; exit 7; fi
    if fault querybatch=400; then code=400; body='{"code":3,"message":"error in query at index 0: invalid ecosystem"}'
    else
      body=$(printf '%s' "$data" | jq -c --rawfile db "$D/osv.tsv" '
        def norm: ascii_downcase | gsub("[-_.]+"; "-");
        ($db | split("\n") | map(select(length > 0) | split("\t"))
             | map({key: (.[0] + "|" + (.[1] | norm) + "|" + .[2]), value: (.[3] | split(" "))}) | from_entries) as $m
        | {results: [.queries[] | ($m[.package.ecosystem + "|" + (.package.name | norm) + "|" + .version] // []) as $ids
             | if ($ids | length) == 0 then {} else {vulns: [$ids[] | {id: ., modified: "2026-10-01T00:00:00Z"}]} end]}')
      fault querybatch=short && body=$(printf '%s' "$body" | jq -c '.results |= .[1:]')
      fault querybatch=paged && body=$(printf '%s' "$body" | jq -c '.results[0].next_page_token = "tok"')
      fault querybatch=nonjson && body='<html><body>502 Bad Gateway</body></html>'
    fi ;;
  "GET https://api.osv.dev/v1/vulns/"*)
    id="${url##*/}"
    if fault vulns=transport; then echo "curl: (28) Operation timed out" >&2; [ -n "$wfmt" ] && printf '000'; exit 28; fi
    if [ -f "$D/vulns/$id.json" ]; then body=$(cat "$D/vulns/$id.json"); else code=404; body='{"code":5,"message":"Vulnerability not found"}'; fi ;;
  "GET https://api.github.com/repos/"*"/security-advisories"*|"GET https://api.github.com/repositories/"*"/security-advisories"*)
    case "$url" in
      https://api.github.com/repos/*) k=$(printf '%s' "$url" | sed -n 's|^https://api.github.com/repos/\([^/]*/[^/?]*\)/security-advisories.*|\1|p') ;;
      *) k="$url" ;;
    esac
    page=$(awk -F'\t' -v k="$k" '$1==k{print $2; exit}' "$D/gh/routes.tsv" 2>/dev/null)
    if [ -z "$page" ]; then code=404; body='{"message":"Not Found"}'
    else
      body=$(cat "$D/gh/$page.json"); [ -f "$D/gh/$page.link" ] && link=$(cat "$D/gh/$page.link")
      fault "gh=$page:403" && { code=403; body='{"message":"API rate limit exceeded"}'; link=""; }
    fi ;;
  "HEAD https://github.com/cli/cli/releases/download/"*|"HEAD https://go.dev/dl/"*|"HEAD https://registry.npmjs.org/"*|"HEAD https://downloads.claude.ai/claude-code-releases/"*|"HEAD https://downloads.openobserve.ai/releases/openobserve/"*)
    grep -qxF "$url" "$D/missing" 2>/dev/null && code=404 ;;
  "GET https://nodejs.org/dist/index.json") body=$(cat "$D/node/index.json") ;;
  "GET https://raw.githubusercontent.com/nodejs/Release/main/schedule.json") body=$(cat "$D/node/schedule.json") ;;
  "GET https://deb.nodesource.com/node_"*".x/dists/nodistro/main/binary-"*"/Packages")
    if grep -qxF "$url" "$D/missing" 2>/dev/null; then code=404; else body=$(cat "$D/node/Packages"); fi ;;
  *) echo "UNROUTED $method $url" >> "$D/calls.log"; echo "curl: (6) Could not resolve host" >&2; [ -n "$wfmt" ] && printf '000'; exit 6 ;;
esac
hblock="HTTP/2 $code"$'\r\n'"content-type: application/json; charset=utf-8"$'\r\n'
[ -n "$link" ] && hblock="$hblock$link"$'\r\n'
hblock="$hblock"$'\r\n'
if [ -n "$hdr" ]; then if [ "$hdr" = - ]; then printf '%s' "$hblock"; else printf '%s' "$hblock" > "$hdr"; fi; fi
if [ "$fail" = 1 ] && [ "$code" -ge 400 ]; then
  echo "curl: (22) The requested URL returned error: $code" >&2
  [ -n "$wfmt" ] && printf '%s' "${wfmt//%\{http_code\}/$code}"
  exit 22
fi
payload="$body"; { [ "$include" = 1 ] || [ "$method" = HEAD ]; } && payload="$hblock$body"
if [ -n "$out" ]; then printf '%s' "$payload" > "$out"; else printf '%s' "$payload"; fi
if [ -n "$wfmt" ]; then
  w="${wfmt//%\{http_code\}/$code}"
  case "$w" in *%\{*) echo "curl stub: unsupported -w $wfmt" >&2; exit 2 ;; esac
  printf '%b' "$w"
fi
exit 0
`

// The real gh and wget sit in /usr/bin, which refEnv keeps on PATH; these stand-ins make any
// network path that bypasses curl visible instead of letting make test reach the internet.
func auditTripwire(name string) string {
	return fmt.Sprintf("echo \"TRIPWIRE %s $*\" >> \"$CURL_STUB_DIR/calls.log\"; exit 97\n", name)
}

const (
	auditScriptRel     = ".github/advisory-audit.sh"
	auditControlLine   = "PyPI\tpip\t24.0\tGHSA-control-pip-24"
	auditBerriRepoPage = "https://api.github.com/repositories/671269505/security-advisories?per_page=100&after=Y3Vyc29yOnYyOpHOAAe-Gg%3D%3D"
	auditOORepoPage    = "https://api.github.com/repositories/235232283/security-advisories?per_page=100&after=Y3Vyc29yOnYyOpHOAAgTAA%3D%3D"
)

type ghVuln struct {
	rng     string
	patched *string
}

func patched(s string) *string { return &s }

func ghAdvisory(ghsa, cve string, vulns ...ghVuln) map[string]any {
	ids := []map[string]string{{"type": "GHSA", "value": ghsa}}
	var cveID any
	if cve != "" {
		ids = append(ids, map[string]string{"type": "CVE", "value": cve})
		cveID = cve
	}
	var vs []map[string]any
	for _, v := range vulns {
		var p any
		if v.patched != nil {
			p = *v.patched
		}
		vs = append(vs, map[string]any{
			"package":                  map[string]string{"ecosystem": "pip", "name": "pkg"},
			"vulnerable_version_range": v.rng,
			"patched_versions":         p,
		})
	}
	return map[string]any{"ghsa_id": ghsa, "cve_id": cveID, "identifiers": ids, "vulnerabilities": vs, "withdrawn_at": nil}
}

func neverMatching(ghsa string) map[string]any {
	return ghAdvisory(ghsa, "", ghVuln{"< 0.0.1", patched("0.0.1")})
}

// auditDB is one stubbed internet: OSV answers keyed by (ecosystem, name, version), OSV vuln
// records, and GitHub advisory pages per repo.
type auditDB struct {
	t   *testing.T
	dir string
}

func newAuditDB(t *testing.T) auditDB {
	t.Helper()
	db := auditDB{t: t, dir: t.TempDir()}
	for _, sub := range []string{"gh", "vulns", "node"} {
		if err := os.MkdirAll(filepath.Join(db.dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db.osv(auditControlLine)
	db.ghPages("BerriAI/litellm", []map[string]any{neverMatching("GHSA-base-berr-0001")}, []map[string]any{neverMatching("GHSA-base-berr-0002")})
	db.ghPages("openobserve/openobserve", []map[string]any{neverMatching("GHSA-base-oo00-0001")})
	db.ghPages("cli/cli", []map[string]any{neverMatching("GHSA-base-cli0-0001")})
	db.nodeFeeds(auditNodeMajor, auditNodeApt)
	return db
}

type auditNodeRelease struct {
	version  string
	security bool
}

// nodeFeeds serves nodejs.org and NodeSource answers under which the pinned nodejs passes: its own
// release is the newest of its major, its major ends far in the future, and both architectures'
// Packages indexes list it.
func (db auditDB) nodeFeeds(major, aptVersion string) {
	db.t.Helper()
	upstream, _, _ := strings.Cut(aptVersion, "-")
	db.nodeIndex(auditNodeRelease{"v" + upstream, true})
	db.nodeSchedule(major, "2099-04-30")
	db.nodePackages(aptVersion)
}

func (db auditDB) nodeIndex(releases ...auditNodeRelease) {
	db.t.Helper()
	var entries []map[string]any
	for _, r := range releases {
		entries = append(entries, map[string]any{"version": r.version, "date": "2026-01-01", "lts": "Synthetic", "security": r.security})
	}
	b, _ := json.Marshal(entries)
	db.write("node/index.json", string(b))
}

func (db auditDB) nodeSchedule(major, end string) {
	db.t.Helper()
	b, _ := json.Marshal(map[string]any{"v" + major: map[string]string{"start": "2025-04-22", "lts": "2025-10-28", "maintenance": "2026-10-20", "end": end, "codename": "Synthetic"}})
	db.write("node/schedule.json", string(b))
}

func (db auditDB) nodePackages(versions ...string) {
	db.t.Helper()
	var b strings.Builder
	for _, v := range versions {
		fmt.Fprintf(&b, "Package: nodejs\nVersion: %s\nArchitecture: amd64\nFilename: pool/main/n/nodejs/nodejs_%s_amd64.deb\n\n", v, v)
	}
	db.write("node/Packages", b.String())
}

// missing makes the asset hosts answer 404 for exactly these URLs.
func (db auditDB) missing(urls ...string) {
	db.write("missing", strings.Join(urls, "\n")+"\n")
}

func (db auditDB) write(rel, content string) {
	db.t.Helper()
	if err := os.WriteFile(filepath.Join(db.dir, rel), []byte(content), 0o644); err != nil {
		db.t.Fatal(err)
	}
}

func (db auditDB) osv(lines ...string) {
	db.write("osv.tsv", strings.Join(lines, "\n")+"\n")
}

func (db auditDB) vuln(id string, aliases ...string) {
	b, _ := json.Marshal(map[string]any{"id": id, "aliases": aliases})
	db.write("vulns/"+id+".json", string(b))
}

func osvRange(typ, fixed string) map[string]any {
	r := map[string]any{"type": typ, "events": []map[string]string{{"introduced": "0"}, {"fixed": fixed}}}
	if typ == "GIT" {
		r["repo"] = "https://github.com/x/y"
	}
	return r
}

// vulnAffected serves an OSV record whose affected[] names one package with these ranges; db.vuln
// and vulnerableDB stay fix-free so the fixtures that predate the fix rule keep their meaning.
func (db auditDB) vulnAffected(id, eco, name string, aliases []string, ranges ...map[string]any) {
	if aliases == nil {
		aliases = []string{}
	}
	b, _ := json.Marshal(map[string]any{"id": id, "aliases": aliases, "affected": []map[string]any{
		{"package": map[string]string{"ecosystem": eco, "name": name}, "ranges": ranges}}})
	db.write("vulns/"+id+".json", string(b))
}

func (db auditDB) vulnFixed(id, eco, name, fixed string, aliases ...string) {
	db.vulnAffected(id, eco, name, aliases, osvRange("ECOSYSTEM", fixed))
}

func (db auditDB) vulnGitFixed(id, eco, name, sha string, aliases ...string) {
	db.vulnAffected(id, eco, name, aliases, osvRange("GIT", sha))
}

func (db auditDB) faults(f ...string) {
	db.write("faults", strings.Join(f, "\n")+"\n")
}

// ghPages serves a repo's advisory list as consecutive pages. Page 1 is keyed on the repo;
// page 2 only on the exact cursor URL page 1's link header carries, as GitHub does.
func (db auditDB) ghPages(repo string, pages ...[]map[string]any) {
	db.t.Helper()
	next := map[string]string{"BerriAI/litellm": auditBerriRepoPage, "openobserve/openobserve": auditOORepoPage}
	var routes []string
	if old, err := os.ReadFile(filepath.Join(db.dir, "gh", "routes.tsv")); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(string(old)), "\n") {
			if l != "" && !strings.HasPrefix(l, repo+"\t") && !strings.HasPrefix(l, next[repo]+"\t") {
				routes = append(routes, l)
			}
		}
	}
	slug := strings.NewReplacer("/", "_").Replace(repo)
	for i, page := range pages {
		name := fmt.Sprintf("%s-%d", slug, i+1)
		b, _ := json.Marshal(page)
		if page == nil {
			b = []byte("[]")
		}
		db.write("gh/"+name+".json", string(b))
		_ = os.Remove(filepath.Join(db.dir, "gh", name+".link"))
		key := repo
		if i == 1 {
			key = next[repo]
		}
		routes = append(routes, key+"\t"+name)
		if i == 0 && len(pages) > 1 {
			// GitHub sends the header lowercase over HTTP/2; the openobserve fixture uses the
			// capitalised form so both spellings are exercised.
			header := "link"
			if repo == "openobserve/openobserve" {
				header = "Link"
			}
			db.write("gh/"+name+".link", fmt.Sprintf(`%s: <%s>; rel="next", <%s>; rel="last"`, header, next[repo], next[repo]))
		}
	}
	db.write("gh/routes.tsv", strings.Join(routes, "\n")+"\n")
}

func (db auditDB) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(db.dir, rel))
	return string(b)
}

func (db auditDB) assertNoStrayCalls() {
	db.t.Helper()
	for _, l := range strings.Split(db.read("calls.log"), "\n") {
		if strings.HasPrefix(l, "TRIPWIRE") || strings.HasPrefix(l, "UNROUTED") || strings.HasPrefix(l, "UNHANDLED") {
			db.t.Errorf("the audit made a call outside the modelled curl surface: %s", l)
		}
	}
}

type auditPin struct{ name, version string }

func writeAuditLock(t *testing.T, path string, pins []auditPin) {
	t.Helper()
	var b strings.Builder
	b.WriteString("#\n# This file is autogenerated by pip-compile with Python 3.12\n# by the following command:\n#\n#    make lock\n#\n--only-binary :all:\n\n")
	for i, p := range pins {
		if p.name == "pip" {
			b.WriteString("\n# The following packages are considered to be unsafe in a requirements file:\n")
		}
		fmt.Fprintf(&b, "%s==%s \\\n    --hash=sha256:%064x\n    # via -r requirements.in\n", p.name, p.version, i+1)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// auditException binds an entry to the package and version it was reviewed for, the pin the
// fixture finding is reported against; an entry excuses nothing at any other pin.
func auditException(id string, aliases []string, reviewedFor auditPin, reviewed, recheck string) advisoryException {
	if aliases == nil {
		aliases = []string{}
	}
	return advisoryException{ID: id, Aliases: &aliases, Package: reviewedFor.name, Version: reviewedFor.version, Reason: "fixture", Evidence: "fixture", Reviewed: reviewed, RecheckBy: recheck}
}

// adNoPin is a package no fixture audits, for entries whose only job is to exist or to expire.
var adNoPin = auditPin{"pkg", "1"}

func baseRegister() []advisoryException {
	return []advisoryException{auditException("AF-TEST-BASE", nil, adNoPin, "2026-01-01", "2026-01-31")}
}

var (
	baseIssueLock   = []auditPin{{"aiohttp", "3.14.4"}, {"pip", "26.2.2"}}
	baseGatewayLock = []auditPin{{"aiohttp", "3.14.4"}, {"litellm[proxy]", "1.93.2"}, {"pip", "26.2.2"}}
)

const (
	auditNodeMajor  = "96"
	auditNodeApt    = "96.50.1-1nodesource1"
	auditNpmLockRel = "internal/cmd/integrations/playwright/af/package-lock.json"
)

// auditSyntheticVersions holds one synthetic value per *_VERSION constant the audit maps to a feed.
// None equals a shipped pin, so a fixture can only pass by reading the fixture quickstart.sh.
var auditSyntheticVersions = map[string]string{
	"GO_TOOLCHAIN_VERSION": "go1.99.1",
	"GH_VERSION":           "2.98.7",
	"OPENOBSERVE_VERSION":  "v1.98.3",
	"PLAYWRIGHT_VERSION":   "1.98.4",
	"CODEX_VERSION":        "0.998.0",
	"CLAUDE_CODE_VERSION":  "2.9.998",
	"NODE_APT_VERSION":     auditNodeApt,
	"LITELLM_VERSION":      "1.99.7",
}

var auditVersionDeclRE = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9_]*_VERSION)=`)

// auditQuickstart writes a quickstart.sh declaring every *_VERSION constant the shipped one declares,
// plus the ones the audit is specified to map, so a constant added to quickstart.sh without a
// synthetic value (and a feed) stops this harness instead of passing unaudited.
func auditQuickstart(t *testing.T, overrides map[string]string) string {
	t.Helper()
	values := map[string]string{"NODE_MAJOR": auditNodeMajor}
	for name, v := range auditSyntheticVersions {
		values[name] = v
	}
	for _, m := range auditVersionDeclRE.FindAllStringSubmatch(readQuickstart(t), -1) {
		name := m[1]
		if _, ok := values[name]; ok {
			continue
		}
		if !strings.HasSuffix(name, "_MIN_VERSION") {
			t.Fatalf("quickstart.sh declares %s, which the advisory fixture has no synthetic value for; add one to auditSyntheticVersions and a feed for it in %s", name, auditScriptRel)
		}
		values[name] = "0.1"
	}
	for name, v := range overrides {
		values[name] = v
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\nset -u\n\n# Configuration\n")
	for _, name := range names {
		fmt.Fprintf(&b, "%s=\"%s\"\n", name, values[name])
	}
	return b.String()
}

// auditNpmLock is a synthetic integration lock with the reference's shape: the CLI and its core at
// the top level, the MCP server with its own nested pre-release pair.
func auditNpmLock() string {
	entry := func(name, version string, deps map[string]string) map[string]any {
		base := name
		if i := strings.LastIndex(name, "/"); i != -1 {
			base = name[i+1:]
		}
		e := map[string]any{
			"version":   version,
			"resolved":  "https://registry.npmjs.org/" + name + "/-/" + base + "-" + version + ".tgz",
			"integrity": "sha512-c3ludGhldGlj",
		}
		if deps != nil {
			e["dependencies"] = deps
		}
		return e
	}
	b, _ := json.MarshalIndent(map[string]any{
		"name":            "af-playwright",
		"lockfileVersion": 3,
		"requires":        true,
		"packages": map[string]any{
			"":                             map[string]any{"dependencies": map[string]string{"playwright": "1.97.0", "@playwright/mcp": "0.0.997"}},
			"node_modules/playwright":      entry("playwright", "1.97.0", map[string]string{"playwright-core": "1.97.0"}),
			"node_modules/playwright-core": entry("playwright-core", "1.97.0", nil),
			"node_modules/@playwright/mcp": entry("@playwright/mcp", "0.0.997", map[string]string{"playwright": "1.97.0-alpha-2099-01-01", "playwright-core": "1.97.0-alpha-2099-01-01"}),
			"node_modules/@playwright/mcp/node_modules/playwright":      entry("playwright", "1.97.0-alpha-2099-01-01", map[string]string{"playwright-core": "1.97.0-alpha-2099-01-01"}),
			"node_modules/@playwright/mcp/node_modules/playwright-core": entry("playwright-core", "1.97.0-alpha-2099-01-01", nil),
		},
	}, "", "  ")
	return string(b) + "\n"
}

// auditTree lays a copy of the audit script beside fixture locks, a fixture quickstart.sh, a fixture
// integration lock and a fixture register; the script finds its inputs relative to its own path, so
// it reads the fixtures unchanged.
func auditTree(t *testing.T, script []byte, issue, gateway []auditPin, register []advisoryException) string {
	t.Helper()
	return auditTreeWith(t, script, issue, gateway, register, auditQuickstart(t, nil), auditNpmLock())
}

func auditTreeWith(t *testing.T, script []byte, issue, gateway []auditPin, register []advisoryException, quickstart, npmLock string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, auditScriptRel), script, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAuditLock(t, filepath.Join(root, "py", "requirements.txt"), issue)
	writeAuditLock(t, filepath.Join(root, "py", "litellm-requirements.txt"), gateway)
	if err := os.WriteFile(filepath.Join(root, "quickstart.sh"), []byte(quickstart), 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, filepath.FromSlash(auditNpmLockRel))
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(npmLock), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := json.MarshalIndent(register, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, advisoryRegisterPath), reg, 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, auditScriptRel)
}

type auditRunner struct {
	t    *testing.T
	bash string
	fake string
}

func (r auditRunner) run(script string, db auditDB, today string, extra ...string) refRunResult {
	r.t.Helper()
	home := r.t.TempDir()
	env := append(refEnv(home, r.fake, "CURL_STUB_DIR="+db.dir, "ADVISORY_AUDIT_TODAY="+today), extra...)
	return refRun(r.t, r.t.TempDir(), env, "", r.bash, script)
}

func wantAuditExit(t *testing.T, res refRunResult, wantZero bool, mustName ...string) {
	t.Helper()
	out := res.stdout + res.stderr
	if wantZero && res.exit != 0 {
		t.Fatalf("want exit 0, got %d:\n%s", res.exit, out)
	}
	if !wantZero && res.exit == 0 {
		t.Fatalf("want non-zero exit, got 0:\n%s", out)
	}
	for _, s := range mustName {
		if !strings.Contains(out, s) {
			t.Errorf("output does not name %q:\n%s", s, out)
		}
	}
}

// auditRecord is schema v1 of the finding record the audit writes to ADVISORY_AUDIT_RECORD. It is
// decoded strictly: a field the producer misspells or adds fails here before a consumer misreads it.
type auditRecord struct {
	Schema    int            `json:"schema"`
	Today     string         `json:"today"`
	ExitClass string         `json:"exit_class"`
	Counts    auditCounts    `json:"counts"`
	Findings  []auditFinding `json:"findings"`
	Errors    []string       `json:"errors"`
}

type auditCounts struct {
	Reported       int `json:"reported"`
	Unexcused      int `json:"unexcused"`
	Expired        int `json:"expired"`
	NodeBehind     int `json:"node_behind"`
	ExcusedFixable int `json:"excused_fixable"`
	Errors         int `json:"errors"`
}

type auditFinding struct {
	Class          string             `json:"class"`
	Key            string             `json:"key"`
	Pinned         bool               `json:"pinned"`
	FixPublished   *bool              `json:"fix_published"`
	PinnedIn       []auditPinLocation `json:"pinned_in"`
	Advisories     []auditRecordAdv   `json:"advisories"`
	Register       []auditRegisterRef `json:"register"`
	Entries        []auditRecordEntry `json:"entries"`
	ExcusedFixable []auditRecordEntry `json:"excused_fixable"`
	Releases       []string           `json:"releases"`
}

type auditPinLocation struct {
	Ecosystem string `json:"ecosystem"`
	Repo      string `json:"repo"`
}

type auditRecordAdv struct {
	ID        string   `json:"id"`
	Aliases   []string `json:"aliases"`
	Source    string   `json:"source"`
	Ecosystem string   `json:"ecosystem"`
	Repo      string   `json:"repo"`
	Against   string   `json:"against"`
	Fixed     []string `json:"fixed"`
	FixedRaw  string   `json:"fixed_raw"`
}

type auditRegisterRef struct {
	ID        string   `json:"id"`
	Aliases   []string `json:"aliases"`
	Reviewed  string   `json:"reviewed"`
	RecheckBy string   `json:"recheck_by"`
	Key       string   `json:"key"`
}

// auditRecordEntry is an expired entry (id … fix_available) or an excused-fixable one (id, advisory, fixed).
type auditRecordEntry struct {
	ID           string   `json:"id"`
	Aliases      []string `json:"aliases"`
	Reviewed     string   `json:"reviewed"`
	RecheckBy    string   `json:"recheck_by"`
	Excuses      []string `json:"excuses"`
	FixAvailable string   `json:"fix_available"`
	Advisory     string   `json:"advisory"`
	Fixed        []string `json:"fixed"`
}

func readAuditRecord(t *testing.T, path string) auditRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the audit wrote no finding record at %s: %v", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("the finding record is not a JSON object: %v\n%s", err, raw)
	}
	for _, list := range []string{"findings", "errors"} {
		if v := strings.TrimSpace(string(top[list])); !strings.HasPrefix(v, "[") {
			t.Errorf("the finding record's %s is %s, want an array (empty is [])", list, v)
		}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var rec auditRecord
	if err := dec.Decode(&rec); err != nil {
		t.Fatalf("the finding record does not decode as schema v1: %v\n%s", err, raw)
	}
	if rec.Schema != 1 {
		t.Errorf("record schema = %d, want 1", rec.Schema)
	}
	return rec
}

// recorded runs the audit with ADVISORY_AUDIT_RECORD set, asserts the exit code exactly, and decodes the record.
func (r auditRunner) recorded(t *testing.T, script string, db auditDB, today string, wantExit int, extra ...string) (refRunResult, auditRecord) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "record.json")
	res := r.run(script, db, today, append([]string{"ADVISORY_AUDIT_RECORD=" + path}, extra...)...)
	if res.exit != wantExit {
		t.Errorf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", res.exit, wantExit, res.stdout, res.stderr)
	}
	rec := readAuditRecord(t, path)
	if rec.Today != today {
		t.Errorf("record today = %q, want %q", rec.Today, today)
	}
	return res, rec
}

func (rec auditRecord) find(t *testing.T, class, key string) auditFinding {
	t.Helper()
	for _, f := range rec.Findings {
		if f.Class == class && f.Key == key {
			return f
		}
	}
	t.Fatalf("the record has no %s finding for %s; findings: %+v", class, key, rec.Findings)
	return auditFinding{}
}

func (rec auditRecord) classKeys() []string {
	var out []string
	for _, f := range rec.Findings {
		out = append(out, f.Class+" "+f.Key)
	}
	return out
}

func acknowledged(e advisoryException, fix string) advisoryException {
	e.FixAvailable = &fix
	return e
}

func TestAdvisoryDecay_FailsOnFixtureAdvisory(t *testing.T) {
	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH; the advisory audit is a bash script")
	}
	// macOS ships bash 3.2 at /bin/bash, the interpreter the audit must stay compatible with.
	if _, err := os.Stat("/bin/bash"); err == nil {
		bashPath = "/bin/bash"
	}
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not on PATH; the advisory audit parses every API answer with jq")
	}

	root := findModuleRoot(t)
	shipped := filepath.Join(root, auditScriptRel)
	script, err := os.ReadFile(shipped)
	if err != nil {
		t.Fatalf("reading %s: %v", auditScriptRel, err)
	}

	fake := refFakeBin(t, map[string]string{
		"curl":    auditCurlStub,
		"gh":      auditTripwire("gh"),
		"wget":    auditTripwire("wget"),
		"nc":      auditTripwire("nc"),
		"python3": auditTripwire("python3"),
	})
	// refEnv's PATH only reaches /usr/bin and /bin; a Homebrew jq lives elsewhere.
	if err := os.Symlink(jqPath, filepath.Join(fake, "jq")); err != nil {
		t.Fatal(err)
	}
	r := auditRunner{t: t, bash: bashPath, fake: fake}

	t.Run("static", func(t *testing.T) {
		src := string(script)
		info, err := os.Stat(shipped)
		if err != nil || info.Mode()&0o111 == 0 {
			t.Errorf("%s must be executable", auditScriptRel)
		}
		for _, want := range []string{`today="${ADVISORY_AUDIT_TODAY:-$(date -u +%F)}"`, `cd "$(dirname "$0")/.."`} {
			if !strings.Contains(src, want) {
				t.Errorf("%s lacks %s", auditScriptRel, want)
			}
		}
		for desc, re := range map[string]string{
			"an uncommented self-hosted (the OSS publish guard scans .github)": `(?m)^[^#\n]*self-hosted`,
			"a pip install line (the CI pip lint demands require-hashes)":      `pip.*install`,
			"a gh or wget call (every request must go through curl)":           `(?m)(^|[;&|( ])(gh|wget)[ \t]`,
			"an absolute curl path (bypasses the PATH stub)":                   `/usr/bin/curl`,
			"a bash >= 4 construct":                                            `declare -A|mapfile|readarray|\$\{[A-Za-z_]+,,\}|\[\[ -v|EPOCHSECONDS|(^|[;&| ])timeout `,
		} {
			if regexp.MustCompile(re).MatchString(src) {
				t.Errorf("%s contains %s", auditScriptRel, desc)
			}
		}
		// curl -X HEAD waits for a body the server never sends: exit 18, or a hang until --max-time.
		if m := regexp.MustCompile(`(-X|--request)[ =]?HEAD`).FindString(src); m != "" {
			t.Errorf("%s sends HEAD as %q; use curl -I", auditScriptRel, m)
		}
	})

	t.Run("shipped locks and register pass at the earliest recheck_by", func(t *testing.T) {
		raw := []byte(readRepoFile(t, advisoryRegisterPath))
		register, err := decodeAdvisoryExceptions(raw)
		if err != nil {
			t.Fatalf("decoding %s: %v", advisoryRegisterPath, err)
		}
		earliest := ""
		for _, e := range register {
			if earliest == "" || e.RecheckBy < earliest {
				earliest = e.RecheckBy
			}
		}
		db := newAuditDB(t)
		if major, apt := auditShippedNode(t); major != "" && apt != "" {
			db.nodeFeeds(major, apt)
		}
		// OSV's record for oauthlib 4.0.0 (#764): a lock that falls back to it fails offline.
		// db.osv replaces the whole table: add later heals' rows to this call.
		db.osv(auditControlLine, "PyPI\toauthlib\t4.0.0\tGHSA-3jp2-3mrg-2vxc")
		db.vulnFixed("GHSA-3jp2-3mrg-2vxc", "PyPI", "oauthlib", "4.0.1", "CVE-2026-49272")
		db.ghPages("BerriAI/litellm",
			[]map[string]any{
				ghAdvisory("GHSA-3cv6-jpf6-8222", "", ghVuln{"<1.94.0", patched("1.96.2, 1.95.1, 1.94.3, 1.93.2, 1.92.2")}),
				ghAdvisory("GHSA-g5ff-637f-6q2m", "", ghVuln{"<1.95.0", patched("1.95.0")}),
				ghAdvisory("GHSA-w4c7-34wj-hqm8", "", ghVuln{"<1.103.10", patched("1.103.10, 1.104.8, 1.105.6, 1.106.5")}),
				ghAdvisory("GHSA-2rwg-2wh4-7c4f", "", ghVuln{"< 1.106.9", patched("1.106.9")}, ghVuln{"< 1.108.6", patched("1.108.6")},
					ghVuln{"< 1.109.1", patched("1.109.1")}, ghVuln{"< 1.110.0-rc.2", patched("1.110.0-rc.2")}),
			},
			[]map[string]any{ghAdvisory("GHSA-7hp6-4w63-5g45", "CVE-2026-114612",
				ghVuln{">= 1.91.0, < 1.100.4", patched("1.100.4")}, ghVuln{">= 1.103.0, <1.103.1", patched("1.103.1")})})
		db.ghPages("openobserve/openobserve",
			[]map[string]any{
				ghAdvisory("GHSA-rw8w-37p9-mrrp", "CVE-2024-41809", ghVuln{">= v0.5.2", patched("v0.5.2")}),
				ghAdvisory("GHSA-3jpx-57gj-w458", "CVE-2025-64744", ghVuln{"All (before fix)", patched("TBD (maintainers will fill once fixed)")}),
			},
			[]map[string]any{neverMatching("GHSA-base-oo00-0002")})

		before := listDir(t, filepath.Join(root, "py")) + listDir(t, filepath.Join(root, ".github"))
		// Not r.recorded: a missing record must not stop the subtests below, which pin the shipped register.
		recPath := filepath.Join(t.TempDir(), "record.json")
		if res := r.run(shipped, db, earliest, "ADVISORY_AUDIT_RECORD="+recPath); res.exit != 0 {
			t.Errorf("exit = %d, want 0\n%s%s", res.exit, res.stdout, res.stderr)
		}
		if _, err := os.Stat(recPath); err != nil {
			t.Errorf("the audit wrote no finding record at %s: %v", recPath, err)
		} else if rec := readAuditRecord(t, recPath); rec.ExitClass != "clean" || len(rec.Findings) != 0 || rec.Counts.Errors != 0 {
			t.Errorf("the shipped tree's record = class %q, findings %v, %d errors; want clean, none, 0", rec.ExitClass, rec.classKeys(), rec.Counts.Errors)
		}
		if after := listDir(t, filepath.Join(root, "py")) + listDir(t, filepath.Join(root, ".github")); after != before {
			t.Errorf("the audit wrote into the repository:\nbefore %s\nafter  %s", before, after)
		}
		db.assertNoStrayCalls()

		queried := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(db.read("queries.log")), "\n") {
			var q []string
			if json.Unmarshal([]byte(l), &q) == nil && len(q) == 3 {
				queried[q[0]+"|"+normalizePyName(q[1])+"|"+q[2]] = true
			}
		}
		for rel, pins := range readInstallLocks(t) {
			for key, p := range pins {
				if !queried["PyPI|"+key+"|"+p.version] {
					t.Errorf("%s pin %s==%s was never sent to OSV", rel, key, p.version)
				}
			}
		}
		calls := db.read("calls.log")
		for _, page := range []string{auditBerriRepoPage, auditOORepoPage} {
			if !strings.Contains(calls, "GET\t"+page+"\t") {
				t.Errorf("page 2 %s was never fetched; pagination is not followed", page)
			}
		}

		t.Run("one day later the register has decayed", func(t *testing.T) {
			d, _ := time.Parse("2006-01-02", earliest)
			var due []string
			for _, e := range register {
				if e.RecheckBy == earliest {
					due = append(due, e.ID)
				}
			}
			res, rec := r.recorded(t, shipped, db, d.AddDate(0, 0, 1).Format("2006-01-02"), 1)
			wantAuditExit(t, res, false, due...)
			// Grouped by the entry's package@version: the 2026-11-27 wave is litellm@1.93.2 (AF-743, 7hp6,
			// g5ff) and openobserve@v1.1.2 (rw8w, 3jpx), read here from the register, not restated.
			want := map[string][]string{}
			fix := map[string]string{}
			for _, e := range register {
				if e.RecheckBy == earliest {
					k := e.Package + "@" + e.Version
					want[k] = append(want[k], e.ID)
					if e.FixAvailable != nil {
						fix[e.ID] = *e.FixAvailable
					}
				}
			}
			got := map[string][]string{}
			for _, f := range rec.Findings {
				if f.Class != "expired" {
					t.Errorf("one day later the record has a %s finding for %s; only the register has decayed", f.Class, f.Key)
					continue
				}
				if !f.Pinned {
					t.Errorf("expired %s is not pinned, but the shipped tree pins it", f.Key)
				}
				for _, e := range f.Entries {
					got[f.Key] = append(got[f.Key], e.ID)
					if e.FixAvailable != fix[e.ID] {
						t.Errorf("expired entry %s fix_available = %q, the register says %q", e.ID, e.FixAvailable, fix[e.ID])
					}
				}
				sort.Strings(got[f.Key])
			}
			for k := range want {
				sort.Strings(want[k])
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("expired findings by key = %v, want %v (one finding per entry key)", got, want)
			}
			if rec.Counts.Expired != len(due) {
				t.Errorf("counts.expired = %d, want %d (one per expired entry)", rec.Counts.Expired, len(due))
			}
		})

		shippedTreeWith := func(t *testing.T, reg []advisoryException) string {
			t.Helper()
			tree := auditTree(t, script, nil, nil, reg)
			shippedInputs := append([]string{"quickstart.sh"}, installLocks...)
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(auditNpmLockRel))); err == nil {
				shippedInputs = append(shippedInputs, auditNpmLockRel)
			}
			for _, rel := range shippedInputs {
				if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(tree)), filepath.FromSlash(rel)), []byte(readRepoFile(t, rel)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			return tree
		}

		t.Run("the register is load-bearing", func(t *testing.T) {
			var without []advisoryException
			for _, e := range register {
				if e.ID != "GHSA-g5ff-637f-6q2m" {
					without = append(without, e)
				}
			}
			wantAuditExit(t, r.run(shippedTreeWith(t, without), db, earliest), false, "GHSA-g5ff-637f-6q2m")
		})

		t.Run("an entry re-versioned off the audited pin no longer excuses", func(t *testing.T) {
			var moved []advisoryException
			for _, e := range register {
				if e.Package == "litellm" {
					e.Version = "1.0.0"
				}
				moved = append(moved, e)
			}
			wantAuditExit(t, r.run(shippedTreeWith(t, moved), db, earliest), false,
				"UNEXCUSED GHSA-7hp6-4w63-5g45", "UNEXCUSED GHSA-g5ff-637f-6q2m",
				"excused   GHSA-rw8w-37p9-mrrp", "excused   GHSA-3jpx-57gj-w458")
		})
	})

	vulnerableIssueLock := []auditPin{{"aiohttp", "3.12.15"}, {"pip", "26.2.2"}}
	vulnerableAiohttp := vulnerableIssueLock[0]
	gatewayLitellm := auditPin{"litellm", "1.93.2"}
	vulnerableDB := func(t *testing.T) auditDB {
		db := newAuditDB(t)
		db.osv(auditControlLine, "PyPI\taiohttp\t3.12.15\tGHSA-2fqr-mr3j-6wp8 PYSEC-2026-2112")
		db.vuln("GHSA-2fqr-mr3j-6wp8", "CVE-2026-54279", "PYSEC-2026-2112")
		db.vuln("PYSEC-2026-2112", "CVE-2026-54279", "GHSA-2fqr-mr3j-6wp8")
		return db
	}

	t.Run("a vulnerable lock line fails", func(t *testing.T) {
		db := vulnerableDB(t)
		wantAuditExit(t, r.run(auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister()), db, "2026-01-15"), false, "aiohttp", "GHSA-2fqr-mr3j-6wp8")
		wantAuditExit(t, r.run(auditTree(t, script, baseIssueLock, baseGatewayLock, baseRegister()), db, "2026-01-15"), true)
		db.assertNoStrayCalls()
	})

	t.Run("an expired entry fails naming its id", func(t *testing.T) {
		reg := append(baseRegister(), auditException("AF-TEST-EXPIRED", nil, adNoPin, "2026-01-01", "2026-01-30"))
		tree := auditTree(t, script, baseIssueLock, baseGatewayLock, reg)
		db := newAuditDB(t)
		wantAuditExit(t, r.run(tree, db, "2026-01-31"), false, "AF-TEST-EXPIRED")
		wantAuditExit(t, r.run(tree, db, "2026-01-30"), true)
	})

	t.Run("transport errors and bad answers fail closed", func(t *testing.T) {
		excused := append(baseRegister(), auditException("GHSA-2fqr-mr3j-6wp8", []string{"CVE-2026-54279", "PYSEC-2026-2112"}, vulnerableAiohttp, "2026-01-01", "2026-01-31"))
		tree := auditTree(t, script, vulnerableIssueLock, baseGatewayLock, excused)
		wantAuditExit(t, r.run(tree, vulnerableDB(t), "2026-01-15"), true)
		for f, mechanism := range map[string]string{
			"querybatch=transport":     "OSV querybatch HTTP 000",
			"querybatch=400":           "OSV querybatch HTTP 400",
			"querybatch=short":         "OSV answered",
			"querybatch=paged":         "next_page_token",
			"vulns=transport":          "FETCH FAILED GHSA-2fqr-mr3j-6wp8",
			"gh=BerriAI_litellm-1:403": "BerriAI/litellm advisories page 1 HTTP 403",
			"gh=BerriAI_litellm-2:403": "BerriAI/litellm advisories page 2 HTTP 403",
			"querybatch=nonjson":       "ERROR: OSV querybatch answer 1 is not JSON",
		} {
			t.Run(f, func(t *testing.T) {
				db := vulnerableDB(t)
				db.faults(f)
				res := r.run(tree, db, "2026-01-15")
				wantAuditExit(t, res, false, mechanism)
				if strings.Contains(res.stdout+res.stderr, "oracle silent") {
					t.Errorf("the %s fault reached the control query, so the exit does not prove the lock batch failed closed", f)
				}
			})
		}
		t.Run("an OSV-reported id with no record", func(t *testing.T) {
			db := vulnerableDB(t)
			if err := os.Remove(filepath.Join(db.dir, "vulns", "PYSEC-2026-2112.json")); err != nil {
				t.Fatal(err)
			}
			wantAuditExit(t, r.run(tree, db, "2026-01-15"), false, "PYSEC-2026-2112")
		})
	})

	t.Run("a silent oracle fails", func(t *testing.T) {
		tree := auditTree(t, script, baseIssueLock, baseGatewayLock, baseRegister())
		db := newAuditDB(t)
		db.osv("PyPI\tunrelated\t1.0\tGHSA-xxxx")
		wantAuditExit(t, r.run(tree, db, "2026-01-15"), false, "oracle silent")
		db = newAuditDB(t)
		db.ghPages("cli/cli", nil)
		wantAuditExit(t, r.run(tree, db, "2026-01-15"), false, "oracle silent", "cli/cli")
	})

	t.Run("an entry excuses an advisory through any of its aliases", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			entry   advisoryException
			wantOK  bool
			mention string
		}{
			{"CVE-keyed entry", auditException("CVE-2026-54279", nil, vulnerableAiohttp, "2026-01-01", "2026-01-31"), true, ""},
			{"GHSA-keyed entry", auditException("GHSA-2fqr-mr3j-6wp8", nil, vulnerableAiohttp, "2026-01-01", "2026-01-31"), true, ""},
			{"PYSEC in aliases", auditException("AF-TEST-X", []string{"PYSEC-2026-2112"}, vulnerableAiohttp, "2026-01-01", "2026-01-31"), true, ""},
			{"unrelated CVE", auditException("CVE-2026-99999", nil, vulnerableAiohttp, "2026-01-01", "2026-01-31"), false, "GHSA-2fqr-mr3j-6wp8"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tree := auditTree(t, script, vulnerableIssueLock, baseGatewayLock, append(baseRegister(), tc.entry))
				wantAuditExit(t, r.run(tree, vulnerableDB(t), "2026-01-15"), tc.wantOK, tc.mention)
			})
		}
		t.Run("a CVE carried only by the GitHub advisory", func(t *testing.T) {
			db := newAuditDB(t)
			db.ghPages("BerriAI/litellm", []map[string]any{ghAdvisory("GHSA-7hp6-4w63-5g45", "CVE-2026-114612", ghVuln{">= 1.91.0, < 1.100.4", patched("1.100.4")})})
			excused := auditTree(t, script, baseIssueLock, baseGatewayLock, append(baseRegister(), acknowledged(auditException("CVE-2026-114612", nil, gatewayLitellm, "2026-01-01", "2026-01-31"), "1.100.4")))
			wantAuditExit(t, r.run(excused, db, "2026-01-15"), true)
			bare := auditTree(t, script, baseIssueLock, baseGatewayLock, baseRegister())
			wantAuditExit(t, r.run(bare, db, "2026-01-15"), false, "GHSA-7hp6-4w63-5g45")
		})
	})

	gatewayAt := func(v string) []auditPin {
		return []auditPin{{"aiohttp", "3.14.4"}, {"litellm[proxy]", v}, {"pip", "26.2.2"}}
	}
	litellmAdvisoryRun := func(t *testing.T, version string, adv map[string]any, register []advisoryException) refRunResult {
		db := newAuditDB(t)
		db.ghPages("BerriAI/litellm", []map[string]any{adv})
		return r.run(auditTree(t, script, baseIssueLock, gatewayAt(version), register), db, "2026-01-15")
	}

	t.Run("a backport listed in patched_versions is not affected", func(t *testing.T) {
		backport := ghAdvisory("GHSA-3cv6-jpf6-8222", "", ghVuln{"<1.94.0", patched("1.96.2, 1.95.1, 1.94.3, 1.93.2, 1.92.2")})
		wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", backport, baseRegister()), true)
		wantAuditExit(t, litellmAdvisoryRun(t, "1.93.5", backport, baseRegister()), true)
		wantAuditExit(t, litellmAdvisoryRun(t, "1.93.1", backport, baseRegister()), false, "GHSA-3cv6-jpf6-8222")
	})

	t.Run("version ranges compare numerically", func(t *testing.T) {
		multi := ghAdvisory("GHSA-7hp6-4w63-5g45", "", ghVuln{">= 1.91.0, < 1.100.4", patched("1.100.4")}, ghVuln{">= 1.103.0, <1.103.1", patched("1.103.1")})
		for v, affected := range map[string]bool{"1.100.3": true, "1.100.4": false, "1.99.0": true, "1.90.9": false, "1.103.0": true, "1.103.1": false} {
			wantAuditExit(t, litellmAdvisoryRun(t, v, multi, baseRegister()), !affected)
		}
		prefixed := ghAdvisory("GHSA-vpfx-0000-0001", "", ghVuln{"< v1.94.0", patched("v1.94.0")})
		wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", prefixed, baseRegister()), false, "GHSA-vpfx-0000-0001")
		wantAuditExit(t, litellmAdvisoryRun(t, "1.94.0", prefixed, baseRegister()), true)
		prerelease := ghAdvisory("GHSA-prer-0000-0001", "", ghVuln{">= 1.104.0rc1, < 1.104.0rc2", patched("1.104.0rc2")})
		wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", prerelease, baseRegister()), true)
		wantAuditExit(t, litellmAdvisoryRun(t, "1.104.0rc1", prerelease, baseRegister()), false, "GHSA-prer-0000-0001")
	})

	t.Run("an unparseable range fails closed unless registered", func(t *testing.T) {
		freeText := ghAdvisory("GHSA-3jpx-57gj-w458", "CVE-2025-64744", ghVuln{"All (before fix)", patched("TBD (maintainers will fill once fixed)")})
		tree := auditTree(t, script, baseIssueLock, baseGatewayLock, baseRegister())
		db := newAuditDB(t)
		db.ghPages("openobserve/openobserve", []map[string]any{freeText})
		wantAuditExit(t, r.run(tree, db, "2026-01-15"), false, "GHSA-3jpx-57gj-w458")
		registered := auditTree(t, script, baseIssueLock, baseGatewayLock, append(baseRegister(), auditException("GHSA-3jpx-57gj-w458", []string{"CVE-2025-64744"}, auditPin{"openobserve", auditSyntheticVersions["OPENOBSERVE_VERSION"]}, "2026-01-01", "2026-01-31")))
		wantAuditExit(t, r.run(registered, db, "2026-01-15"), true)
	})

	t.Run("a multi-row advisory with an unparseable row fails closed unless registered", func(t *testing.T) {
		notAffected, unreadable := ghVuln{"< 1.0.0", patched("1.0.0")}, ghVuln{"All (before fix)", patched("TBD")}
		const reported = "UNEXCUSED GHSA-mrow-0000-0001 [] GitHub BerriAI/litellm 1.93.2 (unparseable)"
		// Only an unparseable row AFTER a "no" row needs the comparator to overrule the status it already holds.
		t.Run("no then unparseable", func(t *testing.T) {
			wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", ghAdvisory("GHSA-mrow-0000-0001", "", notAffected, unreadable), baseRegister()), false, reported)
		})
		t.Run("unparseable then no", func(t *testing.T) {
			wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", ghAdvisory("GHSA-mrow-0000-0001", "", unreadable, notAffected), baseRegister()), false, reported)
		})
		t.Run("registered", func(t *testing.T) {
			registered := append(baseRegister(), auditException("GHSA-mrow-0000-0001", nil, gatewayLitellm, "2026-01-01", "2026-01-31"))
			wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", ghAdvisory("GHSA-mrow-0000-0001", "", notAffected, unreadable), registered), true)
		})
	})

	t.Run("an advisory with no alias is reported field by field", func(t *testing.T) {
		db := newAuditDB(t)
		db.osv(auditControlLine, "PyPI\taiohttp\t3.12.15\tGHSA-osvn-0000-0001")
		db.vuln("GHSA-osvn-0000-0001")
		db.ghPages("BerriAI/litellm", []map[string]any{ghAdvisory("GHSA-noal-0000-0001", "", ghVuln{"< 1.94.0", patched("1.94.0")})})
		res := r.run(auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister()), db, "2026-01-15")
		wantAuditExit(t, res, false)
		for _, line := range []string{
			"UNEXCUSED GHSA-osvn-0000-0001 [] OSV PyPI aiohttp==3.12.15",
			"UNEXCUSED GHSA-noal-0000-0001 [] GitHub BerriAI/litellm 1.93.2 (yes)",
		} {
			if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).MatchString(res.stdout) {
				t.Errorf("the report lacks the line %q:\n%s", line, res.stdout)
			}
		}
		// A shifted field reads the status, the source or a package@version key as an alias, so a
		// register name that happens to equal one would excuse an advisory it never named.
		for _, name := range []string{"yes", "OSV", "aiohttp==3.12.15", "aiohttp@3.12.15", "litellm@1.93.2"} {
			reg := append(baseRegister(),
				auditException("AF-TEST-COLLIDE", []string{name}, vulnerableAiohttp, "2026-01-01", "2026-01-31"),
				auditException("AF-TEST-COLLIDE", []string{name}, gatewayLitellm, "2026-01-01", "2026-01-31"))
			wantAuditExit(t, r.run(auditTree(t, script, vulnerableIssueLock, baseGatewayLock, reg), db, "2026-01-15"), false, "UNEXCUSED GHSA-osvn-0000-0001", "UNEXCUSED GHSA-noal-0000-0001")
		}
		reg := append(baseRegister(),
			auditException("GHSA-osvn-0000-0001", nil, vulnerableAiohttp, "2026-01-01", "2026-01-31"),
			acknowledged(auditException("GHSA-noal-0000-0001", nil, gatewayLitellm, "2026-01-01", "2026-01-31"), "1.94.0"))
		wantAuditExit(t, r.run(auditTree(t, script, vulnerableIssueLock, baseGatewayLock, reg), db, "2026-01-15"), true,
			"excused   GHSA-noal-0000-0001 [] GitHub BerriAI/litellm 1.93.2 (yes) (register: GHSA-noal-0000-0001)")
	})

	t.Run("an entry excuses only the package and version it was reviewed for", func(t *testing.T) {
		belowFix := ghAdvisory("GHSA-pinv-0000-0001", "", ghVuln{"< 1.94.0", patched("1.94.0")})
		reviewedFor := func(pin auditPin) []advisoryException {
			return append(baseRegister(), auditException("GHSA-pinv-0000-0001", nil, pin, "2026-01-01", "2026-01-31"))
		}
		t.Run("the reviewed pin stays excused", func(t *testing.T) {
			reg := reviewedFor(gatewayLitellm)
			reg[len(reg)-1] = acknowledged(reg[len(reg)-1], "1.94.0")
			wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", belowFix, reg), true,
				"excused   GHSA-pinv-0000-0001 [] GitHub BerriAI/litellm 1.93.2 (yes) (register: GHSA-pinv-0000-0001)")
		})
		t.Run("a moved pin re-opens the finding", func(t *testing.T) {
			wantAuditExit(t, litellmAdvisoryRun(t, "1.93.1", belowFix, reviewedFor(gatewayLitellm)), false,
				"UNEXCUSED GHSA-pinv-0000-0001 [] GitHub BerriAI/litellm 1.93.1 (yes)")
		})
		t.Run("another package's entry excuses nothing", func(t *testing.T) {
			wantAuditExit(t, litellmAdvisoryRun(t, "1.93.2", belowFix, reviewedFor(auditPin{"openobserve", "1.93.2"})), false,
				"UNEXCUSED GHSA-pinv-0000-0001")
		})

		aiohttpReviewedAt := func(pin auditPin) []advisoryException {
			return append(baseRegister(), auditException("GHSA-2fqr-mr3j-6wp8", []string{"CVE-2026-54279", "PYSEC-2026-2112"}, pin, "2026-01-01", "2026-01-31"))
		}
		t.Run("an OSV finding stays excused at its reviewed pin", func(t *testing.T) {
			tree := auditTree(t, script, vulnerableIssueLock, baseGatewayLock, aiohttpReviewedAt(vulnerableAiohttp))
			wantAuditExit(t, r.run(tree, vulnerableDB(t), "2026-01-15"), true)
		})
		t.Run("an OSV finding at another version is unexcused", func(t *testing.T) {
			tree := auditTree(t, script, vulnerableIssueLock, baseGatewayLock, aiohttpReviewedAt(auditPin{"aiohttp", "3.12.14"}))
			wantAuditExit(t, r.run(tree, vulnerableDB(t), "2026-01-15"), false, "UNEXCUSED GHSA-2fqr-mr3j-6wp8", "UNEXCUSED PYSEC-2026-2112")
		})

		twoPinRun := func(t *testing.T, reg []advisoryException) refRunResult {
			db := vulnerableDB(t)
			db.osv(auditControlLine,
				"PyPI\taiohttp\t3.12.15\tGHSA-2fqr-mr3j-6wp8 PYSEC-2026-2112",
				"PyPI\taiohttp\t3.12.14\tGHSA-2fqr-mr3j-6wp8 PYSEC-2026-2112")
			gateway := []auditPin{{"aiohttp", "3.12.14"}, {"litellm[proxy]", "1.93.2"}, {"pip", "26.2.2"}}
			return r.run(auditTree(t, script, vulnerableIssueLock, gateway, reg), db, "2026-01-15")
		}
		t.Run("a finding against two pins needs both reviewed", func(t *testing.T) {
			wantAuditExit(t, twoPinRun(t, aiohttpReviewedAt(vulnerableAiohttp)), false,
				"UNEXCUSED GHSA-2fqr-mr3j-6wp8 [CVE-2026-54279 PYSEC-2026-2112] OSV PyPI aiohttp==3.12.14, PyPI aiohttp==3.12.15")
		})
		t.Run("a finding against two pins is excused when each is reviewed", func(t *testing.T) {
			// Ids and aliases are unique across the register, so the second pin is reviewed under an alias.
			reg := append(baseRegister(),
				auditException("GHSA-2fqr-mr3j-6wp8", nil, vulnerableAiohttp, "2026-01-01", "2026-01-31"),
				auditException("CVE-2026-54279", nil, auditPin{"aiohttp", "3.12.14"}, "2026-01-01", "2026-01-31"))
			wantAuditExit(t, twoPinRun(t, reg), true)
		})
	})

	t.Run("an advisory on page 2 is not missed", func(t *testing.T) {
		db := newAuditDB(t)
		db.ghPages("BerriAI/litellm", []map[string]any{neverMatching("GHSA-base-berr-0001")}, []map[string]any{ghAdvisory("GHSA-pag2-0000-0001", "", ghVuln{"< 1.94.0", patched("1.94.0")})})
		wantAuditExit(t, r.run(auditTree(t, script, baseIssueLock, baseGatewayLock, baseRegister()), db, "2026-01-15"), false, "GHSA-pag2-0000-0001")
	})

	t.Run("OSV queries are chunked at 500 and name every pin", func(t *testing.T) {
		pins := []auditPin{{"pip", "26.2.2"}, {"foo", "1.0"}}
		for i := 0; i < 500; i++ {
			pins = append(pins, auditPin{fmt.Sprintf("pkg-%03d", i), "1.0.0"})
		}
		db := newAuditDB(t)
		wantAuditExit(t, r.run(auditTree(t, script, pins, baseGatewayLock, baseRegister()), db, "2026-01-15"), true)
		var sizes []string
		for _, s := range strings.Fields(db.read("batches.log")) {
			sizes = append(sizes, s)
			var n int
			fmt.Sscan(s, &n)
			if n > 500 {
				t.Errorf("an OSV batch carried %d queries; the API limit is 500", n)
			}
		}
		if len(sizes) < 2 {
			t.Errorf("batch sizes %v: 503 pins need at least two batches", sizes)
		}
		q := db.read("queries.log")
		for _, want := range []string{`["PyPI","pkg-499","1.0.0"]`, `["PyPI","litellm","1.93.2"]`, `["PyPI","foo","1.0"]`} {
			if !strings.Contains(q, want) {
				t.Errorf("OSV was never asked about %s", want)
			}
		}
	})

	t.Run("GH_TOKEN goes only to GitHub", func(t *testing.T) {
		tree := auditTree(t, script, baseIssueLock, baseGatewayLock, baseRegister())
		db := newAuditDB(t)
		wantAuditExit(t, r.run(tree, db, "2026-01-15", "GH_TOKEN=test-token"), true)
		var githubCalls, otherCalls int
		for _, l := range strings.Split(strings.TrimSpace(db.read("calls.log")), "\n") {
			f := strings.Split(l, "\t")
			if len(f) != 3 {
				continue
			}
			github := strings.HasPrefix(f[1], "https://api.github.com/")
			if github {
				githubCalls++
			} else {
				otherCalls++
			}
			if github && f[2] != "Authorization: Bearer test-token" {
				t.Errorf("GitHub call without the token: %s", l)
			}
			if !github && f[2] != "noauth" {
				t.Errorf("non-GitHub call carried an Authorization header: %s", l)
			}
		}
		if githubCalls == 0 || otherCalls == 0 {
			t.Fatalf("the audit made %d GitHub and %d other calls; both sources must be queried", githubCalls, otherCalls)
		}
		db = newAuditDB(t)
		wantAuditExit(t, r.run(tree, db, "2026-01-15"), true)
		if strings.Contains(db.read("calls.log"), "Authorization") {
			t.Error("an Authorization header was sent without GH_TOKEN set")
		}
	})

	baseTree := func(t *testing.T) string {
		return auditTree(t, script, baseIssueLock, baseGatewayLock, baseRegister())
	}
	queried := func(t *testing.T, db auditDB, want ...string) {
		t.Helper()
		q := db.read("queries.log")
		for _, w := range want {
			if !strings.Contains(q, w) {
				t.Errorf("OSV was never asked about %s; queries:\n%s", w, q)
			}
		}
	}

	t.Run("a go-prefixed toolchain pin is queried without its prefix", func(t *testing.T) {
		db := newAuditDB(t)
		// OSV reads go1.99.1 as a version string no stdlib range excludes, so the prefixed query
		// is the false positive that must never reach the report.
		db.osv(auditControlLine, "Go\tstdlib\t1.99.1\tGO-2099-0001", "Go\tstdlib\tgo1.99.1\tGO-2099-9999", "Go\ttoolchain\tgo1.99.1\tGO-2099-9999")
		db.vuln("GO-2099-0001", "CVE-2099-0001")
		db.vuln("GO-2099-9999", "CVE-2099-9999")
		res := r.run(baseTree(t), db, "2026-01-15")
		wantAuditExit(t, res, false, "GO-2099-0001")
		if strings.Contains(res.stdout+res.stderr, "GO-2099-9999") {
			t.Errorf("the audit queried the go-prefixed version and reported its false positive:\n%s", res.stdout+res.stderr)
		}
		queried(t, db, `["Go","stdlib","1.99.1"]`, `["Go","toolchain","1.99.1"]`)
		if strings.Contains(db.read("queries.log"), "go1.99.1") {
			t.Errorf("OSV was asked about go1.99.1; strip the go prefix (D-35)")
		}
		db.assertNoStrayCalls()
		excused := auditTree(t, script, baseIssueLock, baseGatewayLock, append(baseRegister(), auditException("GO-2099-0001", nil, auditPin{"stdlib", "1.99.1"}, "2026-01-01", "2026-01-31")))
		wantAuditExit(t, r.run(excused, db, "2026-01-15"), true)
	})

	t.Run("npm pins and the integration lock are audited through OSV", func(t *testing.T) {
		db := newAuditDB(t)
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), true)
		queried(t, db,
			`["npm","playwright","1.98.4"]`, `["npm","playwright-core","1.98.4"]`,
			`["npm","@openai/codex","0.998.0"]`, `["npm","@anthropic-ai/claude-code","2.9.998"]`,
			`["npm","playwright","1.97.0"]`, `["npm","playwright-core","1.97.0"]`, `["npm","@playwright/mcp","0.0.997"]`,
			`["npm","playwright","1.97.0-alpha-2099-01-01"]`, `["npm","playwright-core","1.97.0-alpha-2099-01-01"]`)
		db.assertNoStrayCalls()
		db.osv(auditControlLine, "npm\t@playwright/mcp\t0.0.997\tGHSA-npmx-0000-0001", "npm\t@openai/codex\t0.998.0\tGHSA-cdxx-0000-0001")
		db.vuln("GHSA-npmx-0000-0001")
		db.vuln("GHSA-cdxx-0000-0001")
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), false, "GHSA-npmx-0000-0001", "GHSA-cdxx-0000-0001")
	})

	t.Run("vendor versions come from quickstart.sh", func(t *testing.T) {
		db := newAuditDB(t)
		db.ghPages("cli/cli", []map[string]any{ghAdvisory("GHSA-clix-0000-0001", "", ghVuln{"< 2.100.0", patched("2.100.0")})})
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), false, "GHSA-clix-0000-0001", "cli/cli "+auditSyntheticVersions["GH_VERSION"])
		fixed := auditTreeWith(t, script, baseIssueLock, baseGatewayLock, baseRegister(), auditQuickstart(t, map[string]string{"GH_VERSION": "2.100.0"}), auditNpmLock())
		wantAuditExit(t, r.run(fixed, db, "2026-01-15"), true)

		db = newAuditDB(t)
		db.ghPages("openobserve/openobserve", []map[string]any{ghAdvisory("GHSA-oovx-0000-0001", "", ghVuln{">= v1.98.0, < v1.99.0", patched("v1.99.0")})})
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), false, "GHSA-oovx-0000-0001", "openobserve/openobserve "+auditSyntheticVersions["OPENOBSERVE_VERSION"])
	})

	t.Run("an unmapped _VERSION constant fails closed", func(t *testing.T) {
		db := newAuditDB(t)
		unmapped := auditTreeWith(t, script, baseIssueLock, baseGatewayLock, baseRegister(), auditQuickstart(t, map[string]string{"FOO_VERSION": "1.2.3"}), auditNpmLock())
		wantAuditExit(t, r.run(unmapped, db, "2026-01-15"), false, "FOO_VERSION")
		floor := auditTreeWith(t, script, baseIssueLock, baseGatewayLock, baseRegister(), auditQuickstart(t, map[string]string{"BAR_MIN_VERSION": "1.0"}), auditNpmLock())
		wantAuditExit(t, r.run(floor, db, "2026-01-15"), true)
	})

	t.Run("the nodejs rule fails only on a newer same-major security release", func(t *testing.T) {
		// The pin's own security flag means it CONTAINS fixes (D-36); v96.6.0 is older than the pin
		// numerically but newer as a string.
		base := []auditNodeRelease{{"v97.9.0", true}, {"v96.51.0", false}, {"v96.50.1", true}, {"v96.49.0", true}, {"v96.6.0", true}, {"v95.99.0", true}}
		db := newAuditDB(t)
		db.nodeIndex(base...)
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), true)
		for _, newer := range []string{"v96.52.0", "v96.100.0"} {
			db := newAuditDB(t)
			db.nodeIndex(append([]auditNodeRelease{{newer, true}}, base...)...)
			wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), false, newer)
		}
	})

	t.Run("a nodejs end-of-life within 90 days warns without failing", func(t *testing.T) {
		db := newAuditDB(t)
		db.nodeSchedule(auditNodeMajor, "2026-03-01")
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), true, "end-of-life", "2026-03-01")
		db.nodeSchedule(auditNodeMajor, "2027-04-30")
		res := r.run(baseTree(t), db, "2026-01-15")
		wantAuditExit(t, res, true)
		if strings.Contains(res.stdout+res.stderr, "end-of-life") {
			t.Errorf("an end of life 15 months out must not warn:\n%s", res.stdout+res.stderr)
		}
	})

	t.Run("a pin that no longer resolves fails", func(t *testing.T) {
		gh, goV := auditSyntheticVersions["GH_VERSION"], strings.TrimPrefix(auditSyntheticVersions["GO_TOOLCHAIN_VERSION"], "go")
		codex, claude, oo := auditSyntheticVersions["CODEX_VERSION"], auditSyntheticVersions["CLAUDE_CODE_VERSION"], auditSyntheticVersions["OPENOBSERVE_VERSION"]
		assets := []string{"https://registry.npmjs.org/@openai/codex/-/codex-" + codex + ".tgz"}
		var indexes []string
		for _, a := range []string{"arm64", "amd64"} {
			assets = append(assets,
				"https://github.com/cli/cli/releases/download/v"+gh+"/gh_"+gh+"_linux_"+a+".tar.gz",
				"https://go.dev/dl/go"+goV+".linux-"+a+".tar.gz",
				"https://downloads.openobserve.ai/releases/openobserve/"+oo+"/openobserve-"+oo+"-linux-"+a+".tar.gz")
			indexes = append(indexes, "https://deb.nodesource.com/node_"+auditNodeMajor+".x/dists/nodistro/main/binary-"+a+"/Packages")
		}
		for _, p := range []string{"linux-arm64", "linux-x64"} {
			assets = append(assets,
				"https://downloads.claude.ai/claude-code-releases/"+claude+"/"+p+"/claude",
				"https://registry.npmjs.org/@openai/codex/-/codex-"+codex+"-"+p+".tgz")
		}

		db := newAuditDB(t)
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), true)
		calls := db.read("calls.log")
		for _, u := range assets {
			if !strings.Contains(calls, "HEAD\t"+u+"\t") {
				t.Errorf("the audit never checked that %s still answers (curl -I)", u)
			}
			// A GET of a release asset downloads it: the Claude binary alone is ~290 MB.
			if strings.Contains(calls, "GET\t"+u+"\t") {
				t.Errorf("the audit downloaded %s instead of a HEAD request", u)
			}
		}
		for _, u := range indexes {
			if !strings.Contains(calls, "GET\t"+u+"\t") {
				t.Errorf("the audit never read the NodeSource index %s", u)
			}
		}
		db.assertNoStrayCalls()

		db = newAuditDB(t)
		db.nodePackages("96.49.0-1nodesource1", auditNodeApt+"0", "96.51.0-1nodesource1")
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), false, auditNodeApt)

		claudeAsset := "https://downloads.claude.ai/claude-code-releases/" + claude + "/linux-arm64/claude"
		db = newAuditDB(t)
		db.missing(claudeAsset)
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), false, claudeAsset)

		db = newAuditDB(t)
		db.missing(indexes[1])
		wantAuditExit(t, r.run(baseTree(t), db, "2026-01-15"), false, indexes[1])
	})

	t.Run("every input error fails closed with its own message", func(t *testing.T) {
		// A broken input also starves the checks after it, so a non-zero exit survives deleting the
		// check under test; only its message pins it.
		offMajor := "95.50.1-1nodesource1"
		for _, tc := range []struct {
			name  string
			tree  func(t *testing.T) string
			feeds func(db auditDB)
			want  string
		}{
			{"a missing quickstart.sh", func(t *testing.T) string {
				tree := baseTree(t)
				if err := os.Remove(filepath.Join(filepath.Dir(filepath.Dir(tree)), "quickstart.sh")); err != nil {
					t.Fatal(err)
				}
				return tree
			}, nil, "ERROR: quickstart.sh is missing, so the bootstrap's pins cannot be read"},
			{"a lock that pins nothing", func(t *testing.T) string {
				return auditTree(t, script, nil, baseGatewayLock, baseRegister())
			}, nil, "ERROR: py/requirements.txt is missing or pins nothing"},
			{"a gateway lock without a litellm pin", func(t *testing.T) string {
				return auditTree(t, script, baseIssueLock, baseIssueLock, baseRegister())
			}, nil, "ERROR: py/litellm-requirements.txt has no litellm== pin to compare BerriAI/litellm advisories against"},
			{"a NODE_APT_VERSION off its major", func(t *testing.T) string {
				return auditTreeWith(t, script, baseIssueLock, baseGatewayLock, baseRegister(), auditQuickstart(t, map[string]string{"NODE_APT_VERSION": offMajor}), auditNpmLock())
			}, func(db auditDB) { db.nodeFeeds(auditNodeMajor, offMajor) }, "ERROR: NODE_APT_VERSION " + offMajor + " is not on NODE_MAJOR " + auditNodeMajor},
		} {
			t.Run(tc.name, func(t *testing.T) {
				db := newAuditDB(t)
				if tc.feeds != nil {
					tc.feeds(db)
				}
				wantAuditExit(t, r.run(tc.tree(t), db, "2026-01-15"), false, tc.want)
			})
		}
	})

	// ---- The finding record (ADVISORY_AUDIT_RECORD) and the exit classes ----------------------------

	advisoryIDs := func(f auditFinding) []string {
		var ids []string
		for _, a := range f.Advisories {
			ids = append(ids, a.ID)
		}
		sort.Strings(ids)
		return ids
	}
	onlyAdvisory := func(t *testing.T, f auditFinding) auditRecordAdv {
		t.Helper()
		if len(f.Advisories) != 1 {
			t.Fatalf("%s %s carries %d advisories, want 1: %+v", f.Class, f.Key, len(f.Advisories), f.Advisories)
		}
		return f.Advisories[0]
	}
	wantNoFix := func(t *testing.T, f auditFinding, raw string) {
		t.Helper()
		for _, a := range f.Advisories {
			if len(a.Fixed) != 0 || a.FixedRaw != raw {
				t.Errorf("%s at %s: fixed %q, fixed_raw %q; want [] and %q", a.ID, f.Key, a.Fixed, a.FixedRaw, raw)
			}
		}
		if f.FixPublished == nil || *f.FixPublished {
			t.Errorf("%s %s fix_published = %v, want false", f.Class, f.Key, f.FixPublished)
		}
	}
	litellmRecorded := func(t *testing.T, version string, adv map[string]any, register []advisoryException, wantExit int) (refRunResult, auditRecord) {
		db := newAuditDB(t)
		db.ghPages("BerriAI/litellm", []map[string]any{adv})
		return r.recorded(t, auditTree(t, script, baseIssueLock, gatewayAt(version), register), db, "2026-01-15", wantExit)
	}
	openobserveRecorded := func(t *testing.T, adv map[string]any, register []advisoryException, wantExit int) (refRunResult, auditRecord) {
		db := newAuditDB(t)
		db.ghPages("openobserve/openobserve", []map[string]any{adv})
		return r.recorded(t, auditTree(t, script, baseIssueLock, baseGatewayLock, register), db, "2026-01-15", wantExit)
	}
	ooPin := auditPin{"openobserve", auditSyntheticVersions["OPENOBSERVE_VERSION"]}
	ooKey := ooPin.name + "@" + ooPin.version
	rw8w := ghAdvisory("GHSA-rw8w-37p9-mrrp", "CVE-2024-41809", ghVuln{">= v0.5.2", patched("v0.5.2")})
	tbd := ghAdvisory("GHSA-3jpx-57gj-w458", "CVE-2025-64744", ghVuln{"All (before fix)", patched("TBD (maintainers will fill once fixed)")})
	pinv := ghAdvisory("GHSA-pinv-0000-0001", "", ghVuln{"< 1.94.0", patched("1.94.0")})
	pinvEntry := auditException("GHSA-pinv-0000-0001", nil, gatewayLitellm, "2026-01-01", "2026-01-31")
	const probePinv = "ERROR: register entry GHSA-pinv-0000-0001 excuses litellm@1.93.2 although 1.94.0 is published; set fix_available or move the pin"
	const gitSHA = "4444444444444444444444444444444444444444" // all digits: vparse reads it as a version, so only the GIT filter drops it
	hasLine := func(t *testing.T, out, line string) {
		t.Helper()
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).MatchString(out) {
			t.Errorf("the output lacks the line %q:\n%s", line, out)
		}
	}
	expiredRun := func(t *testing.T) auditRecord {
		reg := append(baseRegister(),
			auditException("AF-TEST-EXP1", nil, adNoPin, "2026-01-01", "2026-01-30"),
			auditException("AF-TEST-EXP2", nil, adNoPin, "2026-01-01", "2026-01-30"),
			auditException("AF-TEST-EXP3", nil, gatewayLitellm, "2026-01-01", "2026-01-30"))
		res, rec := r.recorded(t, auditTree(t, script, baseIssueLock, baseGatewayLock, reg), newAuditDB(t), "2026-01-31", 1)
		hasLine(t, res.stderr, "ERROR: register entry AF-TEST-EXP1 (recheck_by 2026-01-30) has expired as of 2026-01-31; re-review it and set a new recheck_by")
		return rec
	}

	t.Run("record unexcused finding is grouped by key", func(t *testing.T) {
		tree := auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister())
		_, rec := r.recorded(t, tree, vulnerableDB(t), "2026-01-15", 1)
		if rec.ExitClass != "finding" || len(rec.Findings) != 1 {
			t.Fatalf("record = class %q, findings %v; want one finding", rec.ExitClass, rec.classKeys())
		}
		f := rec.find(t, "unexcused", vulnerableAiohttp.name+"@"+vulnerableAiohttp.version)
		if !f.Pinned || !reflect.DeepEqual(f.PinnedIn, []auditPinLocation{{"PyPI", ""}}) {
			t.Errorf("pinned %v, pinned_in %+v; want true and the PyPI lock", f.Pinned, f.PinnedIn)
		}
		if got := advisoryIDs(f); !reflect.DeepEqual(got, []string{"GHSA-2fqr-mr3j-6wp8", "PYSEC-2026-2112"}) {
			t.Errorf("advisories = %v, want both ids reported at the key", got)
		}
		for _, a := range f.Advisories {
			if a.Source != "OSV" || a.Ecosystem != "PyPI" || a.Repo != "" || a.Against != "OSV PyPI aiohttp==3.12.15" {
				t.Errorf("advisory %+v: want source OSV, ecosystem PyPI, no repo, against OSV PyPI aiohttp==3.12.15", a)
			}
		}
		if len(f.Register) != 0 {
			t.Errorf("register = %+v, want [] (no entry names these advisories)", f.Register)
		}
		if c := rec.Counts; c.Reported != 2 || c.Unexcused != 2 || c.Errors != 0 {
			t.Errorf("counts = %+v, want reported 2, unexcused 2 (the summary's operands), errors 0", c)
		}
	})

	t.Run("record unexcused finding lists the entry reviewed for another version", func(t *testing.T) {
		reg := append(baseRegister(), auditException("GHSA-2fqr-mr3j-6wp8", []string{"CVE-2026-54279", "PYSEC-2026-2112"}, auditPin{"aiohttp", "3.12.14"}, "2026-01-01", "2026-01-31"))
		_, rec := r.recorded(t, auditTree(t, script, vulnerableIssueLock, baseGatewayLock, reg), vulnerableDB(t), "2026-01-15", 1)
		f := rec.find(t, "unexcused", "aiohttp@3.12.15")
		want := []auditRegisterRef{{ID: "GHSA-2fqr-mr3j-6wp8", Aliases: []string{"CVE-2026-54279", "PYSEC-2026-2112"}, Reviewed: "2026-01-01", RecheckBy: "2026-01-31", Key: "aiohttp@3.12.14"}}
		if !reflect.DeepEqual(f.Register, want) {
			t.Errorf("register = %+v, want %+v (once, though both advisories name it)", f.Register, want)
		}
	})

	t.Run("record expired entries are grouped by key", func(t *testing.T) {
		rec := expiredRun(t)
		if got := rec.classKeys(); !reflect.DeepEqual(got, []string{"expired litellm@1.93.2", "expired pkg@1"}) {
			t.Fatalf("findings = %v, want one expired finding per entry key", got)
		}
		for key, want := range map[string][]string{"pkg@1": {"AF-TEST-EXP1", "AF-TEST-EXP2"}, "litellm@1.93.2": {"AF-TEST-EXP3"}} {
			f := rec.find(t, "expired", key)
			var ids []string
			for _, e := range f.Entries {
				ids = append(ids, e.ID)
				if e.Reviewed != "2026-01-01" || e.RecheckBy != "2026-01-30" || e.FixAvailable != "" || len(e.Excuses) != 0 {
					t.Errorf("entry %+v: want reviewed 2026-01-01, recheck_by 2026-01-30, no fix_available, excuses []", e)
				}
			}
			if !reflect.DeepEqual(ids, want) {
				t.Errorf("%s entries = %v, want %v", key, ids, want)
			}
			if f.Pinned != (key == "litellm@1.93.2") {
				t.Errorf("%s pinned = %v", key, f.Pinned)
			}
		}
		if c := rec.Counts; c.Expired != 3 || c.Errors != 0 {
			t.Errorf("counts = %+v, want expired 3 and errors 0 (an expired entry is a finding, not an error)", c)
		}
		if rec.ExitClass != "finding" {
			t.Errorf("exit_class = %q, want finding", rec.ExitClass)
		}
	})

	t.Run("record expired finding names where the key is pinned", func(t *testing.T) {
		rec := expiredRun(t)
		if got := rec.find(t, "expired", "litellm@1.93.2").PinnedIn; !reflect.DeepEqual(got, []auditPinLocation{{"PyPI", ""}, {"GitHub", "BerriAI/litellm"}}) {
			t.Errorf("litellm@1.93.2 pinned_in = %+v, want the gateway lock then the BerriAI/litellm vendor pin", got)
		}
		if got := rec.find(t, "expired", "pkg@1").PinnedIn; len(got) != 0 {
			t.Errorf("pkg@1 pinned_in = %+v, want [] (no audited pin carries it)", got)
		}
	})

	t.Run("record expired and excused fixable on one key are one finding", func(t *testing.T) {
		keysAt := func(rec auditRecord, key string) []string {
			var out []string
			for _, f := range rec.Findings {
				if f.Key == key {
					out = append(out, f.Class+" "+f.Key)
				}
			}
			return out
		}
		db := newAuditDB(t)
		db.ghPages("BerriAI/litellm", []map[string]any{ghAdvisory("GHSA-g5ff-637f-6q2m", "", ghVuln{"< 1.95.0", patched("1.95.0")})})
		reg := append(baseRegister(), auditException("GHSA-g5ff-637f-6q2m", nil, gatewayLitellm, "2026-01-01", "2026-01-30"))
		res, rec := r.recorded(t, auditTree(t, script, baseIssueLock, baseGatewayLock, reg), db, "2026-01-31", 1)
		hasLine(t, res.stderr, "ERROR: register entry GHSA-g5ff-637f-6q2m (recheck_by 2026-01-30) has expired as of 2026-01-31; re-review it and set a new recheck_by")
		hasLine(t, res.stderr, "ERROR: register entry GHSA-g5ff-637f-6q2m excuses litellm@1.93.2 although 1.95.0 is published; set fix_available or move the pin")
		if got := keysAt(rec, "litellm@1.93.2"); !reflect.DeepEqual(got, []string{"expired litellm@1.93.2"}) {
			t.Errorf("findings for litellm@1.93.2 = %v, want one expired finding: one key, one issue", got)
		}
		want := []auditRecordEntry{{ID: "GHSA-g5ff-637f-6q2m", Advisory: "GHSA-g5ff-637f-6q2m", Fixed: []string{"1.95.0"}}}
		if got := rec.find(t, "expired", "litellm@1.93.2").ExcusedFixable; !reflect.DeepEqual(got, want) {
			t.Errorf("excused_fixable = %+v, want %+v (the key's probe rows)", got, want)
		}
		if c := rec.Counts; c.Expired != 1 || c.ExcusedFixable != 1 {
			t.Errorf("counts = %+v, want expired 1 and excused_fixable 1 (one per printed line)", c)
		}
		t.Run("an unexpired entry at the key folds too", func(t *testing.T) {
			db := newAuditDB(t)
			db.ghPages("BerriAI/litellm", []map[string]any{pinv})
			reg := append(baseRegister(), auditException("AF-TEST-EXP3", nil, gatewayLitellm, "2026-01-01", "2026-01-30"), pinvEntry)
			res, rec := r.recorded(t, auditTree(t, script, baseIssueLock, baseGatewayLock, reg), db, "2026-01-31", 1)
			hasLine(t, res.stderr, "ERROR: register entry GHSA-pinv-0000-0001 excuses litellm@1.93.2 although 1.94.0 is published; set fix_available or move the pin")
			if got := keysAt(rec, "litellm@1.93.2"); !reflect.DeepEqual(got, []string{"expired litellm@1.93.2"}) {
				t.Errorf("findings for litellm@1.93.2 = %v, want one expired finding: one key, one issue", got)
			}
			f := rec.find(t, "expired", "litellm@1.93.2")
			if len(f.Entries) != 1 || f.Entries[0].ID != "AF-TEST-EXP3" {
				t.Errorf("entries = %+v, want the expired AF-TEST-EXP3 alone", f.Entries)
			}
			want := []auditRecordEntry{{ID: "GHSA-pinv-0000-0001", Advisory: "GHSA-pinv-0000-0001", Fixed: []string{"1.94.0"}}}
			if !reflect.DeepEqual(f.ExcusedFixable, want) {
				t.Errorf("excused_fixable = %+v, want %+v (an unexpired entry at the key folds too)", f.ExcusedFixable, want)
			}
		})
		t.Run("an expired key no probe names carries an empty list", func(t *testing.T) {
			for _, f := range expiredRun(t).Findings {
				if f.ExcusedFixable == nil || len(f.ExcusedFixable) != 0 {
					t.Errorf("%s %s excused_fixable = %#v, want [] (always present)", f.Class, f.Key, f.ExcusedFixable)
				}
			}
		})
	})

	t.Run("record node behind is a finding", func(t *testing.T) {
		db := newAuditDB(t)
		db.nodeIndex(auditNodeRelease{"v96.52.0", true}, auditNodeRelease{"v96.50.1", true})
		res, rec := r.recorded(t, baseTree(t), db, "2026-01-15", 1)
		hasLine(t, res.stderr, "ERROR: nodejs v96.50.1 is behind v96.52.0, a newer v96 security release; move NODE_APT_VERSION")
		f := rec.find(t, "node-behind", "nodejs@v96.50.1")
		if !f.Pinned || !reflect.DeepEqual(f.Releases, []string{"v96.52.0"}) || !reflect.DeepEqual(f.PinnedIn, []auditPinLocation{{"apt", ""}}) {
			t.Errorf("finding = %+v; want pinned, releases [v96.52.0], pinned_in [{apt }] (NODE_APT_VERSION is an apt pin)", f)
		}
		if c := rec.Counts; c.NodeBehind != 1 || c.Errors != 0 || rec.ExitClass != "finding" {
			t.Errorf("counts = %+v, class %q; want node_behind 1, errors 0, finding", c, rec.ExitClass)
		}
		t.Run("two newer releases are one finding", func(t *testing.T) {
			db := newAuditDB(t)
			db.nodeIndex(auditNodeRelease{"v96.53.0", true}, auditNodeRelease{"v96.52.0", true}, auditNodeRelease{"v96.50.1", true})
			_, rec := r.recorded(t, baseTree(t), db, "2026-01-15", 1)
			if f := rec.find(t, "node-behind", "nodejs@v96.50.1"); !reflect.DeepEqual(f.Releases, []string{"v96.53.0", "v96.52.0"}) || len(rec.Findings) != 1 {
				t.Errorf("findings %v, releases %v; want one finding listing both", rec.classKeys(), f.Releases)
			}
			if rec.Counts.NodeBehind != 2 {
				t.Errorf("counts.node_behind = %d, want 2 (one per printed line)", rec.Counts.NodeBehind)
			}
		})
	})

	t.Run("record infrastructure only exits 3", func(t *testing.T) {
		db := newAuditDB(t)
		db.faults("querybatch=transport")
		_, rec := r.recorded(t, baseTree(t), db, "2026-01-15", 3)
		if rec.ExitClass != "infrastructure" || len(rec.Findings) != 0 {
			t.Errorf("record = class %q, findings %v; want infrastructure, none", rec.ExitClass, rec.classKeys())
		}
		found := false
		for _, e := range rec.Errors {
			found = found || strings.Contains(e, "OSV querybatch HTTP 000")
		}
		if !found || rec.Counts.Errors != len(rec.Errors) {
			t.Errorf("errors = %q (counts.errors %d); want the querybatch failure, one element per error", rec.Errors, rec.Counts.Errors)
		}
		t.Run("without the record variable", func(t *testing.T) {
			if res := r.run(baseTree(t), db, "2026-01-15"); res.exit != 3 {
				t.Errorf("exit = %d, want 3\n%s", res.exit, res.stderr)
			}
		})
		t.Run("a register that is not an array", func(t *testing.T) {
			tree := baseTree(t)
			if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(tree)), advisoryRegisterPath), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, rec := r.recorded(t, tree, newAuditDB(t), "2026-01-15", 3); rec.ExitClass != "infrastructure" || len(rec.Findings) != 0 {
				t.Errorf("record = class %q, findings %v; want infrastructure, none", rec.ExitClass, rec.classKeys())
			}
		})
	})

	t.Run("record osv outage with a vendor finding exits 1", func(t *testing.T) {
		db := newAuditDB(t)
		db.faults("querybatch=transport")
		db.ghPages("BerriAI/litellm", []map[string]any{ghAdvisory("GHSA-noal-0000-0001", "", ghVuln{"< 1.94.0", patched("1.94.0")})})
		_, rec := r.recorded(t, baseTree(t), db, "2026-01-15", 1)
		if rec.ExitClass != "finding" || len(rec.Errors) == 0 {
			t.Errorf("record = class %q, %d errors; want finding (a finding the run did observe outranks the outage), errors kept", rec.ExitClass, len(rec.Errors))
		}
		f := rec.find(t, "unexcused", "litellm@1.93.2")
		a := onlyAdvisory(t, f)
		if a.ID != "GHSA-noal-0000-0001" || a.Source != "GitHub" || a.Ecosystem != "GitHub" || a.Repo != "BerriAI/litellm" || !reflect.DeepEqual(a.Fixed, []string{"1.94.0"}) {
			t.Errorf("advisory = %+v; want the BerriAI/litellm GitHub advisory fixed in 1.94.0", a)
		}
		if f.FixPublished == nil || !*f.FixPublished {
			t.Errorf("fix_published = %v, want true", f.FixPublished)
		}
	})

	t.Run("record unset variable writes no file", func(t *testing.T) {
		for name, run := range map[string]func(t *testing.T) (string, auditDB){
			"a finding": func(t *testing.T) (string, auditDB) {
				return auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister()), vulnerableDB(t)
			},
			"a clean tree": func(t *testing.T) (string, auditDB) { return baseTree(t), newAuditDB(t) },
		} {
			t.Run(name, func(t *testing.T) {
				tree, db := run(t)
				treeRoot := filepath.Dir(filepath.Dir(tree))
				runDirs := filepath.Dir(r.t.TempDir())
				beforeTree, beforeRuns := refWalk(t, treeRoot), refWalk(t, runDirs)
				r.run(tree, db, "2026-01-15")
				if after := refWalk(t, treeRoot); !reflect.DeepEqual(after, beforeTree) {
					t.Errorf("the audit wrote into its tree:\nbefore %v\nafter  %v", beforeTree, after)
				}
				if after := refWalk(t, runDirs); !reflect.DeepEqual(after, beforeRuns) {
					t.Errorf("the audit left a file in its HOME, TMPDIR or cwd:\nbefore %v\nafter  %v", beforeRuns, after)
				}
			})
		}
	})

	t.Run("record relative path is a setup error", func(t *testing.T) {
		tree := baseTree(t)
		db := newAuditDB(t)
		treeRoot := filepath.Dir(filepath.Dir(tree))
		before := refWalk(t, treeRoot)
		res := r.run(tree, db, "2026-01-15", "ADVISORY_AUDIT_RECORD=record.json")
		if res.exit != 2 {
			t.Errorf("exit = %d, want 2", res.exit)
		}
		hasLine(t, res.stderr, "ERROR: ADVISORY_AUDIT_RECORD=record.json: cannot write the finding record")
		if after := refWalk(t, treeRoot); !reflect.DeepEqual(after, before) {
			t.Errorf("a relative record path wrote into the tree:\nbefore %v\nafter  %v", before, after)
		}
		if calls := db.read("calls.log"); calls != "" {
			t.Errorf("the audit reached the network before refusing its record path:\n%s", calls)
		}
	})

	t.Run("record unwritable path exits 2", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("a regular file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		path := file + "/record.json" // under a regular file: unwritable for every uid, root included
		tree := auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister())
		res := r.run(tree, vulnerableDB(t), "2026-01-15", "ADVISORY_AUDIT_RECORD="+path)
		if res.exit != 2 {
			t.Errorf("exit = %d, want 2 (a setup error outranks the finding)", res.exit)
		}
		hasLine(t, res.stderr, "ERROR: ADVISORY_AUDIT_RECORD="+path+": cannot write the finding record")
		if !regexp.MustCompile(`(?m)^advisory audit: .*, \d+ unexcused, \d+ expired, \d+ node-behind, \d+ excused-fixable, \d+ errors \(as of 2026-01-15\)$`).MatchString(res.stdout) {
			t.Errorf("the summary line was not printed before the record write failed:\n%s", res.stdout)
		}
		hasLine(t, res.stdout, "UNEXCUSED GHSA-2fqr-mr3j-6wp8 [CVE-2026-54279 PYSEC-2026-2112] OSV PyPI aiohttp==3.12.15")
		t.Run("a directory is not a record path", func(t *testing.T) {
			dir := t.TempDir()
			res := r.run(tree, vulnerableDB(t), "2026-01-15", "ADVISORY_AUDIT_RECORD="+dir)
			if res.exit != 2 {
				t.Errorf("exit = %d, want 2", res.exit)
			}
			hasLine(t, res.stderr, "ERROR: ADVISORY_AUDIT_RECORD="+dir+": cannot write the finding record")
			if left := refWalk(t, dir); len(left) != 0 {
				t.Errorf("the writer left %v in the directory", left)
			}
		})
	})

	t.Run("record api github branch stays get only", func(t *testing.T) {
		fn := regexp.MustCompile(`(?ms)^http\(\) \{\n.*?^\}`).FindString(string(script))
		arm := regexp.MustCompile(`(?s)https://api\.github\.com/\*\)(.*?);;`).FindStringSubmatch(fn)
		if arm == nil {
			t.Fatalf("%s's http() has no https://api.github.com/*) arm", auditScriptRel)
		}
		for _, banned := range []string{"-X", "--request", "--data", "-d ", "-I", "--head"} {
			if strings.Contains(arm[1], banned) {
				t.Errorf("the api.github.com arm of http() passes %q; GitHub is only ever read", banned)
			}
		}
	})

	t.Run("excused fixable without fix_available fails", func(t *testing.T) {
		res, rec := litellmRecorded(t, "1.93.2", pinv, append(baseRegister(), pinvEntry), 1)
		hasLine(t, res.stderr, probePinv)
		hasLine(t, res.stdout, "excused   GHSA-pinv-0000-0001 [] GitHub BerriAI/litellm 1.93.2 (yes) (register: GHSA-pinv-0000-0001)")
		f := rec.find(t, "excused-fixable", "litellm@1.93.2")
		if want := []auditRecordEntry{{ID: "GHSA-pinv-0000-0001", Advisory: "GHSA-pinv-0000-0001", Fixed: []string{"1.94.0"}}}; !reflect.DeepEqual(f.Entries, want) {
			t.Errorf("entries = %+v, want %+v", f.Entries, want)
		}
		if c := rec.Counts; c.ExcusedFixable != 1 || c.Errors != 0 || c.Unexcused != 0 || rec.ExitClass != "finding" {
			t.Errorf("counts = %+v, class %q; want excused_fixable 1, errors 0, unexcused 0, finding", c, rec.ExitClass)
		}
		t.Run("two kept candidates", func(t *testing.T) {
			two := ghAdvisory("GHSA-pinv-0000-0001", "", ghVuln{"< 1.94.0", patched("1.94.0")}, ghVuln{">= 1.95.0, < 1.100.4", patched("1.100.4")})
			res, rec := litellmRecorded(t, "1.93.2", two, append(baseRegister(), pinvEntry), 1)
			hasLine(t, res.stderr, "ERROR: register entry GHSA-pinv-0000-0001 excuses litellm@1.93.2 although 1.94.0, 1.100.4 is published; set fix_available or move the pin")
			if got := rec.find(t, "excused-fixable", "litellm@1.93.2").Entries; len(got) != 1 || !reflect.DeepEqual(got[0].Fixed, []string{"1.94.0", "1.100.4"}) {
				t.Errorf("entries = %+v, want one entry fixed [1.94.0 1.100.4] (every kept candidate, lowest first)", got)
			}
		})
		t.Run("three kept candidates list lowest first", func(t *testing.T) {
			three := ghAdvisory("GHSA-pinv-0000-0001", "", ghVuln{">= 1.95.0, < 1.100.4", patched("1.100.4")}, ghVuln{"< 1.94.0", patched("1.94.0")}, ghVuln{">= 1.94.0, < 1.95.1", patched("1.95.1")})
			res, rec := litellmRecorded(t, "1.93.2", three, append(baseRegister(), pinvEntry), 1)
			hasLine(t, res.stderr, "ERROR: register entry GHSA-pinv-0000-0001 excuses litellm@1.93.2 although 1.94.0, 1.95.1, 1.100.4 is published; set fix_available or move the pin")
			if got := rec.find(t, "excused-fixable", "litellm@1.93.2").Entries; len(got) != 1 || !reflect.DeepEqual(got[0].Fixed, []string{"1.94.0", "1.95.1", "1.100.4"}) {
				t.Errorf("entries = %+v, want one entry fixed [1.94.0 1.95.1 1.100.4], lowest first", got)
			}
			_, rec = litellmRecorded(t, "1.93.2", three, baseRegister(), 1)
			if a := onlyAdvisory(t, rec.find(t, "unexcused", "litellm@1.93.2")); !reflect.DeepEqual(a.Fixed, []string{"1.94.0", "1.95.1", "1.100.4"}) {
				t.Errorf("unexcused fixed = %q, want [1.94.0 1.95.1 1.100.4], lowest first", a.Fixed)
			}
		})
	})

	t.Run("excused fixable acknowledged passes", func(t *testing.T) {
		res, rec := litellmRecorded(t, "1.93.2", pinv, append(baseRegister(), acknowledged(pinvEntry, "1.94.0")), 0)
		if strings.Contains(res.stderr, "although") {
			t.Errorf("an acknowledged fix still printed the probe line:\n%s", res.stderr)
		}
		if rec.ExitClass != "clean" || len(rec.Findings) != 0 {
			t.Errorf("record = class %q, findings %v; want clean, none", rec.ExitClass, rec.classKeys())
		}
		t.Run("a blank acknowledgement does not silence the probe", func(t *testing.T) {
			res, _ := litellmRecorded(t, "1.93.2", pinv, append(baseRegister(), acknowledged(pinvEntry, "")), 1)
			hasLine(t, res.stderr, probePinv)
		})
	})

	t.Run("excused fixable ignores a fix at or below the pin", func(t *testing.T) {
		for name, adv := range map[string]map[string]any{"a patched version below the pin": rw8w, "an unparseable patched version": tbd} {
			t.Run(name, func(t *testing.T) {
				id := adv["ghsa_id"].(string)
				res, rec := openobserveRecorded(t, adv, append(baseRegister(), auditException(id, nil, ooPin, "2026-01-01", "2026-01-31")), 0)
				if strings.Contains(res.stderr, "although") || rec.ExitClass != "clean" {
					t.Errorf("class %q; a candidate at or below the pin, or one that does not parse, is no published fix:\n%s", rec.ExitClass, res.stderr)
				}
			})
		}
		t.Run("a GIT range", func(t *testing.T) {
			db := newAuditDB(t)
			db.osv(auditControlLine, "PyPI\taiohttp\t3.12.15\tGHSA-gitr-0000-0001")
			db.vulnGitFixed("GHSA-gitr-0000-0001", "PyPI", "aiohttp", gitSHA)
			reg := append(baseRegister(), auditException("GHSA-gitr-0000-0001", nil, vulnerableAiohttp, "2026-01-01", "2026-01-31"))
			res, rec := r.recorded(t, auditTree(t, script, vulnerableIssueLock, baseGatewayLock, reg), db, "2026-01-15", 0)
			if strings.Contains(res.stderr, "although") || rec.ExitClass != "clean" {
				t.Errorf("class %q; a commit SHA from a GIT range is no published version:\n%s", rec.ExitClass, res.stderr)
			}
		})
	})

	t.Run("record unexcused fixed excludes a fix at or below the pin", func(t *testing.T) {
		_, rec := openobserveRecorded(t, rw8w, baseRegister(), 1)
		f := rec.find(t, "unexcused", ooKey)
		a := onlyAdvisory(t, f)
		if a.ID != "GHSA-rw8w-37p9-mrrp" || a.Source != "GitHub" || a.Ecosystem != "GitHub" || a.Repo != "openobserve/openobserve" {
			t.Errorf("advisory = %+v; want the openobserve/openobserve GitHub advisory", a)
		}
		wantNoFix(t, f, "v0.5.2")
		if !reflect.DeepEqual(f.PinnedIn, []auditPinLocation{{"GitHub", "openobserve/openobserve"}}) {
			t.Errorf("pinned_in = %+v, want the openobserve/openobserve vendor pin", f.PinnedIn)
		}
		t.Run("a patched version equal to the pin", func(t *testing.T) {
			eq := ghAdvisory("GHSA-eqpn-0000-0001", "", ghVuln{"All (before fix)", patched(ooPin.version)})
			_, rec := openobserveRecorded(t, eq, baseRegister(), 1)
			wantNoFix(t, rec.find(t, "unexcused", ooKey), ooPin.version)
		})
	})

	t.Run("record unexcused fixed excludes git range shas", func(t *testing.T) {
		gitRun := func(t *testing.T, ranges ...map[string]any) auditFinding {
			db := newAuditDB(t)
			db.osv(auditControlLine, "PyPI\taiohttp\t3.12.15\tGHSA-gitr-0000-0001")
			db.vulnAffected("GHSA-gitr-0000-0001", "PyPI", "aiohttp", nil, ranges...)
			_, rec := r.recorded(t, auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister()), db, "2026-01-15", 1)
			return rec.find(t, "unexcused", "aiohttp@3.12.15")
		}
		wantNoFix(t, gitRun(t, osvRange("GIT", gitSHA)), gitSHA)
		t.Run("beside an ECOSYSTEM range", func(t *testing.T) {
			f := gitRun(t, osvRange("GIT", gitSHA), osvRange("ECOSYSTEM", "3.13.0"))
			a := onlyAdvisory(t, f)
			if !reflect.DeepEqual(a.Fixed, []string{"3.13.0"}) || a.FixedRaw != gitSHA {
				t.Errorf("fixed %q, fixed_raw %q; want [3.13.0] and the SHA only in fixed_raw", a.Fixed, a.FixedRaw)
			}
			if f.FixPublished == nil || !*f.FixPublished {
				t.Errorf("fix_published = %v, want true", f.FixPublished)
			}
		})
	})

	t.Run("record unexcused fixed routes an unparseable candidate to fixed_raw", func(t *testing.T) {
		_, rec := openobserveRecorded(t, tbd, baseRegister(), 1)
		wantNoFix(t, rec.find(t, "unexcused", ooKey), "TBD (maintainers will fill once fixed)")
	})

	t.Run("record unexcused fix published is false on the no fix path", func(t *testing.T) {
		_, rec := r.recorded(t, auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister()), vulnerableDB(t), "2026-01-15", 1)
		wantNoFix(t, rec.find(t, "unexcused", "aiohttp@3.12.15"), "")
	})

	t.Run("record and probe agree on the fix", func(t *testing.T) {
		db := newAuditDB(t)
		db.osv(auditControlLine, "PyPI\taiohttp\t3.12.15\tGHSA-agre-0000-0001", "PyPI\taiohttp\t3.12.14\tGHSA-agre-0000-0001")
		db.vulnFixed("GHSA-agre-0000-0001", "PyPI", "aiohttp", "3.13.0")
		gateway := []auditPin{{"aiohttp", "3.12.14"}, {"litellm[proxy]", "1.93.2"}, {"pip", "26.2.2"}}
		reg := append(baseRegister(), auditException("GHSA-agre-0000-0001", nil, vulnerableAiohttp, "2026-01-01", "2026-01-31"))
		res, rec := r.recorded(t, auditTree(t, script, vulnerableIssueLock, gateway, reg), db, "2026-01-15", 1)
		hasLine(t, res.stderr, "ERROR: register entry GHSA-agre-0000-0001 excuses aiohttp@3.12.15 although 3.13.0 is published; set fix_available or move the pin")
		probe := rec.find(t, "excused-fixable", "aiohttp@3.12.15")
		unexcused := rec.find(t, "unexcused", "aiohttp@3.12.14")
		if len(probe.Entries) != 1 || !reflect.DeepEqual(probe.Entries[0].Fixed, []string{"3.13.0"}) || !reflect.DeepEqual(onlyAdvisory(t, unexcused).Fixed, []string{"3.13.0"}) {
			t.Errorf("excused-fixable entries %+v and unexcused advisories %+v must both name [3.13.0], read from the one fix table", probe.Entries, unexcused.Advisories)
		}
		if got := rec.classKeys(); len(got) != 2 {
			t.Errorf("findings = %v, want the excused-fixable and the unexcused one only (the covered key is not unexcused)", got)
		}

		dual := func(t *testing.T, reg []advisoryException, wantExit int) (refRunResult, auditRecord) {
			db := newAuditDB(t)
			db.osv(auditControlLine, "PyPI\tlitellm\t1.93.2\tGHSA-dual-0000-0001")
			db.vulnFixed("GHSA-dual-0000-0001", "PyPI", "litellm", "1.95.0")
			db.ghPages("BerriAI/litellm", []map[string]any{ghAdvisory("GHSA-dual-0000-0001", "", ghVuln{"< 1.95.0", patched("1.95.0")})})
			return r.recorded(t, auditTree(t, script, baseIssueLock, baseGatewayLock, reg), db, "2026-01-15", wantExit)
		}
		t.Run("one candidate from two sources is one row", func(t *testing.T) {
			_, rec := dual(t, baseRegister(), 1)
			f := rec.find(t, "unexcused", "litellm@1.93.2")
			a := onlyAdvisory(t, f)
			if a.Source != "OSV" || !reflect.DeepEqual(a.Fixed, []string{"1.95.0"}) {
				t.Errorf("advisory = %+v; want the first ($work/findings order: OSV) copy, fixed [1.95.0] once", a)
			}
			if rec.Counts.Unexcused != 2 {
				t.Errorf("counts.unexcused = %d, want 2 (one per printed row)", rec.Counts.Unexcused)
			}
			res, rec := dual(t, append(baseRegister(), auditException("GHSA-dual-0000-0001", nil, gatewayLitellm, "2026-01-01", "2026-01-31")), 1)
			if n := strings.Count(res.stderr, "ERROR: register entry GHSA-dual-0000-0001 excuses litellm@1.93.2 although 1.95.0 is published"); n != 1 || rec.Counts.ExcusedFixable != 1 {
				t.Errorf("the probe line printed %d times, counts.excused_fixable %d; want 1 and 1 for one advisory reported by two sources", n, rec.Counts.ExcusedFixable)
			}
		})
	})

	t.Run("record does not change the printed output", func(t *testing.T) {
		expiredReg := append(baseRegister(), auditException("AF-TEST-EXPIRED", nil, adNoPin, "2026-01-01", "2026-01-30"))
		for name, fixture := range map[string]func(t *testing.T) (string, auditDB, string){
			"clean": func(t *testing.T) (string, auditDB, string) { return baseTree(t), newAuditDB(t), "2026-01-15" },
			"unexcused": func(t *testing.T) (string, auditDB, string) {
				return auditTree(t, script, vulnerableIssueLock, baseGatewayLock, baseRegister()), vulnerableDB(t), "2026-01-15"
			},
			"expired": func(t *testing.T) (string, auditDB, string) {
				return auditTree(t, script, baseIssueLock, baseGatewayLock, expiredReg), newAuditDB(t), "2026-01-31"
			},
			"node behind": func(t *testing.T) (string, auditDB, string) {
				db := newAuditDB(t)
				db.nodeIndex(auditNodeRelease{"v96.52.0", true}, auditNodeRelease{"v96.50.1", true})
				return baseTree(t), db, "2026-01-15"
			},
			"infrastructure": func(t *testing.T) (string, auditDB, string) {
				db := newAuditDB(t)
				db.faults("querybatch=transport")
				return baseTree(t), db, "2026-01-15"
			},
			"excused fixable": func(t *testing.T) (string, auditDB, string) {
				db := newAuditDB(t)
				db.ghPages("BerriAI/litellm", []map[string]any{pinv})
				return auditTree(t, script, baseIssueLock, baseGatewayLock, append(baseRegister(), pinvEntry)), db, "2026-01-15"
			},
			"vendor finding": func(t *testing.T) (string, auditDB, string) {
				db := newAuditDB(t)
				db.ghPages("BerriAI/litellm", []map[string]any{ghAdvisory("GHSA-noal-0000-0001", "", ghVuln{"< 1.94.0", patched("1.94.0")})})
				return baseTree(t), db, "2026-01-15"
			},
		} {
			t.Run(name, func(t *testing.T) {
				tree, db, today := fixture(t)
				unset := r.run(tree, db, today)
				set := r.run(tree, db, today, "ADVISORY_AUDIT_RECORD="+filepath.Join(t.TempDir(), "record.json"))
				if set.exit != unset.exit || set.stdout != unset.stdout || set.stderr != unset.stderr {
					t.Errorf("setting ADVISORY_AUDIT_RECORD changed the run:\nexit %d vs %d\nstdout set:\n%s\nstdout unset:\n%s\nstderr set:\n%s\nstderr unset:\n%s",
						set.exit, unset.exit, set.stdout, unset.stdout, set.stderr, unset.stderr)
				}
			})
		}
	})
}

// auditShippedNode reads the shipped quickstart.sh's nodejs pins by pattern, so the shipped-tree run
// serves feeds consistent with whatever is pinned (D-61: no pin value is restated here). Either is
// empty while quickstart.sh does not declare it.
func auditShippedNode(t *testing.T) (major, aptVersion string) {
	t.Helper()
	qs := readQuickstart(t)
	if m := regexp.MustCompile(`(?m)^NODE_MAJOR="([0-9]+)"$`).FindStringSubmatch(qs); m != nil {
		major = m[1]
	}
	if m := regexp.MustCompile(`(?m)^NODE_APT_VERSION="([^"$]+)"$`).FindStringSubmatch(qs); m != nil {
		aptVersion = m[1]
	}
	return major, aptVersion
}

func listDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
