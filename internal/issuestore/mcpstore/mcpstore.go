// Package mcpstore is an issuestore.Store adapter that speaks MCP JSON-RPC
// 2.0 over HTTP loopback to the in-tree Python server at py/issuestore/.
//
// The adapter owns the server's process lifecycle: New() discovers an
// existing server via .runtime/mcp_server.json or spawns one, serializing
// concurrent starts through .runtime/mcp_start.lock. All I/O is bound to
// 127.0.0.1 (R-SEC-1). No go.mod dependencies are added beyond stdlib.
//
// Actor scoping flows in as an explicit constructor argument; the adapter
// never reads process env (#98 env-isolation invariant).
package mcpstore

import (
	"context"
	"net/http"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

// MCPStore is a Store implementation backed by the in-tree Python MCP
// server. Construct it with New; the zero value is not useful.
type MCPStore struct {
	client      *rpcClient
	actor       string
	factoryRoot string // retained for error context / future use (IMPLREADME L163)
}

// New discovers a running MCP server at factoryRoot or spawns a new one,
// then returns an adapter configured with the given actor. actor=="" disables
// Gate-4 client-side scoping; any non-empty value scopes List when the
// caller passes IncludeAllAgents=false AND leaves Filter.Assignee empty.
func New(factoryRoot, actor string) (*MCPStore, error) {
	endpoint, err := discoverOrStart(factoryRoot)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return &MCPStore{
		client:      newRPCClient(endpoint, httpClient),
		actor:       actor,
		factoryRoot: factoryRoot,
	}, nil
}

// Get returns the issue with the given id, or a wrapped ErrNotFound.
func (m *MCPStore) Get(ctx context.Context, id string) (issuestore.Issue, error) {
	var out issuestore.Issue
	if err := m.client.call(ctx, "issuestore_get", map[string]any{"id": id}, &out); err != nil {
		return issuestore.Issue{}, err
	}
	return out, nil
}

// List returns issues matching filter. Gate-4 scoping is injected on the
// client side so the server's assignee filter applies uniformly across
// adapters: when the store has a non-empty actor, IncludeAllAgents is false,
// and the caller did not supply an explicit Assignee, we pass assignee=actor.
func (m *MCPStore) List(ctx context.Context, filter issuestore.Filter) ([]issuestore.Issue, error) {
	args := listArgs(filter, m.actor)
	var out []issuestore.Issue
	if err := m.client.call(ctx, "issuestore_list", args, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Ready returns non-terminal issues whose dependencies are all terminal.
func (m *MCPStore) Ready(ctx context.Context, filter issuestore.Filter) (issuestore.ReadyResult, error) {
	args := listArgs(filter, m.actor)
	if filter.MoleculeID != "" {
		args["molecule_id"] = filter.MoleculeID
	}
	var resp readyResponse
	if err := m.client.call(ctx, "issuestore_ready", args, &resp); err != nil {
		return issuestore.ReadyResult{}, err
	}
	return issuestore.ReadyResult{
		Steps:      resp.Steps,
		TotalSteps: resp.TotalSteps,
		MoleculeID: resp.MoleculeID,
	}, nil
}

// Create inserts a new issue and returns the populated DTO.
func (m *MCPStore) Create(ctx context.Context, params issuestore.CreateParams) (issuestore.Issue, error) {
	args := map[string]any{
		"title":    params.Title,
		"type":     string(params.Type),
		"priority": int(params.Priority),
	}
	if params.Description != "" {
		args["description"] = params.Description
	}
	if params.Assignee != "" {
		args["assignee"] = params.Assignee
	}
	if params.Parent != "" {
		args["parent"] = params.Parent
	}
	if params.Actor != "" {
		args["actor"] = params.Actor
	}
	// Preserve insertion order + no dedup (C13): send the slice as-is, even
	// if empty we omit the key so the server uses its empty-list default.
	if len(params.Labels) > 0 {
		args["labels"] = params.Labels
	}
	var out issuestore.Issue
	if err := m.client.call(ctx, "issuestore_create", args, &out); err != nil {
		return issuestore.Issue{}, err
	}
	return out, nil
}

// Patch applies a partial update. Notes is the only field the public Patch
// DTO carries today (Gotcha 11); other fields are additive-future-work.
// Description is never sent — the server guarantees C-10 (Patch never
// mutates Description) but we also refuse to send it from here.
func (m *MCPStore) Patch(ctx context.Context, id string, patch issuestore.Patch) error {
	args := map[string]any{"id": id}
	if patch.Notes != nil {
		args["notes"] = *patch.Notes
	}
	return m.client.call(ctx, "issuestore_patch", args, nil)
}

// Close transitions an issue to status=closed with the given reason.
func (m *MCPStore) Close(ctx context.Context, id string, reason string) error {
	args := map[string]any{"id": id, "reason": reason}
	return m.client.call(ctx, "issuestore_close", args, nil)
}

// DepAdd records that issueID depends on dependsOnID.
func (m *MCPStore) DepAdd(ctx context.Context, issueID, dependsOnID string) error {
	args := map[string]any{
		"issue_id":      issueID,
		"depends_on_id": dependsOnID,
	}
	return m.client.call(ctx, "issuestore_dep_add", args, nil)
}

// Render returns human-readable display output for `af bead show`. DO NOT
// parse the output.
func (m *MCPStore) Render(ctx context.Context, id string) (string, error) {
	var out string
	if err := m.client.call(ctx, "issuestore_render", map[string]any{"id": id}, &out); err != nil {
		return "", err
	}
	return out, nil
}

// RenderList returns human-readable display output for `af bead list`. DO
// NOT parse the output.
func (m *MCPStore) RenderList(ctx context.Context, filter issuestore.Filter) (string, error) {
	args := listArgs(filter, m.actor)
	var out string
	if err := m.client.call(ctx, "issuestore_render_list", args, &out); err != nil {
		return "", err
	}
	return out, nil
}

// SetStatus is a TEST-ONLY helper that transitions an issue to any of the
// six Status values via the server's issuestore_patch tool (which accepts
// an optional status field without validation). Production code MUST NOT
// call this — lifecycle transitions belong to Close/Patch. Exported so
// RunStoreContract can reach it through a type assertion, matching the
// memstore SetStatus idiom (D11 / C-1 / Gotcha 9).
func (m *MCPStore) SetStatus(ctx context.Context, id string, status issuestore.Status) error {
	args := map[string]any{
		"id":     id,
		"status": string(status),
	}
	return m.client.call(ctx, "issuestore_patch", args, nil)
}

// listArgs builds the shared filter payload used by List, Ready, and
// RenderList. Gate-4 client-side scoping is injected here so every
// List-like call applies it consistently.
func listArgs(filter issuestore.Filter, actor string) map[string]any {
	args := map[string]any{}
	if filter.Parent != "" {
		args["parent"] = filter.Parent
	}
	if len(filter.Statuses) > 0 {
		statuses := make([]string, len(filter.Statuses))
		for i, s := range filter.Statuses {
			statuses[i] = string(s)
		}
		args["statuses"] = statuses
	}
	if filter.Type != "" {
		args["type"] = string(filter.Type)
	}
	if len(filter.Labels) > 0 {
		args["labels"] = filter.Labels
	}
	if filter.IncludeAllAgents {
		args["include_all_agents"] = true
	}
	if filter.IncludeClosed {
		args["include_closed"] = true
	}

	// Assignee resolution: explicit caller value wins; otherwise inject
	// Gate-4 actor scoping when IncludeAllAgents=false AND the store has
	// a non-empty actor.
	switch {
	case filter.Assignee != "":
		args["assignee"] = filter.Assignee
	case actor != "" && !filter.IncludeAllAgents:
		args["assignee"] = actor
	}

	return args
}

// readyResponse mirrors the Python server's issuestore_ready payload.
// The server returns snake_case keys; issuestore.ReadyResult has no JSON
// tags, so we decode into this adapter struct and copy.
type readyResponse struct {
	Steps      []issuestore.Issue `json:"steps"`
	TotalSteps int                `json:"total_steps"`
	MoleculeID string             `json:"molecule_id"`
}

// Compile-time check: *MCPStore satisfies issuestore.Store.
var _ issuestore.Store = (*MCPStore)(nil)
