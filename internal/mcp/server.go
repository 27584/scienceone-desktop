package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ToolSchema is the shape the platform reads from tools/list.
type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Server speaks Streamable HTTP well enough for the platform's httpx client
// (protocol 2024-11-05) and for newer callers.
//
// Observed client behaviour: a full handshake on every tool call
// (initialize -> notifications/initialized -> tools/list -> tools/call), no
// session state, and the auth header replayed into the query string. Because it
// re-initialises each time, nothing is kept per session here.
type Server struct {
	Tools *Tools
	Token string
	Log   func(event string, data any)
}

func NewServer(tools *Tools, token string) *Server {
	return &Server{Tools: tools, Token: token, Log: func(string, any) {}}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		s.Log("unauthorized", map[string]any{"remote": r.RemoteAddr, "path": r.URL.Path})
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Some clients probe for an SSE stream before posting. Answer with a
		// description instead of holding the connection open forever.
		writeJSON(w, http.StatusOK, map[string]any{
			"server":    "panshi-desktop",
			"transport": "streamable-http",
			"tools":     toolNames(),
		})
		return
	case http.MethodPost:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "parse error"},
		})
		return
	}

	resp, ok := s.dispatch(r.Context(), &req)
	if !ok {
		// A notification: acknowledge with 202 and no body.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(resp)
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// dispatch returns (response, true) for requests and (nil, false) for
// notifications, which take no reply.
func (s *Server) dispatch(ctx context.Context, req *rpcRequest) (*rpcResponse, bool) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2024-11-05"
		}
		s.Log("initialize", map[string]any{"client": p.ClientInfo.Name, "protocol": p.ProtocolVersion})
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				// Echo the caller's version so every client generation is satisfied.
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
				"serverInfo":      map[string]any{"name": "panshi-desktop", "version": Version},
			},
		}, true

	case "notifications/initialized", "initialized", "notifications/cancelled":
		return nil, false

	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}, true

	case "tools/list":
		s.Log("tools/list", nil)
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": ToolList()}}, true

	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return s.errorResponse(req.ID, -32602, "invalid params"), true
		}
		text, isErr := s.callTool(ctx, p.Name, p.Arguments)
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			},
		}, true
	}

	return s.errorResponse(req.ID, -32601, "method not found: "+req.Method), true
}

func (s *Server) errorResponse(id json.RawMessage, code int, msg string) *rpcResponse {
	return &rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}

func (s *Server) callTool(ctx context.Context, name string, args map[string]any) (string, bool) {
	if args == nil {
		args = map[string]any{}
	}
	var (
		text string
		err  error
	)
	switch name {
	case "list_dir":
		text, err = s.Tools.listDir(args)
	case "read_file":
		text, err = s.Tools.readFile(args)
	case "write_file":
		text, err = s.Tools.writeFile(args)
	case "apply_patch":
		text, err = s.Tools.applyPatch(args)
	case "search_code":
		text, err = s.Tools.searchCode(args)
	case "find_files":
		text, err = s.Tools.findFiles(args)
	case "replace_in_files":
		text, err = s.Tools.replaceInFiles(args)
	case "git_status":
		text, err = s.Tools.gitStatus(ctx, args)
	case "git_diff":
		text, err = s.Tools.gitDiff(ctx, args)
	case "run_shell":
		text, err = s.Tools.runShell(ctx, args)
	case "open_path":
		text, err = s.Tools.openPath(args)
	case "ping":
		text = s.Tools.ping()
	default:
		text, err = "unknown tool: "+name, fmt.Errorf("unknown tool")
	}

	isErr := err != nil
	if isErr {
		text = "Error: " + err.Error()
	}
	s.Log("tools/call", map[string]any{"name": name, "arguments": args, "isError": isErr, "result": firstRunes(text, 500)})
	return text, isErr
}

func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	candidates := []string{
		r.Header.Get("X-MCP-Token"),
		r.Header.Get("X-Mcp-Token"),
		strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
		r.URL.Query().Get("X-MCP-Token"), // the platform replays the header here
		r.URL.Query().Get("token"),
	}
	for _, c := range candidates {
		if c != "" && c == s.Token {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func toolNames() []string {
	names := make([]string, 0, len(ToolList()))
	for _, t := range ToolList() {
		names = append(names, t.Name)
	}
	return names
}
