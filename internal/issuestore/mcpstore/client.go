package mcpstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

// rpcClient speaks MCP JSON-RPC 2.0 over HTTP to the in-tree Python server.
//
// The server wraps tool results in a double-encoded envelope: the outer
// JSON-RPC response carries a content array whose first entry has a `text`
// field containing the tool's JSON-encoded payload. call() unwraps both
// layers.
type rpcClient struct {
	http     *http.Client
	endpoint string
	nextID   atomic.Int64
}

func newRPCClient(endpoint string, httpClient *http.Client) *rpcClient {
	return &rpcClient{http: httpClient, endpoint: endpoint}
}

// call invokes the named MCP tool and unmarshals its structured payload into
// result. Pass nil for tools that return null (patch, close, dep_add).
//
// Error semantics:
//   - Tool-level "issue not found: <id>" errors are wrapped with
//     issuestore.ErrNotFound so callers can use errors.Is(err, ErrNotFound).
//   - Other tool-level errors (isError:true) surface as rpcToolError.
//   - JSON-RPC protocol errors surface as rpcProtocolError.
func (c *rpcClient) call(ctx context.Context, tool string, args any, result any) error {
	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  "tools/call",
		Params:  rpcCallParams{Name: tool, Arguments: args},
	})
	if err != nil {
		return fmt.Errorf("mcpstore: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/", bytes.NewReader(reqBody))
	if err != nil {
		return fmt.Errorf("mcpstore: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("mcpstore: %s: %w", tool, err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("mcpstore: read %s response: %w", tool, err)
	}

	var env rpcResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("mcpstore: decode %s envelope: %w (body=%q)", tool, err, string(body))
	}
	if env.Error != nil {
		return rpcProtocolError{Code: env.Error.Code, Message: env.Error.Message, Tool: tool}
	}
	if len(env.Result.Content) == 0 {
		return fmt.Errorf("mcpstore: %s: empty content array", tool)
	}
	text := env.Result.Content[0].Text
	if env.Result.IsError {
		// Python server wraps KeyError messages in single-quote repr
		// (e.g. `'issue not found: af-xyz'`), so use Contains not HasPrefix.
		if strings.Contains(text, "issue not found:") {
			return fmt.Errorf("%s: %w", strings.TrimSpace(text), issuestore.ErrNotFound)
		}
		return rpcToolError{Tool: tool, Message: text}
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(text), result); err != nil {
		return fmt.Errorf("mcpstore: decode %s payload: %w (text=%q)", tool, err, text)
	}
	return nil
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int64         `json:"id"`
	Method  string        `json:"method"`
	Params  rpcCallParams `json:"params"`
}

type rpcCallParams struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

type rpcResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      any        `json:"id"`
	Result  rpcResult  `json:"result"`
	Error   *rpcError  `json:"error,omitempty"`
}

type rpcResult struct {
	Content []rpcContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type rpcContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcProtocolError struct {
	Tool    string
	Code    int
	Message string
}

func (e rpcProtocolError) Error() string {
	return fmt.Sprintf("mcpstore: %s: jsonrpc error %d: %s", e.Tool, e.Code, e.Message)
}

type rpcToolError struct {
	Tool    string
	Message string
}

func (e rpcToolError) Error() string {
	return fmt.Sprintf("mcpstore: %s: %s", e.Tool, e.Message)
}
