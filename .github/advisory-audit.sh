#!/bin/bash
# Advisory audit: reports every known advisory against what the bootstrap pins (both Python locks,
# the Go toolchain and the npm pins through OSV, the vendor-released components through their GitHub
# repository advisories) and exits non-zero unless each one is excused by
# .github/advisory-exceptions.json. It also fails when a newer nodejs release on the pinned major
# carries security fixes, and when a pinned release asset no longer answers.
# Bash 3.2 compatible, because macOS runs it under /bin/bash.
# With ADVISORY_AUDIT_RECORD=<absolute path> it also writes the finding record (schema v1) there, the
# one input the healing-issue filer reads. Exit codes: 0 clean, 1 finding, 2 setup, 3 infrastructure
# errors only.
set -u

cd "$(dirname "$0")/.." || exit 2

today="${ADVISORY_AUDIT_TODAY:-$(date -u +%F)}"
register=.github/advisory-exceptions.json
locks="py/requirements.txt py/litellm-requirements.txt"
qs=quickstart.sh
npm_lock=internal/cmd/integrations/playwright/af/package-lock.json
rec="${ADVISORY_AUDIT_RECORD:-}"
case "$rec" in
  ""|/*) ;;
  *) echo "ERROR: ADVISORY_AUDIT_RECORD=$rec: cannot write the finding record" >&2; exit 2 ;;
esac

work=$(mktemp -d "${TMPDIR:-/tmp}/advisory-audit.XXXXXX") || exit 2
trap 'rm -rf "$work"' EXIT

# Every optional file the record writer reads exists, so a run that stops early still writes a record.
for f in errors expired node_newer unexcused candidates fixes fixes_raw probes; do : > "$work/$f"; done
echo '[]' > "$work/register.json"

errors=0 expired=0 node_behind=0 excused_fixable=0
# The record splits errors on the record separator, because a message may carry remote text with newlines.
err() {
  echo "ERROR: $*" >&2
  printf '%s\036' "$*" >> "$work/errors"
  errors=$((errors + 1))
}

gh_auth=()
if [ -n "${GH_TOKEN:-}" ]; then
  gh_auth=(-H "Authorization: Bearer $GH_TOKEN")
fi

# http METHOD URL OUT [BODY_FILE]: the one place this script touches the network. Prints the HTTP
# status ("000" when the request never completed); response headers land in OUT.hdr.
http() {
  local method=$1 url=$2 out=$3 body=${4:-} code
  case "$url" in
    https://api.github.com/*)
      code=$(curl -sS --max-time 120 -o "$out" -D "$out.hdr" -w '%{http_code}' ${gh_auth[@]+"${gh_auth[@]}"} "$url") || code=000 ;;
    *)
      if [ "$method" = POST ]; then
        code=$(curl -sS --max-time 120 -X POST -H 'Content-Type: application/json' --data-binary "@$body" -o "$out" -D "$out.hdr" -w '%{http_code}' "$url") || code=000
      elif [ "$method" = HEAD ]; then
        # -I, never a HEAD method override: that makes curl wait for a body the server never sends.
        code=$(curl -sS --max-time 120 -I -o "$out" -D "$out.hdr" -w '%{http_code}' "$url") || code=000
      else
        code=$(curl -sS --max-time 120 -o "$out" -D "$out.hdr" -w '%{http_code}' "$url") || code=000
      fi ;;
  esac
  printf '%s' "$code"
}

# findings rows: id <US> space-separated aliases <US> what it was reported against <US> the
# package@version it was found at, which the register is keyed on. The separator is
# the ASCII unit separator, not a tab: tab is IFS whitespace, so `read` would collapse an empty
# aliases field and shift the next field into the alias list that the register is matched against.
us=$(printf '\037')
: > "$work/findings"

# ---- quickstart.sh's pins ----------------------------------------------------------------------

qs_value() { sed -n 's/^'"$1"'="\([^"$]*\)"$/\1/p' "$qs" 2>/dev/null | head -1; }

# pins rows: ecosystem name version, one OSV query each. vendors rows: repo version.
: > "$work/pins"
: > "$work/vendors"
[ -f "$qs" ] || err "$qs is missing, so the bootstrap's pins cannot be read"
node_apt=""
for name in $(sed -n 's/^\([A-Z][A-Z0-9_]*_VERSION\)=.*/\1/p' "$qs" 2>/dev/null | LC_ALL=C sort -u); do
  case "$name" in *_MIN_VERSION) continue ;; esac
  value=$(qs_value "$name")
  if [ -z "$value" ]; then
    err "$qs's $name is not a literal \"...\" value this audit can read"
    continue
  fi
  case "$name" in
    LITELLM_VERSION) ;; # audited from py/litellm-requirements.txt's litellm pin below
    GO_TOOLCHAIN_VERSION) printf 'Go stdlib %s\nGo toolchain %s\n' "${value#go}" "${value#go}" >> "$work/pins" ;;
    PLAYWRIGHT_VERSION) printf 'npm playwright %s\nnpm playwright-core %s\n' "$value" "$value" >> "$work/pins" ;;
    CODEX_VERSION) printf 'npm @openai/codex %s\n' "$value" >> "$work/pins" ;;
    CLAUDE_CODE_VERSION) printf 'npm @anthropic-ai/claude-code %s\n' "$value" >> "$work/pins" ;;
    GH_VERSION) printf 'cli/cli %s\n' "$value" >> "$work/vendors" ;;
    OPENOBSERVE_VERSION) printf 'openobserve/openobserve %s\n' "$value" >> "$work/vendors" ;;
    NODE_APT_VERSION) node_apt=$value ;;
    *) err "$qs declares $name, which this audit maps to no advisory feed; map it before shipping it" ;;
  esac
