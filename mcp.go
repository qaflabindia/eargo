package ear

import "strings"

// McpServer is one MCP server declared to the runtime in plain English.
//
// Servers are stacked in memory.md under the `## MCP` section, one bullet per
// server, `name: what it provides`, with the launch command backticked or a
// URL written inline:
//
//	## MCP
//
//	- credit_bureau: pulls credit reports and score history, via `bureau-mcp-server`
//	- core_banking: reads account balances and repayment history, via `corebank-mcp-server`
//
// Like Tools, they are surfaced to the Reasoner as part of the operating
// strategy so the model reasons about them in natural language, and the
// declaration records how to reach the server for the runtime to connect.
//
// The declaration is the authorization. Runtime.ConnectMCP will only connect
// to a server the stack already named, so connecting one is the runtime
// reaching out to what the author declared -- never a capability that appears
// from nowhere.
type McpServer struct {
	Name        string
	Description string
	Command     string
	URL         string
}

// Describe renders the server for the model's strategy block.
func (s McpServer) Describe() string {
	line := s.Name
	if s.Description != "" {
		line += ": " + s.Description
	}
	if reach := s.Reach(); reach != "" {
		line += " (reached via `" + reach + "`)"
	}
	return line
}

// Reach is how the server is contacted: its launch command, or its URL.
func (s McpServer) Reach() string {
	if s.Command != "" {
		return s.Command
	}
	return s.URL
}

// readMCP parses the `## MCP` section's bullets into declared servers. A
// bullet naming no way to reach the server is still recorded -- the author
// described a capability, and the strategy block should show it -- but
// ConnectMCP will refuse it rather than guess a command.
func (s *Strategy) readMCP(body Body) {
	s.MCP = body.Prose
	for _, bullet := range body.Bullets {
		name, description := splitDeclaration(bullet)
		if strings.TrimSpace(name) == "" {
			continue
		}
		command, url, cleaned := extractReach(description)
		s.McpServers = append(s.McpServers, McpServer{
			Name:        name,
			Description: cleaned,
			Command:     command,
			URL:         url,
		})
	}
}

// McpServerNamed returns the declared server with this name, matched the same
// case- and punctuation-insensitive way as every other cross-reference in the
// stack.
func (s *Strategy) McpServerNamed(name string) (McpServer, bool) {
	want := Normalize(name)
	for _, server := range s.McpServers {
		if Normalize(server.Name) == want {
			return server, true
		}
	}
	return McpServer{}, false
}
