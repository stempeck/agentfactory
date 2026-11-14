#!/bin/bash
# Healing-issue filer: turns the finding record the advisory audit writes at ADVISORY_AUDIT_RECORD
# (schema v1) into one locked issue per pin a pull request changes, labelled from ADVISORY_ISSUE_LABELS,
# and files nothing a rerun already filed: it searches the open issues carrying ADVISORY_DEDUPE_LABEL,
# which must be one of those labels. It reads the record and nothing else, so the audit stays the one
# owner of class, key and fix; the filer only groups findings that come from one pin.
# ADVISORY_ISSUE_DRY_RUN=1 prints the would-be issues without any gh call.
# Bash 3.2 compatible, because macOS runs it under /bin/bash.
set -u

# Every exit appends one line to the job summary, so a run that stopped before filing reads differently
# on the run page from one that had nothing to file.
filed=0 already_open=0 failed=0 would_file=0 finished=0 work=""
dry_run=0
[ "${ADVISORY_ISSUE_DRY_RUN:-}" = 1 ] && dry_run=1
summarize() {
  local status=$? line
  [ -z "$work" ] || rm -rf "$work"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    if [ "$dry_run" -eq 1 ]; then
      line="advisory issues (dry run): $would_file would be filed, none filed"
    else
      line="advisory issues: $filed filed, $already_open already open, $failed failed"
    fi
    if [ "$status" -ne 0 ] && [ "$finished" -eq 0 ]; then
      line="$line; stopped early (exit $status), see the step log"
    fi
    printf '%s\n' "$line" >> "$GITHUB_STEP_SUMMARY" ||
      echo "WARNING: advisory issue: cannot append to the job summary at $GITHUB_STEP_SUMMARY" >&2
  fi
  exit "$status"
}
trap summarize EXIT

record="${ADVISORY_AUDIT_RECORD:-}"
if [ -z "$record" ]; then
  echo "ERROR: ADVISORY_AUDIT_RECORD is not set, so there is no finding record to file from" >&2
  exit 1
fi
if [ ! -f "$record" ]; then
  echo "ERROR: no finding record at $record; the audit exited before the register stage (setup failure) — nothing to file" >&2
  exit 1
fi

repo="${GITHUB_REPOSITORY:-}"
server="${GITHUB_SERVER_URL:-}"
run_id="${GITHUB_RUN_ID:-}"
repo_re='^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$'
server_re='^https?://[A-Za-z0-9.-]+(:[0-9]+)?$'
digits_re='^[0-9]+$'
if ! [[ $repo =~ $repo_re ]]; then
  echo "ERROR: GITHUB_REPOSITORY=\"$repo\" is not an owner/name repository" >&2
  exit 1
fi
if ! [[ $server =~ $server_re ]]; then
  echo "ERROR: GITHUB_SERVER_URL=\"$server\" is not an http(s) server URL" >&2
  exit 1
fi
if ! [[ $run_id =~ $digits_re ]]; then
  echo "ERROR: GITHUB_RUN_ID=\"$run_id\" is not a run number" >&2
  exit 1
fi

work=$(mktemp -d "${TMPDIR:-/tmp}/advisory-issue.XXXXXX") || exit 1

# ---- Validation: every field that is rendered, before anything is rendered ----------------------
# The record crosses a trust boundary (advisory feeds fill it), so only values that pass a closed
# grammar reach an issue body or a gh argv. The anchors are \A and \z because jq's ^ and $ also
# match at a newline inside the value.