done

# ---- OSV: the Python locks, the Go toolchain and the npm pins ----------------------------------

sed -n 's/^\([A-Za-z0-9][A-Za-z0-9._-]*\)\(\[[^]]*\]\)\{0,1\}==\([^ ;\\]*\).*/PyPI \1 \3/p' $locks 2>/dev/null >> "$work/pins"
for lock in $locks; do
  grep -q '==' "$lock" 2>/dev/null || err "$lock is missing or pins nothing"
done
if jq -e '.packages | type == "object"' "$npm_lock" > /dev/null 2>&1; then
  jq -r '.packages | to_entries[] | select(.key != "") | "npm \(.key | sub(".*node_modules/"; "")) \(.value.version)"' "$npm_lock" >> "$work/pins"
else
  err "$npm_lock is missing or has no packages map"
fi
LC_ALL=C sort -u -o "$work/pins" "$work/pins"

jq -R -s -c 'split("\n") | map(select(length > 0) | split(" ") | {package: {ecosystem: .[0], name: .[1]}, version: .[2]})' \
  "$work/pins" > "$work/queries.json"
# OSV's querybatch accepts at most 1000 queries per request; 500 leaves headroom.
jq -c '[range(0; length; 500) as $i | .[$i:$i+500]] | .[] | {queries: .}' "$work/queries.json" > "$work/batches.jsonl"

