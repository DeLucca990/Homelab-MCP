package telegram

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

var adminOnlyTools = map[string]bool{
	"radarr_movie_edit":             true,
	"sonarr_series_edit":            true,
	"jellyfin_session_message":      true,
	"jellyfin_user_preferences_set": true,
	"bazarr_language_profile_set":   true,
	"bazarr_providers_reset":        true,
	"prowlarr_indexer_add":          true,
	"prowlarr_indexer_update":       true,
}

func adminOnly(t *sdk.Tool) bool {
	a := t.Annotations
	switch {
	case adminOnlyTools[t.Name], a == nil:
		return true
	case a.ReadOnlyHint:
		return false
	default:
		return a.DestructiveHint == nil || *a.DestructiveHint
	}
}

const (
	adminMark = "Admin only: the bot refuses it unless the sender is one of its admins. "
	adminNote = "The latest user message was sent by an admin of this bot."
	userNote  = "The latest user message was sent by someone who is not an admin of this bot. Tools marked Admin only are refused for them: " +
		"do not offer or attempt those; if they ask for one, say it needs an admin."
)
