package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/mail"
)

const (
	integrationReportNotBound    = "not_bound"
	integrationReportDegraded    = "degraded"
	integrationReportGuardDenied = "guard-denied"
	integrationReportSkipped     = "skipped"
)

// integrationReportRecipient receives every integration report (D17).
const integrationReportRecipient = "manager"

// integrationReportMailFn is the egress for integration reports; tests swap it for a recorder. Like
// sendHandoffMail it sends nothing under the test binary, so a test that does not install the recorder
// never routes real mail.
var integrationReportMailFn = func(root, to, subject, body string) error {
	if isTestBinary() {
		return nil
	}
	store, err := storeForMail(root)
	if err != nil {
		return err
	}
	router, err := mail.NewRouter(root, store)
	if err != nil {
		return err
	}
	return router.Send(context.Background(), mail.NewMessage("af", to, subject, body))
}

// integrationReportMarker lives at the factory root, not in an agent's .runtime, because every dispatcher
// --reset wipes the agent's .runtime and would re-mail the same condition each tick (D4).
func integrationReportMarker(root, name, condition string) string {
	return filepath.Join(root, ".runtime", "integration_report", name+"."+condition)
}

// Locks live beside the marker dir, not in it: clearSlingRefusalReports globs markers and must never remove
// a live lock.
func integrationReportLockPath(root, name, condition string) string {
	return filepath.Join(root, ".runtime", "integration_report_lock", name+"."+condition)
}

// reportIntegration mails the manager once per (integration, condition).
func reportIntegration(root, name, condition, subject, body string) error {
	return reportIntegrationTo(root, integrationReportRecipient, name, condition, subject, body)
}

// reportIntegrationTo copies deliverCorrective's order: marker, lock, re-check under the lock, send, and the
// marker only after a successful send, so a failed send is retried by the next report. A live lock means a
// concurrent reporter is sending this same condition, so bailing cannot lose the mail.
func reportIntegrationTo(root, to, name, condition, subject, body string) error {
	marker := integrationReportMarker(root, name, condition)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	l := lock.NewWithPath(integrationReportLockPath(root, name, condition))
	if err := l.Acquire(os.Getenv("CLAUDE_SESSION_ID")); err != nil {
		if errors.Is(err, lock.ErrLocked) {
			return nil
		}
		return fmt.Errorf("locking report %s.%s: %w", name, condition, err)
	}
	defer func() { _ = l.Release() }()
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := integrationReportMailFn(root, to, subject, body); err != nil {
		return fmt.Errorf("mailing %s %q: %w", to, subject, err)
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, 0o644)
}

// clearIntegrationReport re-arms a condition; callers do so only on positive evidence that it has ended.
func clearIntegrationReport(root, name, condition string) {
	if err := os.Remove(integrationReportMarker(root, name, condition)); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "warning: clearing integration report marker: %v\n", err)
	}
}