: > "$work/results.jsonl"
n=0
while IFS= read -r batch; do
  n=$((n + 1))
  printf '%s' "$batch" > "$work/batch.$n"
  code=$(http POST https://api.osv.dev/v1/querybatch "$work/answer.$n" "$work/batch.$n")
  if [ "$code" = 200 ]; then
    jq -c '.results[]' "$work/answer.$n" >> "$work/results.jsonl" || err "OSV querybatch answer $n is not JSON"
  else
    err "OSV querybatch HTTP $code: $(jq -r '.message // empty' "$work/answer.$n" 2>/dev/null)"
  fi
done < "$work/batches.jsonl"

want=$(jq length "$work/queries.json")
got=$(wc -l < "$work/results.jsonl" | tr -d ' ')
[ "$got" -eq "$want" ] || err "OSV answered $got results for $want queries"
if grep -q next_page_token "$work/results.jsonl"; then
  err "OSV paged a result (next_page_token); this audit does not follow OSV paging"
fi

if [ "$got" -eq "$want" ]; then
  jq -n -r --slurpfile r "$work/results.jsonl" --slurpfile q "$work/queries.json" '
    range(0; $r | length) as $i | $r[$i].vulns[]?
    | $q[0][$i] as $p
    | [.id, ($p.package.ecosystem + " " + $p.package.name + (if $p.package.ecosystem == "PyPI" then "==" else "@" end) + $p.version), ($p.package.name + "@" + $p.version)]
    | @tsv' > "$work/osv_hits.tsv"
  cut -f1 "$work/osv_hits.tsv" | LC_ALL=C sort -u | while IFS= read -r id; do
    code=$(http GET "https://api.osv.dev/v1/vulns/$id" "$work/vuln.$id")
    if [ "$code" != 200 ]; then
      echo "FETCH FAILED $id (HTTP $code)"
      continue
    fi
    against=$(awk -F'\t' -v id="$id" '$1 == id {printf "%s%s", sep, $2; sep = ", "}' "$work/osv_hits.tsv")
    keys=$(awk -F'\t' -v id="$id" '$1 == id {printf "%s%s", sep, $3; sep = " "}' "$work/osv_hits.tsv")
    printf '%s\037%s\037OSV %s\037%s\n' "$id" "$(jq -r '(.aliases // []) | join(" ")' "$work/vuln.$id")" "$against" "$keys" >> "$work/findings"
  done > "$work/fetch_failures"
  while IFS= read -r line; do
    err "$line"
  done < "$work/fetch_failures"
fi

# An oracle that answers {} for everything is indistinguishable from a clean lock, so a package
# with long-standing advisories must come back with at least one.
printf '%s' '{"queries":[{"package":{"ecosystem":"PyPI","name":"pip"},"version":"24.0"}]}' > "$work/control.json"
code=$(http POST https://api.osv.dev/v1/querybatch "$work/control.answer" "$work/control.json")
if [ "$code" != 200 ] || [ "$(jq '[.results[0].vulns[]?] | length' "$work/control.answer" 2>/dev/null || echo 0)" -eq 0 ]; then
  err "oracle silent: OSV returned no advisory for the control query PyPI pip==24.0 (HTTP $code)"
fi

# ---- Vendor-released components via their GitHub repository advisories ------------------------

litellm_version=$(sed -n 's/^litellm\(\[[^]]*\]\)\{0,1\}==\([^ ;\\]*\).*/\2/p' py/litellm-requirements.txt 2>/dev/null | head -1)
[ -n "$litellm_version" ] || err "py/litellm-requirements.txt has no litellm== pin to compare BerriAI/litellm advisories against"

printf 'BerriAI/litellm %s\n' "$litellm_version" >> "$work/vendors"

# The comparator. A version is fixed when it equals a patched version, is at or above every
# patched version, or sits above a patched version on its own major.minor line (a backport).
# A range or patched version it cannot read fails closed, and so does an open-ended range whose
# only patched version is at or below its lower bound: such data cannot say what is fixed.
cat > "$work/vfuncs.awk" <<'AWK'
function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
function vparse(v, a,    s, rel, pre, parts, n, i) {
  s = trim(v); sub(/^[vV]/, "", s); sub(/-stable$/, "", s)
  if (!match(s, /^[0-9]+(\.[0-9]+)*/)) return 0
  rel = substr(s, 1, RLENGTH); pre = substr(s, RLENGTH + 1)
  n = split(rel, parts, ".")
  if (n > 4) return 0
  for (i = 1; i <= 4; i++) a[i] = (i <= n) ? parts[i] + 0 : 0
  sub(/^[-.]/, "", pre)
  if (pre == "") { a[5] = 3; a[6] = 0; return 1 }
  if (pre ~ /^(a|alpha)[.]?[0-9]*$/) a[5] = 0
  else if (pre ~ /^(b|beta)[.]?[0-9]*$/) a[5] = 1
  else if (pre ~ /^(rc|c)[.]?[0-9]*$/) a[5] = 2
  else return 0
  gsub(/[^0-9]/, "", pre); a[6] = pre + 0
  return 1
}
function vcmp(a, b,    i) {
  for (i = 1; i <= 6; i++) { if (a[i] < b[i]) return -1; if (a[i] > b[i]) return 1 }
  return 0
}
function copyv(src, dst,    i) { for (i = 1; i <= 6; i++) dst[i] = src[i] }
# status of version V against one (range, patched) pair: yes | no | unparseable
function status(range, patched,    cons, n, i, c, op, ver, X, r, upper, haveLow, LOW, items, m, P, M, haveMax) {
  range = trim(range); patched = trim(patched)
  if (range == "") return "unparseable"
  n = split(range, cons, ",")
  upper = 0; haveLow = 0
  for (i = 1; i <= n; i++) {
    c = trim(cons[i])
    if (match(c, /^(>=|<=|==|=|>|<)/)) { op = substr(c, 1, RLENGTH); ver = substr(c, RLENGTH + 1) } else { op = "=="; ver = c }
    if (!vparse(ver, X)) return "unparseable"
    r = vcmp(V, X)
    if (op == "<" || op == "<=" || op == "==" || op == "=") upper = 1
    if (op == ">=" || op == ">") { if (!haveLow || vcmp(X, LOW) > 0) { copyv(X, LOW); haveLow = 1 } }
    if ((op == ">=" && r < 0) || (op == ">" && r <= 0) || (op == "<=" && r > 0) || (op == "<" && r >= 0) || ((op == "==" || op == "=") && r != 0)) return "no"
  }
  if (patched == "") return "yes"
  m = split(patched, items, ",")
  haveMax = 0
  for (i = 1; i <= m; i++) {
    c = trim(items[i]); sub(/^>=[ ]*/, "", c)
    if (!vparse(c, P)) return "unparseable"
    if (!haveMax || vcmp(P, M) > 0) { copyv(P, M); haveMax = 1 }
    if (vcmp(V, P) == 0) return "no"
    if (V[1] == P[1] && V[2] == P[2] && vcmp(V, P) > 0) return "no"
  }
  if (!upper && haveLow && vcmp(M, LOW) <= 0) return "unparseable"
  if (vcmp(V, M) >= 0) return "no"
  return "yes"
}
AWK
cat > "$work/compare.awk" <<'AWK'
BEGIN { FS = "\t"; if (!vparse(version, V)) bad = 1 }
{
  if (bad) { s = "unparseable" } else if (NF < 4 || $3 == "") { s = "unparseable" } else { s = status($3, $4) }
  if (s == "yes" || (s == "unparseable" && st[$1] != "yes")) st[$1] = s
  else if (!($1 in st)) st[$1] = s
  al[$1] = $2
}
END { for (id in st) if (st[id] != "no") printf "%s\037%s\037%s\n", id, al[id], st[id] }
AWK

while read -r repo version; do
  [ -n "$version" ] || continue
  slug=$(printf '%s' "$repo" | tr '/' '_')
  url="https://api.github.com/repos/$repo/security-advisories?per_page=100"
  page=0
  : > "$work/advisories.$slug"
  while [ -n "$url" ]; do
    page=$((page + 1))
    if [ "$page" -gt 50 ]; then
      err "$repo advisories: more than 50 pages; refusing to continue"
      break
    fi
    out="$work/page.$slug.$page"
    code=$(http GET "$url" "$out")
    if [ "$code" != 200 ]; then
      err "$repo advisories page $page HTTP $code: $(jq -r '.message // empty' "$out" 2>/dev/null)"
      : > "$work/advisories.$slug.failed"
      break
    fi
    jq -c '.[]' "$out" >> "$work/advisories.$slug" || { err "$repo advisories page $page is not a JSON array"; break; }
    url=$(tr -d '\r' < "$out.hdr" | grep -i '^link:' | tr ',' '\n' | grep 'rel="next"' | sed -n 's/.*<\([^>]*\)>.*/\1/p' | head -1)
  done
  [ -e "$work/advisories.$slug.failed" ] && continue
  if [ ! -s "$work/advisories.$slug" ]; then
    err "oracle silent: GitHub returned no advisories for $repo"
    continue
  fi
  jq -r 'select(.withdrawn_at == null)
    | .ghsa_id as $id
    | ([.identifiers[]?.value, .cve_id] | map(select(. != null and . != "" and . != $id)) | unique | join(" ")) as $aliases
    | if ((.vulnerabilities // []) | length) == 0 then [$id, $aliases, "", ""]
      else (.vulnerabilities[] | [$id, $aliases, (.vulnerable_version_range // ""), (.patched_versions // "")]) end
    | @tsv' "$work/advisories.$slug" \
    | awk -v version="$version" -f "$work/vfuncs.awk" -f "$work/compare.awk" \
    | while IFS="$us" read -r id aliases st; do
        printf '%s\037%s\037GitHub %s %s (%s)\037%s@%s\n' "$id" "$aliases" "$repo" "$version" "$st" "${repo##*/}" "$version" >> "$work/findings"
      done
done < "$work/vendors"

# ---- nodejs: the release line the NodeSource pin sits on ----------------------------------------

node_major=$(qs_value NODE_MAJOR)
if [ -z "$node_major" ] || [ -z "$node_apt" ]; then
  err "$qs has no literal NODE_MAJOR and NODE_APT_VERSION to audit nodejs against"
else
  node_pin="v${node_apt%%-*}"
  [ "${node_pin%%.*}" = "v$node_major" ] || err "NODE_APT_VERSION $node_apt is not on NODE_MAJOR $node_major"
  # A release's security flag means it carries fixes, so only a NEWER same-major one is a finding.
  code=$(http GET https://nodejs.org/dist/index.json "$work/node_index")
  if [ "$code" != 200 ] || ! jq -e 'type == "array"' "$work/node_index" > /dev/null 2>&1; then
    err "nodejs.org release index HTTP $code"
  elif ! jq -e --arg pin "$node_pin" 'any(.[]; .version == $pin)' "$work/node_index" > /dev/null; then
    err "oracle silent: nodejs.org's release index does not list $node_pin"
  else
    jq -r --arg pin "$node_pin" --arg line "v$node_major." '
      def num: ltrimstr("v") | split(".") | map(tonumber? // 0);
      ($pin | num) as $p
      | .[] | select((.version | startswith($line)) and .security == true and ((.version | num) > $p)) | .version' \
      "$work/node_index" > "$work/node_newer"
    while IFS= read -r newer; do
      echo "ERROR: nodejs $node_pin is behind $newer, a newer v$node_major security release; move NODE_APT_VERSION" >&2
      node_behind=$((node_behind + 1))
    done < "$work/node_newer"
  fi

  code=$(http GET https://raw.githubusercontent.com/nodejs/Release/main/schedule.json "$work/node_schedule")
  node_end=$(jq -r --arg k "v$node_major" '.[$k].end // empty' "$work/node_schedule" 2>/dev/null)
  if [ "$code" != 200 ] || [ -z "$node_end" ]; then
    echo "WARNING: the nodejs Release schedule gives no end date for v$node_major (HTTP $code)" >&2
  else
    days=$(jq -n -r --arg end "$node_end" --arg today "$today" \
      '((($end + "T00:00:00Z") | fromdateiso8601) - (($today + "T00:00:00Z") | fromdateiso8601)) / 86400 | floor')
    if [ "$days" -le 90 ]; then
      echo "WARNING: nodejs v$node_major reaches end-of-life on $node_end, $days days after $today; plan the NODE_MAJOR move" >&2
    fi
  fi
fi

# ---- Pin availability: every pinned release asset still answers --------------------------------

asset_answers() {
  local code
  code=$(http HEAD "$1" "$work/head")
  case "$code" in
    2??|3??) ;;
    *) err "pinned asset $1 answered HTTP $code" ;;
  esac
}

gh_v=$(qs_value GH_VERSION)
go_v=$(qs_value GO_TOOLCHAIN_VERSION)
go_v=${go_v#go}
oo_v=$(qs_value OPENOBSERVE_VERSION)
codex_v=$(qs_value CODEX_VERSION)
claude_v=$(qs_value CLAUDE_CODE_VERSION)
[ -n "$codex_v" ] && asset_answers "https://registry.npmjs.org/@openai/codex/-/codex-$codex_v.tgz"
for arch in arm64 amd64; do
  [ -n "$gh_v" ] && asset_answers "https://github.com/cli/cli/releases/download/v$gh_v/gh_${gh_v}_linux_$arch.tar.gz"
  [ -n "$go_v" ] && asset_answers "https://go.dev/dl/go$go_v.linux-$arch.tar.gz"
  [ -n "$oo_v" ] && asset_answers "https://downloads.openobserve.ai/releases/openobserve/$oo_v/openobserve-$oo_v-linux-$arch.tar.gz"
  if [ -n "$node_major" ] && [ -n "$node_apt" ]; then
    index="https://deb.nodesource.com/node_$node_major.x/dists/nodistro/main/binary-$arch/Packages"
    code=$(http GET "$index" "$work/node_packages")
    if [ "$code" != 200 ]; then
      err "NodeSource index $index HTTP $code"
    elif ! grep -qxF "Version: $node_apt" "$work/node_packages"; then
      err "NodeSource index $index no longer lists nodejs $node_apt"
    fi
  fi
done
for platform in linux-arm64 linux-x64; do
  [ -n "$claude_v" ] && asset_answers "https://downloads.claude.ai/claude-code-releases/$claude_v/$platform/claude"
  [ -n "$codex_v" ] && asset_answers "https://registry.npmjs.org/@openai/codex/-/codex-$codex_v-$platform.tgz"
done

# ---- The exception register ---------------------------------------------------------------------
# An entry excuses its id and aliases only at the package@version it was reviewed for, so a pin move
# re-opens the finding until the entry is re-reviewed. A finding reported against several pins is
# excused only when every one of them is covered.

if jq -e 'type == "array"' "$register" > /dev/null 2>&1; then
  jq -r '.[] | (.package + "@" + .version) as $k | (.id, (.aliases // [])[]) | . + "\u001f" + $k' "$register" | LC_ALL=C sort -u > "$work/excused"
  jq -r --arg today "$today" '.[] | select(.recheck_by < $today) | "\(.id) (recheck_by \(.recheck_by))"' "$register" > "$work/expired"
  while IFS= read -r line; do
    echo "ERROR: register entry $line has expired as of $today; re-review it and set a new recheck_by" >&2
    expired=$((expired + 1))
  done < "$work/expired"
  cp "$register" "$work/register.json"
else
  err "$register is missing or is not a JSON array"
  : > "$work/excused"
fi

# ---- The fix table ------------------------------------------------------------------------------
# The one place a fix is derived: the excused-fixable probe and the record writer both read
# $work/fixes. A candidate is kept only when it parses and is strictly newer than the pin; every
# other one (unparseable, at or below the pin, a GIT-range commit, or any candidate of a pin that does
# not parse) goes to $work/fixes_raw for display. GitHub patched_versions are not filtered by
# package: the advisory's whole list binds to the vendor key, and the newer-than-pin filter narrows it.

while IFS="$us" read -r id aliases against keys; do
  case "$against" in
    "OSV "*)
      [ -f "$work/vuln.$id" ] || continue
      for key in $keys; do
        pin=$(awk -v k="$key" '$2 "@" $3 == k {print $1 " " $2; exit}' "$work/pins")
        eco=${pin%% *} pkg=${pin#* }
        jq -r --arg id "$id" --arg key "$key" --arg eco "$eco" --arg pkg "$pkg" '
          def norm: if $eco == "PyPI" then ascii_downcase | gsub("[-_.]+"; "-") else . end;
          (.affected // [])[] | select((.package.ecosystem // "") == $eco and ((.package.name // "") | norm) == ($pkg | norm))
          | (.ranges // [])[] | (if .type == "GIT" then "git" else "version" end) as $kind
          | (.events // [])[] | select(.fixed != null) | [$id, $key, $kind, .fixed] | join("\u001f")' "$work/vuln.$id" >> "$work/candidates"
      done ;;
    "GitHub "*)
      repo=${against#GitHub }
      slug=$(printf '%s' "${repo%% *}" | tr '/' '_')
      jq -r --arg id "$id" --arg key "$keys" '
        select(.ghsa_id == $id) | (.vulnerabilities // [])[] | (.patched_versions // "") | split(",")[]
        | sub("^[ \t]+"; "") | sub("[ \t]+$"; "") | sub("^>=[ ]*"; "") | select(length > 0)
        | [$id, $key, "version", .] | join("\u001f")' "$work/advisories.$slug" >> "$work/candidates" ;;
  esac
done < "$work/findings"

# Each kept row carries its vparse tuple as a fixed-width third field, so the sort orders an
# advisory's fixes lowest version first (1.94.0 before 1.100.4) and the cut drops it again.
cat > "$work/newer.awk" <<'AWK'
BEGIN { FS = "\037"; printf "" > fixes }
{
  pin = $2; sub(/.*@/, "", pin)
  if ($3 != "git" && vparse(pin, P) && vparse($4, C) && vcmp(C, P) > 0)
    printf "%s%s%s%s%012d%012d%012d%012d%d%012d%s%s\n", $1, FS, $2, FS, C[1], C[2], C[3], C[4], C[5], C[6], FS, $4 > fixes
  else print $1 FS $2 FS $4 > raw
}
AWK
awk -v fixes="$work/fixes.keyed" -v raw="$work/fixes_raw" -f "$work/vfuncs.awk" -f "$work/newer.awk" "$work/candidates"
LC_ALL=C sort -u "$work/fixes.keyed" | cut -d "$us" -f 1,2,4 > "$work/fixes"
LC_ALL=C sort -u -o "$work/fixes_raw" "$work/fixes_raw"

# probe ID KEY NAME: an entry that excuses KEY while a fix above the pin is published must say so with
# fix_available, or the exception outlives its reason. One line per (entry, key, advisory): an
# advisory both OSV and GitHub report is one finding, not two.
probe() {
  local id=$1 key=$2 name=$3 fixed entry ack
  fixed=$(awk -F"$us" -v id="$id" -v k="$key" '$1 == id && $2 == k {printf "%s%s", sep, $3; sep = ", "}' "$work/fixes")
  [ -n "$fixed" ] || return 0
  jq -r --arg n "$name" --arg k "$key" '.[] | select(.package + "@" + .version == $k and (.id == $n or any((.aliases // [])[]; . == $n)))
    | [.id, (.fix_available // "")] | join("\u001f")' "$work/register.json" > "$work/probe.entries"
  while IFS="$us" read -r entry ack; do
    [ -z "$ack" ] || continue
    grep -qxF "$entry$us$key$us$id" "$work/probes" && continue
    printf '%s\037%s\037%s\n' "$entry" "$key" "$id" >> "$work/probes"
    echo "ERROR: register entry $entry excuses $key although $fixed is published; set fix_available or move the pin" >&2
    excused_fixable=$((excused_fixable + 1))
  done < "$work/probe.entries"
}

unexcused=0
while IFS="$us" read -r id aliases against keys; do
  excused_by=""
  uncovered=0
  for key in $keys; do
    hit=""
    for name in $id $aliases; do
      if grep -qxF "$name$us$key" "$work/excused"; then
        hit=$name
        break
      fi
    done
    if [ -z "$hit" ]; then
      uncovered=1
      printf '%s\037%s\037%s\037%s\n' "$id" "$aliases" "$against" "$key" >> "$work/unexcused"
      continue
    fi
    [ -n "$excused_by" ] || excused_by=$hit
    probe "$id" "$key" "$hit"
  done
  if [ "$uncovered" -eq 0 ] && [ -n "$excused_by" ]; then
    echo "excused   $id [$aliases] $against (register: $excused_by)"
  else
    echo "UNEXCUSED $id [$aliases] $against"
    unexcused=$((unexcused + 1))
  fi
done < "$work/findings"

if [ "$unexcused" -gt 0 ] || [ "$expired" -gt 0 ] || [ "$node_behind" -gt 0 ] || [ "$excused_fixable" -gt 0 ]; then
  class=finding
elif [ "$errors" -gt 0 ]; then
  class=infrastructure
else
  class=clean
fi

echo "advisory audit: $(jq length "$work/queries.json") OSV pins, $(wc -l < "$work/vendors" | tr -d ' ') vendor repos, $(wc -l < "$work/findings" | tr -d ' ') reported, $unexcused unexcused, $expired expired, $node_behind node-behind, $excused_fixable excused-fixable, $errors errors (as of $today)"

# ---- The finding record ---------------------------------------------------------------------------
# Written after the summary so a record that cannot be written still leaves the run's output intact.
# The writer groups and joins the $work files; it never re-derives a class, a key or a fix. A key
# with an expired finding carries its probe rows as that finding's excused_fixable rather than as a
# second finding, so one key files one issue whose remedy names the published fix.

if [ -n "$rec" ]; then
  cat > "$work/record.jq" <<'JQ'
def rows($s): $s | split("\n") | map(select(length > 0) | split("\u001f"));
def lines($s): $s | split("\n") | map(select(length > 0));
def firsts(f): reduce .[] as $x ([]; if any(.[]; (. | f) == ($x | f)) then . else . + [$x] end);
def meets($a; $b): any($a[]; . as $x | any($b[]; . == $x));
def ingroups(f): to_entries | group_by(.value | f) | map(sort_by(.key) | map(.value));
(lines($pins) | map(split(" "))) as $P
| (lines($vendors) | map(split(" "))) as $V
| rows($fixes) as $F
| rows($fixes_raw) as $R
| rows($findings) as $FR
| $reg[0] as $G
| def pinned_in($k): ([$P[] | select(.[1] + "@" + .[2] == $k) | {ecosystem: .[0], repo: ""}]
      + [$V[] | select((.[0] | split("/") | last) + "@" + .[1] == $k) | {ecosystem: "GitHub", repo: .[0]}]) | firsts(.);
  def located($class; $k): {class: $class, key: $k, pinned: (pinned_in($k) | length > 0), pinned_in: pinned_in($k)};
  def fixed($id; $k): [$F[] | select(.[0] == $id and .[1] == $k) | .[2]];
  def names: [.id] + (.aliases // []);
  def splitnames($s): $s | split(" ") | map(select(length > 0));
  (rows($unexcused) | ingroups(.[3]) | map(.[0][3] as $k
    | (map(. as $r | ($r[2] | split(" ")) as $w | {
        id: $r[0], aliases: splitnames($r[1]), source: $w[0],
        ecosystem: (if $w[0] == "GitHub" then "GitHub" else ([$P[] | select(.[1] + "@" + .[2] == $k) | .[0]] | first // "") end),
        repo: (if $w[0] == "GitHub" then $w[1] else "" end),
        against: $r[2], fixed: fixed($r[0]; $k),
        fixed_raw: ([$R[] | select(.[0] == $r[0] and .[1] == $k) | .[2]] | join(", "))}) | firsts(.id)) as $adv
    | ($adv | map(.id, .aliases[])) as $an
    | located("unexcused"; $k) + {
        fix_published: any($adv[]; .fixed | length > 0),
        advisories: $adv,
        register: ([$G[] | (.package + "@" + .version) as $ek | select($ek != $k and meets(names; $an))
          | {id, aliases: (.aliases // []), reviewed, recheck_by, key: $ek}] | unique_by([.key, .id]))})) as $unexcusedF
  | (rows($probes) | ingroups(.[1]) | map(.[0][1] as $k
    | {key: $k, entries: (map({id: .[0], advisory: .[2], fixed: fixed(.[2]; $k)}) | sort_by(.id, .advisory))})) as $probed
  | def probed($k): [$probed[] | select(.key == $k) | .entries[]];
  ([$G[] | select(.recheck_by < $today)] | ingroups(.package + "@" + .version) | map((.[0].package + "@" + .[0].version) as $k
    | located("expired"; $k) + {entries: (sort_by(.id) | map(names as $en | {
        id, aliases: (.aliases // []), reviewed, recheck_by,
        excuses: ([$FR[] | select(meets([.[0]] + splitnames(.[1]); $en) and (splitnames(.[3]) | index([$k]) != null)) | .[0]] | unique),
        fix_available: (.fix_available // "")})),
      excused_fixable: probed($k)})) as $expiredF
  | (lines($node_newer) as $nn | if ($nn | length) > 0
      then [{class: "node-behind", key: ("nodejs@" + $node_pin), pinned: true, pinned_in: [{ecosystem: "apt", repo: ""}], releases: $nn}]
      else [] end) as $nodeF
  | ($expiredF | map(.key)) as $expiredKeys
  | [$probed[] | .key as $k | select(any($expiredKeys[]; . == $k) | not) | located("excused-fixable"; $k) + {entries}] as $fixableF
  | {schema: 1, today: $today, exit_class: $class, counts: $counts,
     findings: ($unexcusedF + $expiredF + $nodeF + $fixableF | sort_by(.class, .key)),
     errors: ($errs | split("\u001e") | map(select(length > 0)))}
JQ
  if [ -d "$rec" ] || ! : 2>/dev/null > "$rec.tmp"; then
    echo "ERROR: ADVISORY_AUDIT_RECORD=$rec: cannot write the finding record" >&2
    exit 2
  fi
  counts=$(jq -n -c --argjson r "$(wc -l < "$work/findings" | tr -d ' ')" --argjson u "$unexcused" --argjson e "$expired" \
    --argjson n "$node_behind" --argjson f "$excused_fixable" --argjson x "$errors" \
    '{reported: $r, unexcused: $u, expired: $e, node_behind: $n, excused_fixable: $f, errors: $x}')
  if ! jq -n -f "$work/record.jq" --arg today "$today" --arg class "$class" --argjson counts "$counts" --arg node_pin "${node_pin:-}" \
      --rawfile pins "$work/pins" --rawfile vendors "$work/vendors" --rawfile findings "$work/findings" \
      --rawfile unexcused "$work/unexcused" --rawfile fixes "$work/fixes" --rawfile fixes_raw "$work/fixes_raw" \
      --rawfile probes "$work/probes" --rawfile node_newer "$work/node_newer" --rawfile errs "$work/errors" \
      --slurpfile reg "$work/register.json" > "$rec.tmp" || ! mv "$rec.tmp" "$rec"; then
    rm -f "$rec.tmp"
    echo "ERROR: ADVISORY_AUDIT_RECORD=$rec: cannot write the finding record" >&2
    exit 2
  fi
fi

case $class in
  finding) exit 1 ;;
  infrastructure) exit 3 ;;
esac
exit 0
