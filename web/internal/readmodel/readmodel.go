// Package readmodel is the C3 honest read-model for the web module.
//
// It assembles an AgentView per agent from Phase 0's `af agents list --json` (whose `status`
// is ALREADY honestly derived — internal/cmd/agents.go:249-266) plus the web module's own raw
// `tmux list-sessions` liveness probe. It surfaces that honest status faithfully and adds its
// own AssembledAt stamp (the source of the Floor's "updated Ns ago" staleness clock). It never
// reports a running-but-formula-less agent as "working", and it cross-checks liveness: a
// session that is not actually live renders Stopped, never Working.
//
// Two honesty invariants:
//   - status is INHERITED from Phase 0, never re-derived off running==true alone (must-not-regress).
//   - reads branch on the JSON .state shape (error envelope), never on the process exit code —
//     af read commands always exit 0 and encode failure as {"state":"error","error":"…"}.
package readmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// AgentsLister yields the raw stdout of `af agents list --json`. exec.Wrapper satisfies it.
type AgentsLister interface {
	AgentsListJSON(ctx context.Context) (string, error)
}

// Liveness yields the current raw tmux session names. The web module re-implements this rather
// than importing internal/tmux (compiler-enforced decoupling).
type Liveness interface {
	Sessions(ctx context.Context) ([]string, error)
}

// agentListItem re-implements the Phase-0 contract (internal/cmd/agents.go:70-82). It is bound
// to the real 11-key JSON shape, including step_id (which the outline's AgentView prose omits)
// and the deliberate is_gate(omitempty)/gate_id(no-omitempty) asymmetry.
type agentListItem struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Formula   string            `json:"formula"`
	Running   bool              `json:"running"`
	Status    string            `json:"status"`
	StepID    string            `json:"step_id"`
	StepTitle string            `json:"step_title"`
	StepState string            `json:"step_state"`
	IsGate    bool              `json:"is_gate,omitempty"`
	GateID    string            `json:"gate_id"`
	Inputs    map[string]string `json:"inputs"`
}

// errorEnvelope is the af read-command failure shape ({"state":"error","error":"…"}).
type errorEnvelope struct {
	State string `json:"state"`
	Error string `json:"error"`
}

// AgentView is the honest, UI-facing projection of one agent.
type AgentView struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Formula is the RUNNING (most-recent formula-instance) formula, bead-derived, for the Floor's
	// honest status. It is DELIBERATELY DISTINCT from the DECLARED formula in agents.json that the
	// Sling form resolves via config.Service.AgentFormula (#455). Do NOT unify these fields.
	Formula string `json:"formula"`

	Running     bool              `json:"running"`
	Status      string            `json:"status"`
	StepID      string            `json:"step_id"`
	StepTitle   string            `json:"step_title"`
	StepState   string            `json:"step_state"`
	IsGate      bool              `json:"is_gate"`
	GateID      string            `json:"gate_id"`
	Inputs      map[string]string `json:"inputs"`
	AssembledAt time.Time         `json:"assembled_at"`
}

// ReadModel assembles AgentViews from the agents lister + the liveness probe.
type ReadModel struct {
	agents AgentsLister
	live   Liveness
	now    func() time.Time
}

// New builds a ReadModel over the given sources.
func New(a AgentsLister, l Liveness) *ReadModel {
	return &ReadModel{agents: a, live: l, now: time.Now}
}

// Assemble produces the honest agent views. It branches on the JSON .state (error envelope),
// surfaces Phase-0's honest status, and cross-checks each agent against the live tmux session
// set so a dead session can never read as Working.
func (rm *ReadModel) Assemble(ctx context.Context) ([]AgentView, error) {
	raw, err := rm.agents.AgentsListJSON(ctx)
	if err != nil {
		return nil, fmt.Errorf("agents list: %w", err)
	}
	trimmed := strings.TrimSpace(raw)

	// Branch on .state, not exit code: an error envelope is a JSON object, success is an array.
	if strings.HasPrefix(trimmed, "{") {
		var env errorEnvelope
		if jerr := json.Unmarshal([]byte(trimmed), &env); jerr == nil && env.State == "error" {
			return nil, fmt.Errorf("upstream read failed: %s", env.Error)
		}
		return nil, fmt.Errorf("unexpected agents list payload")
	}

	var items []agentListItem
	if jerr := json.Unmarshal([]byte(trimmed), &items); jerr != nil {
		return nil, fmt.Errorf("decode agents list: %w", jerr)
	}

	// Liveness is best-effort: TmuxLiveness already maps the benign no-server / no-tmux
	// case to (nil, nil), so a non-nil error here is an UNEXPECTED probe failure (tmux
	// crash, socket perms, unrecognized stderr). Silently leaving sessionSet nil would
	// force EVERY agent to "stopped" and lie that running agents are dead — tempting an
	// operator to reset agents that are actively running formulas. Instead, record the
	// probe failure and fall back to Phase-0's honest status below.
	var sessionSet map[string]bool
	liveProbeOK := true
	if rm.live != nil {
		sessions, lerr := rm.live.Sessions(ctx)
		if lerr == nil {
			sessionSet = toSet(sessions)
		} else {
			liveProbeOK = false
		}
	}

	stamp := rm.now()
	views := make([]AgentView, 0, len(items))
	for _, it := range items {
		live := sessionSet[sessionName(it.Name)]
		status := safeStatus(it)
		running := live
		switch {
		case !liveProbeOK:
			// Our liveness probe failed unexpectedly: trust Phase-0's honest running
			// value rather than overriding the status to "stopped" for every agent.
			running = it.Running
		case !live:
			// Our own fresh probe succeeded and says the session is gone: it cannot be Working.
			status = "stopped"
		}
		views = append(views, AgentView{
			Name:        it.Name,
			Type:        it.Type,
			Formula:     it.Formula,
			Running:     running,
			Status:      status,
			StepID:      it.StepID,
			StepTitle:   it.StepTitle,
			StepState:   it.StepState,
			IsGate:      it.IsGate || it.GateID != "",
			GateID:      it.GateID,
			Inputs:      it.Inputs,
			AssembledAt: stamp,
		})
	}
	return views, nil
}

// safeStatus surfaces Phase-0's honest status faithfully. When status is present it is used
// verbatim (Phase 0 owns the honesty derivation). When absent, it derives conservatively and
// NEVER returns "working" off running/step_state alone — the must-not-regress guard.
func safeStatus(it agentListItem) string {
	if it.Status != "" {
		return it.Status
	}
	switch it.StepState {
	case "blocked":
		return "blocked"
	case "ready":
		if it.IsGate || it.GateID != "" {
			return "gated"
		}
		return "idle" // conservative: do not claim Working in the fallback path
	default: // no_formula, all_complete, error, empty
		return "idle"
	}
}

// sessionName re-implements internal/session/names.go: "af-" + trimmed agent name.
func sessionName(agent string) string {
	return "af-" + strings.TrimRight(strings.TrimSpace(agent), "/")
}

func toSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s != "" {
			set[s] = true
		}
	}
	return set
}
