package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/mail"
)

// errNoMail is returned when af mail check finds no mail (exit code 1).
type errNoMail struct{}

func (e errNoMail) Error() string { return "no new mail" }

var mailCmd = &cobra.Command{
	Use:   "mail",
	Short: "Inter-agent messaging",
	Long:  "Send, receive, and manage messages between agents.",
}

func init() {
	rootCmd.AddCommand(mailCmd)

	// send
	sendCmd := &cobra.Command{
		Use:   "send <to>",
		Short: "Send a message to an agent or group",
		Args:  cobra.ExactArgs(1),
		RunE:  runMailSend,
	}
	sendCmd.Flags().StringP("subject", "s", "", "Message subject (required)")
	sendCmd.Flags().StringP("message", "m", "", "Message body (required)")
	sendCmd.Flags().String("priority", "normal", "Priority: urgent, high, normal, low")
	sendCmd.Flags().String("reply-to", "", "ID of message being replied to")
	_ = sendCmd.MarkFlagRequired("subject")
	_ = sendCmd.MarkFlagRequired("message")
	mailCmd.AddCommand(sendCmd)

	// inbox
	inboxCmd := &cobra.Command{
		Use:   "inbox",
		Short: "List unread messages",
		RunE:  runMailInbox,
	}
	inboxCmd.Flags().Bool("json", false, "Output as JSON")
	mailCmd.AddCommand(inboxCmd)

	// read
	readCmd := &cobra.Command{
		Use:   "read <id>",
		Short: "Read a specific message",
		Args:  cobra.ExactArgs(1),
		RunE:  runMailRead,
	}
	readCmd.Flags().Bool("json", false, "Output as JSON")
	mailCmd.AddCommand(readCmd)

	// delete
	deleteCmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a message",
		Args:  cobra.ExactArgs(1),
		RunE:  runMailDelete,
	}
	mailCmd.AddCommand(deleteCmd)

	// check
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Check for new mail",
		RunE:  runMailCheck,
	}
	checkCmd.Flags().Bool("inject", false, "Output system-reminder XML for hooks")
	checkCmd.Flags().Bool("json", false, "Output as JSON")
	mailCmd.AddCommand(checkCmd)

	// reply
	replyCmd := &cobra.Command{
		Use:   "reply <id>",
		Short: "Reply to a message",
		Args:  cobra.ExactArgs(1),
		RunE:  runMailReply,
	}
	replyCmd.Flags().StringP("message", "m", "", "Reply body (required)")
	replyCmd.Flags().StringP("subject", "s", "", "Override reply subject")
	_ = replyCmd.MarkFlagRequired("message")
	mailCmd.AddCommand(replyCmd)
}

func runMailSend(cmd *cobra.Command, args []string) error {
	to := args[0]
	subject, _ := cmd.Flags().GetString("subject")
	body, _ := cmd.Flags().GetString("message")
	priorityStr, _ := cmd.Flags().GetString("priority")
	replyToID, _ := cmd.Flags().GetString("reply-to")

	wd, err := getWd()
	if err != nil {
		return err
	}

	sender, err := detectSender(wd)
	if err != nil {
		return err
	}

	var msg *mail.Message
	if replyToID != "" {
		// Fetch original to inherit ThreadID
		mbox, err := newMailboxForSender(sender, wd)
		if err != nil {
			return err
		}
		original, err := mbox.Get(cmd.Context(), replyToID)
		if err != nil {
			return fmt.Errorf("fetching original message for reply: %w", err)
		}
		msg = mail.NewReplyMessage(sender, to, subject, body, original)
	} else {
		msg = mail.NewMessage(sender, to, subject, body)
	}
	msg.Priority = mail.ParsePriority(priorityStr)

	store, err := storeForMail(wd)
	if err != nil {
		return err
	}
	router, err := mail.NewRouter(wd, store)
	if err != nil {
		return err
	}

	if err := router.Send(cmd.Context(), msg); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Sent to %s: %s\n", to, subject)
	return nil
}

func runMailInbox(cmd *cobra.Command, _ []string) error {
	asJSON, _ := cmd.Flags().GetBool("json")

	wd, err := getWd()
	if err != nil {
		return err
	}

	sender, err := detectSender(wd)
	if err != nil {
		return err
	}

	mbox, err := newMailboxForSender(sender, wd)
	if err != nil {
		return err
	}

	msgs, err := mbox.List(cmd.Context())
	if err != nil {
		return err
	}

	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(msgs)
	}

	if len(msgs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No unread messages.")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tFROM\tSUBJECT\tPRIORITY\tTIME")
	for _, m := range msgs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			m.ID, m.From, m.Subject, m.Priority,
			m.Timestamp.Format("2006-01-02 15:04"))
	}
	return w.Flush()
}