cat > "$work/check.jq" <<'JQ'
def id_re: "\\A[A-Za-z0-9][A-Za-z0-9._:-]{0,99}\\z";
def version_re: "\\A[A-Za-z0-9][A-Za-z0-9._@+=-]{0,99}\\z";
def key_re: "\\A(@[A-Za-z0-9._-]+/)?[A-Za-z0-9][A-Za-z0-9._@+=-]{0,99}\\z";
def date_re: "\\A[0-9]{4}-[0-9]{2}-[0-9]{2}\\z";
def repo_re: "\\A[A-Za-z0-9._-]+/[A-Za-z0-9._-]+\\z";
. as $r
| def at($p): $r | try getpath($p) catch null;
  def name($p): $p | map(if type == "number" then "[\(.)]" else ".\(.)" end) | join("") | ltrimstr(".");
  def bad($p; $why): "\(name($p)) \($why)";
  def grammar($p; $re; $what): at($p) as $v
    | if ($v | type) == "string" and ($v | test($re)) then empty else bad($p; "is not \($what)") end;
  def optional($p; $re; $what): if at($p) == "" then empty else grammar($p; $re; $what) end;
  def text($p): if (at($p) | type) == "string" then empty else bad($p; "is not a string") end;
  def flag($p): if (at($p) | type) == "boolean" then empty else bad($p; "is not true or false") end;
  def list($p; $min; $max): at($p) as $v
    | if ($v | type) != "array" then bad($p; "is not an array")
      elif ($v | length) > $max then bad($p; "has more than \($max) elements")
      elif ($v | length) < $min then bad($p; "is empty")
      else empty end;
  def each($p): range(0; at($p) | if type == "array" then length else 0 end) as $i | $p + [$i];
  def id($p): grammar($p; id_re; "an advisory id");
  def ids($p): list($p; 0; 50), (each($p) as $q | id($q));
  def versions($p; $min; $max): list($p; $min; $max), (each($p) as $q | grammar($q; version_re; "a version"));
  def key($p): grammar($p; key_re; "a package@version key");
  def dates($p): grammar($p + ["reviewed"]; date_re; "a YYYY-MM-DD date"), grammar($p + ["recheck_by"]; date_re; "a YYYY-MM-DD date");
  def location($p): grammar($p + ["ecosystem"]; "\\A(PyPI|npm|Go|GitHub|apt)\\z"; "PyPI, npm, Go, GitHub or apt"),
    (if at($p + ["ecosystem"]) == "GitHub" then grammar($p + ["repo"]; repo_re; "an owner/name repository") else empty end);
  def fixable($p): list($p; 0; 50), (each($p) as $q | id($q + ["id"]), id($q + ["advisory"]), versions($q + ["fixed"]; 1; 20));
  def advisory($p): id($p + ["id"]), ids($p + ["aliases"]), grammar($p + ["source"]; "\\A(OSV|GitHub)\\z"; "OSV or GitHub"),
    location($p), versions($p + ["fixed"]; 0; 20), text($p + ["fixed_raw"]);
  def finding($p):
    key($p + ["key"]), flag($p + ["pinned"]),
    list($p + ["pinned_in"]; 0; 50), (each($p + ["pinned_in"]) as $q | location($q)),
    (at($p + ["class"]) as $class
     | if $class == "unexcused" then
         flag($p + ["fix_published"]),
         list($p + ["advisories"]; 1; 50), (each($p + ["advisories"]) as $q | advisory($q)),
         list($p + ["register"]; 0; 50), (each($p + ["register"]) as $q | id($q + ["id"]), ids($q + ["aliases"]), dates($q), key($q + ["key"]))
       elif $class == "expired" then
         list($p + ["entries"]; 1; 50),
         (each($p + ["entries"]) as $q | id($q + ["id"]), ids($q + ["aliases"]), dates($q), ids($q + ["excuses"]),
           optional($q + ["fix_available"]; version_re; "a version")),
         fixable($p + ["excused_fixable"])
       elif $class == "excused-fixable" then
         list($p + ["entries"]; 1; 50), fixable($p + ["entries"])
       elif $class == "node-behind" then
         versions($p + ["releases"]; 1; 50)
       else bad($p + ["class"]; "has no issue template (want unexcused, expired, excused-fixable or node-behind)") end);
  (if at(["schema"]) == 1 then empty else bad(["schema"]; "is not 1") end),
  grammar(["today"]; date_re; "a YYYY-MM-DD date"),
  grammar(["exit_class"]; "\\A(finding|infrastructure|clean)\\z"; "finding, infrastructure or clean"),
  (if (at(["errors"]) | type) == "array" then empty else bad(["errors"]; "is not an array") end),
  list(["findings"]; 0; 50),
  (at(["exit_class"]) as $class | (at(["findings"]) | if type == "array" then length else 0 end) as $n
   | if $class == "finding" and $n == 0 then bad(["findings"]; "is empty although exit_class is finding")
     elif ($class == "infrastructure" or $class == "clean") and $n > 0 then bad(["findings"]; "is not empty although exit_class is \($class)")
     else empty end),
  (each(["findings"]) as $p | finding($p))
