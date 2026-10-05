package cmd

import (
	"strings"
	"testing"
)

type recordedReport struct {
	root, to, subject, body string
}

// installIntegrationReportRecorder swaps the integration-report mail egress for a recorder, so a test counts
// sends without a router (whose tmux notifier is package-private to internal/mail).
func installIntegrationReportRecorder(t *testing.T) *[]recordedReport {
	t.Helper()
	var recs []recordedReport
	orig := integrationReportMailFn
	integrationReportMailFn = func(root, to, subject, body string) error {
		recs = append(recs, recordedReport{root, to, subject, body})
		return nil
	}
	t.Cleanup(func() { integrationReportMailFn = orig })
	return &recs
}

func reportsWithPrefix(recs []recordedReport, prefix string) []recordedReport {
	var out []recordedReport
	for _, r := range recs {
		if strings.HasPrefix(r.subject, prefix) {
			out = append(out, r)
		}
	}
	return out
}
