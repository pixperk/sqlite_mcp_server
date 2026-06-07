// Package handlers wires the key/value store to the MCP server: it exposes the
// store's operations as MCP tools and registers a prompt that pulls live data
// out of the store.
package handlers

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pixperk/mcp_toy/internal/store"
)

// Handlers holds the dependencies the tool/prompt handlers need. Using a struct
// (instead of a package-level global) keeps state explicit and testable.
type Handlers struct {
	Store *store.Store
}

// New returns a Handlers backed by the given store.
func New(s *store.Store) *Handlers { return &Handlers{Store: s} }

// ----------------------------------------------------------------------------
// Tool input/output types. Struct tags drive the auto-generated JSON Schema:
// `json` is the wire name, `jsonschema` is the description the model reads.
// ----------------------------------------------------------------------------

type SetInput struct {
	Key   string `json:"key" jsonschema:"the key to store the value under"`
	Value string `json:"value" jsonschema:"the value to store"`
}
type SetOutput struct {
	Status string `json:"status" jsonschema:"a short confirmation message"`
}

type GetInput struct {
	Key string `json:"key" jsonschema:"the key to look up"`
}
type GetOutput struct {
	Found bool   `json:"found" jsonschema:"true if the key existed"`
	Value string `json:"value" jsonschema:"the stored value, empty if not found"`
}

type DeleteInput struct {
	Key string `json:"key" jsonschema:"the key to delete"`
}
type DeleteOutput struct {
	Deleted bool `json:"deleted" jsonschema:"true if a row was actually deleted"`
}

type ListInput struct{}
type ListOutput struct {
	Keys []string `json:"keys" jsonschema:"all keys currently stored, sorted"`
}

type QueryInput struct {
	SQL string `json:"sql" jsonschema:"a read-only SELECT/WITH SQL query against the 'kv' table, which has columns: key, value"`
}
type QueryOutput struct {
	Columns []string   `json:"columns" jsonschema:"the result column names"`
	Rows    [][]string `json:"rows" jsonschema:"result rows; each row is a list of cell values as strings"`
}

// ----------------------------------------------------------------------------
// Tool handlers. Signature: (ctx, *CallToolRequest, In) (*CallToolResult, Out, error).
// Returning a nil *CallToolResult lets the SDK build the result from Out.
// ----------------------------------------------------------------------------

func (h *Handlers) set(ctx context.Context, _ *mcp.CallToolRequest, in SetInput) (*mcp.CallToolResult, SetOutput, error) {
	if err := h.Store.Set(ctx, in.Key, in.Value); err != nil {
		return nil, SetOutput{}, fmt.Errorf("set failed: %w", err)
	}
	return nil, SetOutput{Status: fmt.Sprintf("stored %q", in.Key)}, nil
}

func (h *Handlers) get(ctx context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, GetOutput, error) {
	value, found, err := h.Store.Get(ctx, in.Key)
	if err != nil {
		return nil, GetOutput{}, fmt.Errorf("get failed: %w", err)
	}
	return nil, GetOutput{Found: found, Value: value}, nil
}

func (h *Handlers) del(ctx context.Context, _ *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, DeleteOutput, error) {
	deleted, err := h.Store.Delete(ctx, in.Key)
	if err != nil {
		return nil, DeleteOutput{}, fmt.Errorf("delete failed: %w", err)
	}
	return nil, DeleteOutput{Deleted: deleted}, nil
}

func (h *Handlers) list(ctx context.Context, _ *mcp.CallToolRequest, _ ListInput) (*mcp.CallToolResult, ListOutput, error) {
	keys, err := h.Store.List(ctx)
	if err != nil {
		return nil, ListOutput{}, fmt.Errorf("list failed: %w", err)
	}
	return nil, ListOutput{Keys: keys}, nil
}

func (h *Handlers) query(ctx context.Context, _ *mcp.CallToolRequest, in QueryInput) (*mcp.CallToolResult, QueryOutput, error) {
	res, err := h.Store.Query(ctx, in.SQL)
	if err != nil {
		return nil, QueryOutput{}, fmt.Errorf("query failed: %w", err)
	}
	return nil, QueryOutput{Columns: res.Columns, Rows: res.Rows}, nil
}

// ----------------------------------------------------------------------------
// Prompt handler. A prompt is a reusable, server-defined template the user
// invokes (it shows up like a slash command in the client). This one reads the
// CURRENT contents of the store and templates them into a message asking the
// model to summarise, a nice demo of a prompt pulling live data.
// ----------------------------------------------------------------------------

func (h *Handlers) summarizeStore(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	focus := req.Params.Arguments["focus"] // optional templating argument

	data, err := h.Store.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading store: %w", err)
	}

	text := "Here are the current contents of the key/value store:\n\n"
	if len(data) == 0 {
		text += "(the store is empty)\n"
	} else {
		for k, v := range data {
			text += fmt.Sprintf("- %s = %s\n", k, v)
		}
	}
	text += "\nPlease summarise what this data represents"
	if focus != "" {
		text += ", focusing on: " + focus
	}
	text += "."

	return &mcp.GetPromptResult{
		Description: "Summarise the current key/value store",
		Messages: []*mcp.PromptMessage{
			{
				Role:    "user",
				Content: &mcp.TextContent{Text: text},
			},
		},
	}, nil
}

// Register adds all tools and prompts to the server. Tool Descriptions matter:
// they are how the model decides WHEN to call each tool.
func (h *Handlers) Register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "kv_set",
		Description: "Store a value under a key. Overwrites any existing value for that key.",
	}, h.set)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kv_get",
		Description: "Look up the value stored under a key. Returns found=false if the key does not exist.",
	}, h.get)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kv_delete",
		Description: "Delete a key and its value from the store.",
	}, h.del)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kv_list",
		Description: "List all keys currently stored, sorted alphabetically.",
	}, h.list)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "kv_query",
		Description: "Run a read-only SQL SELECT query against the 'kv' table (columns: key, value). Use for filtering, counting, or pattern matching that the simpler tools can't express.",
	}, h.query)

	server.AddPrompt(&mcp.Prompt{
		Name:        "summarize_store",
		Description: "Summarise the current contents of the key/value store.",
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "focus",
				Description: "Optional aspect to focus the summary on (e.g. 'user preferences').",
				Required:    false,
			},
		},
	}, h.summarizeStore)
}
