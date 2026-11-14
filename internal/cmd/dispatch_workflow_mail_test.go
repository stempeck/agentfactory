package cmd

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/mail"
)

// The dispatcher is a daemon with no agent identity, so a subprocess `af mail send` resolved no
// sender and filed nothing; the completion mail must be filed in-process with sender "af".
func TestWorkflowCompleteMail_FilesWithoutASenderIdentity(t *testing.T) {
	t.Run("the mail is filed from af to the notify agent", func(t *testing.T) {
		root := setupMailSendFixture(t)
		_, hermetic := setupHermeticSessions(t)
		t.Chdir(root)
		t.Setenv("AF_ROLE", "")

		store := memstore.NewWithActor(dispatchStoreActor)
		const subject = "WORKFLOW_COMPLETE: advisory-heal"
		if err := fileDispatcherMail(context.Background(), store, root, "manager", subject, `Workflow "advisory-heal" completed all phases for stempeck/agentfactory#7.`); err != nil {
			t.Fatalf("fileDispatcherMail: %v", err)
		}
		if other, err := mail.NewMailbox("manager", hermetic).List(context.Background()); err != nil || len(other) != 0 {
			t.Errorf("the mail reached a store other than the one it was handed (%d messages, %v)", len(other), err)
		}
		msgs, err := mail.NewMailbox("manager", store).List(context.Background())
		if err != nil {
			t.Fatalf("listing manager's mailbox: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("manager's mailbox has %d messages, want 1", len(msgs))
		}
		if m := msgs[0]; m.Subject != subject || m.From != "af" || m.To != "manager" {
			t.Errorf("filed %q from %q to %q, want %q from %q to %q", m.Subject, m.From, m.To, subject, "af", "manager")
		}
	})

	t.Run("the seam files in-process with the unchanged subject and body", func(t *testing.T) {
		src := readRepoFile(t, "internal/cmd/dispatch.go")
		seam := regexp.MustCompile(`(?ms)^var sendWorkflowCompleteMail = func\(.*?^}`).FindString(src)
		if seam == "" {
			t.Fatal("internal/cmd/dispatch.go has no sendWorkflowCompleteMail var")
		}
		for _, want := range []string{
			"func(ctx context.Context, store issuestore.Store, root, recipient, workflow, itemKey string) error",
			"isTestBinary()",
			"fileDispatcherMail(ctx, store, root, recipient,",
			`fmt.Sprintf("WORKFLOW_COMPLETE: %s", workflow)`,
			`fmt.Sprintf("Workflow %q completed all phases for %s.", workflow, itemKey)`,
		} {
			if !strings.Contains(seam, want) {
				t.Errorf("sendWorkflowCompleteMail lacks %q:\n%s", want, seam)
			}
		}
		for _, banned := range []string{"exec.Command", `"mail", "send"`} {
			if strings.Contains(seam, banned) {
				t.Errorf("sendWorkflowCompleteMail still runs %s as a subprocess", banned)
			}
		}
		if !strings.Contains(src, "sendWorkflowCompleteMail(w.ctx, w.store, w.root, ") {
			t.Error("terminal does not pass the tick's open store and the factory root to sendWorkflowCompleteMail")
		}
	})

	t.Run("without a store the mail is an error", func(t *testing.T) {
		root := setupMailSendFixture(t)
		err := fileDispatcherMail(context.Background(), nil, root, "manager", "WORKFLOW_STALL: advisory-heal", "body")
		if err == nil || !strings.Contains(err.Error(), "issue store unavailable") {
			t.Errorf("fileDispatcherMail with no store = %v, want the error %q", err, "issue store unavailable")
		}
	})

	t.Run("the mail is filed through the store the tick opened", func(t *testing.T) {
		src := readRepoFile(t, "internal/cmd/dispatch.go")
		fn := regexp.MustCompile(`(?ms)^func fileDispatcherMail\(.*?^}`).FindString(src)
		if fn == "" {
			t.Fatal("internal/cmd/dispatch.go has no fileDispatcherMail func")
		}
		for _, banned := range []string{"storeForMail(", "newIssueStore("} {
			if strings.Contains(fn, banned) {
				t.Errorf("fileDispatcherMail opens its own issue store (%s) beside the tick's, a second connection and actor per mail:\n%s", banned, fn)
			}
		}
		for _, want := range []string{"store == nil", "issue store unavailable", "mail.NewRouter(root, store)"} {
			if !strings.Contains(fn, want) {
				t.Errorf("fileDispatcherMail lacks %q (a missing store is an error, never a second open):\n%s", want, fn)
			}
		}
	})
}