JQ

if ! jq -e -s 'length == 1 and (.[0] | type) == "object"' "$record" > /dev/null 2>&1; then
  echo "ERROR: the finding record at $record is not one JSON object" >&2
  exit 1
fi
if ! problems=$(jq -r -f "$work/check.jq" "$record"); then
  echo "ERROR: the finding record at $record could not be validated" >&2
  exit 1
fi
if [ -n "$problems" ]; then
  printf '%s\n' "$problems" | sed 's/^/ERROR: finding record field /' >&2
  exit 1
fi

case "$(jq -r .exit_class "$record")" in
  infrastructure)
    echo "advisory issue: infrastructure errors only ($(jq '.errors | length' "$record")); no finding, no issue"
    exit 0 ;;
  clean)
    echo "advisory issue: no finding; no issue"
    exit 0 ;;
esac

# ---- The issue bodies -----------------------------------------------------------------------------
# "What fixes it" reads only the class's named source field and never compares versions: the audit's
# fix rule already decided what counts as a fix. litellm is the one policy constant here, because
# ADR-024 / D21 holds its pin even when a fix is published.

cat > "$work/body.jq" <<'JQ'
def code: "`\(.)`";
def codes: map(code) | join(", ");
def aka: if length > 0 then " (aliases \(codes))" else "" end;
def link: if startswith("GHSA-") then "https://github.com/advisories/\(.)" else "https://osv.dev/vulnerability/\(.)" end;
def once: reduce .[] as $x ([]; if any(.[]; . == $x) then . else . + [$x] end);
def versions_ok: split(", ") | all(test("\\A[A-Za-z0-9][A-Za-z0-9._@+=-]{0,99}\\z"));
def rows: "their rows in `internal/cmd/quickstart_pins_test.go`";
def where:
  if .ecosystem == "PyPI" then "- PyPI: the hash locks `py/requirements.txt` and `py/litellm-requirements.txt`; raise the floor in the `py/*.in` input, then run `make lock`, which also rewrites `py/lock-wheels.txt`"
  elif .ecosystem == "npm" then "- npm: a `*_VERSION` constant in `quickstart.sh` with its checksum constants and \(rows), or `internal/cmd/integrations/playwright/af/package-lock.json`"
  elif .ecosystem == "Go" then "- Go: `GO_TOOLCHAIN_VERSION`, `GO_SHA256_ARM64` and `GO_SHA256_AMD64` in `quickstart.sh`; the `toolchain` line of `go.mod` and of `web/go.mod`; the four `go-version` lanes of `.github/workflows/test.yml`; and \(rows)"
  elif .ecosystem == "apt" then "- apt: `NODE_APT_VERSION` in `quickstart.sh` (the NodeSource `nodejs` package) and its row in `internal/cmd/quickstart_pins_test.go`"
  else "- GitHub: the release of \(.repo | code), pinned by its `*_VERSION` constant in `quickstart.sh` with its `*_SHA256_*` constants and \(rows)" end;
def mirrors: "Files outside `quickstart.sh` that restate a pin are listed in `internal/cmd/keeptrue_pin_mirrors_test.go` and `internal/cmd/keeptrue_toolchain_parity_test.go`; `make test` fails until each agrees with the pin.";
def pin_move($key; $fixes):
  "Move the pin \($key | code) to a fixed version: \($fixes | join(", ")). An exception entry is not an acceptable resolution when a fixed version is published — the audit's `excused-fixable` probe fails it.";
def held($fixes):
  "`litellm` stays pinned under ADR-024 / operator decision D21 (its pin moves only on a session-shaped probe), so this finding takes the exception path although \($fixes | join(", ")) is published.";
