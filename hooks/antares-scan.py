#!/usr/bin/env python3
"""antares-scan.py — native driver for the antares-1b vulnerability-localization model.

This is NOT a Claude Code hook. It is a factory-shipped TOOL invoked by the
fable-secure formula (Stage 2) as an optional candidate-generation scanner. It
runs the antares-1b model in its OWN trained harness — a terminal agent that
issues read-only shell commands and terminates by submitting a list of
candidate-vulnerable files — against a local LM Studio (or any OpenAI-compatible)
endpoint on the host. Nothing about the model runs in the container: the container
sends JSON to host.docker.internal and executes the model's read-only commands
against the target repo on its behalf.

Design invariants (these are load-bearing, do not relax):
  * The model's output is UNTRUSTED. Its `terminal` commands are executed only
    through a read-only whitelist, argv-exec'd (never a shell), confined to the
    target repo, with shell metacharacters refused outright.
  * The driver produces CANDIDATE SIGNAL, never a finding. A "clean" result is
    not evidence of safety. The formula re-derives every candidate with file:line
    evidence downstream.
  * Zero third-party dependencies — Python standard library only.

Exit codes:
  0  ran to completion (candidates found, clean, or incomplete — see the artifact)
  2  usage error (bad arguments)
  3  tool absent (endpoint unreachable or model not loaded) — the formula treats
     this as "record tool absent, fall back to the manual sweep, proceed"
  1  unexpected runtime error
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import subprocess
import sys
import time
import urllib.error
import urllib.request

# --------------------------------------------------------------------------- #
# Configuration (env-overridable; sane container defaults)
# --------------------------------------------------------------------------- #
BASE_URL = os.environ.get("ANTARES_BASE_URL", "http://host.docker.internal:11434").rstrip("/")
MODEL = os.environ.get("ANTARES_MODEL", "antares-1b")
API_KEY = os.environ.get("ANTARES_API_KEY", "lmstudio")
MAX_CALLS = int(os.environ.get("ANTARES_MAX_CALLS", "15"))          # model card: 15 terminal calls
HTTP_TIMEOUT = int(os.environ.get("ANTARES_HTTP_TIMEOUT", "120"))
CMD_TIMEOUT = int(os.environ.get("ANTARES_CMD_TIMEOUT", "20"))
MAX_OUTPUT = int(os.environ.get("ANTARES_MAX_OUTPUT", "8000"))      # chars per tool result
TEMPERATURE = float(os.environ.get("ANTARES_TEMPERATURE", "0.3"))   # model card recommendation
TOP_P = float(os.environ.get("ANTARES_TOP_P", "1.0"))

# Read-only command whitelist. argv[0] must be one of these. We exec argv
# directly (no shell), so pipes/redirects/substitution are never interpreted;
# we ALSO reject the metacharacters below so a whitelisted tool can't receive a
# redirect target as a positional arg and so the model gets an honest error.
READONLY_CMDS = {
    "grep", "egrep", "fgrep", "rg", "find", "cat", "ls", "head", "tail", "wc",
    "sed", "awk", "file", "stat", "tree", "sort", "uniq", "cut", "dirname",
    "basename", "realpath", "nl", "strings", "cksum", "md5sum", "sha256sum",
}
SHELL_METACHARS = re.compile(r"[;&|`\n\r]|\$\(|\$\{|>>|<<|(?<!\d)>|<")

# Per-tool escape hatches that turn a "read-only" tool into a writer/executor.
FORBIDDEN_ARG_TOKENS = {
    "find": {"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint",
             "-fprintf", "-fprint0"},
    "sed": {"-i", "--in-place"},
    "awk": {"-i", "--in-place", "inplace"},
    "rg": {"--pre", "--hostname-bin", "--search-zip"},  # --pre runs a program
}
# Substrings that indicate a write/exec even without a distinct token.
FORBIDDEN_SUBSTR = {
    "sed": ("w ", "e ", "W ", "R ", "s/e"),   # sed w/e/W/R commands write or exec
    "awk": ("system(", "print >", "printf >", "| \"", "\" |", "> \""),
}

# Directories the model should never crawl (noise + never expose the private
# security workspace or VCS internals to a 1B model).
DEFAULT_EXCLUDES = [
    ".git", ".agentfactory", ".security", ".runtime", "node_modules",
    ".venv", "venv", "__pycache__", "dist", "build", ".next", "target",
]


def eprint(*a: object) -> None:
    print(*a, file=sys.stderr, flush=True)


# --------------------------------------------------------------------------- #
# HTTP
# --------------------------------------------------------------------------- #
def _post_json(url: str, payload: dict, timeout: int) -> dict:
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(url, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", f"Bearer {API_KEY}")
    req.add_header("x-api-key", API_KEY)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _get_json(url: str, timeout: int) -> dict:
    req = urllib.request.Request(url, method="GET")
    req.add_header("Authorization", f"Bearer {API_KEY}")
    req.add_header("x-api-key", API_KEY)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def preflight() -> tuple[bool, str]:
    """Return (ok, detail). ok=False means the tool is absent (exit 3)."""
    try:
        models = _get_json(f"{BASE_URL}/v1/models", timeout=10)
    except (urllib.error.URLError, OSError, ValueError) as e:
        return False, f"endpoint {BASE_URL} unreachable: {e}"
    ids = [m.get("id", "") for m in models.get("data", [])]
    # Accept an exact id or a prefix match (LM Studio may report a longer key).
    if MODEL in ids or any(mid == MODEL or mid.startswith(MODEL) or MODEL in mid for mid in ids):
        return True, f"model '{MODEL}' present at {BASE_URL}"
    return False, (f"model '{MODEL}' not loaded at {BASE_URL} "
                   f"(available: {', '.join(ids) or 'none'})")


# --------------------------------------------------------------------------- #
# Sandbox for the model's `terminal` tool
# --------------------------------------------------------------------------- #
def _within_repo(path: str, repo_root: str) -> bool:
    resolved = os.path.realpath(os.path.join(repo_root, path))
    root = os.path.realpath(repo_root) + os.sep
    return (resolved + os.sep).startswith(root) or resolved == os.path.realpath(repo_root)


def run_terminal(command: str, repo_root: str) -> str:
    """Execute a single read-only command, confined to repo_root. Returns a
    result string safe to feed back to the model. Never raises."""
    command = (command or "").strip()
    if not command:
        return "ERROR: empty command."
    if SHELL_METACHARS.search(command):
        return ("ERROR: shell metacharacters (; & | ` newline $() ${} > <) are not "
                "permitted. Issue ONE read-only command with plain arguments.")
    try:
        argv = shlex.split(command)
    except ValueError as e:
        return f"ERROR: could not parse command: {e}"
    if not argv:
        return "ERROR: empty command."

    prog = os.path.basename(argv[0])
    if prog not in READONLY_CMDS:
        return (f"ERROR: '{prog}' is not permitted. This is a READ-ONLY inspection "
                f"sandbox. Allowed: {', '.join(sorted(READONLY_CMDS))}.")

    lowered = [a.lower() for a in argv]
    for bad in FORBIDDEN_ARG_TOKENS.get(prog, set()):
        if bad in lowered:
            return f"ERROR: '{bad}' is not permitted with {prog} (write/exec vector)."
    for sub in FORBIDDEN_SUBSTR.get(prog, ()):  # substring guard for sed/awk
        if sub in command:
            return f"ERROR: '{sub.strip()}' is not permitted with {prog} (write/exec vector)."

    # Path confinement: any absolute path, or any arg escaping the repo via .., is refused.
    for a in argv[1:]:
        if a.startswith("-"):
            continue
        if os.path.isabs(a) or ".." in a.split(os.sep):
            if not _within_repo(a, repo_root):
                return (f"ERROR: path '{a}' is outside the assessment repository. "
                        f"Use paths relative to the repo root.")

    env = {"PATH": "/usr/bin:/bin:/usr/local/bin", "LC_ALL": "C", "HOME": repo_root}
    try:
        proc = subprocess.run(
            argv, cwd=repo_root, env=env, timeout=CMD_TIMEOUT,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, errors="replace",
        )
    except subprocess.TimeoutExpired:
        return f"ERROR: command timed out after {CMD_TIMEOUT}s."
    except (OSError, ValueError) as e:
        return f"ERROR: could not execute: {e}"

    out = proc.stdout or ""
    if len(out) > MAX_OUTPUT:
        out = out[:MAX_OUTPUT] + f"\n... [truncated at {MAX_OUTPUT} chars]"
    if not out.strip():
        out = f"(exit {proc.returncode}, no output)"
    return out


# --------------------------------------------------------------------------- #
# Tool-call extraction (structured OpenAI OR antares-native <tool_call> XML)
# --------------------------------------------------------------------------- #
_XML_TOOLCALL = re.compile(r"<tool_call>\s*(\{.*?\})\s*</tool_call>", re.DOTALL)


def extract_tool_call(message: dict) -> tuple[str, dict, str | None]:
    """Return (name, arguments, tool_call_id).
    tool_call_id is set only for the structured OpenAI path (drives the reply
    message shape). Returns ("", {}, None) if no tool call is present."""
    # 1. Structured OpenAI tool_calls
    tcs = message.get("tool_calls") or []
    if tcs:
        tc = tcs[0]
        fn = tc.get("function", {})
        name = fn.get("name", "")
        raw = fn.get("arguments", "{}")
        try:
            args = json.loads(raw) if isinstance(raw, str) else (raw or {})
        except ValueError:
            args = {}
        return name, args, tc.get("id")

    content = message.get("content") or ""
    if isinstance(content, list):  # some servers return content as blocks
        content = " ".join(b.get("text", "") for b in content if isinstance(b, dict))

    # 2. antares-native <tool_call>{...}</tool_call>
    m = _XML_TOOLCALL.search(content)
    if m:
        try:
            obj = json.loads(m.group(1))
            return obj.get("name", ""), obj.get("arguments", {}) or {}, None
        except ValueError:
            pass

    # 3. Bare JSON object with name/arguments somewhere in content
    m = re.search(r'\{[^{}]*"name"\s*:\s*"[^"]+".*?\}', content, re.DOTALL)
    if m:
        try:
            obj = json.loads(m.group(0))
            return obj.get("name", ""), obj.get("arguments", {}) or {}, None
        except ValueError:
            pass

    return "", {}, None


def normalize_file_list(args: dict) -> list[str]:
    """submit_vulnerable_files argument shape varies; accept the common keys."""
    for key in ("files", "file_paths", "paths", "vulnerable_files", "filenames"):
        v = args.get(key)
        if isinstance(v, list):
            return [str(x).strip() for x in v if str(x).strip()]
        if isinstance(v, str) and v.strip():
            return [p.strip() for p in re.split(r"[,\n]", v) if p.strip()]
    # Some models pass the bare list as the whole arguments object.
    if isinstance(args, list):
        return [str(x).strip() for x in args if str(x).strip()]
    return []


# --------------------------------------------------------------------------- #
# System prompt (antares' trained framing)
# --------------------------------------------------------------------------- #
def build_system_prompt(repo_root: str, cwe_hint: str, excludes: list[str]) -> str:
    return (
        "You are antares, an autonomous security agent that localizes vulnerable "
        "code in a repository. You work by issuing read-only terminal commands to "
        "explore the code, then submit the files you believe contain a vulnerability.\n\n"
        f"Repository root (your working directory): {repo_root}\n"
        f"Focus (CWE / weakness categories): {cwe_hint or 'any exploitable weakness'}\n"
        f"Ignore these directories: {', '.join(excludes)}\n\n"
        "Rules:\n"
        f"- You have at most {MAX_CALLS} terminal calls. Spend them narrowing to the "
        "most likely vulnerable files.\n"
        "- The terminal is READ-ONLY: only grep/rg/find/cat/ls/head/tail/sed(read)/awk"
        "(read)/wc and similar inspection tools. No writes, no pipes, no redirection, "
        "one command per call. Use paths relative to the repo root.\n"
        "- When you have identified the vulnerable file(s), call submit_vulnerable_files "
        "with their paths. If after investigating you find nothing, call "
        "submit_no_vulnerability_found. You MUST end by calling one of these two.\n"
        "- Report file-level localization only; do not attempt to write or run exploits."
    )


def build_tools() -> list[dict]:
    return [
        {
            "type": "function",
            "function": {
                "name": "terminal",
                "description": "Run one read-only shell command in the repository to inspect code.",
                "parameters": {
                    "type": "object",
                    "properties": {
                        "command": {"type": "string", "description": "A single read-only command."}
                    },
                    "required": ["command"],
                },
            },
        },
        {
            "type": "function",
            "function": {
                "name": "submit_vulnerable_files",
                "description": "Submit the list of files believed to contain a vulnerability, ending the task.",
                "parameters": {
                    "type": "object",
                    "properties": {
                        "files": {
                            "type": "array",
                            "items": {"type": "string"},
                            "description": "Repo-relative paths of vulnerable files.",
                        }
                    },
                    "required": ["files"],
                },
            },
        },
        {
            "type": "function",
            "function": {
                "name": "submit_no_vulnerability_found",
                "description": "Declare the repository clean, ending the task.",
                "parameters": {"type": "object", "properties": {}},
            },
        },
    ]


# --------------------------------------------------------------------------- #
# Artifact
# --------------------------------------------------------------------------- #
def write_artifact(out_path: str, *, status: str, files: list[str], calls_used: int,
                   transcript: list[str], detail: str, repo_root: str, cwe_hint: str) -> None:
    os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
    stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    lines = [
        "# Stage 2 — antares-1b candidate localization (pre-pass)",
        "",
        f"- Generated: {stamp}",
        f"- Model: `{MODEL}` @ `{BASE_URL}`",
        f"- Repository: `{repo_root}`",
        f"- CWE / focus hint: {cwe_hint or '(broad)'}",
        f"- Terminal calls used: {calls_used}/{MAX_CALLS}",
        f"- Status: **{status}**",
        "",
        "> **UNVERIFIED CANDIDATE SIGNAL — NOT A FINDING.** This is the output of a 1B "
        "detection model run in its own harness. Per this program's doctrine, *detection "
        "is a failing grade* and *green means secure is false*: every file below is a LEAD "
        "to confirm with file:line evidence in the manual lens sweep, and an empty/clean "
        "result is NOT proof of safety. Nothing here closes a gap or files an issue on its own.",
        "",
    ]
    if detail:
        lines += [f"_{detail}_", ""]
    if status == "candidates" and files:
        lines.append("## Candidate files (confirm each — lower bound, sweep for more)")
        lines += [f"- [ ] `{f}` — UNVERIFIED; confirm vulnerability with file:line" for f in files]
    elif status == "clean":
        lines.append("## Result: model reported no candidates")
        lines.append("Treat as UNPROVEN, not safe. The manual lens sweep proceeds in full.")
    elif status == "disabled":
        lines.append("## antares candidate pre-pass not run: disabled for this run")
        lines.append("`antares_scan` was not enabled. The manual lens sweep is unaffected.")
    elif status == "absent":
        lines.append("## antares candidate pre-pass not run: tool absent")
        lines.append("The model/endpoint was unreachable or the driver is not installed. "
                     "Falling back to the manual lens sweep (Failure Modes: recorded, not skipped).")
    else:  # incomplete
        lines.append("## Result: incomplete")
        lines.append("The scan did not terminate via a submit call within the call budget. "
                     "Any candidates gathered are listed above; treat coverage as partial.")
        if files:
            lines += [f"- [ ] `{f}` — UNVERIFIED (partial run)" for f in files]

    if transcript:
        lines += ["", "## Command transcript (for audit)", "```"]
        lines += transcript
        lines += ["```"]

    machine = {"status": status, "files": files, "calls_used": calls_used,
               "model": MODEL, "endpoint": BASE_URL, "generated": stamp}
    lines += ["", "## Machine-readable summary", "```json",
              json.dumps(machine, indent=2), "```", ""]
    with open(out_path, "w", encoding="utf-8") as fh:
        fh.write("\n".join(lines))


# --------------------------------------------------------------------------- #
# Agent loop
# --------------------------------------------------------------------------- #
def run_scan(repo_root: str, cwe_hint: str, out_path: str, excludes: list[str]) -> int:
    system = build_system_prompt(repo_root, cwe_hint, excludes)
    tools = build_tools()
    messages: list[dict] = [
        {"role": "system", "content": system},
        {"role": "user", "content":
            "Begin. Explore the repository with terminal commands and localize any "
            "vulnerable files. End by calling submit_vulnerable_files or "
            "submit_no_vulnerability_found."},
    ]
    transcript: list[str] = []
    candidates: list[str] = []
    calls_used = 0

    for _ in range(MAX_CALLS + 2):  # +2 headroom for a final submit turn
        try:
            resp = _post_json(
                f"{BASE_URL}/v1/chat/completions",
                {"model": MODEL, "messages": messages, "tools": tools,
                 "tool_choice": "auto", "temperature": TEMPERATURE, "top_p": TOP_P,
                 "max_tokens": 1024},
                timeout=HTTP_TIMEOUT,
            )
        except (urllib.error.URLError, OSError, ValueError) as e:
            write_artifact(out_path, status="absent", files=candidates, calls_used=calls_used,
                           transcript=transcript, detail=f"request failed mid-scan: {e}",
                           repo_root=repo_root, cwe_hint=cwe_hint)
            eprint(f"antares-scan: request failed: {e}")
            return 3

        choices = resp.get("choices") or [{}]
        message = choices[0].get("message", {}) or {}
        name, args, tc_id = extract_tool_call(message)

        if name == "terminal":
            if calls_used >= MAX_CALLS:
                messages.append(message)
                _append_tool_reply(messages, tc_id,
                                   "ERROR: terminal call budget exhausted. Submit your findings now.")
                continue
            calls_used += 1
            command = str(args.get("command", "")).strip()
            result = run_terminal(command, repo_root)
            transcript.append(f"$ {command}")
            first = result.splitlines()[0] if result.splitlines() else ""
            transcript.append(f"  -> {first[:200]}")
            messages.append(message)
            _append_tool_reply(messages, tc_id, result)
            continue

        if name == "submit_vulnerable_files":
            files = normalize_file_list(args)
            files = [f for f in files if _within_repo(f, repo_root)]
            write_artifact(out_path, status="candidates" if files else "clean",
                           files=files, calls_used=calls_used, transcript=transcript,
                           detail="", repo_root=repo_root, cwe_hint=cwe_hint)
            eprint(f"antares-scan: {len(files)} candidate file(s) in {calls_used} call(s)")
            return 0

        if name == "submit_no_vulnerability_found":
            write_artifact(out_path, status="clean", files=[], calls_used=calls_used,
                           transcript=transcript, detail="", repo_root=repo_root, cwe_hint=cwe_hint)
            eprint(f"antares-scan: model reported clean in {calls_used} call(s)")
            return 0

        # No recognizable tool call — nudge once, then give up gracefully.
        messages.append(message)
        messages.append({"role": "user", "content":
                         "No tool call detected. Issue a terminal command, or submit your "
                         "result with submit_vulnerable_files / submit_no_vulnerability_found."})

    write_artifact(out_path, status="incomplete", files=candidates, calls_used=calls_used,
                   transcript=transcript, detail="", repo_root=repo_root, cwe_hint=cwe_hint)
    eprint("antares-scan: incomplete (no terminal submit within budget)")
    return 0


def _append_tool_reply(messages: list[dict], tc_id: str | None, content: str) -> None:
    """Reply in the shape that matches how the call was parsed: a role:tool
    message for the structured OpenAI path, else an antares-native
    <tool_response> user turn."""
    if tc_id:
        messages.append({"role": "tool", "tool_call_id": tc_id, "content": content})
    else:
        messages.append({"role": "user",
                         "content": f"<tool_response>\n{content}\n</tool_response>"})


# --------------------------------------------------------------------------- #
# Entry
# --------------------------------------------------------------------------- #
def main() -> int:
    ap = argparse.ArgumentParser(
        description="Run antares-1b as a read-only candidate-vulnerability localizer.")
    ap.add_argument("--repo", required=True, help="Target repository root to scan.")
    ap.add_argument("--out", required=True, help="Path to write the candidate artifact.")
    ap.add_argument("--cwe-hint", default="", help="CWE / weakness focus for the system prompt.")
    ap.add_argument("--exclude", action="append", default=[],
                    help="Extra directory name to exclude (repeatable).")
    args = ap.parse_args()

    repo_root = os.path.realpath(args.repo)
    if not os.path.isdir(repo_root):
        eprint(f"antares-scan: --repo '{args.repo}' is not a directory")
        return 2
    excludes = DEFAULT_EXCLUDES + [e for e in args.exclude if e]

    ok, detail = preflight()
    if not ok:
        write_artifact(args.out, status="absent", files=[], calls_used=0, transcript=[],
                       detail=detail, repo_root=repo_root, cwe_hint=args.cwe_hint)
        eprint(f"antares-scan: tool absent — {detail}")
        return 3

    try:
        return run_scan(repo_root, args.cwe_hint, args.out, excludes)
    except Exception as e:  # never crash the calling formula step
        write_artifact(args.out, status="incomplete", files=[], calls_used=0, transcript=[],
                       detail=f"unexpected error: {e}", repo_root=repo_root, cwe_hint=args.cwe_hint)
        eprint(f"antares-scan: unexpected error: {e}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
