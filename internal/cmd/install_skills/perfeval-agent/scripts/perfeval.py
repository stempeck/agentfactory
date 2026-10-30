#!/usr/bin/env python3
"""Measurements for the perfeval-agent skill, read from an agentfactory factory's own data.

  steps     wall-clock profile per formula step (af telemetry report --json)
  runs      every run's identity and counts from telemetry alone; with --repo, the formula
            text each run ran, whether today's text has run, and the commits since
  children  which parent step waited for each child agent's run
  tokens    token ledger: main session per step, sub-agents per role, graders, ranked centres
  time      where a step's wall clock goes, from the transcripts
  gate      fidelity-gate evaluations, flag clusters and the wakes gate mail caused
  selftest  build a fixture factory and check every command against hand-computed values

Standard library only; Python 3.9+ (vanilla macOS) and Linux."""
import argparse
import collections
import contextlib
import datetime as dt
import glob
import hashlib
import io
import json
import os
import re
import shutil
import statistics
import subprocess
import sys
import tempfile

SPAN_STATUSES = ("closed", "gate-waiting")
GRADER_PREFIX = "Current step:"
SESSION_START = "session start (launch or handoff)"
# IE (input equivalent) weights each token class by its price relative to uncached input.
IE_WEIGHTS = {"input": 1.0, "cache_read": 0.1, "cache_create": 1.25, "output": 5.0}
USAGE_KEYS = {"input": "input_tokens", "cache_read": "cache_read_input_tokens",
              "cache_create": "cache_creation_input_tokens", "output": "output_tokens"}
LEAD = dt.timedelta(seconds=90)
SLACK = dt.timedelta(minutes=10)
LONG_GAP = 600
PAUSE = 55
DOMINANT = 0.5
WAIT_SHAPED = re.compile(r"\b(Await|Dispatch|Wait)\b", re.I)
WRITE_TOOLS = ("Write", "Edit", "MultiEdit", "NotebookEdit")


# ---- small helpers ------------------------------------------------------------------------

def parse_ts(s):
    if not s or len(s) < 19:
        return None
    try:
        t = dt.datetime.strptime(s[:19], "%Y-%m-%dT%H:%M:%S")
    except ValueError:
        return None
    frac = re.match(r"\.(\d+)", s[19:])
    return t + dt.timedelta(microseconds=int(frac.group(1)[:6].ljust(6, "0"))) if frac else t


def fmt_ts(t):
    return t.strftime("%Y-%m-%dT%H:%M:%SZ") if t else "-"


def utc(iso):
    """A git %cI time (with offset) as naive UTC, comparable with telemetry times."""
    t = dt.datetime.strptime(iso[:19], "%Y-%m-%dT%H:%M:%S")
    off = iso[19:]
    if off and off != "Z":
        sign = 1 if off[0] == "+" else -1
        t -= sign * dt.timedelta(hours=int(off[1:3]), minutes=int(off[4:6]))
    return t


def median(values):
    values = [v for v in values if v is not None]
    return statistics.median(values) if values else None


def pearson(xs, ys):
    if len(xs) < 3:
        return None
    mx, my = statistics.mean(xs), statistics.mean(ys)
    sxx, syy = sum((x - mx) ** 2 for x in xs), sum((y - my) ** 2 for y in ys)
    if not sxx or not syy:
        return None
    return sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / (sxx * syy) ** 0.5


def read_jsonl(path):
    with open(path, errors="replace") as fh:
        for line in fh:
            try:
                yield json.loads(line)
            except ValueError:
                continue


def minutes(seconds):
    return round(seconds / 60.0, 1)


def rounded(v, digits=1):
    return None if v is None else round(v, digits)


def mtok(v):
    return "%.2fM" % (v / 1e6)


def pct(part, whole):
    return "%.1f%%" % (100.0 * part / whole) if whole else "-"


def dominant(parts):
    """The part holding at least half of the total, else 'mixed': the only basis for calling
    something context-, output- or wait-bound."""
    total = sum(parts.values())
    if not total:
        return "-"
    k = max(parts, key=parts.get)
    return k if parts[k] >= DOMINANT * total else "mixed"


def table(title, header, rows, limit=None):
    rows = [[str(c) for c in r] for r in (rows[:limit] if limit else rows)]
    if title:
        print("\n" + title)
    if not rows:
        print("  (none)")
        return
    widths = [min(60, max(len(str(h)), *(len(r[i]) for r in rows))) for i, h in enumerate(header)]
    for r in [list(map(str, header))] + rows:
        print("  " + "  ".join(c[:w].ljust(w) for c, w in zip(r, widths)).rstrip())


# ---- factory data: telemetry report and steps log -----------------------------------------

def load_report(path, instances=None):
    with open(path) as fh:
        rows = json.load(fh)["rows"]
    for r in rows:
        r["start"] = parse_ts(r.get("started"))
        r["end"] = r["start"] + dt.timedelta(milliseconds=r.get("duration_ms") or 0) if r["start"] else None
        r["label"] = re.sub(r"^Step: ", "", r.get("step") or "")
    return [r for r in rows if not instances or r.get("instance_id") in instances]


def is_span(r):
    return r["start"] is not None and r.get("status") in SPAN_STATUSES


def spans(rows):
    """instance id -> sorted (start, end, label) of its rows that carry a full span."""
    out = collections.defaultdict(list)
    for r in rows:
        if is_span(r):
            out[r["instance_id"]].append((r["start"], r["end"], r["label"]))
    return {k: sorted(v) for k, v in out.items()}


def identities(steps_log):
    """instance id -> what its telemetry records say about it: worktree, model, formula
    digest, af build (instance_start), host versions, sub-agent launches, gate flags (step_end)."""
    ident = {}
    for rec in read_jsonl(steps_log):
        iid = rec.get("instance_id")
        if not iid:
            continue
        x = ident.setdefault(iid, {"worktree": None, "model": "-", "digest": "", "af_commit": "-", "hosts": set(),
                                   "subagent_launches": None, "gate_flags": None})
        x["worktree"] = x["worktree"] or rec.get("worktree_id") or None
        if rec.get("event") == "instance_start":
            x.update(model=rec.get("model") or "-", digest=rec.get("formula_digest") or "", af_commit=rec.get("af_commit") or "-")
        elif rec.get("event") == "step_end":
            if rec.get("host_version"):
                x["hosts"].add(rec["host_version"])
            for k in ("subagent_launches", "gate_flags"):
                if rec.get(k) is not None:
                    x[k] = (x[k] or 0) + rec[k]
    return ident


# ---- transcripts --------------------------------------------------------------------------