def terms($key): "for exactly \($key | code), with a reason, evidence citing this issue, `reviewed` = today and `recheck_by` at most 30 days later";
def acceptance: "Start the pull request body, on the line after its `Resolves` line, with `Operator acceptance required:` — the operator's merge is the acceptance.";
def no_fix: "no fixed version above the pin is published";
def fixable_lines: "These register entries excuse this key although a fix above the pin is published:",
  (.[] | "- entry \(.id | code) excuses \(.advisory | code), published fix: \(.fixed | join(", ")): \(.advisory | link)");

def finding:
  "Class \(.class | code), key \(.key | code), pinned: \(if .pinned then "yes" else "no" end).",
  "",
  (if .class == "unexcused" then
     "No register entry excuses these advisories at this key:",
     (.advisories[] | "- \(.id | code)\(.aliases | aka), reported by "
       + (if .ecosystem == "GitHub" then "the repository advisories of \(.repo | code)" else "\(.source) (\(.ecosystem))" end)
       + (if (.fixed | length) > 0 then ", fixed above the pin in \(.fixed | join(", "))" else "" end)
       + ": \(.id | link)"),
     (if (.register | length) > 0 then
        "",
        "Register entries name these advisories at another version; an entry reviewed for an earlier pin does not carry over:",
        (.register[] | "- \(.id | code)\(.aliases | aka) at \(.key | code), reviewed \(.reviewed), recheck_by \(.recheck_by)")
      else empty end)
   elif .class == "expired" then
     "These register entries for \(.key | code) are past their recheck_by date:",
     (.entries[] | "- \(.id | code)\(.aliases | aka), reviewed \(.reviewed), recheck_by \(.recheck_by), "
       + (if (.excuses | length) > 0 then "excuses \(.excuses | codes)" else "excuses no advisory reported in this run" end)
       + (if .fix_available != "" then ", fix_available \(.fix_available)" else "" end)),
     (if (.excused_fixable | length) > 0 then "", (.excused_fixable | fixable_lines) else empty end)
   elif .class == "excused-fixable" then
     (.entries | fixable_lines)
   else
     "Newer nodejs releases on the pinned major carry security fixes: \(.releases | join(", "))."
   end);

def remedy:
  .key as $key
  | (($key | sub("@[^@]*$"; "")) == "litellm") as $litellm
  | if .class == "unexcused" then
      ([.advisories[].fixed[]] | once) as $fixes
      | if .fix_published and ($litellm | not) then pin_move($key; $fixes)
        elif .fix_published then
          held($fixes),
          "Add an exception entry in `.github/advisory-exceptions.json` \(terms($key)), and with `fix_available` set to the lowest published fix to acknowledge it.",
          acceptance
        else
          (.advisories[] | "- \(.id | code): \(no_fix)"
            + (if .fixed_raw != "" and (.fixed_raw | versions_ok) then " (discarded candidates: \(.fixed_raw))" else "; see \(.id | link)" end)),
          "",
          "Add an exception entry in `.github/advisory-exceptions.json` \(terms($key)).",
          acceptance
        end
    elif .class == "expired" then
      ([.entries[].fix_available | select(. != "")] + [.excused_fixable[].fixed[]] | once) as $fixes
      | if ($fixes | length) > 0 and ($litellm | not) then
          pin_move($key; $fixes),
          "Then delete the register entries for \($key | code): they excuse nothing once the pin has moved."
        elif ($fixes | length) > 0 then
          held($fixes),
          "Renew each expired entry in `.github/advisory-exceptions.json` \(terms($key)), keeping any `fix_available` it has as it is.",
          (if (.excused_fixable | length) > 0
           then "Add `fix_available` to each entry listed as excusing a published fix, set to the lowest published fix it names."
           else empty end),
          acceptance
        else
          "No entry names a `fix_available`, and no advisory these entries excuse has a published fix above the pin.",
          "Renew each expired entry in `.github/advisory-exceptions.json` \(terms($key)) once a fresh review confirms that.",
          acceptance
        end
    elif .class == "excused-fixable" then
      ([.entries[].fixed[]] | once) as $fixes
      | if $litellm then
          held($fixes),
          "Add `fix_available` to each entry listed above, set to the lowest published fix it names, and change nothing else in it.",
          acceptance
        else
          pin_move($key; $fixes),
          "Then delete the entries that excuse \($key | code): they excuse nothing once the pin has moved."
        end
    else pin_move($key; .releases)
    end;

