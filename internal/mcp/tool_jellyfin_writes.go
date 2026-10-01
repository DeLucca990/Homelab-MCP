package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DeLucca990/homelab-mcp/internal/jellyfin"
)

// JELLYFIN WRITE TOOLS
//
// Each of these changes what someone sees or can do — a stream that stops, a
// message on a TV, a user who can no longer reach a library — so each asks
// first, and each confirmation shows the value before and after.

// --- library scan --------------------------------------------------------------------

type jellyfinScanInput struct {
	Library string `json:"library,omitempty" jsonschema:"one library, by name — homelab://jellyfin/libraries lists them"`
	ItemID  string `json:"item_id,omitempty" jsonschema:"one item, from jellyfin_find_item — refreshes just that film or series"`
}

func handleJellyfinScan(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinScanInput,
) (*sdk.CallToolResult, jellyfin.ScanPlan, error) {
	plan, err := jellyfin.PlanScan(ctx, in.Library, in.ItemID)
	if err != nil {
		return nil, jellyfin.ScanPlan{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Scan %s now?\n", plan.Target())
	if plan.Library != nil && len(plan.Library.Paths) > 0 {
		fmt.Fprintf(&b, "\n    %s\n", strings.Join(plan.Library.Paths, "\n    "))
	}
	if plan.Item != nil && plan.Item.Path != "" {
		fmt.Fprintf(&b, "\n    %s\n", plan.Item.Path)
	}
	b.WriteString("\nNew and changed files are picked up; edited metadata and images are kept.\n")
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("jellyfin_library_scan", plan.Scope, plan.Target()),
		refusal:     "no scan was started",
		subject:     "scan " + plan.Target() + " in jellyfin",
	})
	if !approved {
		return pending, jellyfin.ScanPlan{}, err
	}

	out, err := jellyfin.Scan(ctx, plan)
	if err != nil {
		return nil, jellyfin.ScanPlan{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "jellyfin is scanning %s\n", out.Target())
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

// --- session stop and message --------------------------------------------------------

type jellyfinStopInput struct {
	SessionID string `json:"session_id" jsonschema:"from a fresh jellyfin_active_sessions — ids change when a client reconnects"`
	Message   string `json:"message,omitempty" jsonschema:"shown on the viewer's screen before the stream stops"`
}

func handleJellyfinStop(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinStopInput,
) (*sdk.CallToolResult, jellyfin.StopResult, error) {
	plan, err := jellyfin.PlanStop(ctx, in.SessionID, in.Message)
	if err != nil {
		return nil, jellyfin.StopResult{}, err
	}
	s := plan.Session

	var b strings.Builder
	fmt.Fprintf(&b, "Stop this stream?\n\n")
	fmt.Fprintf(&b, "    %s is watching %s\n", s.Who(), s.NowPlaying)
	fmt.Fprintf(&b, "    %s", s.Work)
	if s.Stale {
		b.WriteString(", NOT REPORTING PROGRESS")
	}
	b.WriteString("\n\n")
	if plan.SendsStop {
		b.WriteString("The app is told to stop playback.\n")
	}
	if plan.KillsTranscode {
		b.WriteString("The transcode for it is ended on the server.\n")
	}
	if plan.Message != "" {
		fmt.Fprintf(&b, "First, this is shown on their screen: %q\n", plan.Message)
	}
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message: b.String(),
		fingerprint: fingerprint("jellyfin_session_stop", s.ID, s.User, s.NowPlaying,
			strconv.FormatBool(plan.SendsStop), strconv.FormatBool(plan.KillsTranscode), plan.Message),
		refusal: "no stream was stopped",
		subject: fmt.Sprintf("stop %s watching %s", s.Who(), s.NowPlaying),
	})
	if !approved {
		return pending, jellyfin.StopResult{}, err
	}

	out, err := jellyfin.Stop(ctx, plan)
	if err != nil {
		return nil, jellyfin.StopResult{}, err
	}
	var r strings.Builder
	fmt.Fprintf(&r, "stopped %s watching %s", s.Who(), s.NowPlaying)
	var how []string
	if plan.SendsStop {
		how = append(how, "stop sent to the app")
	}
	if plan.KillsTranscode {
		how = append(how, "transcode ended")
	}
	fmt.Fprintf(&r, " (%s)\n", strings.Join(how, ", "))
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

type jellyfinMessageInput struct {
	SessionID string `json:"session_id" jsonschema:"from a fresh jellyfin_active_sessions"`
	Text      string `json:"text" jsonschema:"one or two sentences; shown on screen for 10 seconds"`
}

func handleJellyfinMessage(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinMessageInput,
) (*sdk.CallToolResult, jellyfin.MessagePlan, error) {
	plan, err := jellyfin.PlanMessage(ctx, in.SessionID, in.Text)
	if err != nil {
		return nil, jellyfin.MessagePlan{}, err
	}
	who := plan.Session.Who()

	approved, pending, err := requireApproval(req, approval{
		message:     fmt.Sprintf("Show this on %s's screen?\n\n    %q\n", who, plan.Text),
		fingerprint: fingerprint("jellyfin_session_message", plan.Session.ID, plan.Text),
		refusal:     "no message was sent",
		subject:     "message " + who,
	})
	if !approved {
		return pending, jellyfin.MessagePlan{}, err
	}

	if err := jellyfin.SendMessage(ctx, plan); err != nil {
		return nil, jellyfin.MessagePlan{}, err
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{
		Text: fmt.Sprintf("shown on %s's screen: %q\n", who, plan.Text),
	}}}, plan, nil
}

