package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/bazarr"
)

// BAZARR PROVIDERS RESET TOOL
//
// Clears every provider throttle — the Reset button on Bazarr's Providers page.
// It is the fix for "every provider is throttled" when the cause has gone
// away, and a way back into the same wall when it has not, so the
// confirmation names each throttle and why it was set.

func handleBazarrProvidersReset(
	ctx context.Context,
	req *sdk.CallToolRequest,
	_ emptyInput,
) (*sdk.CallToolResult, bazarr.ProviderReset, error) {
	before, err := bazarr.GetProviders(ctx)
	if err != nil {
		return nil, bazarr.ProviderReset{}, err
	}

	var throttled []string
	for _, p := range before {
		if p.Throttled {
			throttled = append(throttled, p.Name+":"+p.Status)
		}
	}
	if len(throttled) == 0 {
		return nil, bazarr.ProviderReset{}, fmt.Errorf(
			"no provider is throttled right now (%d enabled), so there is nothing to reset", len(before))
	}

	approved, pending, err := requireApproval(req, approval{
		message: bazarrResetConfirmation(before),
		fingerprint: fingerprint(
			"bazarr_providers_reset",
			strconv.Itoa(len(before)),
			strings.Join(throttled, ","),
		),
		refusal: "no provider throttle was reset",
		subject: fmt.Sprintf("reset %d throttled bazarr provider(s)", len(throttled)),
	})
	if !approved {
		return pending, bazarr.ProviderReset{}, err
	}

	out, err := bazarr.ResetProviders(ctx, before)
	if err != nil {
		return nil, bazarr.ProviderReset{}, err
	}

	return &sdk.CallToolResult{
		Content: []sdk.Content{
			&sdk.TextContent{Text: renderBazarrReset(out)},
		},
	}, out, nil
}

func bazarrResetConfirmation(providers []bazarr.Provider) string {
	var b strings.Builder

	b.WriteString("Clear the throttle on these subtitle providers?\n\n")
	for _, p := range providers {
		if !p.Throttled {
			continue
		}
		fmt.Fprintf(&b, "    %s — %s", p.Name, p.Status)
		if p.Retry != "" {
			fmt.Fprintf(&b, " (would retry %s)", p.Retry)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nBazarr will query them again on its next search. A provider throttled for " +
		"too many requests or a bad login is likely to be throttled again straight away.\n")
	return b.String()
}

func renderBazarrReset(r bazarr.ProviderReset) string {
	var b strings.Builder

	fmt.Fprintf(&b, "cleared %d provider throttle(s)\n", r.ClearedCount)

	if len(r.Providers) > 0 {
		b.WriteString("\n")
		cols := []column{
			{"PROVIDER", alignLeft},
			{"STATUS", alignLeft},
		}
		rows := make([][]string, 0, len(r.Providers))
		for _, p := range r.Providers {
			rows = append(rows, []string{p.Name, p.Status})
		}
		b.WriteString(table(cols, rows))
	}

	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nwarning: %s\n", w)
	}

	return b.String()
}