. as $r
| [$members[] | $r.findings[.]] as $g
| [$r.findings | to_entries[] | select(.key as $k | any($members[]; . == $k) | not) | .value | "- \(.key | code) (\(.class))"] as $others
| ([$g[].pinned_in[]] | once) as $locs
| [
    "## Finding", "",
    (if ($g | length) > 1 then "These findings come from one pin, so one pull request moves it.", "" else empty end),
    ($g | to_entries[] | (if .key > 0 then "" else empty end), (.value | finding)), "",
    "## Where it is pinned", "",
    (if ($locs | length) == 0 then "No audited pin carries this key." else ($locs[] | where), "", mirrors end), "",
    "## What fixes it", "",
    (if ($g | length) == 1 then ($g[0] | remedy)
     else "One version must fix every finding above.", ($g[] | "", "For \("\(.class) \(.key)" | code):", remedy) end), "",
    "## Other findings in this run", "",
    (if ($others | length) == 0 then "None."
     else $others[], "", "Each finding above belongs to another pin and has its own issue. Do not move those pins on purpose; if moving this pin forces one of them to move, keep that move and list it in the pull request. The `advisory-decay` check on your pull request stays red while any of them is open, and the dispatcher does not wait for it." end), "",
    "## Definition of done", "",
    "- The audit run on this pull request's branch with `ADVISORY_AUDIT_RECORD` set reports no finding with key "
      + ([$g[] | ("\(.class) \(.key)" | code), (if .class == "expired" and (.excused_fixable | length) > 0 then ("excused-fixable \(.key)" | code) else empty end)] | join(" or "))
      + ": run `ADVISORY_AUDIT_RECORD=$(mktemp) .github/advisory-audit.sh` and read the record's `findings`. `make test` cannot see this finding: its audit runs against stubs with a pinned clock.",
    "- `make test` passes on this pull request's branch.",
    "- The pull request body's first line begins with \("Resolves \($repo)#N" | code), N being this issue's number.",
    "- The pull request lists every pin that moved.",
    "- One pull request per issue.", "",
    "## Run", "",
    "\($run) (as of \($r.today))" + (if ($r.errors | length) > 0 then "; infrastructure errors in the run: \($r.errors | length)" else "" end), "",
    ($g[] | "<!-- advisory-decay-key: \(.class) \(.key) -->"),
    "<!-- advisory-decay-pin: \($pin) -->"
  ]
| join("\n")
JQ

# ---- One issue per pin ------------------------------------------------------------------------------
# A pin is what one pull request moves. Two lines of the audit query one quickstart.sh constant as two
# advisory-database packages (GO_TOOLCHAIN_VERSION as Go stdlib and toolchain, PLAYWRIGHT_VERSION as npm
# playwright and playwright-core), so those siblings at one version are one pin; any other finding's
# pin is its key, which also puts every class of one key in one issue. Groups keep the record's order.

cat > "$work/groups.jq" <<'JQ'
def sibling_pins: [{ecosystem: "Go", names: ["stdlib", "toolchain"], pin: "go"}, {ecosystem: "npm", names: ["playwright", "playwright-core"], pin: "playwright"}];
def pin_of:
  . as $f
  | ($f.key | sub("@[^@]*$"; "")) as $name
  | ($f.key | sub("^.*@"; "")) as $version
  | first((sibling_pins[]
      | select(any(.names[]; . == $name))
      | select(.ecosystem as $e | ($f.pinned_in | length) > 0 and all($f.pinned_in[]; .ecosystem == $e))
      | "\(.pin)@\($version)"), $f.key);
