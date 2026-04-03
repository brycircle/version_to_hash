package mcp

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sirupsen/logrus"

	"github.com/version-to-hash/version-to-hash/internal/cache"
	"github.com/version-to-hash/version-to-hash/internal/github"
)

// Server wraps an MCP server that exposes the resolve_github_action tool.
type Server struct {
	cache     *cache.Cache
	ghClient  *github.Client
	log       *logrus.Logger
	mcpServer *server.MCPServer
}

// NewServer creates and configures the MCP server.
func NewServer(c *cache.Cache, ghClient *github.Client, log *logrus.Logger) *Server {
	s := &Server{cache: c, ghClient: ghClient, log: log}

	mcpSrv := server.NewMCPServer(
		"version-to-hash",
		"1.0.0",
	)

	tool := mcpgo.NewTool("resolve_github_action",
		mcpgo.WithDescription(
			"Resolves a GitHub Actions version tag (e.g. 'actions/checkout@v4') to a "+
				"pinned commit hash (e.g. 'actions/checkout@abc123...'). "+
				"Use this to prevent tag-shifting attacks where a compromised tag could point "+
				"to malicious code. The result can be used directly in a GitHub Actions workflow.",
		),
		mcpgo.WithString("action",
			mcpgo.Required(),
			mcpgo.Description(
				"GitHub action reference in the format 'owner/repo@version', "+
					"e.g. 'actions/checkout@v4' or 'actions/setup-python@v5'.",
			),
		),
	)

	mcpSrv.AddTool(tool, s.handleResolve)
	s.mcpServer = mcpSrv
	return s
}

func (s *Server) handleResolve(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	action, err := req.RequireString("action")
	if err != nil {
		return mcpgo.NewToolResultError("parameter 'action' is required"), nil
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return mcpgo.NewToolResultError("parameter 'action' must not be empty"), nil
	}

	s.log.WithField("action", action).Info("MCP: resolving action")

	hash, cached, err := s.resolve(action)
	if err != nil {
		s.log.WithError(err).WithField("action", action).Error("MCP: resolve failed")
		return mcpgo.NewToolResultError(fmt.Sprintf("failed to resolve %q: %v", action, err)), nil
	}

	pinnedRef := pinned(action, hash)
	cacheNote := ""
	if cached {
		cacheNote = " (from cache)"
	}

	return mcpgo.NewToolResultText(
		fmt.Sprintf("Pinned reference: %s\nCommit hash: %s%s", pinnedRef, hash, cacheNote),
	), nil
}

func (s *Server) resolve(action string) (hash string, cached bool, err error) {
	if h, ok := s.cache.Get(action); ok {
		return h, true, nil
	}

	hash, err = s.ghClient.ResolveToHash(action)
	if err != nil {
		return "", false, err
	}

	if storeErr := s.cache.Set(action, hash); storeErr != nil {
		s.log.WithError(storeErr).Warn("MCP: failed to cache result")
	}
	return hash, false, nil
}

// HTTPHandler returns an http.Handler serving the MCP streamable HTTP transport.
func (s *Server) HTTPHandler() http.Handler {
	return server.NewStreamableHTTPServer(s.mcpServer)
}

// ServeStdio runs the MCP server over stdin/stdout (for Claude Code and other
// MCP clients that launch the binary as a subprocess).
func (s *Server) ServeStdio() error {
	return server.ServeStdio(s.mcpServer)
}

func pinned(action, hash string) string {
	if i := strings.Index(action, "@"); i >= 0 {
		return action[:i+1] + hash
	}
	return action + "@" + hash
}