// --- user preferences and access --------------------------------------------------------

type jellyfinPreferencesInput struct {
	User              string  `json:"user" jsonschema:"the user, by name — jellyfin_users lists them"`
	AudioLanguage     *string `json:"audio_language,omitempty" jsonschema:"preferred audio language: 'por', 'eng', 'pt-BR', 'English'…; 'none' clears it and plays the file's default track"`
	SubtitleLanguage  *string `json:"subtitle_language,omitempty" jsonschema:"preferred subtitle language, same forms; 'none' clears it"`
	SubtitleMode      string  `json:"subtitle_mode,omitempty" jsonschema:"Default (the file's own flags decide), Always (a subtitle in the preferred language, always), Smart (only when the audio is in another language), OnlyForced, or None"`
	RememberSelection *bool   `json:"remember_subtitle_selections,omitempty" jsonschema:"whether a subtitle picked by hand is used again for the next episode, ahead of the preference"`
}

func handleJellyfinPreferences(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinPreferencesInput,
) (*sdk.CallToolResult, jellyfin.UserChangeResult, error) {
	plan, err := jellyfin.PlanPreferences(ctx, jellyfin.PreferencesRequest{
		User:              in.User,
		AudioLanguage:     in.AudioLanguage,
		SubtitleLanguage:  in.SubtitleLanguage,
		SubtitleMode:      in.SubtitleMode,
		RememberSubtitles: in.RememberSelection,
	})
	if err != nil {
		return nil, jellyfin.UserChangeResult{}, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Change %s's playback preferences?\n\n", plan.User)
	writeChanges(&b, plan.Changes)
	for _, c := range plan.Changes {
		if c.Field == "subtitle mode" {
			fmt.Fprintf(&b, "\n%s %s.\n", c.To, jellyfin.SubtitleModeMeaning(c.To))
		}
	}
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("jellyfin_user_preferences_set", plan.UserID, plan.Summary()),
		refusal:     "no preference was changed",
		subject:     fmt.Sprintf("set jellyfin preferences of %s (%s)", plan.User, plan.Summary()),
	})
	if !approved {
		return pending, jellyfin.UserChangeResult{}, err
	}

	out, err := jellyfin.SetPreferences(ctx, plan)
	if err != nil {
		return nil, jellyfin.UserChangeResult{}, err
	}
	return userChangeResult(plan.User, out), out, nil
}

type jellyfinAccessInput struct {
	User              string   `json:"user" jsonschema:"the user, by name"`
	Libraries         []string `json:"libraries,omitempty" jsonschema:"the libraries they may see, by name — replaces the current list; ['all'] grants every library"`
	RemoteBitrateMbps *float64 `json:"remote_bitrate_mbps,omitempty" jsonschema:"cap on streams outside the home network, in Mbps; 0 removes the cap"`
	VideoTranscoding  *bool    `json:"video_transcoding,omitempty" jsonschema:"whether Jellyfin may re-encode video for them; off means a file their client cannot play directly fails"`
	Remuxing          *bool    `json:"remuxing,omitempty"`
	Disabled          *bool    `json:"disabled,omitempty" jsonschema:"true signs them out everywhere and blocks sign-in; administrators cannot be disabled"`
}

func handleJellyfinAccess(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinAccessInput,
) (*sdk.CallToolResult, jellyfin.UserChangeResult, error) {
	plan, err := jellyfin.PlanAccess(ctx, jellyfin.AccessRequest{
		User:              in.User,
		Libraries:         in.Libraries,
		RemoteBitrateMbps: in.RemoteBitrateMbps,
		VideoTranscoding:  in.VideoTranscoding,
		Remuxing:          in.Remuxing,
		Disabled:          in.Disabled,
	})
	if err != nil {
		return nil, jellyfin.UserChangeResult{}, err
	}

	var b strings.Builder
	role := "user"
	if plan.Administrator {
		role = "administrator"
	}
	fmt.Fprintf(&b, "Change what %s (%s) can do in Jellyfin?\n\n", plan.User, role)
	writeChanges(&b, plan.Changes)
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("jellyfin_user_access_set", plan.UserID, plan.Summary()),
		refusal:     "no access was changed",
		subject:     fmt.Sprintf("set jellyfin access of %s (%s)", plan.User, plan.Summary()),
	})
	if !approved {
		return pending, jellyfin.UserChangeResult{}, err
	}

	out, err := jellyfin.SetAccess(ctx, plan)
	if err != nil {
		return nil, jellyfin.UserChangeResult{}, err
	}
	return userChangeResult(plan.User, out), out, nil
}