reduce (.findings | to_entries[]) as $e ([];
  ($e.value | pin_of) as $p
  | if any(.[]; .pin == $p) then map(if .pin == $p then .members += [$e.key] else . end)
    else . + [{pin: $p, members: [$e.key]}] end)
JQ

if ! jq -c -f "$work/groups.jq" "$record" > "$work/groups.json"; then
  echo "ERROR: cannot group the record's findings by pin" >&2
  exit 1
fi
run_url="$server/$repo/actions/runs/$run_id"
ngroups=$(jq length "$work/groups.json")
g=0
while [ "$g" -lt "$ngroups" ]; do
  members=$(jq -c --argjson g "$g" '.[$g].members' "$work/groups.json")
  pin=$(jq -r --argjson g "$g" '.[$g].pin' "$work/groups.json")
  if ! jq -r --argjson members "$members" --arg pin "$pin" --arg repo "$repo" --arg run "$run_url" -f "$work/body.jq" "$record" > "$work/body.$g" \
      || ! jq -c --argjson members "$members" '[$members[] as $i | .findings[$i] | "\(.class) \(.key)"]' "$record" > "$work/keys.$g"; then
    echo "ERROR: cannot render the issue body for the pin $pin" >&2
    exit 1
  fi
  printf '%s' "$pin" > "$work/pin.$g"
  g=$((g + 1))
done

title_of() { printf 'advisory-decay: %s' "$(jq -r 'join(", ")' "$work/keys.$1")"; }

if [ "$dry_run" -eq 1 ]; then
  g=0
  while [ "$g" -lt "$ngroups" ]; do
    echo "advisory issue (dry run): $(title_of "$g")"
    echo
    cat "$work/body.$g"
    echo
    g=$((g + 1))
    would_file=$g
  done
  exit 0
fi

# ---- Filing -----------------------------------------------------------------------------------------
# Every gh read is checked before its answer is used: a failed issue list would match no marker and
# file a duplicate, and a failed label list would report every label missing.

gh_failed() {
  echo "::error::advisory issue: gh $1 failed (exit $2)" >&2
  exit 1
}

jq -rn --arg s "${ADVISORY_ISSUE_LABELS:-}" '$s | split(",")[] | sub("^\\s+"; "") | sub("\\s+$"; "") | select(length > 0)' > "$work/labels"
dedupe="${ADVISORY_DEDUPE_LABEL:-}"
labels=()
label_args=()
dedupe_listed=0
while IFS= read -r l; do
  labels+=("$l")
  label_args+=(-l "$l")
  if [ -n "$dedupe" ] && [ "$l" = "$dedupe" ]; then dedupe_listed=1; fi
done < "$work/labels"
# The dedupe query reads this one label, so an issue filed without it would be filed again on every
# rerun.
if [ "$dedupe_listed" -eq 0 ]; then
  echo "ERROR: ADVISORY_DEDUPE_LABEL=\"$dedupe\" must name one of ADVISORY_ISSUE_LABELS (dedupe reads it); got \"${ADVISORY_ISSUE_LABELS:-}\"" >&2
  exit 1
fi

known=$(gh label list --repo "$repo" --limit 200 --json name) || gh_failed "label list" $?
if ! printf '%s' "$known" | jq -e 'type == "array" and all(.[]; (.name | type) == "string")' > /dev/null 2>&1; then
  echo "::error::advisory issue: gh label list answered something other than a list of labels" >&2
  exit 1
fi
missing=0
for l in ${labels[@]+"${labels[@]}"}; do
  if ! printf '%s' "$known" | jq -e --arg l "$l" 'any(.[]; .name == $l)' > /dev/null; then
    echo "ERROR: label \"$l\" does not exist in $repo; create it (gh label create) before the loop can file" >&2
    missing=1
  fi
done
[ "$missing" -eq 0 ] || exit 1

# Pins are unique within one record, so nothing this run files can duplicate a later pin of the same
# run, and one list answers every pin. A full page may be a truncated list, whose missing issues would
# be filed again.
open_limit=100
open=$(gh issue list --repo "$repo" --state open --label "$dedupe" --limit "$open_limit" --json number,title,body) || gh_failed "issue list" $?
if ! open_count=$(printf '%s' "$open" | jq -e 'if type == "array" then length else false end' 2> /dev/null); then
  echo "::error::advisory issue: gh issue list answered something other than a list of issues" >&2
  exit 1
