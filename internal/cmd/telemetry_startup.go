package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

const (
	startupStateMeasured      = "measured"
	startupStateNoMeasurement = "no measurement"
)

type startupHistoryDTO struct {
	SessionID      string `json:"session_id"`
	Tokens         int64  `json:"tokens"`
	Budget         int64  `json:"budget"`
	HarnessVersion string `json:"harness_version"`
	At             string `json:"at"`
}

type startupAgentDTO struct {
	Agent          string              `json:"agent"`
	State          string              `json:"state"`
	SessionID      string              `json:"session_id,omitempty"`
	Tokens         *int64              `json:"tokens,omitempty"`
	Budget         *int64              `json:"budget,omitempty"`
	OverBudget     *bool               `json:"over_budget,omitempty"`
	HarnessVersion string              `json:"harness_version,omitempty"`
	RulingsBlock   string              `json:"rulings_block,omitempty"`
	At             string              `json:"at,omitempty"`
	History        []startupHistoryDTO `json:"history,omitempty"`
}

type startupReportDTO struct {
	V      int               `json:"v"`
	State  string            `json:"state"`
	Agents []startupAgentDTO `json:"agents"`
}

func startupReport(cmd *cobra.Command, root string) (startupReportDTO, error) {
	names := startupMeasuredAgents
	if name, _ := cmd.Flags().GetString("agent"); name != "" {
		if !startupMeasured(name) {
			return startupReportDTO{}, fmt.Errorf("--agent %q: the startup turn is measured for manager and supervisor only", name)
		}
		names = []string{name}
	}
	telemetryOn := telemetryFactoryEnabled(root)
	report := startupReportDTO{V: telemetry.SchemaVersion, State: "ok"}
	for _, name := range names {
		if measureStartupTurn(root, name) {
			fmt.Fprintf(cmd.ErrOrStderr(), startupScanCapWarning, name, startupTranscriptScanCap)
		}
		a := startupAgentDTO{Agent: name, State: startupStateNoMeasurement}
		if t, ok := readStartupTurn(root, name); ok {
			over := t.Budget > 0 && t.Tokens > t.Budget
			a.State, a.SessionID, a.Tokens, a.Budget, a.OverBudget = startupStateMeasured, t.SessionID, &t.Tokens, &t.Budget, &over
			a.HarnessVersion, a.RulingsBlock, a.At = t.HarnessVersion, t.RulingsBlock, t.At
		}
		if telemetryOn {
			a.History = startupHistory(root, name)
		}
		report.Agents = append(report.Agents, a)
	}
	return report, nil
}

func startupHistory(root, agent string) []startupHistoryDTO {
	events, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		return nil
	}
	var out []startupHistoryDTO
	for _, ev := range events {
		if ev.Event != telemetry.EventStartupEnd {
			continue
		}
		h := startupHistoryDTO{SessionID: startupUnknown, Budget: ev.CtxBoundTokens, HarnessVersion: startupHarnessToken(ev.HostVersion), At: startupUnknown}
		if statusline.ValidSessionID(ev.SessionID) {
			h.SessionID = ev.SessionID
		}
		if ev.CtxTokensUsed != nil {
			h.Tokens = *ev.CtxTokensUsed
		}
		if _, err := time.Parse(time.RFC3339, ev.TS); err == nil {
			h.At = ev.TS
		}
		out = append(out, h)
	}
	return out
}

func runTelemetryStartup(cmd *cobra.Command, root string) error {
	report, err := startupReport(cmd, root)
	if err != nil {
		fmt.Printf("telemetry startup: state: error (%v)\n", err)
		return nil
	}
	for _, a := range report.Agents {
		if a.State != startupStateMeasured {
			fmt.Printf("%s: state: %s\n", a.Agent, a.State)
			continue
		}
		verdict := "within budget"
		if *a.OverBudget {
			verdict = "over budget"
		}
		fmt.Printf("%s: %d tokens at the first Stop, budget %d, %s; harness %s; rulings_block: %s; session %s; at %s\n",
			a.Agent, *a.Tokens, *a.Budget, verdict, a.HarnessVersion, a.RulingsBlock, a.SessionID, a.At)
	}
	return nil
}

func emitTelemetryStartupJSON(cmd *cobra.Command, root string) error {
	report, err := startupReport(cmd, root)
	if err != nil {
		return emitTelemetryJSONError(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		return emitTelemetryJSONError(err)
	}
	fmt.Println(string(data))
	return nil
}