func userChangeResult(name string, out jellyfin.UserChangeResult) *sdk.CallToolResult {
	var b strings.Builder
	fmt.Fprintf(&b, "changed %s:\n", name)
	writeChanges(&b, out.Changes)
	writeWarnings(&b, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: b.String()}}}
}

// --- transcoding ------------------------------------------------------------------------

type jellyfinTranscodingInput struct {
	Acceleration     string   `json:"acceleration,omitempty" jsonschema:"none, qsv (Intel), vaapi (Intel or AMD on Linux), nvenc (NVIDIA), amf, videotoolbox, rkmpp or v4l2m2m"`
	Device           string   `json:"device,omitempty" jsonschema:"for vaapi or qsv: the render node, usually /dev/dri/renderD128"`
	DecodeCodecs     []string `json:"decode_codecs,omitempty" jsonschema:"codecs to decode on the GPU — replaces the list: h264, hevc, mpeg2video, mpeg4, vc1, vp8, vp9, av1. Only what the GPU supports"`
	HardwareEncoding *bool    `json:"hardware_encoding,omitempty" jsonschema:"encode on the GPU too, not only decode"`
	Tonemapping      *bool    `json:"tonemapping,omitempty" jsonschema:"convert HDR to SDR for screens that cannot show HDR"`
}

func handleJellyfinTranscoding(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinTranscodingInput,
) (*sdk.CallToolResult, jellyfin.EncodingResult, error) {
	plan, err := jellyfin.PlanEncoding(ctx, jellyfin.EncodingRequest{
		Acceleration:     in.Acceleration,
		Device:           in.Device,
		DecodeCodecs:     in.DecodeCodecs,
		HardwareEncoding: in.HardwareEncoding,
		Tonemapping:      in.Tonemapping,
	})
	if err != nil {
		return nil, jellyfin.EncodingResult{}, err
	}

	var b strings.Builder
	b.WriteString("Change how Jellyfin transcodes? This applies to every stream that needs a transcode.\n\n")
	writeChanges(&b, plan.Changes)
	b.WriteString("\nTo undo:\n")
	writeChanges(&b, plan.Revert)
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("jellyfin_transcoding_set", plan.Summary()),
		refusal:     "the transcoding settings were not changed",
		subject:     "set jellyfin transcoding (" + plan.Summary() + ")",
	})
	if !approved {
		return pending, jellyfin.EncodingResult{}, err
	}

	out, err := jellyfin.SetEncoding(ctx, plan)
	if err != nil {
		return nil, jellyfin.EncodingResult{}, err
	}
	var r strings.Builder
	r.WriteString("changed jellyfin's transcoding:\n")
	writeChanges(&r, out.Changes)
	r.WriteString("\nto undo:\n")
	writeChanges(&r, out.Revert)
	writeWarnings(&r, out.Warnings)
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: r.String()}}}, out, nil
}

// --- played state -----------------------------------------------------------------------

type jellyfinPlayedInput struct {
	User   string `json:"user" jsonschema:"the user whose watched state changes"`
	ItemID string `json:"item_id" jsonschema:"from jellyfin_find_item; a series marks every episode"`
	Played *bool  `json:"played,omitempty" jsonschema:"true for watched (the default), false for unwatched"`
}

func handleJellyfinPlayed(
	ctx context.Context,
	req *sdk.CallToolRequest,
	in jellyfinPlayedInput,
) (*sdk.CallToolResult, jellyfin.PlayedPlan, error) {
	played := boolOr(in.Played, true)
	plan, err := jellyfin.PlanPlayed(ctx, in.User, in.ItemID, played)
	if err != nil {
		return nil, jellyfin.PlayedPlan{}, err
	}
	word := "watched"
	if !played {
		word = "unwatched"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Mark %s as %s for %s?\n", plan.Item.Name, word, plan.User)
	writeNotes(&b, plan.Warnings)

	approved, pending, err := requireApproval(req, approval{
		message:     b.String(),
		fingerprint: fingerprint("jellyfin_mark_played", plan.UserID, plan.Item.ID, word),
		refusal:     "nothing was marked",
		subject:     fmt.Sprintf("mark %s %s for %s", plan.Item.Name, word, plan.User),
	})
	if !approved {
		return pending, jellyfin.PlayedPlan{}, err
	}

	out, err := jellyfin.SetPlayed(ctx, plan)
	if err != nil {
		return nil, jellyfin.PlayedPlan{}, err
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{
		Text: fmt.Sprintf("marked %s as %s for %s\n", out.Item.Name, word, out.User),
	}}}, out, nil
}

func writeChanges(b *strings.Builder, changes []jellyfin.Change) {
	for _, c := range changes {
		fmt.Fprintf(b, "    %s: %s → %s\n", c.Field, c.From, c.To)
	}
}
