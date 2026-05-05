package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
)

var beadCmd = &cobra.Command{
	Use:   "bead",
	Short: "Bead operations",
	Long:  "Create, query, update, and manage beads.",
}

func init() {
	rootCmd.AddCommand(beadCmd)

	// show
	showCmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Display bead details",
		Args:  cobra.ExactArgs(1),
		RunE:  runBeadShow,
	}
	showCmd.Flags().Bool("json", false, "Output as JSON")
	beadCmd.AddCommand(showCmd)

	// create
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new bead",
		RunE:  runBeadCreate,
	}
	createCmd.Flags().String("type", "", "Bead type (bug, task, feature, epic, gate)")
	createCmd.Flags().String("title", "", "Bead title")
	createCmd.Flags().StringP("description", "d", "", "Bead description")
	createCmd.Flags().String("priority", "", "Priority (0-3)")
	createCmd.Flags().String("labels", "", "Comma-separated labels")
	createCmd.Flags().String("parent", "", "Parent bead ID")
	createCmd.Flags().Bool("json", false, "Output as JSON")
	_ = createCmd.MarkFlagRequired("title")
	_ = createCmd.MarkFlagRequired("type")
	beadCmd.AddCommand(createCmd)

	// update
	updateCmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update bead metadata",
		Args:  cobra.ExactArgs(1),
		RunE:  runBeadUpdate,
	}
	updateCmd.Flags().String("notes", "", "Completion notes")
	_ = updateCmd.MarkFlagRequired("notes")
	beadCmd.AddCommand(updateCmd)

	// list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List beads",
		RunE:  runBeadList,
	}
	listCmd.Flags().String("parent", "", "Filter by parent bead ID")
	listCmd.Flags().String("status", "", "Filter by status (open, closed)")
	listCmd.Flags().Bool("json", false, "Output as JSON")
	listCmd.Flags().Bool("all", false, "Show all beads (bypass agent scoping, include closed)")
	beadCmd.AddCommand(listCmd)

	// close
	closeCmd := &cobra.Command{
		Use:   "close <id>",
		Short: "Close a bead",
		Args:  cobra.ExactArgs(1),
		RunE:  runBeadClose,
	}
	closeCmd.Flags().String("reason", "", "Reason for closing")
	beadCmd.AddCommand(closeCmd)

	// dep
	depCmd := &cobra.Command{
		Use:   "dep <id> <depends-on-id>",
		Short: "Add a dependency between beads",
		Args:  cobra.ExactArgs(2),
		RunE:  runBeadDep,
	}
	beadCmd.AddCommand(depCmd)
}

func runBeadShow(cmd *cobra.Command, args []string) error {
	id := args[0]
	ctx := cmd.Context()
	cwd, err := getWd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}
	beadsDir := filepath.Join(factoryRoot, ".beads")

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(cwd, beadsDir, actor)
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}

	jsonFlag, _ := cmd.Flags().GetBool("json")
	if jsonFlag {
		iss, err := store.Get(ctx, id)
		if err != nil {
			if errors.Is(err, issuestore.ErrNotFound) {
				return fmt.Errorf("bead %s not found", id)
			}
			return fmt.Errorf("showing bead %s: %w", id, err)
		}
		out, err := json.Marshal(iss)
		if err != nil {
			return fmt.Errorf("marshaling bead %s: %w", id, err)
		}
		fmt.Println(string(out))
		return nil
	}

	text, err := store.Render(ctx, id)
	if err != nil {
		if errors.Is(err, issuestore.ErrNotFound) {
			return fmt.Errorf("bead %s not found", id)
		}
		return fmt.Errorf("showing bead %s: %w", id, err)
	}
	fmt.Print(text)
	return nil
}

func runBeadCreate(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cwd, err := getWd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}
	beadsDir := filepath.Join(factoryRoot, ".beads")

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(cwd, beadsDir, actor)
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}

	title, _ := cmd.Flags().GetString("title")
	beadType, _ := cmd.Flags().GetString("type")
	desc, _ := cmd.Flags().GetString("description")
	priorityStr, _ := cmd.Flags().GetString("priority")
	labelsStr, _ := cmd.Flags().GetString("labels")
	parent, _ := cmd.Flags().GetString("parent")
	jsonFlag, _ := cmd.Flags().GetBool("json")

	// Auto-tag with creating agent identity (best-effort) — same behavior
	// as before the migration.
	agentLabel := detectCreatingAgent(cwd, factoryRoot)
	if agentLabel != "" {
		if labelsStr != "" {
			labelsStr = labelsStr + ",created-by:" + agentLabel
		} else {
			labelsStr = "created-by:" + agentLabel
		}
	}

	// Labels are []string on the wire (C13: insertion-order preserved across adapters).
	var labels []string
	if labelsStr != "" {
		labels = strings.Split(labelsStr, ",")
	}

	if parent != "" && agentLabel == "" {
		return fmt.Errorf("af bead create --parent requires an assignable identity; run from an agent workspace")
	}

	params := issuestore.CreateParams{
		Title:       title,
		Description: desc,
		Type:        issuestore.IssueType(beadType),
		Parent:      parent,
		Labels:      labels,
		Assignee:    agentLabel,
	}
	if priorityStr != "" {
		n, err := strconv.Atoi(priorityStr)
		if err != nil {
			return fmt.Errorf("invalid --priority %q: %w", priorityStr, err)
		}
		params.Priority = issuestore.Priority(n)
	} else {
		// Explicitly default to Normal. The zero value of Priority is
		// PriorityUrgent (0), which would silently bump every unflagged
		// bead to urgent — preserve the pre-migration "no priority override"
		// semantic by mapping an empty flag to Normal.
		params.Priority = issuestore.PriorityNormal
	}

	iss, err := store.Create(ctx, params)
	if err != nil {
		return fmt.Errorf("creating bead: %w", err)
	}

	if jsonFlag {
		out, _ := json.Marshal(map[string]string{"id": iss.ID})
		fmt.Println(string(out))
	} else {
		fmt.Fprintf(os.Stderr, "✓ Created bead %s: %q\n", iss.ID, title)
	}
	return nil
}