def transcript_roots(roots):
    if roots:
        return roots
    return [os.path.join(os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude"), "projects")]


def project_dirs(roots, agent, worktree=None):
    """One group per project directory name, holding that directory's copy under every root."""
    pattern = "*%s--agentfactory-agents-%s" % (worktree, agent) if worktree else "*agentfactory-agents-%s" % agent
    groups = collections.defaultdict(list)
    for root in roots:
        for d in sorted(glob.glob(os.path.join(root, pattern))):
            if os.path.isdir(d):
                groups[os.path.basename(d)].append(d)
    return [tuple(groups[k]) for k in sorted(groups)]


def merged_files(group, *parts, recursive=False):
    """Files under every copy of one project directory, one per relative path. A backup root and
    the live root can both hold a session and either copy may be the more complete, so the
    larger wins."""
    best = {}
    for d in group:
        for f in glob.glob(os.path.join(d, *parts), recursive=recursive):
            rel = os.path.relpath(f, d)
            if rel not in best or os.path.getsize(f) > os.path.getsize(best[rel]):
                best[rel] = f
    return [best[k] for k in sorted(best)]


def content_of(rec):
    msg = rec.get("message")
    return msg.get("content") if isinstance(msg, dict) else None


def text_of(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(b.get("text", "") for b in content if isinstance(b, dict) and b.get("type") == "text")
    return ""


def is_tool_result(content):
    return isinstance(content, list) and bool(content) and isinstance(content[0], dict) and content[0].get("type") == "tool_result"


def prompt_text(rec):
    c = content_of(rec)
    return c if isinstance(c, str) else json.dumps(c)


def first_prompt(path):
    for rec in read_jsonl(path):
        if rec.get("type") == "user":
            return prompt_text(rec)
    return ""


def session_files(group):
    """(main sessions, grader sessions) of a project directory group."""
    mains, graders = [], []
    for f in merged_files(group, "*.jsonl"):
        (graders if first_prompt(f).startswith(GRADER_PREFIX) else mains).append(f)
    return mains, graders


def subagent_files(group):
    return merged_files(group, "*", "subagents", "**", "agent-*.jsonl", recursive=True)


def role_of(path):
    try:
        with open(path[: -len(".jsonl")] + ".meta.json") as fh:
            meta = json.load(fh)
        return meta.get("description") or meta.get("agentType") or "?"
    except (OSError, ValueError):
        return "?"


def file_window(path):
    ts = [t for t in (parse_ts(r.get("timestamp")) for r in read_jsonl(path)) if t]
    return (min(ts), max(ts)) if ts else (None, None)


def wake_kind(text):
    if "task-notification" in text:
        return "sub-agent completion notification"
    m = re.search(r"NEW MAIL from ([\w.-]+)\s+Subject: ([A-Za-z_:]+)", text)
    if m:
        if m.group(2) in ("STEP_FIDELITY", "FIDELITY_ESCALATION"):
            return "fidelity-gate mail (%s)" % m.group(2)
        return "mail from %s: %s" % m.groups()
    if "are you doing your job" in text:
        return "watchdog nudge"
    if text.strip().startswith("af prime") or "Run `af prime`" in text:
        return "re-prime"
    if "improve" in text.lower() and "agent" in text.lower():
        return "improvement hook"
    return "other prompt"


def prompts(path):
    """(time, kind) of every prompt that starts a turn. A session's first prompt is its start: a
    launch and a handoff's fresh session send the same text."""
    first = True
    for rec in read_jsonl(path):
        if rec.get("type") == "user" and not is_tool_result(content_of(rec)):
            yield parse_ts(rec.get("timestamp")), SESSION_START if first else wake_kind(prompt_text(rec))
            first = False


def assistant_calls(path):
    """One entry per API response, at its FINAL usage, tagged with the prompt that started its
    turn. A response is written as several records (thinking, text, tool_use) sharing one message
    id, and only the last carries the final output count."""
    calls, kinds = {}, prompts(path)
    kind = SESSION_START
    for rec in read_jsonl(path):
        if rec.get("type") == "user":
            if not is_tool_result(content_of(rec)):
                kind = next(kinds)[1]
            continue
        if rec.get("type") != "assistant":
            continue
        msg = rec.get("message") or {}
        mid, usage = msg.get("id") or rec.get("uuid"), msg.get("usage") or {}
        if mid not in calls:
            calls[mid] = {"ts": parse_ts(rec.get("timestamp")), "usage": usage, "wake": kind}
        elif usage.get("output_tokens", 0) >= calls[mid]["usage"].get("output_tokens", 0):
            calls[mid]["usage"] = usage
    return list(calls.values())


def ie_parts(usage):
    return {k: IE_WEIGHTS[k] * usage.get(USAGE_KEYS[k], 0) for k in IE_WEIGHTS}


def ie(usage):
    return sum(ie_parts(usage).values())


def context_tokens(usage):
    return sum(usage.get(USAGE_KEYS[k], 0) for k in ("input", "cache_read", "cache_create"))


def step_of(t, steps):
    for s, e, label in steps:
        if s <= t <= e:
            return label
    if not steps or t < steps[0][0]:
        return "(before first step)"
    if t > max(e for _, e, _ in steps):
        return "(after last step)"
    return "(after %s)" % [label for s, e, label in steps if e < t][-1]


def windows(insts):
    """(instance, lo, hi): an instance's transcript window runs from its first step start minus
    LEAD to the next instance's start in the same directories."""
    starts = collections.defaultdict(list)
    for x in insts:
        for d in x["dirs"]:
            starts[d].append(x["start"])
    out = []
    for x in insts:
        later = [s for d in x["dirs"] for s in starts[d] if s > x["start"]]
        out.append((x, x["start"] - LEAD, min(later) if later else dt.datetime.max))
    return out


def resolve(a):
    """The selected instances, each with its step spans and its transcript directory groups."""
    wanted = set(a.instances.split(",")) if a.instances else None
    rows = load_report(a.report, wanted)
    ident, roots = identities(a.steps_log), transcript_roots(a.transcripts)
    out = []
    for iid, st in sorted(spans(rows).items(), key=lambda kv: kv[1][0][0]):
        wt = ident.get(iid, {}).get("worktree")
        out.append({"id": iid, "worktree": wt, "steps": st, "start": st[0][0], "end": max(e for _, e, _ in st),
                    "dirs": project_dirs(roots, a.agent, wt) if wt else []})
    return out


# ---- steps --------------------------------------------------------------------------------

def step_ids(steps_log):
    """(instance id, step title) -> the formula's step id, which survives a title change."""
    ids = {}
    for rec in read_jsonl(steps_log) if steps_log else []:
        if rec.get("event") == "step_end" and rec.get("step_label"):
            ids[(rec.get("instance_id"), re.sub(r"^Step: ", "", rec.get("step_title") or ""))] = rec["step_label"]
    return ids


def cmd_steps(a):
    rows = load_report(a.report, set(a.instances.split(",")) if a.instances else None)
    ids, latest = step_ids(a.steps_log), {}
    for r in sorted((r for r in rows if r["start"]), key=lambda r: r["start"]):
        key = ids.get((r.get("instance_id"), r["label"]))
        if key:
            latest[key] = r["label"]
    for r in rows:
        key = ids.get((r.get("instance_id"), r["label"]))
        r["label"] = latest.get(key, r["label"])
    by, longest, no_start, excluded = collections.defaultdict(list), {}, collections.defaultdict(list), []
    for r in rows:
        mins = (r.get("duration_ms") or 0) / 60000.0
        if r.get("status") not in SPAN_STATUSES:
            excluded.append(r)
        elif r["start"] is None:
            no_start[r["label"]].append(mins)
        else:
            by[r["label"]].append(mins)
            if mins >= longest.get(r["label"], (-1, ""))[0]:
                longest[r["label"]] = (mins, r.get("instance_id"))
    steps = sorted(({"step": k, "runs": len(v), "median_min": round(median(v), 1), "min_min": round(min(v), 1),
                     "max_min": round(max(v), 1), "max_instance": longest[k][1], "wait_shaped": bool(WAIT_SHAPED.search(k))}
                    for k, v in by.items()), key=lambda x: -x["median_min"])
    run_min = sum(x["median_min"] for x in steps)
    for x in steps:
        x["share_of_median_run_pct"] = round(100.0 * x["median_min"] / run_min) if run_min else 0
    starts = [r["start"] for r in rows if r["start"]]
    out = {"summary": {"instances": len({r.get("instance_id") for r in rows}), "first": fmt_ts(min(starts)) if starts else "-",
                       "last": fmt_ts(max(starts)) if starts else "-", "median_run_min": round(run_min, 1),
                       "wait_shaped_share_pct": round(100.0 * sum(x["median_min"] for x in steps if x["wait_shaped"]) / run_min)
                       if run_min else 0,
                       "rows_without_start": {k: {"runs": len(v), "median_min": round(median(v), 1)} for k, v in no_start.items()}},
           "steps": steps,
           "excluded": [{"status": r.get("status"), "instance": r.get("instance_id"), "step": r["label"],
                         "minutes": round((r.get("duration_ms") or 0) / 60000.0), "started": r.get("started")} for r in excluded]}
    if a.json:
        return out
    s = out["summary"]
    print("instances=%(instances)d  %(first)s..%(last)s  median run=%(median_run_min)s min  "
          "wait-shaped steps=%(wait_shaped_share_pct)s%%" % s)
    table("steps by median span (closed and gate-waiting rows are both the whole span)",
          ["step", "runs", "median", "min", "max", "share", "max run", ""],
          [[x["step"], x["runs"], x["median_min"], x["min_min"], x["max_min"], "%d%%" % x["share_of_median_run_pct"], x["max_instance"],
            "wait-shaped" if x["wait_shaped"] else ""] for x in steps])
    table("rows with no start time, not part of the median run (the improvement hook runs after the last step)",
          ["step", "runs", "median"], [[k, v["runs"], v["median_min"]] for k, v in s["rows_without_start"].items()])
    table("excluded rows (not a full span; INTERRUPTED durations are meaningless)", ["status", "instance", "step", "minutes", "started"],
          [[e["status"], e["instance"], e["step"], e["minutes"], e["started"]] for e in out["excluded"]])
    return None


# ---- runs ---------------------------------------------------------------------------------

def git(repo, *args):
    return subprocess.run(["git", "-C", repo] + list(args), capture_output=True, check=True).stdout


def formula_commits(repo, paths):
    """(utc time, hash, subject, path at that commit) of every commit touching any path."""
    found = {}
    for path in paths:
        try:
            log = git(repo, "log", "--follow", "--format=%x00%H%x09%cI%x09%s", "--name-only", "--", path).decode()
        except subprocess.CalledProcessError:
            continue
        for chunk in log.split("\x00")[1:]:
            lines = [l for l in chunk.splitlines() if l.strip()]
            h, when, subject = lines[0].split("\t", 2)
            found[(h, lines[-1])] = (utc(when), h[:8], subject[:60], lines[-1])
    return sorted(found.values())


def commit_digests(repo, commits):
    out = {}
    for t, h, subject, path in commits:
        try:
            out.setdefault(hashlib.sha256(git(repo, "show", "%s:%s" % (h, path))).hexdigest(), (t, h, subject, path))
        except subprocess.CalledProcessError:
            continue
    return out


def read_values(path, windows):
    kept, dropped = {}, 0
    with open(path) as fh:
        for line in fh:
            parts = line.rstrip("\n").split("\t")
            t = parse_ts(parts[0]) if len(parts) >= 2 else None
            if not t:
                continue
            owner = next((iid for iid, (lo, hi) in windows.items() if lo <= t <= hi), None)
            if owner:
                kept.setdefault(owner, float(parts[1]))
            else:
                dropped += 1
    return kept, dropped


def cmd_runs(a):
    rows = load_report(a.report)
    ident, sp = identities(a.steps_log), spans(rows)
    windows = {iid: (st[0][0], max(e for _, e, _ in st)) for iid, st in sp.items()}
    values, dropped = read_values(a.values, windows) if a.values else ({}, 0)
    commits = formula_commits(a.repo, a.formula) if a.repo else []
    known = commit_digests(a.repo, commits) if a.repo else {}
    runs = []
    for iid, st in sorted(sp.items(), key=lambda kv: kv[1][0][0]):
        x = ident.get(iid, {"model": "-", "digest": "", "af_commit": "-", "hosts": set(), "subagent_launches": None, "gate_flags": None})
        hit = known.get(x["digest"])
        runs.append({"instance": iid, "start": fmt_ts(st[0][0]), "span_min": minutes((windows[iid][1] - windows[iid][0]).total_seconds()),
                     "model": x["model"], "digest": x["digest"][:8] or "-", "af_commit": x["af_commit"],
                     "host": ",".join(sorted(x["hosts"])) or "-",
                     "subagent_launches": x["subagent_launches"], "gate_flags": x["gate_flags"], "value": values.get(iid),
                     "text": hit[1] if hit else ("uncommitted" if x["digest"] and a.repo else "-"),
                     "steps": {f: (round(sum((e - s).total_seconds() for s, e, l in st if f.lower() in l.lower()) / 60.0, 1)
                                   if any(f.lower() in l.lower() for _, _, l in st) else None) for f in a.step}})
    groups = collections.OrderedDict()
    for r, x in ((r, ident.get(r["instance"], {})) for r in runs):
        g = groups.setdefault(x.get("digest", ""), {"runs": [], "models": collections.Counter()})
        g["runs"].append(r)
        g["models"][r["model"]] += 1
    today = []
    for path in a.formula if a.repo else []:
        full = os.path.join(a.repo, path)
        if os.path.exists(full):
            with open(full, "rb") as fh:
                d = hashlib.sha256(fh.read()).hexdigest()
            today.append({"path": path, "digest": d[:8], "runs": len(groups.get(d, {"runs": []})["runs"])})
    last = max((parse_ts(r["start"]) for r in runs), default=None)
    newer = [{"commit": h, "time": fmt_ts(t), "path": p, "subject": s} for t, h, s, p in commits if last and t > last]
    unclosed = sorted(set(ident) - set(sp))
    out = {"runs": runs, "unclosed": unclosed, "values_dropped": dropped,
           "texts": [{"digest": d[:8] or "-", "commit": known[d][1] if d in known else ("uncommitted" if d else "-"),
                      "commit_time": fmt_ts(known[d][0]) if d in known else "-", "path": known[d][3] if d in known else "-",
                      "runs": [r["instance"] for r in g["runs"]], "models": dict(g["models"]),
                      "median_min": {f: rounded(median([r["steps"][f] for r in g["runs"]])) for f in a.step}} for d, g in groups.items()],
           "today": today, "commits_after_last_run": newer}
    if a.json:
        return out
    show = lambda v: "-" if v is None else v
    table("runs (span = first step start to last step end)",
          ["instance", "start", "span", "model", "digest", "text", "af", "host", "subs", "flags"]
          + (["value"] if a.values else []) + a.step,
          [[r["instance"], r["start"], r["span_min"], r["model"], r["digest"], r["text"], r["af_commit"][:9], r["host"],
            show(r["subagent_launches"]), show(r["gate_flags"])]
           + ([show(r["value"])] if a.values else []) + [show(r["steps"][f]) for f in a.step]
           for r in runs])
    if a.values:
        print("values: %d runs carry one; %d values outside every run window dropped" % (len(values), dropped))
    if unclosed:
        print("instances in the steps log that closed no step (interrupted or aborted): %s" % ", ".join(unclosed))
    if a.repo:
        table("runs grouped by the formula text they ran (instance_start formula_digest matched to a commit's file)",
              ["digest", "commit", "committed (UTC)", "path", "runs", "models"] + ["median %s" % f for f in a.step],
              [[t["digest"], t["commit"], t["commit_time"], t["path"], len(t["runs"]), ",".join("%s:%d" % kv for kv in t["models"].items())]
               + [show(t["median_min"][f]) for f in a.step] for t in out["texts"]])
        for t in out["texts"]:
            print("  %s: %s" % (t["digest"], ", ".join(t["runs"])))
        if not commits:
            print("no commit of %s in %s's git: the text an older run ran cannot be recovered, only compared by digest" % (
                " or ".join(a.formula), a.repo))
        print("\ntoday's text")
        for t in today:
            print("  %s: digest %s, %d run(s) on this exact text%s" % (t["path"], t["digest"], t["runs"], "" if t["runs"] else
                  " -- the measurements come from older text; diff it against today's before proposing a lever"))
        table("commits to the formula after the last run started", ["commit", "time", "path", "subject"],
              [[c["commit"], c["time"], c["path"], c["subject"]] for c in newer])
    return None


# ---- children -----------------------------------------------------------------------------

def holder(t, parent_rows):
    """The parent row whose window holds t (latest-starting), else the first starting within SLACK after it."""
    inside = [r for r in parent_rows if r["start"] <= t <= r["end"]]
    if inside:
        return max(inside, key=lambda r: r["start"])
    ahead = [r for r in parent_rows if dt.timedelta(0) < r["start"] - t <= SLACK]
    return min(ahead, key=lambda r: r["start"]) if ahead else None


def cmd_children(a):
    """An orchestrator dispatches a child in one step and waits for it in a later one, so each
    child run belongs to the parent step it ENDED in, the step that waited for it."""
    parent = [r for r in load_report(a.report) if is_span(r)]
    by_step = collections.defaultdict(list)
    for r in parent:
        by_step[r["label"]].append(r)
    result = []
    for spec in a.child:
        name, path = spec.split("=", 1)
        ended = {}
        for st in spans(load_report(path)).values():
            s, e = st[0][0], max(x[1] for x in st)
            row = holder(e, parent)
            if row is not None and (id(row) not in ended or e > ended[id(row)][1][1]):
                ended[id(row)] = (row, (s, e))
        for label, rs in by_step.items():
            hits = [ended[id(r)] for r in rs if id(r) in ended]
            if hits:
                dispatched = collections.Counter((holder(s, parent) or {"label": "(no parent step)"})["label"] for _, (s, e) in hits)
                result.append({"parent_step": label, "child": name, "matched": len(hits), "runs": len(rs),
                               "step_median_min": minutes(median([r["duration_ms"] / 1000.0 for r in rs])),
                               "child_median_min": minutes(median([(e - s).total_seconds() for _, (s, e) in hits])),
                               "end_delta_min": minutes(median([(e - r["end"]).total_seconds() for r, (s, e) in hits])),
                               "dispatched_in": dispatched.most_common(1)[0][0]})
    if a.json:
        return result
    table("parent steps by the child runs that ended in them (end delta = child end - step end)",
          ["parent step", "child", "match", "step med", "child med", "end delta", "dispatched in"],
          [[x["parent_step"], x["child"], "%d/%d" % (x["matched"], x["runs"]), x["step_median_min"], x["child_median_min"],
            "%+.1f" % x["end_delta_min"], x["dispatched_in"]] for x in sorted(result, key=lambda x: -x["matched"])])
    return None


# ---- tokens -------------------------------------------------------------------------------

def subagent_detail(path, held_file):
    """Per sub-agent: tool calls, file-writing share of output, files read, and what keeping
    held_file in context cost: its tokens once at 1.25 (the cache write after the read) plus at
    0.1 on every later call (the cache read)."""
    tool_calls, write_bytes, reads, read_ids, held_ids, held_bytes, held_reads = 0, 0, collections.Counter(), {}, set(), 0, 0.0
    seen = set()
    for rec in read_jsonl(path):
        c = content_of(rec)
        if rec.get("type") == "assistant":
            mid = (rec.get("message") or {}).get("id")
            if mid and mid not in seen:
                seen.add(mid)
                held_reads += 0.1 * held_bytes / 4.0
            for b in c or []:
                if b.get("type") != "tool_use":
                    continue
                tool_calls += 1
                inp, name = b.get("input") or {}, b.get("name")
                if name in WRITE_TOOLS:
                    write_bytes += len(json.dumps(inp))
                elif name == "Bash" and ("<<" in inp.get("command", "") or "cat >" in inp.get("command", "")):
                    write_bytes += len(inp.get("command", ""))
                if name == "Read" and inp.get("file_path"):
                    path = inp["file_path"]
                    read_ids[b.get("id")] = ("tool output " if "/tool-results/" in path else "") + os.path.basename(path)
                if held_file and held_file in json.dumps(inp):
                    held_ids.add(b.get("id"))
        elif rec.get("type") == "user" and is_tool_result(c):
            for b in c:
                size = len(json.dumps(b.get("content")))
                if b.get("tool_use_id") in read_ids:
                    reads[read_ids[b["tool_use_id"]]] += size
                if b.get("tool_use_id") in held_ids:
                    held_bytes += size
    held = 1.25 * held_bytes / 4.0 + held_reads if held_file else 0.0
    return tool_calls, write_bytes, reads, held_bytes, held


def cmd_tokens(a):
    insts = resolve(a)
    counters = lambda: collections.defaultdict(collections.Counter)
    steps, wakes, roles, sub_steps = counters(), counters(), counters(), counters()
    step_wake, graders, per_instance, detail, reads = collections.Counter(), collections.Counter(), [], [], collections.Counter()
    for x, lo, hi in windows(insts):
        tot = collections.Counter()
        for d in x["dirs"]:
            mains, grader_files = session_files(d)
            for f in mains:
                for t, kind in prompts(f):
                    if t and lo <= t < hi:
                        wakes[kind]["wakes"] += 1
                for c in assistant_calls(f):
                    if not (c["ts"] and lo <= c["ts"] < hi):
                        continue
                    label, parts = step_of(c["ts"], x["steps"]), ie_parts(c["usage"])
                    steps[label].update(parts)
                    steps[label]["calls"] += 1
                    steps[label]["peak"] = max(steps[label]["peak"], context_tokens(c["usage"]))
                    step_wake[(label, c["wake"])] += sum(parts.values())
                    wakes[c["wake"]]["calls"] += 1
                    wakes[c["wake"]]["ie"] += sum(parts.values())
                    tot["main"] += sum(parts.values())
            for f in grader_files:
                first, _ = file_window(f)
                if first and lo <= first < hi:
                    calls = assistant_calls(f)
                    for c in calls:
                        graders.update(ie_parts(c["usage"]))
                    v = sum(ie(c["usage"]) for c in calls)
                    graders["sessions"] += 1
                    graders["ie"] += v
                    tot["graders"] += v
            for f in subagent_files(d):
                first, last = file_window(f)
                if not first or not (lo <= first < hi):
                    continue
                calls, role = assistant_calls(f), role_of(f)
                r, in_step = roles[role], sub_steps[step_of(first, x["steps"])]
                for c in calls:
                    r.update(ie_parts(c["usage"]))
                    in_step.update(ie_parts(c["usage"]))
                    r["peak"] = max(r["peak"], context_tokens(c["usage"]))
                r["n"] += 1
                r["calls"] += len(calls)
                r["output_tokens"] += sum(c["usage"].get("output_tokens", 0) for c in calls)
                r["minutes"] += (last - first).total_seconds() / 60.0
                v = sum(ie(c["usage"]) for c in calls)
                tot["subagents"] += v
                if a.detail or a.held_file:
                    tools, wbytes, rd, held_bytes, held = subagent_detail(f, a.held_file)
                    out_tok = sum(c["usage"].get("output_tokens", 0) for c in calls)
                    reads.update(rd)
                    detail.append({"instance": x["id"], "step": step_of(first, x["steps"]), "role": role,
                                   "start": fmt_ts(first), "end": fmt_ts(last), "minutes": minutes((last - first).total_seconds()),
                                   "calls": len(calls), "tool_calls": tools, "output_tokens": out_tok,
                                   "write_share_pct": round(100.0 * (wbytes / 4.0) / max(out_tok, 1)),
                                   "peak_ctx": max([0] + [context_tokens(c["usage"]) for c in calls]), "ie": round(v),
                                   "held_file_read_kb": round(held_bytes / 1000.0), "held_file_ie": round(held)})
        if tot:
            per_instance.append({"instance": x["id"], "worktree": x["worktree"], **{k: round(v) for k, v in tot.items()}})
    n = max(len(per_instance), 1)
    ie_of = lambda c: sum(c[k] for k in IE_WEIGHTS)
    total = sum(ie_of(s) for s in steps.values()) + sum(ie_of(r) for r in roles.values()) + graders["ie"]
    centres = [{"centre": k, "kind": "main session, after WORK_DONE" if k == "(after last step)" else "main session step",
                "ie": ie_of(s), "dominant": dominant({p: s[p] for p in IE_WEIGHTS}),
                "controller": ("factory config (improvement hook) or the sender of late mail" if k == "(after last step)"
                               else "formula: step text")}
               for k, s in steps.items()]
    # Sub-agents are ranked by the step they ran in: their descriptions are free text that
    # varies run to run, the step label does not.
    centres += [{"centre": "sub-agents in " + k, "kind": "in-step sub-agents", "ie": ie_of(r),
                 "dominant": dominant({p: r[p] for p in IE_WEIGHTS}),
                 "controller": "formula: the step's sub-agent prompts"} for k, r in sub_steps.items()]
    centres.append({"centre": "fidelity-gate graders", "kind": "graders", "ie": graders["ie"],
                    "dominant": dominant({p: graders[p] for p in IE_WEIGHTS}),
                    "controller": "gate code; step text in the 4096-byte window"})
    centres.sort(key=lambda c: -c["ie"])
    for i, c in enumerate(centres, 1):
        c.update(rank=i, share_pct=round(100.0 * c["ie"] / total, 1) if total else 0, per_run=round(c["ie"] / n), ie=round(c["ie"]))
    gate_turns = sum(v["ie"] for k, v in wakes.items() if k.startswith("fidelity-gate mail"))
    out = {"coverage": {"profiled": len(per_instance), "instances": len(insts)}, "per_instance": per_instance, "total_ie": round(total),
           "centres": centres,
           "main_by_step": {k: {"calls": s["calls"], "ie": round(ie_of(s)), "peak_ctx": s["peak"], **{p: round(s[p]) for p in IE_WEIGHTS}}
                            for k, s in steps.items()},
           "wakes": {k: dict(v) for k, v in wakes.items()},
           "main_by_step_and_wake": [{"step": k[0], "wake": k[1], "ie": round(v)} for k, v in step_wake.most_common()],
           "subagents_by_role": {k: {"n": r["n"], "ie": round(ie_of(r)), "calls": r["calls"], "output_tokens": r["output_tokens"],
                                     "peak_ctx": r["peak"], "minutes": round(r["minutes"], 1),
                                     **{p + "_ie": round(r[p]) for p in IE_WEIGHTS}}
                                 for k, r in roles.items()},
           "graders": {"sessions": graders["sessions"], "ie": round(graders["ie"])},
           "summary": {"main session": round(sum(ie_of(s) for s in steps.values())),
                       "in-step sub-agents": round(sum(ie_of(r) for r in roles.values())),
                       "fidelity-gate graders": round(graders["ie"]),
                       "gate cost (graders + turns started by gate mail)": round(graders["ie"] + gate_turns)},
           "subagents": detail, "files_read": dict(reads.most_common(8))}
    if a.json:
        return out
    print("coverage: %d of %d instances have transcripts; every figure below is summed over those %d"
          % (len(per_instance), len(insts), len(per_instance)))
    table("cost centres ranked (rows sum to TOTAL; a part is dominant only at >= 50% of its centre)",
          ["rank", "centre", "kind", "IE", "share", "per run", "dominant part", "controller"],
          [[c["rank"], c["centre"], c["kind"], mtok(c["ie"]), "%.1f%%" % c["share_pct"], mtok(c["per_run"]), c["dominant"],
            c["controller"]] for c in centres])
    table("summary (gate cost overlaps the main-session rows that gate mail started)", ["centre", "IE", "share", "per run"],
          [[k, mtok(v), pct(v, total), mtok(v / n)] for k, v in out["summary"].items()])
    table("per instance", ["instance", "worktree", "main", "sub-agents", "graders"],
          [[r["instance"], r["worktree"], mtok(r.get("main", 0)), mtok(r.get("subagents", 0)), mtok(r.get("graders", 0))]
           for r in per_instance])
    table("main session by step", ["step", "calls", "IE", "cache_read", "cache_create", "output", "peak ctx"],
          [[k, s["calls"], mtok(s["ie"]), mtok(s["cache_read"]), mtok(s["cache_create"]), mtok(s["output"]),
            "%dk" % (s["peak_ctx"] // 1000)]
           for k, s in sorted(out["main_by_step"].items(), key=lambda kv: -kv[1]["ie"])])
    table("main-session turns by the prompt that started them (a turn's IE is all its work until the next prompt)",
          ["prompt", "wakes", "calls", "IE", "share"],
          [[k, v.get("wakes", 0), v.get("calls", 0), mtok(v.get("ie", 0)), pct(v.get("ie", 0), total)]
           for k, v in sorted(out["wakes"].items(), key=lambda kv: -kv[1].get("ie", 0))])
    table("main-session IE by step and the prompt that started the turn", ["step", "prompt", "IE"],
          [[x["step"], x["wake"], mtok(x["ie"])] for x in out["main_by_step_and_wake"]], limit=12)
    table("in-step sub-agents by role", ["role", "n", "IE", "agent-min", "output", "peak", "cache_read", "cache_create", "output IE"],
          [[k, r["n"], mtok(r["ie"]), r["minutes"], "%dk" % (r["output_tokens"] // 1000), "%dk" % (r["peak_ctx"] // 1000),
            pct(r["cache_read_ie"], r["ie"]), pct(r["cache_create_ie"], r["ie"]), pct(r["output_ie"], r["ie"])]
           for k, r in sorted(out["subagents_by_role"].items(), key=lambda kv: -kv[1]["ie"])])
    if detail:
        table("sub-agents", ["instance", "step", "role", "start", "end", "min", "calls", "tools", "output", "write", "peak", "IE"]
              + (["held file"] if a.held_file else []),
              [[d["instance"], d["step"], d["role"], d["start"][11:19], d["end"][11:19], d["minutes"], d["calls"], d["tool_calls"],
                "%dk" % (d["output_tokens"] // 1000),
                "%d%%" % d["write_share_pct"], "%dk" % (d["peak_ctx"] // 1000), mtok(d["ie"])]
               + (["%dKB %s" % (d["held_file_read_kb"], mtok(d["held_file_ie"]))] if a.held_file else []) for d in detail])
        print("files read by these sub-agents, largest first (KB): " + ", ".join("%s %d" % (k, v // 1000) for k, v in reads.most_common(6)))
        if a.held_file:
            held = sum(d["held_file_ie"] for d in detail)
            sub_ie = sum(d["ie"] for d in detail)
            print("held cost of %s: %s of %s sub-agent IE (%s)" % (a.held_file, mtok(held), mtok(sub_ie), pct(held, sub_ie)))
    print("\ngrader sessions: %d" % graders["sessions"])
    print("TOTAL IE=%s over %d profiled instance(s); per profiled run=%s" % (mtok(total), len(per_instance), mtok(total / n)))
    return None


# ---- time ---------------------------------------------------------------------------------

def classify(rec):
    """(category, description) of a transcript entry; the gap BEFORE it is charged to category."""
    c, kind = content_of(rec), rec.get("type")
    if kind == "assistant":
        b = c[0] if isinstance(c, list) and c and isinstance(c[0], dict) else {}
        if b.get("type") == "thinking":
            return "model: thinking", "(thinking; text not persisted)"
        if b.get("type") == "tool_use":
            inp = b.get("input") or {}
            return "model: tool_use", "%s %s" % (b.get("name"), inp.get("command") or inp.get("file_path") or inp.get("description") or "")
        return "model: %s" % b.get("type", "other"), text_of(c)
    if kind == "user":
        if is_tool_result(c):
            return "tool execution", "tool result"
        return "waiting for a prompt", text_of(c) or (c if isinstance(c, str) else "")
    if kind == "queue-operation":
        return "waiting for a prompt", "queued prompt (%s)" % rec.get("operation", "")
    sub = (rec.get("attachment") or {}).get("type", "") if kind == "attachment" else rec.get("subtype", "")
    if str(sub).startswith("hook"):
        return "hook execution", "%s %s" % (kind, sub)
    return "meta", "%s %s" % (kind, sub)


def attribute(paths, lo, hi, pairs):
    """Per transcript file, summed: parallel sub-agents never share a timeline, and the time
    between two session files belongs to neither."""
    sums, long_s, pauses = collections.Counter(), 0.0, []
    for path in paths:
        ev = sorted((t, *classify(r)) for r in read_jsonl(path) for t in [parse_ts(r.get("timestamp"))] if t and lo <= t <= hi)
        for prev, cur in zip(ev, ev[1:]):
            g = (cur[0] - prev[0]).total_seconds()
            sums[cur[1]] += g
            long_s += g if g >= LONG_GAP else 0
            if pairs and g >= PAUSE:
                pauses.append({"seconds": round(g), "before": "[%s] %s" % (prev[1], str(prev[2])[:100].replace("\n", " ")),
                               "after": "[%s] %s" % (cur[1], str(cur[2])[:100].replace("\n", " "))})
    return sums, long_s, pauses


def cmd_time(a):
    insts = resolve(a)
    rows, total, total_long, outside = [], collections.Counter(), 0.0, 0.0
    with_step = [x for x in insts if any(a.step.lower() in l.lower() for _, _, l in x["steps"])]
    for x in with_step:
        if not x["dirs"]:
            continue
        s, e = [(s, e) for s, e, l in x["steps"] if a.step.lower() in l.lower()][0]
        paths = [p for d in x["dirs"] for p in (subagent_files(d) if a.subagents else session_files(d)[0])]
        sums, long_s, pauses = attribute(paths, s, e, a.pairs)
        if not sums:
            continue
        total.update(sums)
        total_long += long_s
        if not a.subagents:
            outside += max(0.0, (e - s).total_seconds() - sum(sums.values()))
        rows.append({"instance": x["id"], "step_min": minutes((e - s).total_seconds()),
                     "minutes": {k: minutes(v) for k, v in sums.items()}, "pauses": pauses})
    dom = dominant(dict(total))
    lead = max(total, key=total.get) if total else None
    r = pearson([x["step_min"] for x in rows], [x["minutes"].get(lead, 0) for x in rows]) if lead else None
    out = {"coverage": {"profiled": len(rows), "instances_with_step": len(with_step)}, "agent_minutes": a.subagents,
           "minutes": {k: minutes(v) for k, v in total.items()}, "long_gap_min": minutes(total_long),
           "outside_transcripts_min": None if a.subagents else minutes(outside), "dominant": dom, "largest": lead,
           "r_step_vs_largest": None if r is None else round(r, 2), "instances": rows}
    if a.json:
        return out
    print("coverage: %d profiled of %d instances with this step%s" % (len(rows), len(with_step),
          "; sub-agent figures are agent-minutes (parallel sub-agents summed)" if a.subagents else ""))
    whole = sum(total.values())
    table("time by category (the gap before an entry is charged to the entry)", ["category", "minutes", "share"],
          [[k, minutes(v), pct(v, whole)] for k, v in total.most_common()])
    print("of which single gaps >= %ds: %.1f min" % (LONG_GAP, total_long / 60.0))
    if not a.subagents:
        print("step time outside these transcripts (before the first entry, after the last, between files): %.1f min" % (outside / 60.0))
    print("dominant category (>= 50%%): %s; largest: %s; r(step duration, its minutes) = %s over %d instance(s)" % (
        dom, lead, out["r_step_vs_largest"] if r is not None else "n/a (<3 instances)", len(rows)))
    for x in rows:
        top3 = sorted(x["minutes"].items(), key=lambda kv: -kv[1])[:3]
        print("  %s step %.1f min; %s" % (x["instance"], x["step_min"], ", ".join("%s %.1f" % kv for kv in top3)))
        for p in x["pauses"]:
            print("    pause %4ds after %s\n               -> %s" % (p["seconds"], p["before"], p["after"]))
    return None


# ---- gate ---------------------------------------------------------------------------------

QUOTED = re.compile(r"\"([^\"]{15,}?)\"|“([^”]{15,}?)”|‘([^’]{15,}?)’"
                    r"|'((?:[^']|(?<=[A-Za-z])'(?=[A-Za-z])){15,}?)'(?![A-Za-z])")
LABEL = re.compile(r"^[A-Z][A-Z ]{3,}:\s*")


def directive(reason):
    """The first sentence of the directive the grader quotes, without markdown, so one
    instruction clusters as one. An apostrophe inside a word is not a quote mark."""
    for m in QUOTED.finditer(reason):
        text = LABEL.sub("", " ".join(re.sub(r"[*`]", "", next(g for g in m.groups() if g)).split()))
        return re.split(r"(?<=[.!?])\s", text)[0].rstrip(".!? ")[:80]
    return " ".join(reason.split()[:10])


TRIPLE_QUOTED = re.compile(r"^description\s*=\s*(\"\"\"|''')\n?(.*?)\1", re.M | re.S)


def step_descriptions(path, use_tomllib=True):
    """(id, title, description) of each formula step: tomllib on Python 3.11+, else the formula
    layout itself (a triple-quoted description under each [[steps]])."""
    if use_tomllib:
        try:
            import tomllib
        except ImportError:
            tomllib = None
        if tomllib:
            with open(path, "rb") as fh:
                steps = tomllib.load(fh).get("steps", [])
            return [(st.get("id", ""), st.get("title", ""), st.get("description", "")) for st in steps]
    with open(path, encoding="utf-8") as fh:
        blocks = fh.read().split("[[steps]]")[1:]
    out = []
    for b in blocks:
        sid, title = (re.search(r'^%s\s*=\s*"([^"]*)"' % k, b, re.M) for k in ("id", "title"))
        desc = TRIPLE_QUOTED.search(b)
        out.append((sid.group(1) if sid else "", title.group(1) if title else "", desc.group(2) if desc else ""))
    return out


def phrase_offsets(path, phrases, use_tomllib=True):
    """For each phrase, every step whose description holds it: its byte offset, and whether the
    whole phrase sits inside the first 4096 bytes, the part of the step text the grader sees."""
    out = {}
    for p in phrases:
        out[p] = []
        for sid, title, desc in step_descriptions(path, use_tomllib):
            i = desc.find(p)
            if i >= 0:
                start = len(desc[:i].encode())
                out[p].append({"step": sid, "title": title, "description_bytes": len(desc.encode()), "offset": start,
                               "inside_window": start + len(p.encode()) <= 4096})
    return out


def grader_record(path):
    prompt, verdict = first_prompt(path), None
    for rec in read_jsonl(path):
        if rec.get("type") == "assistant":
            verdict = text_of(content_of(rec)) or verdict
    ok, reason = None, ""
    m = re.search(r"\{.*\}", verdict or "", re.S)
    if m:
        try:
            v = json.loads(m.group(0))
            ok, reason = v.get("ok"), v.get("reason", "")
        except ValueError:
            pass
    shown = prompt.split("\n", 1)[1] if "\n" in prompt else prompt
    shown = re.split(r"\nStep description[^\n]*\n", shown, maxsplit=1)[-1].split("\n---", 1)[0]
    title = re.search(r"Step title: (.*)", prompt)
    resp = prompt.split("Assistant response:", 1)[1].split("\n---", 1)[0].strip() if "Assistant response:" in prompt else ""
    evidence = prompt.split("Tool activity for THIS TURN ONLY", 1)[-1] if "Tool activity for THIS TURN ONLY" in prompt else ""
    tools = sorted(set(re.findall(r"\b(Agent|Read|Write|Edit|Bash|Grep|Glob|TaskStop)\(", evidence))
                   | set(re.findall(r"\b(af [a-z]+(?: [a-z-]+)?|git [a-z]+)", evidence)))
    return {"ts": fmt_ts(file_window(path)[0]), "step": title.group(1).strip() if title else "?",
            "truncated": "description truncated" in prompt, "ok": ok, "reason": reason, "shown": shown,
            "response": resp[:150].replace("\n", " "), "tools": tools or (["none"] if "No tool calls were made" in prompt else [])}


def cmd_gate(a):
    """Every grader session of the agent, or with --instances only those inside the runs' windows."""
    evals, wakes, replies = [], collections.Counter(), 0
    if a.instances:
        if not (a.report and a.steps_log):
            sys.exit("gate --instances needs --report and --steps-log")
        scopes = [(d, lo, hi) for x, lo, hi in windows(resolve(a)) for d in x["dirs"]]
    else:
        scopes = [(d, dt.datetime.min, dt.datetime.max) for d in project_dirs(transcript_roots(a.transcripts), a.agent)]
    for d, lo, hi in scopes:
        mains, graders = session_files(d)
        evals += [grader_record(f) for f in graders if lo <= (file_window(f)[0] or dt.datetime.min) < hi]
        for f in mains:
            for rec in read_jsonl(f):
                t = parse_ts(rec.get("timestamp"))
                if not t or not (lo <= t < hi):
                    continue
                c = content_of(rec)
                if rec.get("type") == "assistant":
                    replies += sum(1 for b in c or []
                                   if b.get("type") == "tool_use" and "af mail reply" in json.dumps(b.get("input") or {}))
                elif rec.get("type") == "user" and not is_tool_result(c):
                    m = re.search(r"NEW MAIL from ([\w.-]+)\s+Subject: ([^\\\n]*)", prompt_text(rec))
                    if m and m.group(1) == a.agent:
                        subject = m.group(2).strip()
                        wakes["echo of the agent's own reply" if subject.startswith("Re:") else
                              "gate mail" if subject.startswith(("STEP_FIDELITY", "FIDELITY_ESCALATION")) else "other self-sent mail"] += 1
    flagged = [e for e in evals if e["ok"] is False]
    by_step = collections.defaultdict(lambda: {"evaluations": 0, "flagged": 0, "truncated": 0})
    for e in evals:
        s = by_step[e["step"]]
        s["evaluations"] += 1
        s["flagged"] += e["ok"] is False
        s["truncated"] += e["truncated"]
    clusters = collections.defaultdict(list)
    for e in flagged:
        clusters[directive(e["reason"])].append(e)
    phrases = {p: {step: [sum(p in e["shown"] for e in evals if e["step"] == step), v["evaluations"]]
                   for step, v in by_step.items()} for p in a.phrase}
    window = phrase_offsets(a.formula, a.phrase) if a.formula else {}
    out = {"evaluations": len(evals), "flagged": len(flagged), "by_step": dict(by_step),
           "clusters": {k: len(v) for k, v in clusters.items()}, "formula_window": window,
           "self_sent_wakes": dict(wakes), "af_mail_reply_calls": replies, "phrase_visible": phrases}
    if a.json:
        return out
    for p, hits in window.items():
        where = "; ".join("%s at byte %d of %d (%s the grader's window)" % (
            h["step"], h["offset"], h["description_bytes"], "inside" if h["inside_window"] else "OUTSIDE") for h in hits)
        print("phrase %r in %s: %s" % (p, a.formula, where or "in no step"))
    if not evals:
        print("no grader sessions found for %s (gate off, or transcripts purged)" % a.agent)
        return None
    print("grader evaluations: %d, flagged %d (%s)" % (len(evals), len(flagged), pct(len(flagged), len(evals))))
    table("per step (truncated = the grader saw only the first 4096 bytes of the step text)",
          ["step", "evaluations", "flagged", "truncated"],
          [[k, v["evaluations"], v["flagged"], v["truncated"]] for k, v in sorted(by_step.items(), key=lambda kv: -kv[1]["flagged"])])
    print("\nflag clusters (the directive the grader says was skipped)")
    for k, v in sorted(clusters.items(), key=lambda kv: -len(kv[1])):
        print("  %3d  %s" % (len(v), k))
        for e in v[: a.examples]:
            print("       %s  response: %s\n       %s  tools: %s" % (e["ts"][:16], e["response"], " " * 16, ", ".join(e["tools"]) or "?"))
    for p, steps in phrases.items():
        print("phrase %r inside the text the grader saw: %s" % (p, "; ".join("%s %d/%d" % (k, v[0], v[1]) for k, v in steps.items())))
    print("main-session wakes by self-sent mail: gate mail=%d, echo of the agent's own reply=%d, other=%d; `af mail reply` calls=%d" % (
        wakes["gate mail"], wakes["echo of the agent's own reply"], wakes["other self-sent mail"], replies))
    return None


# ---- command line -------------------------------------------------------------------------

def parser():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)

    def add(name, fn, report=True, agent=False, transcripts=False):
        s = sub.add_parser(name, help=(fn.__doc__ or name).strip().split("\n")[0])
        s.set_defaults(fn=fn)
        if report:
            s.add_argument("--report", required=True, help="af telemetry report --agent AGENT --json")
        if agent:
            s.add_argument("--agent", required=True)
            s.add_argument("--steps-log", required=True, help=".agentfactory/telemetry/steps/AGENT.jsonl")
        if transcripts:
            s.add_argument("--transcripts", action="append", help="transcript projects root (repeatable; default ~/.claude/projects)")
        s.add_argument("--instances", help="comma-separated instance ids to limit to")
        s.add_argument("--json", action="store_true")
        return s

    s = add("steps", cmd_steps)
    s.add_argument("--steps-log", help="group steps by formula step id, so a renamed step stays one row (latest title shown)")
    s = add("runs", cmd_runs)
    s.add_argument("--steps-log", required=True)
    s.add_argument("--step", action="append", default=[], help="step title fragment whose minutes to show (repeatable)")
    s.add_argument("--repo", help="git checkout holding the formula")
    s.add_argument("--formula", action="append", default=[], help="formula path inside --repo (repeatable: source and store copy)")
    s.add_argument("--values", help="per-run series, lines ISO_TIME<TAB>VALUE[<TAB>LABEL]; values outside every run are dropped")
    s = add("children", cmd_children)
    s.add_argument("--child", action="append", required=True, help="NAME=REPORT_JSON of a child agent (repeatable)")
    s = add("tokens", cmd_tokens, agent=True, transcripts=True)
    s.add_argument("--detail", action="store_true", help="one row per sub-agent, and the files they read")
    s.add_argument("--held-file", help="file name whose cost of being kept in sub-agent context to price")
    s = add("time", cmd_time, agent=True, transcripts=True)
    s.add_argument("--step", required=True, help="step title fragment")
    s.add_argument("--subagents", action="store_true", help="attribute in-step sub-agents instead of the main session")
    s.add_argument("--pairs", action="store_true", help="the entries either side of every pause >= 55 s")
    s = add("gate", cmd_gate, report=False, transcripts=True)
    s.add_argument("--agent", required=True)
    s.add_argument("--report", help="with --steps-log and --instances: only those runs' evaluations")
    s.add_argument("--steps-log")
    s.add_argument("--examples", type=int, default=2)
    s.add_argument("--phrase", action="append", default=[], help="text to look for in the step text each grader saw (repeatable)")
    s.add_argument("--formula", help="formula file (e.g. today's text, never run): where each --phrase sits against the 4096-byte window")
    sub.add_parser("selftest", help="check every command against a fixture factory").set_defaults(fn=lambda a: selftest())
    return p


def main(argv=None):
    a = parser().parse_args(argv)
    out = a.fn(a)
    if out is not None:
        json.dump(out, sys.stdout, indent=1, default=sorted)
        print()


# ---- selftest -----------------------------------------------------------------------------

def selftest():
    root = tempfile.mkdtemp(prefix="perfeval-selftest-")
    try:
        checks(*fixture(root))
    finally:
        shutil.rmtree(root, ignore_errors=True)
    print("selftest: all checks passed (Python %s)" % sys.version.split()[0])


def fixture(root):
    def usage(i, cr, cc, o):
        return {"input_tokens": i, "cache_read_input_tokens": cr, "cache_creation_input_tokens": cc, "output_tokens": o}

    def asst(ts, mid, block, u):
        return {"type": "assistant", "timestamp": ts, "message": {"id": mid, "content": [block], "usage": u}}

    def user(ts, content):
        return {"type": "user", "timestamp": ts, "message": {"content": content}}

    def result(ts, tid, content="ok"):
        return user(ts, [{"type": "tool_result", "tool_use_id": tid, "content": content}])

    def write(path, recs):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w") as fh:
            fh.writelines(json.dumps(r) + "\n" for r in recs)

    def write_json(path, obj):
        with open(path, "w") as fh:
            json.dump(obj, fh)

    def t(hms):
        return "2026-01-01T%s.000Z" % hms

    def row(agent, step, status, ms, started, iid):
        return {"agent": agent, "step": "Step: " + step if step != "improvement" else step, "status": status,
                "duration_ms": ms, "started": started, "instance_id": iid}

    sha = lambda text: hashlib.sha256(text.encode()).hexdigest()
    write_json(os.path.join(root, "report.json"), {"rows": [
        row("demo", "Setup", "closed", 60000, "2026-01-01T00:00:00Z", "af-0001"),
        row("demo", "Phase 2: Analysis", "closed", 600000, "2026-01-01T00:01:00Z", "af-0001"),
        row("demo", "Verify", "gate-waiting", 120000, "2026-01-01T00:11:00Z", "af-0001"),
        row("demo", "improvement", "closed", 30000, "", "af-0001"),
        row("demo", "Phase 2: Analysis", "closed", 1200000, "2026-01-01T01:01:00Z", "af-0002"),
        row("demo", "Setup", "INTERRUPTED", 6000000000, "2026-01-01T02:00:00Z", "af-0003")]})
    write_json(os.path.join(root, "kid.json"), {"rows": [
        row("kid", "Work", "closed", 500000, "2026-01-01T00:01:30Z", "af-0100"),
        row("kid", "Work", "closed", 1200000, "2026-01-01T00:59:00Z", "af-0101"),
        row("kid", "Work", "closed", 600000, "2026-01-01T00:00:20Z", "af-0102")]})
    write(os.path.join(root, "steps.jsonl"), [
        {"event": "instance_start", "instance_id": "af-0001", "worktree_id": "wt-aaaaaa", "model": "model-a",
         "formula_digest": sha("# v0\n")},
        {"event": "instance_start", "instance_id": "af-0002", "worktree_id": "wt-bbbbbb", "model": "model-b",
         "formula_digest": sha("# v0\n# v1\n# local edit\n")},
        {"event": "step_end", "instance_id": "af-0001", "host_version": "1.0.0", "subagent_launches": 1, "gate_flags": 1},
        {"event": "step_end", "instance_id": "af-0001", "host_version": "1.0.1", "gate_flags": 0},
        {"event": "instance_start", "instance_id": "af-0003", "worktree_id": "wt-dddddd"},
        {"event": "step_end", "instance_id": "af-0001", "step_title": "Step: Old title", "step_label": "step-x"},
        {"event": "step_end", "instance_id": "af-0002", "step_title": "Step: New title", "step_label": "step-x"}])
    write_json(os.path.join(root, "renamed.json"), {"rows": [
        row("demo", "Old title", "closed", 120000, "2026-01-01T00:00:00Z", "af-0001"),
        row("demo", "New title", "closed", 240000, "2026-01-01T01:00:00Z", "af-0002")]})
    with open(os.path.join(root, "values.tsv"), "w") as fh:
        fh.write("2026-01-01T00:05:00Z\t100\n2026-01-01T01:05:00Z\t1000\n2026-01-01T05:00:00Z\t999\n")
    projects = os.path.join(root, "projects")
    proj = os.path.join(projects, "-f--agentfactory-worktrees-wt-aaaaaa--agentfactory-agents-demo")
    write(os.path.join(proj, "s1.jsonl"), [
        user("2025-12-31T23:59:30.000Z", "session start"),
        asst("2025-12-31T23:59:40.000Z", "m0", {"type": "text", "text": "primed"}, usage(100, 0, 0, 0)),
        user(t("00:00:05"), "start the formula"),
        asst(t("00:00:20"), "m1", {"type": "thinking", "thinking": ""}, usage(10, 1000, 100, 50)),
        asst(t("00:00:25"), "m1", {"type": "tool_use", "id": "u1", "name": "Bash", "input": {"command": "ls"}}, usage(10, 1000, 100, 80)),
        result(t("00:00:30"), "u1"),
        asst(t("00:02:00"), "m2", {"type": "text", "text": "launched"}, usage(5, 2000, 0, 20)),
        {"type": "attachment", "timestamp": t("00:02:05"), "attachment": {"type": "hook_success"}},
        {"type": "queue-operation", "timestamp": t("00:04:50"), "operation": "enqueue"},
        user(t("00:05:00"), "NEW MAIL from demo  Subject: STEP_FIDELITY"),
        asst(t("00:05:10"), "m3", {"type": "tool_use", "id": "u3", "name": "Bash", "input": {"command": "af mail reply x -m evidence"}},
             usage(1, 3000, 0, 30)),
        result(t("00:05:20"), "u3"),
        asst(t("00:05:30"), "m4", {"type": "text", "text": "waiting"}, usage(1, 3000, 0, 10)),
        user(t("00:05:40"), "NEW MAIL from demo  Subject: Re: STEP_FIDELITY"),
        user(t("00:12:00"), "your job is to `af prime`, execute, `af done`, are you doing your job?"),
        asst(t("00:12:10"), "m5", {"type": "text", "text": "verifying"}, usage(1, 4000, 0, 10)),
        user(t("00:14:00"), "Use /improve-agent to improve this agent"),
        asst(t("00:14:10"), "m6", {"type": "text", "text": "improving"}, usage(1, 5000, 0, 100))])
    grader = ("Current step:\nFormula: demo\nStep title: Phase 2: Analysis\n\nStep description (the contract):\n"
              "Spawn ALL THREE. Waiting is in-contract.\n[step description truncated: showing 4096 of 9000 bytes]\n\n---\n\n"
              "Assistant response: Waiting for the analyst.\n\n---\n\nNo tool calls were made in this turn.")
    verdict = lambda ok, reason: {"type": "text", "text": json.dumps({"ok": ok, "reason": reason})}
    write(os.path.join(proj, "g1.jsonl"), [user(t("00:05:01"), grader), asst(t("00:05:03"), "gm1", verdict(
        False, "Principle 3: the step says 'Spawn ALL THREE sub-agents in a SINGLE message' and this turn made no Agent call."),
        usage(2000, 0, 0, 40))])
    verify_grader = grader.replace("Phase 2: Analysis", "Verify").replace(" Waiting is in-contract.", "")
    write(os.path.join(proj, "g2.jsonl"), [user(t("00:12:05"), verify_grader),
                                           asst(t("00:12:07"), "gm2", verdict(True, ""), usage(1000, 0, 0, 10))])
    write(os.path.join(proj, "g7.jsonl"), [user("2025-12-31T20:00:00.000Z", grader),
                                           asst("2025-12-31T20:00:02.000Z", "gm7", verdict(True, ""), usage(500, 0, 0, 10))])
    write(os.path.join(proj, "s0.jsonl"), [user("2025-12-31T20:01:00.000Z", "earlier session"),
                                           user("2025-12-31T20:02:00.000Z", "NEW MAIL from demo  Subject: STEP_FIDELITY")])
    other = os.path.join(projects, "-f--agentfactory-worktrees-wt-cccccc--agentfactory-agents-demo")
    write(os.path.join(other, "g3.jsonl"), [user(t("03:00:00"), grader), asst(t("03:00:02"), "gm3", verdict(
        False, "The step's critical instruction states: "
               "'CRITICAL INSTRUCTION: Spawn ALL THREE sub-agents in a **SINGLE** message. Use Claude Code' "
               "and no Agent call was made."), usage(500, 0, 0, 10))])
    write(os.path.join(other, "g4.jsonl"), [user(t("03:10:00"), grader), asst(t("03:10:02"), "gm4", verdict(
        False, "The step says 'Use Claude Code's Agent tool three times in the SAME response'; none was used."), usage(500, 0, 0, 10))])
    write(os.path.join(other, "g5.jsonl"), [user(t("03:20:00"), grader), asst(t("03:20:02"), "gm5", verdict(
        False, "Step says 'Run this command: `af done --phase-complete`' and it was not run."), usage(500, 0, 0, 10))])
    write(os.path.join(other, "g6.jsonl"), [user(t("03:30:00"), grader), asst(t("03:30:02"), "gm6", verdict(
        False, "Step says 'Run this command: `af mail check`' and it was not run."), usage(500, 0, 0, 10))])
    sub1 = os.path.join(proj, "s1", "subagents", "agent-a1")
    write(sub1 + ".jsonl", [
        user(t("00:01:10"), "You are the lens analyst"),
        asst(t("00:01:30"), "s1", {"type": "tool_use", "id": "r1", "name": "Read", "input": {"file_path": "/x/snapshot.md"}},
             usage(10, 0, 2000, 100)),
        result(t("00:01:31"), "r1", "X" * 4000),
        asst(t("00:05:00"), "s2", {"type": "tool_use", "id": "w1", "name": "Write",
                                   "input": {"file_path": "/x/out.md", "content": "Y" * 2000}},
             usage(5, 3000, 500, 1000)),
        result(t("00:05:01"), "w1"),
        asst(t("00:06:00"), "s3", {"type": "text", "text": "done"}, usage(1, 3500, 0, 50))])
    write_json(sub1 + ".meta.json", {"description": "Lens Analyst"})
    sub2 = os.path.join(proj, "s1", "subagents", "agent-a2")
    write(sub2 + ".jsonl", [
        user(t("00:01:20"), "You are the gap analyst"),
        asst(t("00:02:00"), "q1", {"type": "tool_use", "id": "r2", "name": "Read", "input": {"file_path": "/p/s1/tool-results/abc123.txt"}},
             usage(0, 0, 0, 0)),
        result(t("00:02:01"), "r2", "Z" * 1000),
        asst(t("00:04:00"), "q2", {"type": "text", "text": "done"}, usage(0, 0, 0, 0))])
    write_json(sub2 + ".meta.json", {"description": "Gap Analyst"})
    backup = os.path.join(root, "backup", os.path.basename(proj))
    os.makedirs(backup)
    with open(os.path.join(proj, "s1.jsonl")) as src, open(os.path.join(backup, "s1.jsonl"), "w") as dst:
        dst.writelines(src.readlines()[:5])
    write(os.path.join(backup, "purged.jsonl"), [user(t("00:00:35"), "start"),
                                                  asst(t("00:00:40"), "p1", {"type": "text", "text": "x"}, usage(7, 0, 0, 0))])
    repo = os.path.join(root, "repo")
    os.makedirs(repo)
    def run_git(*args, **env):
        subprocess.run(["git", "-C", repo] + list(args), check=True, capture_output=True, env=dict(os.environ, **env))
    run_git("init", "-q")
    for i, when in enumerate(["2025-12-31T07:00:00-05:00", "2025-12-31T19:30:00-05:00", "2026-01-01T02:00:00+00:00"]):
        with open(os.path.join(repo, "demo.formula.toml"), "a") as fh:
            fh.write("# v%d\n" % i)
        run_git("add", "demo.formula.toml")
        run_git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "change %d" % i,
                GIT_AUTHOR_DATE=when, GIT_COMMITTER_DATE=when)
    return root, projects, proj


def capture(*argv):
    buf = io.StringIO()
    with contextlib.redirect_stdout(buf):
        main(list(argv))
    text = buf.getvalue()
    return json.loads(text) if "--json" in argv else text


def check(name, cond, detail=""):
    if not cond:
        print("FAIL %s\n%s" % (name, str(detail)[-1500:]))
        sys.exit(1)
    print("ok   %s" % name)


def checks(root, projects, proj):
    R, S = os.path.join(root, "report.json"), os.path.join(root, "steps.jsonl")
    common = ["--report", R, "--agent", "demo", "--steps-log", S, "--transcripts", projects]

    p = capture("steps", "--report", R, "--json")
    p2 = [x for x in p["steps"] if x["step"] == "Phase 2: Analysis"][0]
    check("steps: median over closed and gate-waiting spans", p2["median_min"] == 15.0 and p2["runs"] == 2, p2)
    check("steps: longest run named", p2["max_instance"] == "af-0002", p2)
    check("steps: INTERRUPTED excluded", [e["status"] for e in p["excluded"]] == ["INTERRUPTED"], p["excluded"])
    check("steps: rows without a start kept out of the median run",
          p["summary"]["rows_without_start"] == {"improvement": {"runs": 1, "median_min": 0.5}}
          and p["summary"]["median_run_min"] == 18.0, p["summary"])
    renamed = os.path.join(root, "renamed.json")
    check("steps: a renamed step stays one row when the steps log names its step id",
          [(x["step"], x["runs"]) for x in capture("steps", "--report", renamed, "--steps-log", S, "--json")["steps"]] == [("New title", 2)]
          and len(capture("steps", "--report", renamed, "--json")["steps"]) == 2)
    arm = capture("steps", "--report", R, "--instances", "af-0002", "--json")
    check("steps: one arm only", [(x["step"], x["runs"], x["median_min"]) for x in arm["steps"]] == [("Phase 2: Analysis", 1, 20.0)],
          arm["steps"])

    kid = "kid=" + os.path.join(root, "kid.json")
    ch = {(x["parent_step"], x["child"]): x for x in capture("children", "--report", R, "--child", kid, "--json")}
    k = ch.get(("Phase 2: Analysis", "kid"), {})
    check("children: runs tied to the step they ended in", k.get("matched") == 2 and k.get("child_median_min") == 15.0
          and k.get("end_delta_min") == -1.3, ch)
    check("children: a run dispatched in an earlier step is still tied to the step that waited", ("Setup", "kid") not in ch
          and ("Verify", "kid") not in ch, ch)

    tk = capture("tokens", *(common + ["--json"]))
    steps = tk["main_by_step"]
    check("tokens: coverage", tk["coverage"] == {"profiled": 1, "instances": 2}, tk["coverage"])
    check("tokens: final usage per message id", steps.get("Setup", {}).get("ie") == 635, steps)
    check("tokens: per-step attribution", steps.get("Phase 2: Analysis", {}).get("ie") == 1107
          and steps.get("Verify", {}).get("ie") == 451, steps)
    check("tokens: after-last-step tail", steps.get("(after last step)", {}).get("ie") == 1001, steps)
    check("tokens: lead window before the first step", steps.get("(before first step)", {}).get("ie") == 100, steps)
    check("tokens: graders kept apart", tk["graders"] == {"sessions": 2, "ie": 3250}, tk["graders"])
    lens = tk["subagents_by_role"].get("Lens Analyst", {})
    check("tokens: sub-agent role and IE split", lens.get("ie") == 9541 and lens.get("output_ie") == 5750
          and lens.get("cache_create_ie") == 3125, lens)
    check("tokens: total", tk["total_ie"] == 3294 + 3250 + 9541, tk["total_ie"])
    w = tk["wakes"]
    check("tokens: turns by the prompt that started them", w.get("fidelity-gate mail (STEP_FIDELITY)", {}).get("ie") == 802
          and w.get("watchdog nudge", {}).get("ie") == 451 and w.get("improvement hook", {}).get("ie") == 1001
          and w.get("session start (launch or handoff)", {}).get("ie") == 100 and "mail from demo: Re:" in w, w)
    check("tokens: IE by step and wake",
          {"step": "Phase 2: Analysis", "wake": "fidelity-gate mail (STEP_FIDELITY)", "ie": 802} in tk["main_by_step_and_wake"])
    c = tk["centres"]
    check("tokens: centres ranked, summing to TOTAL",
          [x["centre"] for x in c[:2]] == ["sub-agents in Phase 2: Analysis", "fidelity-gate graders"]
          and sum(x["ie"] for x in c) == tk["total_ie"] and c[0]["rank"] == 1 and c[0]["share_pct"] == 59.3, c)
    check("tokens: sub-agents ranked by the step they ran in, every role of it in one row",
          [x["ie"] for x in c if x["kind"] == "in-step sub-agents"] == [9541] and "Gap Analyst" in tk["subagents_by_role"], c)
    check("tokens: a part is dominant only at half its centre", c[0]["dominant"] == "output"
          and [x["dominant"] for x in c if x["centre"] == "Setup"] == ["output"]
          and [x["dominant"] for x in c if x["centre"] == "Phase 2: Analysis"] == ["cache_read"]
          and [x["dominant"] for x in c if x["centre"] == "(after last step)"] == ["mixed"], c)
    check("tokens: gate cost = graders + turns started by gate mail",
          tk["summary"]["gate cost (graders + turns started by gate mail)"] == 4052, tk["summary"])
    both = capture("tokens", *(common[:-2] + ["--transcripts", os.path.join(root, "backup"), "--transcripts", projects, "--json"]))
    check("tokens: roots merged per file, larger copy wins, backup-only session kept",
          both["main_by_step"].get("Setup", {}).get("ie") == 642 and both["total_ie"] == tk["total_ie"] + 7, both["main_by_step"])
    dt_ = capture("tokens", *(common + ["--held-file", "snapshot.md", "--json"]))
    s = [x for x in dt_["subagents"] if x["role"] == "Lens Analyst"][0]
    check("tokens: sub-agent row", s["calls"] == 3 and s["output_tokens"] == 1150 and s["step"] == "Phase 2: Analysis"
          and 40 <= s["write_share_pct"] <= 50 and s["start"] == "2026-01-01T00:01:10Z" and s["end"] == "2026-01-01T00:06:00Z", s)
    check("tokens: held-file cost = cache write once + cache read per later call", s["held_file_ie"] == 1451, s)
    check("tokens: files read listed, harness-saved tool output labelled",
          dt_["files_read"] == {"snapshot.md": 4002, "tool output abc123.txt": 1002},
          dt_["files_read"])

    tm = capture("time", *(common + ["--step", "Phase 2", "--instances", "af-0001", "--pairs", "--json"]))
    m = tm["minutes"]
    check("time: waiting charged to the prompt and the queued-prompt record", m.get("waiting for a prompt") == 3.1, m)
    check("time: hook execution apart from meta", m.get("hook execution") == 0.1 and "meta" not in m, m)
    check("time: model and tool time", "model: tool_use" in m and "tool execution" in m, m)
    pz = tm["instances"][0]["pauses"]
    check("time: pause pairs name both entries",
          any(x["seconds"] == 165 and x["before"].startswith("[hook execution] attachment hook_success")
                                                      and x["after"] == "[waiting for a prompt] queued prompt (enqueue)" for x in pz), pz)
    check("time: coverage",
          capture("time", *(common + ["--step", "Phase 2", "--json"]))["coverage"] == {"profiled": 1, "instances_with_step": 2})
    ts = capture("time", *(common + ["--step", "Phase 2", "--subagents", "--json"]))
    check("time: parallel sub-agents attributed per file and summed",
          ts["minutes"].get("model: tool_use") == 4.5 and ts["agent_minutes"], ts["minutes"])

    g = capture("gate", "--agent", "demo", "--transcripts", projects, "--phrase", "Waiting is in-contract", "--json")
    check("gate: evaluations and flags", g["evaluations"] == 7 and g["flagged"] == 5, g)
    check("gate: one cluster per directive despite apostrophes, labels, markdown and run-on quotes",
          g["clusters"].get("Spawn ALL THREE sub-agents in a SINGLE message") == 2
          and g["clusters"].get("Use Claude Code's Agent tool three times in the SAME response") == 1, g["clusters"])
    check("gate: different commands stay different clusters", g["clusters"].get("Run this command: af done --phase-complete") == 1
          and g["clusters"].get("Run this command: af mail check") == 1, g["clusters"])
    check("gate: echo wakes counted apart from gate mail",
          g["self_sent_wakes"] == {"gate mail": 2, "echo of the agent's own reply": 1} and g["af_mail_reply_calls"] == 1, g)
    toml = os.path.join(root, "demo-steps.formula.toml")
    with open(toml, "w", encoding="utf-8") as fh:
        fh.write('description = """top-level text, not a step"""\n\n'
                 '[[steps]]\nid = "first"\ntitle = "First"\ndescription = """\nWaiting is in-contract.\n"""\n\n'
                 "[[steps]]\nid = \"second\"\ntitle = \"Second\"\ndescription = '''\n" + "x" * 4088 + "— Waiting is in-contract.\n'''\n")
    expect = {"Waiting is in-contract": [
        {"step": "first", "title": "First", "description_bytes": 24, "offset": 0, "inside_window": True},
        {"step": "second", "title": "Second", "description_bytes": 4116, "offset": 4092, "inside_window": False}]}
    gw = capture("gate", "--agent", "demo", "--transcripts", projects, "--formula", toml, "--phrase", "Waiting is in-contract", "--json")
    check("gate: phrase position in a formula file against the grader's window", gw["formula_window"] == expect, gw["formula_window"])
    plain_read = phrase_offsets(toml, ["Waiting is in-contract"], use_tomllib=False)
    check("gate: the formula layout reads the same without tomllib", plain_read == expect, plain_read)
    gi = capture("gate", "--agent", "demo", "--transcripts", projects, "--report", R, "--steps-log", S, "--instances", "af-0001", "--json")
    check("gate: limited to the selected runs' windows", gi["evaluations"] == 2 and gi["flagged"] == 1
          and gi["self_sent_wakes"] == {"gate mail": 1, "echo of the agent's own reply": 1}, gi)
    check("gate: phrase visibility in the text each grader saw",
          g["phrase_visible"]["Waiting is in-contract"] == {"Phase 2: Analysis": [6, 6], "Verify": [0, 1]}, g["phrase_visible"])

    repo = os.path.join(root, "repo")
    rn = capture("runs", "--report", R, "--steps-log", S, "--step", "Phase 2", "--repo", repo, "--formula", "demo.formula.toml",
                 "--values", os.path.join(root, "values.tsv"), "--json")
    rs = {r["instance"]: r for r in rn["runs"]}
    check("runs: identity and counts from telemetry alone", rs["af-0001"]["model"] == "model-a" and rs["af-0001"]["host"] == "1.0.0,1.0.1"
          and rs["af-0001"]["subagent_launches"] == 1 and rs["af-0001"]["gate_flags"] == 1 and rs["af-0001"]["span_min"] == 13.0
          and rs["af-0002"]["steps"]["Phase 2"] == 20.0 and rs["af-0002"]["subagent_launches"] is None, rs)
    check("runs: instances that closed no step listed", rn["unclosed"] == ["af-0003"], rn["unclosed"])
    check("runs: values kept per run inside its window, others dropped", rs["af-0001"]["value"] == 100 and rs["af-0002"]["value"] == 1000
          and rn["values_dropped"] == 1, rn)
    texts = {x["commit"]: x for x in rn["texts"]}
    v0 = [x for x in rn["texts"] if x["runs"] == ["af-0001"]]
    check("runs: run matched to the commit whose text it ran, commit time in UTC", v0 and v0[0]["commit_time"] == "2025-12-31T12:00:00Z"
          and v0[0]["models"] == {"model-a": 1} and v0[0]["median_min"]["Phase 2"] == 10.0, rn["texts"])
    check("runs: uncommitted text reported", texts.get("uncommitted", {}).get("runs") == ["af-0002"], rn["texts"])
    check("runs: today's text never ran",
          rn["today"] == [{"path": "demo.formula.toml", "digest": sha8("# v0\n# v1\n# v2\n"), "runs": 0}], rn["today"])
    check("runs: commits after the last run listed",
          [x["subject"] for x in rn["commits_after_last_run"]] == ["change 2"], rn["commits_after_last_run"])

    plain = os.path.join(root, "not-a-checkout")
    os.makedirs(plain)
    check("runs: a factory root without git history still compares digests", "cannot be recovered" in capture(
        "runs", "--report", R, "--steps-log", S, "--repo", plain, "--formula", "demo.formula.toml"))
    for name, argv in (("steps", ["--report", R]), ("children", ["--report", R, "--child", "kid=" + os.path.join(root, "kid.json")]),
                       ("tokens", common + ["--detail", "--held-file", "snapshot.md"]), ("time", common + ["--step", "Phase 2", "--pairs"]),
                       ("gate", ["--agent", "demo", "--transcripts", projects, "--phrase", "Waiting"]),
                       ("runs", ["--report", R, "--steps-log", S, "--repo", repo, "--formula", "demo.formula.toml", "--step", "Phase 2"])):
        check("%s: text output renders" % name, len(capture(name, *argv)) > 50)


def sha8(text):
    return hashlib.sha256(text.encode()).hexdigest()[:8]


if __name__ == "__main__":
    main()
