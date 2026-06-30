package main

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

//go:embed skills/investigate.md
var investigateSkillMD string

// registerPrompts adds MCP prompts to the server. Prompts appear as
// /mcp__fleet-qa__<name> slash commands in any Claude Code project that has
// this server configured — no need to have the fleet-qa-mcp repo open.
func registerPrompts(s *server.MCPServer) {
	s.AddPrompt(mcp.Prompt{
		Name:        "investigate",
		Description: "Run a full QA investigation for a Fleet GitHub issue: reproduce via API + browser, root-cause in deployed code, classify released/unreleased, draft a prefilled bug report.",
		Arguments: []mcp.PromptArgument{
			{
				Name:        "issue",
				Description: "GitHub issue number or URL, e.g. 47812 or https://github.com/fleetdm/fleet/issues/47812",
				Required:    true,
			},
			{
				Name:        "mode",
				Description: "'' = general investigation, 'reproduce' = follow the bug's steps to reproduce, 'testplan' = walk the story's test plan",
				Required:    false,
			},
		},
	}, func(_ context.Context, r mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		issue := r.Params.Arguments["issue"]
		mode := r.Params.Arguments["mode"]

		header := fmt.Sprintf("Investigate Fleet issue %s", issue)
		if mode != "" {
			header += fmt.Sprintf(" (mode: %s)", mode)
		}
		text := header + ".\n\n" + investigateSkillMD

		return &mcp.GetPromptResult{
			Description: "Fleet QA investigation workflow",
			Messages: []mcp.PromptMessage{
				{
					Role:    mcp.RoleUser,
					Content: mcp.TextContent{Type: "text", Text: text},
				},
			},
		}, nil
	})
}
