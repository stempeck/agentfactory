package templates

// MemoryProtocolSection is the externalization protocol carried, identically, by every role
// template (#515). It lives here because this package owns role-template text and both consumers
// can reach it: internal/cmd's generateAgentTemplate appends it to the 40 machine-made templates,
// and the sweep in templates_test.go holds the 42 committed files against it — including the two
// hand-authored built-ins, whose copies are mirrored by hand and have nothing else to be checked
// against.
//
// Editing this const alone does not ship the change: the committed .md.tmpl files are the artifact
// agents read, so the sweep goes red until they are regenerated and the built-ins re-mirrored.
// That redness is the point.
//
// {{ .Role }} is a live template action, not documentation. It renders to the agent's own name so
// the vault path names a directory that agent can actually open — a section that told an agent to
// look in <your-role>/ would be one more instruction it has to translate before it can act, which
// is the failure this whole phase exists to remove.
const MemoryProtocolSection = "## Memory Protocol\n\n" +
	"Your learnings vault at `.agentfactory/memory/{{ .Role }}/` outlives this session, your worktree, and every teardown path — it is the one place durable state survives without operator archaeology.\n\n" +
	"- Record a learning the moment you earn it: `af memory add -s \"<subject>\" -m \"<what you learned>\" --type gotcha` (types: `gotcha`, `model-behavior`, `ops`, `outcome`, `improvement`).\n" +
	"- Read before you re-derive: `af memory list`, then `af memory show <id>` for the full note. `af memory check --inject` already serves your own notes at session start.\n" +
	"- Close the loop when a learning lands somewhere durable: `af memory graduate <id> --to commit:<sha>` (also `issue#N`, `pr#N`, `doc:<path>`, `formula:<name>`). When it stops being true: `af memory expire <id>`.\n" +
	"- Notes are append-only and there is no delete verb — graduating or expiring one stops it costing you context without destroying the record.\n" +
	"- `af memory status` reports what the vault holds and what is due for graduation.\n"
