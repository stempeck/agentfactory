#!/bin/bash
# Advisory audit: reports every known advisory against what the bootstrap pins (both Python locks,
# the Go toolchain and the npm pins through OSV, the vendor-released components through their GitHub
# repository advisories) and exits non-zero unless each one is excused by
# .github/advisory-exceptions.json. It also fails when a newer nodejs release on the pinned major
# carries security fixes, and when a pinned release asset no longer answers.
# Bash 3.2 compatible, because macOS runs it under /bin/bash.
set -u

cd "$(dirname "$0")/.." || exit 2

today="${ADVISORY_AUDIT_TODAY:-$(date -u +%F)}"
register=.github/advisory-exceptions.json
locks="py/requirements.txt py/litellm-requirements.txt"
qs=quickstart.sh
npm_lock=internal/cmd/integrations/playwright/af/package-lock.json

work=$(mktemp -d "${TMPDIR:-/tmp}/advisory-audit.XXXXXX") || exit 2
trap 'rm -rf "$work"' EXIT

errors=0
err() {
  echo "ERROR: $*" >&2
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
cat > "$work/compare.awk" <<'AWK'
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
    | awk -v version="$version" -f "$work/compare.awk" \
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
      err "nodejs $node_pin is behind $newer, a newer v$node_major security release; move NODE_APT_VERSION"
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
    err "register entry $line has expired as of $today; re-review it and set a new recheck_by"
  done < "$work/expired"
else
  err "$register is missing or is not a JSON array"
  : > "$work/excused"
fi

unexcused=0
while IFS="$us" read -r id aliases against keys; do
  excused_by=""
  for key in $keys; do
    hit=""
    for name in $id $aliases; do
      if grep -qxF "$name$us$key" "$work/excused"; then
        hit=$name
        break
      fi
    done
    if [ -z "$hit" ]; then
      excused_by=""
      break
    fi
    [ -n "$excused_by" ] || excused_by=$hit
  done
  if [ -n "$excused_by" ]; then
    echo "excused   $id [$aliases] $against (register: $excused_by)"
  else
    echo "UNEXCUSED $id [$aliases] $against"
    unexcused=$((unexcused + 1))
  fi
done < "$work/findings"

echo "advisory audit: $(jq length "$work/queries.json") OSV pins, $(wc -l < "$work/vendors" | tr -d ' ') vendor repos, $(wc -l < "$work/findings" | tr -d ' ') reported, $unexcused unexcused, $errors errors (as of $today)"
[ "$errors" -eq 0 ] && [ "$unexcused" -eq 0 ]