fi
if [ "$open_count" -ge "$open_limit" ]; then
  echo "::error::advisory issue: $open_count open $dedupe issues reach the list limit of $open_limit, so the dedupe answer may be truncated; close the stale ones before the loop files again" >&2
  exit 1
fi

# An open issue is this pin's when it carries the pin's marker, a key marker for any member's key in
# any class (every class of one key is one pin), the pin's title or a member's own title. A pin whose
# findings grew since its issue was filed stays on that issue (a second pull request on one pin would
# undo the first); the findings it does not name are reported and file once it closes.
g=0
while [ "$g" -lt "$ngroups" ]; do
  title=$(title_of "$g")
  keys=$(cat "$work/keys.$g")
  pin_marker="<!-- advisory-decay-pin: $(cat "$work/pin.$g") -->"
  body="$work/body.$g"
  g=$((g + 1))

  if ! hit=$(printf '%s' "$open" | jq -r --arg pm "$pin_marker" --arg t "$title" --argjson keys "$keys" \
      '($keys | map(sub("^[^ ]+ "; ""))) as $members
       | [.[] | . as $issue | ($issue.body // "") as $b
        | select(($b | contains($pm)) or $issue.title == $t
            or any($keys[]; $issue.title == "advisory-decay: \(.)")
            or any($b | scan("<!-- advisory-decay-key: [^ ]+ ([^ ]+) -->"); .[0] as $k | any($members[]; . == $k)))]
       | first | .number // empty' 2> /dev/null) \
      || { [ -n "$hit" ] && ! [[ $hit =~ $digits_re ]]; }; then
    echo "::error::advisory issue: gh issue list answered something other than a list of issues" >&2
    exit 1
  fi

  if [ -n "$hit" ]; then
    echo "advisory issue: \"$title\" already open as #$hit; not filing"
    already_open=$((already_open + 1))
    printf '%s' "$open" | jq -r --argjson n "$hit" --argjson keys "$keys" \
      '.[] | select(.number == $n) | (.body // "") as $b | $keys[] | select(. as $k | $b | contains("<!-- advisory-decay-key: \($k) -->") | not)
       | "advisory issue: #\($n) does not mark \(.); it is filed after #\($n) closes"'
    locked=$(gh api "repos/$repo/issues/$hit" --jq .locked) || gh_failed "api repos/$repo/issues/$hit" $?
    case "$locked" in
      true) ;;
      false)
        if ! gh issue lock "$hit" --repo "$repo"; then
          echo "::error::advisory issue: gh issue lock #$hit failed; the issue is open but unlocked" >&2
          failed=$((failed + 1))
        fi ;;
      *)
        echo "::error::advisory issue: gh api repos/$repo/issues/$hit answered .locked=\"$locked\", not true or false" >&2
        exit 1 ;;
    esac
    continue
  fi

  if ! url=$(gh issue create --repo "$repo" -t "$title" -F "$body" ${label_args[@]+"${label_args[@]}"}); then
    echo "::error::advisory issue: gh issue create \"$title\" failed" >&2
    failed=$((failed + 1))
    continue
  fi
  number=${url##*/}
  case "$url" in
    "$server/$repo/issues/$number") ;;
    *) number="" ;;
  esac
  if ! [[ $number =~ $digits_re ]]; then
    echo "::error::advisory issue: gh issue create \"$title\" printed \"$url\", not an issue URL" >&2
    failed=$((failed + 1))
    continue
  fi
  echo "advisory issue: filed #$number $url"
  filed=$((filed + 1))
  if ! gh issue lock "$number" --repo "$repo"; then
    echo "::error::advisory issue: gh issue lock #$number failed; the issue is filed but unlocked" >&2
    failed=$((failed + 1))
  fi
done

finished=1
[ "$failed" -eq 0 ] || exit 1
