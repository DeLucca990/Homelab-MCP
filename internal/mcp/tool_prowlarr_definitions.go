package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/prowlarr"
)

// PROWLARR INDEXER DEFINITIONS TOOL
//
// The catalogue of sites Prowlarr can add, with the settings each needs. It is
// the first step of adding one: prowlarr_indexer_add takes a definition name
// and the settings listed here. Read-only.

type prowlarrDefinitionsInput struct {
	Term     string `json:"term" jsonschema:"part of the site's name, e.g. '1337x' or 'nyaa'"`
	Protocol string `json:"protocol,omitempty" jsonschema:"'torrent' or 'usenet'"`
	Privacy  string `json:"privacy,omitempty" jsonschema:"'public', 'semiPrivate' or 'private' — public sites need no account"`
	Limit    int    `json:"limit,omitempty" jsonschema:"default 15, maximum 50"`
}

func handleProwlarrDefinitions(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in prowlarrDefinitionsInput,
) (*sdk.CallToolResult, prowlarr.Definitions, error) {
	out, err := prowlarr.GetDefinitions(ctx, in.Term, in.Protocol, in.Privacy, in.Limit)
	if err != nil {
		return nil, prowlarr.Definitions{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderProwlarrDefinitions(out)},
		},
	}, out, nil
}

func renderProwlarrDefinitions(d prowlarr.Definitions) string {
	var b strings.Builder

	for i, def := range d.Definitions {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%s   [definition: %s]\n", def.Name, def.Definition)
		fmt.Fprintf(&b, "  %s, %s", def.Protocol, def.Privacy)
		if def.Language != "" {
			fmt.Fprintf(&b, ", %s", def.Language)
		}
		if def.AlreadyAdded {
			b.WriteString(", ALREADY ADDED")
		}
		if def.NeedsFlareSolverr {
			b.WriteString(", needs FlareSolverr")
		}
		b.WriteString("\n")
		if def.Description != "" {
			desc := def.Description
			if len(desc) > 140 {
				desc = desc[:140] + "…"
			}
			fmt.Fprintf(&b, "  %s\n", desc)
		}
		if len(def.Settings) > 0 {
			parts := make([]string, 0, len(def.Settings))
			for _, s := range def.Settings {
				p := s.Name
				switch {
				case s.Secret:
					p += " (secret)"
				case len(s.Options) > 0:
					p += " (" + strings.Join(s.Options, "|") + ")"
				case s.Type == "checkbox":
					p += " (true|false)"
				}
				parts = append(parts, p)
			}
			fmt.Fprintf(&b, "  settings: %s\n", strings.Join(parts, ", "))
		}
	}

	fmt.Fprintf(&b, "\n%d of %d definitions matched, %d shown\n", d.MatchedCount, d.TotalCount, d.ShownCount)

	for _, w := range d.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
