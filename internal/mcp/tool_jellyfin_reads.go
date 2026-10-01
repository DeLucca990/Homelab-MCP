package mcp

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
)

// JELLYFIN USERS, ITEM SEARCH AND ACTIVITY TOOLS
//
// The reads that the writes build on: who the users are and how they are set
// up, whether an item is in the library at all, and what Jellyfin itself
// recorded happening. None of them change anything.

// --- users ---------------------------------------------------------------------------

func handleJellyfinUsers(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, jellyfin.Users, error) {
	out, err := jellyfin.GetUsers(ctx)
	if err != nil {
		return nil, jellyfin.Users{}, err
	}
	return &sdk.CallToolResult{
		Content: []sdk.Content{&sdk.TextContent{Text: renderJellyfinUsers(out)}},
	}, out, nil
}

func renderJellyfinUsers(u jellyfin.Users) string {
	var b strings.Builder

	cols := []column{
		{"USER", alignLeft},
		{"ROLE", alignLeft},
		{"AUDIO", alignLeft},
		{"SUBS", alignLeft},
		{"SUB MODE", alignLeft},
		{"LIBRARIES", alignLeft},
		{"REMOTE", alignLeft},
		{"TRANSCODE", alignLeft},
		{"SEEN", alignRight},
	}
	rows := make([][]string, 0, len(u.Users))
	for _, user := range u.Users {
		role := "user"
		switch {
		case user.Disabled:
			role = "DISABLED"
		case user.Administrator:
			role = "admin"
		}
		libs := "all"
		if !user.AllLibraries {
			libs = blank(strings.Join(user.Libraries, ", "))
		}
		remote := "no cap"
		if user.RemoteBitrateMbps > 0 {
			remote = fmt.Sprintf("%g Mbps", user.RemoteBitrateMbps)
		}
		rows = append(rows, []string{
			user.Name, role,
			blank(user.AudioLanguage), blank(user.SubtitleLanguage), user.SubtitleMode,
			libs, remote, yesNo(user.VideoTranscoding),
			compactDuration(user.LastActivitySecondsAgo),
		})
	}
	b.WriteString(table(cols, rows))
	b.WriteString("\nlanguages are Jellyfin's three-letter codes; '-' means no preference\n")

	for _, w := range u.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}
	return b.String()
}

// --- find item ---------------------------------------------------------------------

type jellyfinFindInput struct {
	Term  string `json:"term" jsonschema:"part of the title"`
	User  string `json:"user,omitempty" jsonschema:"a user name, to also show whether they have watched each item"`
	Limit int    `json:"limit,omitempty" jsonschema:"default 20, maximum 100"`
}

func handleJellyfinFind(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinFindInput,
) (*sdk.CallToolResult, jellyfin.ItemSearch, error) {
	out, err := jellyfin.SearchItems(ctx, in.Term, in.User, in.Limit)
	if err != nil {
		return nil, jellyfin.ItemSearch{}, err
	}

	var b strings.Builder
	if len(out.Items) > 0 {
		cols := []column{
			{"ITEM", alignLeft},
			{"TYPE", alignLeft},
			{"ADDED", alignRight},
		}
		if out.User != "" {
			cols = append(cols, column{"WATCHED", alignLeft})
		}
		cols = append(cols, column{"ID", alignLeft})
		rows := make([][]string, 0, len(out.Items))
		for _, it := range out.Items {
			row := []string{it.Name, it.Type, compactDuration(it.AddedSecondsAgo)}
			if out.User != "" {
				watched := "-"
				if it.Played != nil {
					watched = yesNo(*it.Played)
				}
				row = append(row, watched)
			}
			rows = append(rows, append(row, it.ID))
		}
		b.WriteString(table(cols, rows))
	}
	fmt.Fprintf(&b, "\n%d match %q\n", out.TotalCount, out.Term)
	for _, w := range out.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}

// --- activity ----------------------------------------------------------------------

type jellyfinActivityInput struct {
	Hours        int  `json:"hours,omitempty" jsonschema:"how far back to look; default 24, maximum 720"`
	Limit        int  `json:"limit,omitempty" jsonschema:"entries to list; default 50, maximum 200"`
	OnlyProblems bool `json:"only_problems,omitempty" jsonschema:"only warnings, errors and failed sign-ins"`
}

func handleJellyfinActivity(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinActivityInput,
) (*sdk.CallToolResult, jellyfin.Activity, error) {
	out, err := jellyfin.GetActivity(ctx, in.Hours, in.Limit, in.OnlyProblems)
	if err != nil {
		return nil, jellyfin.Activity{}, err
	}

	var b strings.Builder
	if len(out.Entries) > 0 {
		cols := []column{
			{"AGO", alignRight},
			{"SEVERITY", alignLeft},
			{"EVENT", alignLeft},
		}
		rows := make([][]string, 0, len(out.Entries))
		for _, e := range out.Entries {
			event := e.Name
			if e.Overview != "" && !strings.Contains(e.Name, e.Overview) {
				event += " — " + e.Overview
			}
			if len(event) > 140 {
				event = event[:140] + "…"
			}
			rows = append(rows, []string{compactDuration(e.SecondsAgo), e.Severity, event})
		}
		b.WriteString(table(cols, rows))
	}
	fmt.Fprintf(&b, "\nlast %dh: %d entries, %d warnings or errors, %d failed sign-ins\n",
		out.Hours, out.TotalCount, out.ProblemEntries, out.FailedLogins)
	for _, w := range out.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}, out, nil
}