func runMailRead(cmd *cobra.Command, args []string) error {
	id := args[0]
	asJSON, _ := cmd.Flags().GetBool("json")

	wd, err := getWd()
	if err != nil {
		return err
	}

	sender, err := detectSender(wd)
	if err != nil {
		return err
	}

	mbox, err := newMailboxForSender(sender, wd)
	if err != nil {
		return err
	}

	msg, err := mbox.Get(cmd.Context(), id)
	if err != nil {
		return err
	}

	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(msg)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "ID:       %s\n", msg.ID)
	fmt.Fprintf(out, "From:     %s\n", msg.From)
	fmt.Fprintf(out, "To:       %s\n", msg.To)
	fmt.Fprintf(out, "Subject:  %s\n", msg.Subject)
	fmt.Fprintf(out, "Priority: %s\n", msg.Priority)
	fmt.Fprintf(out, "Type:     %s\n", msg.Type)
	fmt.Fprintf(out, "Time:     %s\n", msg.Timestamp.Format("2006-01-02 15:04:05"))
	if msg.ThreadID != "" {
		fmt.Fprintf(out, "Thread:   %s\n", msg.ThreadID)
	}
	if msg.ReplyTo != "" {
		fmt.Fprintf(out, "ReplyTo:  %s\n", msg.ReplyTo)
	}
	fmt.Fprintf(out, "\n%s\n", msg.Body)
	return nil
}

func runMailDelete(cmd *cobra.Command, args []string) error {
	id := args[0]

	wd, err := getWd()
	if err != nil {
		return err
	}

	sender, err := detectSender(wd)
	if err != nil {
		return err
	}

	mbox, err := newMailboxForSender(sender, wd)
	if err != nil {
		return err
	}

	if err := mbox.Delete(cmd.Context(), id); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Deleted message %s\n", id)
	return nil
}

func runMailCheck(cmd *cobra.Command, _ []string) error {
	inject, _ := cmd.Flags().GetBool("inject")
	asJSON, _ := cmd.Flags().GetBool("json")

	wd, err := getWd()
	if err != nil {
		if inject {
			return nil
		}
		return err
	}

	sender, err := detectSender(wd)
	if err != nil {
		if inject {
			return nil
		}
		return err
	}

	mbox, err := newMailboxForSender(sender, wd)
	if err != nil {
		if inject {
			return nil
		}
		return err
	}

	msgs, err := mbox.List(cmd.Context())
	if err != nil {
		if inject {
			return nil
		}
		return err
	}

	count := len(msgs)

	if inject {
		if count == 0 {
			return nil
		}
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, "<system-reminder>")
		fmt.Fprintf(out, "You have %d unread message(s):\n\n", count)
		for _, m := range msgs {
			fmt.Fprintf(out, "From: %s\nSubject: %s\nPriority: %s\n\n%s\n\n",
				m.From, m.Subject, m.Priority, m.Body)
		}
		fmt.Fprintln(out, "</system-reminder>")
		return nil
	}

	if asJSON {
		enc := json.NewEncoder(cmd.OutOrStdout())
		return enc.Encode(map[string]int{"count": count})
	}

	if count == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No new mail.")
		return errNoMail{}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "You have %d unread message(s).\n", count)
	return nil
}

func runMailReply(cmd *cobra.Command, args []string) error {
	id := args[0]
	body, _ := cmd.Flags().GetString("message")
	subjectOverride, _ := cmd.Flags().GetString("subject")

	wd, err := getWd()
	if err != nil {
		return err
	}

	sender, err := detectSender(wd)
	if err != nil {
		return err
	}

	mbox, err := newMailboxForSender(sender, wd)
	if err != nil {
		return err
	}

	original, err := mbox.Get(cmd.Context(), id)
	if err != nil {
		return err
	}

	subject := subjectOverride
	if subject == "" {
		subject = "Re: " + original.Subject
	}

	reply := mail.NewReplyMessage(sender, original.From, subject, body, original)

	store, err := storeForMail(wd)
	if err != nil {
		return err
	}
	router, err := mail.NewRouter(wd, store)
	if err != nil {
		return err
	}

	if err := router.Send(cmd.Context(), reply); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Replied to %s: %s\n", original.From, subject)
	return nil
}

// detectSender determines the current agent name from the working directory.
// Delegates to resolveAgentName (helpers.go) for three-tier resolution, then
// validates against agents.json.
func detectSender(wd string) (string, error) {
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return "", fmt.Errorf("detecting sender: %w", err)
	}

	agentName, err := resolveAgentName(wd, root)
	if err != nil {
		return "", fmt.Errorf("detecting sender: %w", err)
	}

	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return "", fmt.Errorf("detecting sender: %w", err)
	}

	if _, ok := agentsCfg.Agents[agentName]; !ok {
		return "", fmt.Errorf("agent %q not found in agents.json", agentName)
	}

	return agentName, nil
}

// storeForMail constructs the issuestore.Store used by mail operations.
// It is the single place where cmd/mail.go delegates to the production
// adapter via the newIssueStore seam; tests inject memstore directly into
// mail.NewMailbox / mail.NewRouter without going through this helper.
func storeForMail(wd string) (issuestore.Store, error) {
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return nil, err
	}
	beadsDir := filepath.Join(root, ".beads")
	actor := os.Getenv("BD_ACTOR")
	return newIssueStore(wd, beadsDir, actor)
}

func newMailboxForSender(sender, wd string) (*mail.Mailbox, error) {
	store, err := storeForMail(wd)
	if err != nil {
		return nil, err
	}
	return mail.NewMailbox(sender, store), nil
}

