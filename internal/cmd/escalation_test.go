package cmd

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/session"
)

// None of these may call t.Parallel: they reassign escalationMailFn, sendHandoffMail and newCmdTmux.

type escalationSend struct{ root, to, subject, body string }

type escalationRecorder struct {
	sent    []escalationSend
	handoff []recordedMail
	err     error
}

func (r *escalationRecorder) install(t *testing.T) *escalationRecorder {
	t.Helper()
	origEscalation, origHandoff := escalationMailFn, sendHandoffMail
	escalationMailFn = func(root, to, subject, body string) error {
		r.sent = append(r.sent, escalationSend{root, to, subject, body})
		return r.err
	}
	sendHandoffMail = func(agentName, subject, body string) error {
		r.handoff = append(r.handoff, recordedMail{agentName, subject, body})
		return nil
	}
	t.Cleanup(func() { escalationMailFn, sendHandoffMail = origEscalation, origHandoff })
	return r
}

// escalationFactory is a bare factory whose roster holds the named agents, whose startup.json
// lists startup, and whose tmux reports a session for each name in live.
func escalationFactory(t *testing.T, roster, startup, live []string) string {
	t.Helper()
	root := t.TempDir()
	configDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agents := map[string]any{}
	for _, name := range roster {
		agents[name] = map[string]string{"type": "autonomous", "description": "x"}
	}
	writeTestJSON(t, filepath.Join(configDir, "factory.json"), map[string]any{"type": "factory", "version": 1, "name": "f"})
	writeTestJSON(t, filepath.Join(configDir, "agents.json"), map[string]any{"agents": agents})
	if startup != nil {
		writeTestJSON(t, filepath.Join(configDir, "startup.json"), map[string]any{"agents": startup})
	}

	fake := newFakeTmux()
	for _, name := range live {
		fake.present[session.SessionName(name)] = true
	}
	origTmux := newCmdTmux
	newCmdTmux = func() cmdTmux { return fake }
	t.Cleanup(func() { newCmdTmux = origTmux })
	return root
}

func readUndeliveredLines(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(recoveryUndeliveredPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the undelivered breadcrumb: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// undeliveredMessage strips the RFC3339 stamp that leads every breadcrumb line.
func undeliveredMessage(t *testing.T, line string) string {
	t.Helper()
	stamp, msg, ok := strings.Cut(line, " ")
	if !ok {
		t.Fatalf("breadcrumb line %q has no stamp", line)
	}
	if _, err := time.Parse(time.RFC3339, stamp); err != nil {
		t.Errorf("breadcrumb line %q does not open with an RFC3339 stamp: %v", line, err)
	}
	return msg
}

func TestEscalateOperator_SendsInProcessNeverHandoffMail(t *testing.T) {
	root := escalationFactory(t, []string{"supervisor"}, nil, []string{"supervisor"})
	rec := (&escalationRecorder{}).install(t)

	escalateOperator(root, "supervisor", "WINDOW", "the subject", "the body")

	want := []escalationSend{{root, "supervisor", "the subject", "the body"}}
	if !reflect.DeepEqual(rec.sent, want) {
		t.Errorf("escalationMailFn sends = %+v, want exactly %+v", rec.sent, want)
	}
	if len(rec.handoff) != 0 {
		t.Errorf("an operator escalation must never go through sendHandoffMail, got %+v", rec.handoff)
	}

	src, err := os.ReadFile("escalation.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "escalation.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var from, to int
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for _, name := range vs.Names {
				if name.Name == "escalationMailFn" {
					from, to = fset.Position(vs.Pos()).Line, fset.Position(vs.End()).Line
				}
			}
		}
	}
	if from == 0 {
		t.Errorf("escalation.go does not declare var escalationMailFn")
	}

	inside := func(needle string) (lines []int) {
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, needle) {
				lines = append(lines, i+1)
			}
		}
		return lines
	}
	if got := inside("fileDispatcherMail("); len(got) != 1 || got[0] < from || got[0] > to {
		t.Errorf("escalation.go must spell fileDispatcherMail( on exactly one line, inside escalationMailFn "+
			"(lines %d-%d); found it on lines %v", from, to, got)
	}
	if got := inside("newIssueStoreAt("); len(got) == 0 || got[0] < from || got[0] > to {
		t.Errorf("escalationMailFn (lines %d-%d) must take its store from newIssueStoreAt(; found it on lines %v",
			from, to, got)
	}
	for _, banned := range []string{"storeForMail(", "exec.Command", `"--from"`} {
		if got := inside(banned); len(got) != 0 {
			t.Errorf("escalation.go must not use %s (lines %v): the send is in-process, as af, on an already-validated root",
				banned, got)
		}
	}
}

