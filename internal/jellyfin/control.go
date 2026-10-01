package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Stopping a stream is two different operations wearing one name, and the
// session tool's "stale" finding is exactly the case where the obvious one
// does nothing.
//
// "Stop" as Jellyfin's dashboard sends it is a command to the client app: it
// only arrives if the app is still connected and accepts remote control. A
// viewer who closed a laptop lid is not connected — that is what made the
// session stale — so the command goes nowhere, and the ffmpeg process behind
// the stream keeps encoding for nobody. What ends that is killing the
// transcode itself, by device and play session, which is the second request
// here. A stop sends whichever of the two applies, and says which.

// How long a message stays on screen.
const messageTimeoutMs = 10_000

type StopPlan struct {
	Session Session `json:"session"`

	SendsStop      bool   `json:"sends_stop" jsonschema:"the client accepts remote control, so it is told to stop"`
	KillsTranscode bool   `json:"kills_transcode" jsonschema:"a transcode is running for this stream, and it is ended on the server"`
	Message        string `json:"message,omitempty" jsonschema:"shown on the viewer's screen before the stop"`

	Warnings []string `json:"warnings,omitempty"`

	deviceID      string
	playSessionID string
}

type StopResult struct {
	Plan         StopPlan `json:"plan"`
	StillPlaying bool     `json:"still_playing" jsonschema:"the session still reports playback right after the stop"`
	Warnings     []string `json:"warnings,omitempty"`
}

// PlanStop resolves a session stop without sending it.
func PlanStop(ctx context.Context, sessionID, message string) (StopPlan, error) {
	c, err := newClient()
	if err != nil {
		return StopPlan{}, err
	}
	raw, err := c.session(ctx, sessionID)
	if err != nil {
		return StopPlan{}, err
	}
	s := raw.toSession()
	if s.Work == WorkIdle {
		return StopPlan{}, fmt.Errorf("%s is not playing anything — there is nothing to stop", s.who())
	}

	p := StopPlan{Session: s, SendsStop: raw.SupportsRemoteControl, Message: strings.TrimSpace(message),
		deviceID: raw.DeviceID}
	if raw.PlayState != nil {
		p.playSessionID = raw.PlayState.PlaySessionID
	}
	p.KillsTranscode = raw.TranscodingInfo != nil && p.deviceID != "" && p.playSessionID != ""

	if !p.SendsStop && !p.KillsTranscode {
		return StopPlan{}, fmt.Errorf("%s does not accept remote control and is not transcoding, "+
			"so there is nothing the server can stop — it is streaming the file as it is, which "+
			"costs only disk and network, and ends when the client lets go", s.who())
	}
	if p.Message != "" && !p.SendsStop {
		p.Warnings = append(p.Warnings, "this client does not accept remote control, so the "+
			"message cannot be shown")
		p.Message = ""
	}
	if !p.SendsStop {
		p.Warnings = append(p.Warnings, "the client does not accept remote control; only the "+
			"transcode is ended, and the app shows a playback error rather than stopping cleanly")
	}
	if s.Stale {
		p.Warnings = append(p.Warnings, "the session has not reported progress in "+
			compactSeconds(s.LastCheckInSecondsAgo)+" — nobody is watching, and ending the "+
			"transcode is what frees the server")
	}
	return p, nil
}

// Stop sends a planned stop and reads the session back.
func Stop(ctx context.Context, p StopPlan) (StopResult, error) {
	c, err := newClient()
	if err != nil {
		return StopResult{}, err
	}

	res := StopResult{Plan: p}
	id := url.PathEscape(p.Session.ID)

	if p.Message != "" {
		if err := c.sendMessage(ctx, p.Session.ID, p.Message); err != nil {
			res.Warnings = append(res.Warnings, "the message could not be shown: "+err.Error())
		}
	}
	if p.SendsStop {
		if err := c.send(ctx, http.MethodPost, "/Sessions/"+id+"/Playing/Stop", nil, nil); err != nil {
			if !p.KillsTranscode {
				return StopResult{}, err
			}
			res.Warnings = append(res.Warnings, "the stop command was refused ("+err.Error()+
				"); ending the transcode instead")
		}
	}
	if p.KillsTranscode {
		q := url.Values{"deviceId": {p.deviceID}, "playSessionId": {p.playSessionID}}
		if err := c.send(ctx, http.MethodDelete, "/Videos/ActiveEncodings", q, nil); err != nil {
			return res, fmt.Errorf("the transcode could not be ended: %w", err)
		}
	}

	if after, err := c.session(ctx, p.Session.ID); err == nil && after.NowPlayingItem != nil {
		res.StillPlaying = true
		res.Warnings = append(res.Warnings, "the session still reports playing right after the "+
			"stop — a client acknowledges a few seconds later; jellyfin_active_sessions in a "+
			"moment says whether it took")
	}
	return res, nil
}

type MessagePlan struct {
	Session Session `json:"session"`
	Header  string  `json:"header"`
	Text    string  `json:"text"`
}

// PlanMessage resolves a message without sending it.
func PlanMessage(ctx context.Context, sessionID, text string) (MessagePlan, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return MessagePlan{}, fmt.Errorf("'text' is required — what to show on the screen")
	}
	if len(text) > 500 {
		return MessagePlan{}, fmt.Errorf("a message on a TV screen should be a sentence, not %d characters", len(text))
	}
	c, err := newClient()
	if err != nil {
		return MessagePlan{}, err
	}
	raw, err := c.session(ctx, sessionID)
	if err != nil {
		return MessagePlan{}, err
	}
	s := raw.toSession()
	if !raw.SupportsRemoteControl {
		return MessagePlan{}, fmt.Errorf("%s does not accept remote control, so it cannot show a "+
			"message", s.who())
	}
	return MessagePlan{Session: s, Header: "Homelab", Text: text}, nil
}

// SendMessage shows a planned message on the session's screen.
func SendMessage(ctx context.Context, p MessagePlan) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	return c.sendMessage(ctx, p.Session.ID, p.Text)
}

func (c *client) sendMessage(ctx context.Context, sessionID, text string) error {
	return c.send(ctx, http.MethodPost, "/Sessions/"+url.PathEscape(sessionID)+"/Message", nil,
		map[string]any{"Header": "Homelab", "Text": text, "TimeoutMs": messageTimeoutMs})
}

// session finds one session by id among the recently active ones.
func (c *client) session(ctx context.Context, id string) (sessionJSON, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return sessionJSON{}, fmt.Errorf("a 'session_id' is required — jellyfin_active_sessions lists them")
	}
	var raw []sessionJSON
	q := url.Values{"activeWithinSeconds": {strconv.Itoa(activeWindowSeconds)}}
	if err := c.get(ctx, "/Sessions", q, &raw); err != nil {
		return sessionJSON{}, err
	}
	for _, r := range raw {
		if r.ID == id {
			return r, nil
		}
	}
	return sessionJSON{}, fmt.Errorf("no active session %q — session ids change when a client "+
		"reconnects, so take one from a fresh jellyfin_active_sessions", id)
}