// detectCreatingAgent returns the agent name from cwd, or "" if not detectable.
// Delegates to resolveAgentName (helpers.go) for three-tier resolution.
func detectCreatingAgent(cwd, factoryRoot string) string {
	name, err := resolveAgentName(cwd, factoryRoot)
	if err != nil {
		return ""
	}
	return name
}

func runBeadUpdate(cmd *cobra.Command, args []string) error {
	id := args[0]
	ctx := cmd.Context()
	cwd, err := getWd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}
	beadsDir := filepath.Join(factoryRoot, ".beads")

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(cwd, beadsDir, actor)
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}

	notes, _ := cmd.Flags().GetString("notes")
	if err := store.Patch(ctx, id, issuestore.Patch{Notes: &notes}); err != nil {
		return fmt.Errorf("updating bead %s: %w", id, err)
	}
	fmt.Fprintf(os.Stderr, "✓ Updated bead %s\n", id)
	return nil
}

func runBeadList(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	cwd, err := getWd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}
	beadsDir := filepath.Join(factoryRoot, ".beads")

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(cwd, beadsDir, actor)
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}

	parent, _ := cmd.Flags().GetString("parent")
	status, _ := cmd.Flags().GetString("status")
	jsonFlag, _ := cmd.Flags().GetBool("json")
	allFlag, _ := cmd.Flags().GetBool("all")

	filter := issuestore.Filter{Parent: parent}

	// Agent scoping: same policy as the old --assignee <agent> auto-injection.
	// Orchestrators/supervisors pass --all to bypass.
	if !allFlag {
		if agent := detectCreatingAgent(cwd, factoryRoot); agent != "" {
			filter.Assignee = agent
		}
	}

	// --all means "bypass agent scoping AND include closed" (D13 splits
	// bd's overloaded --all into two axes).
	if allFlag {
		filter.IncludeAllAgents = true
		filter.IncludeClosed = true
	}

	// Status flag → Filter.Statuses. Preserve bd's old passthrough semantics
	// by accepting any status string. Terminal statuses (closed/done) imply
	// IncludeClosed so the default nil-Statuses terminal-hiding does not
	// swallow them.
	if status != "" {
		s := issuestore.Status(status)
		filter.Statuses = []issuestore.Status{s}
		if s.IsTerminal() {
			filter.IncludeClosed = true
		}
	}

	if jsonFlag {
		items, err := store.List(ctx, filter)
		if err != nil {
			return fmt.Errorf("listing beads: %w", err)
		}
		out, _ := json.Marshal(items)
		fmt.Println(string(out))
		return nil
	}

	text, err := store.RenderList(ctx, filter)
	if err != nil {
		return fmt.Errorf("listing beads: %w", err)
	}
	if text != "" {
		fmt.Print(text)
	}
	return nil
}

func runBeadClose(cmd *cobra.Command, args []string) error {
	id := args[0]
	ctx := cmd.Context()
	cwd, err := getWd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}
	beadsDir := filepath.Join(factoryRoot, ".beads")

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(cwd, beadsDir, actor)
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}

	reason, _ := cmd.Flags().GetString("reason")
	if err := store.Close(ctx, id, reason); err != nil {
		return fmt.Errorf("closing bead %s: %w", id, err)
	}
	fmt.Fprintf(os.Stderr, "✓ Closed bead %s\n", id)
	return nil
}

func runBeadDep(cmd *cobra.Command, args []string) error {
	issueID := args[0]
	dependsOnID := args[1]
	ctx := cmd.Context()
	cwd, err := getWd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}
	beadsDir := filepath.Join(factoryRoot, ".beads")

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(cwd, beadsDir, actor)
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}

	if err := store.DepAdd(ctx, issueID, dependsOnID); err != nil {
		return fmt.Errorf("adding dependency: %w", err)
	}
	fmt.Fprintf(os.Stderr, "✓ Dependency added: %s → %s\n", issueID, dependsOnID)
	return nil
}