func TestEscalateOperator_ReturnsSentAndLive(t *testing.T) {
	for _, tc := range []struct {
		name            string
		roster, startup []string
		live            []string
		sendErr         error
		wantSent        bool
		wantLive        bool
		wantSends       int
		wantBreadcrumb  bool
		breadcrumbHolds string
	}{
		{
			name:           "unreachable recipient is not sent",
			roster:         []string{"supervisor"},
			wantSends:      0,
			wantBreadcrumb: true,
		},
		{
			name:            "send error",
			roster:          []string{"manager"},
			live:            []string{"manager"},
			sendErr:         errors.New("store unavailable"),
			wantLive:        true,
			wantSends:       1,
			wantBreadcrumb:  true,
			breadcrumbHolds: "send failed: store unavailable",
		},
		{
			name:           "sent to a recipient with no live session",
			roster:         []string{"manager"},
			startup:        []string{"manager"},
			wantSent:       true,
			wantSends:      1,
			wantBreadcrumb: true,
		},
		{
			name:      "sent and live",
			roster:    []string{"manager"},
			live:      []string{"manager"},
			wantSent:  true,
			wantLive:  true,
			wantSends: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := escalationFactory(t, tc.roster, tc.startup, tc.live)
			rec := (&escalationRecorder{err: tc.sendErr}).install(t)

			sent, live := escalateOperator(root, "manager", "WINDOW", "s", "b")

			if sent != tc.wantSent || live != tc.wantLive {
				t.Errorf("escalateOperator = (%v, %v), want (%v, %v)", sent, live, tc.wantSent, tc.wantLive)
			}
			if len(rec.sent) != tc.wantSends {
				t.Errorf("escalationMailFn called %d times, want %d", len(rec.sent), tc.wantSends)
			}
			lines := readUndeliveredLines(t, root)
			if !tc.wantBreadcrumb {
				if len(lines) != 0 {
					t.Errorf("a delivered escalation must leave no breadcrumb, got %q", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("want exactly one breadcrumb line, got %q", lines)
			}
			if !strings.Contains(lines[0], `(escalation undelivered to "manager")`) {
				t.Errorf("breadcrumb %q does not name the undelivered recipient", lines[0])
			}
			if tc.breadcrumbHolds != "" && !strings.Contains(lines[0], tc.breadcrumbHolds) {
				t.Errorf("breadcrumb %q does not hold %q", lines[0], tc.breadcrumbHolds)
			}
		})
	}
}

func TestEscalateOperator_BreadcrumbCarriesClassLabel(t *testing.T) {
	t.Run("WINDOW to manager", func(t *testing.T) {
		root := escalationFactory(t, []string{"supervisor"}, nil, nil)
		(&escalationRecorder{}).install(t)

		escalateOperator(root, "manager", "WINDOW", "s", "b")

		lines := readUndeliveredLines(t, root)
		if len(lines) != 1 {
			t.Fatalf("want exactly one line in .runtime/recovery_halt_undelivered, got %q", lines)
		}
		if msg := undeliveredMessage(t, lines[0]); !strings.HasPrefix(msg, `WINDOW (escalation undelivered to "manager"): `) {
			t.Errorf("breadcrumb = %q, want it led by the WINDOW class label", lines[0])
		}
	})

	t.Run("RECOVERY HALTED is the line recovery writes today", func(t *testing.T) {
		if escalationClassRecovery != "RECOVERY HALTED" {
			t.Errorf("escalationClassRecovery = %q, want %q", escalationClassRecovery, "RECOVERY HALTED")
		}
		const today = `RECOVERY HALTED (escalation undelivered to "supervisor"): ` +
			"recipient is absent from agents.json, or absent from both the live-session set and startup.json"

		viaOperator := escalationFactory(t, nil, nil, nil)
		(&escalationRecorder{}).install(t)
		escalateOperator(viaOperator, "supervisor", escalationClassRecovery, "s", "b")
		lines := readUndeliveredLines(t, viaOperator)
		if len(lines) != 1 {
			t.Fatalf("want exactly one breadcrumb line from escalateOperator, got %q", lines)
		}
		got := undeliveredMessage(t, lines[0])
		if got != today {
			t.Errorf("escalateOperator(escalationClassRecovery) wrote %q, want today's %q", got, today)
		}

		viaRecovery := escalationFactory(t, nil, nil, nil)
		var st recoveryState
		escalateRecovery(viaRecovery, "supervisor", "s", "b", &st)
		recoveryLines := readUndeliveredLines(t, viaRecovery)
		if len(recoveryLines) != 1 {
			t.Fatalf("want exactly one breadcrumb line from escalateRecovery, got %q", recoveryLines)
		}
		if want := undeliveredMessage(t, recoveryLines[0]); got != want {
			t.Errorf("escalateOperator(escalationClassRecovery) wrote %q, escalateRecovery writes %q", got, want)
		}
	})
}

func TestEscalationTargets_IncludesManager(t *testing.T) {
	if got, want := escalationTargets(), []string{"supervisor", "manager"}; !reflect.DeepEqual(got, want) {
		t.Errorf("escalationTargets() = %v, want %v", got, want)
	}
}
