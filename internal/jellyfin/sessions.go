package jellyfin

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// Sessions is the one question no other tool on this server can answer: why the
// CPU is busy.
//
// homelab_overview deliberately reports no CPU, because a media server
// transcoding looks exactly like one in trouble. This is the reading that tells
// the two apart — and it does so at a finer grain than Jellyfin's own
// "Transcode" label, which covers both a remux costing nothing and a 4K HEVC
// re-encode saturating every core. What separates them is IsVideoDirect: if the
// video stream is passing through untouched, only the container or the audio is
// being rewritten. If it is not, ffmpeg is re-encoding every frame, and whether
// that lands on a GPU or on the CPU is the difference between a busy server and
// an unusable one.

// The window Jellyfin is asked about. A session that is playing something
// checks in every few seconds, so nothing being watched can fall outside this;
// what it excludes is the long tail of devices that connected earlier today and
// have been doing nothing since.
const activeWindowSeconds = 900

// How long a playing session may go without reporting progress before it is
// treated as a ghost. Clients report every few seconds, so minutes of silence
// while still "playing" means the viewer is gone and the transcode is not.
const staleCheckInSeconds = 300

// What a session is costing, which is not what PlayMethod says.
const (
	WorkDirect   = "direct"             // nothing is being rewritten
	WorkRemux    = "remux"              // container or audio only; video passes through
	WorkHardware = "hardware transcode" // video re-encoded on a GPU
	WorkSoftware = "software transcode" // video re-encoded on the CPU
	WorkIdle     = "idle"               // connected, playing nothing
)

type Session struct {
	ID   string `json:"id" jsonschema:"Jellyfin's session id; it is reassigned when a client reconnects"`
	User string `json:"user,omitempty"`

	Client     string `json:"client,omitempty" jsonschema:"the Jellyfin app, e.g. Jellyfin Web or Android TV"`
	Device     string `json:"device,omitempty" jsonschema:"the machine it is running on"`
	AppVersion string `json:"app_version,omitempty"`
	RemoteAddr string `json:"remote_address,omitempty" jsonschema:"where the client is connecting from; behind a reverse proxy every session reports the proxy's address"`

	NowPlaying string `json:"now_playing,omitempty" jsonschema:"what is being watched, empty when the session is idle"`
	ItemType   string `json:"item_type,omitempty" jsonschema:"Movie, Episode, Audio and so on"`
	Container  string `json:"container,omitempty" jsonschema:"the source file's container"`

	PlayMethod string `json:"play_method,omitempty" jsonschema:"what Jellyfin calls it: DirectPlay, DirectStream or Transcode"`
	Work       string `json:"work,omitempty" jsonschema:"what it actually costs: direct, remux, hardware transcode, software transcode, or idle. Transcode covers both a remux costing nothing and a full re-encode saturating the CPU, and this is the field that separates them"`

	HardwareAccel    string   `json:"hardware_acceleration,omitempty" jsonschema:"the GPU backend doing the re-encode: qsv, nvenc, vaapi, videotoolbox, amf, rkmpp. Empty during a video re-encode means it is being done on the CPU"`
	TranscodeReasons []string `json:"transcode_reasons,omitempty" jsonschema:"why Jellyfin would not send the file as it is, e.g. VideoCodecNotSupported or SubtitleCodecNotSupported"`

	Paused          bool    `json:"paused,omitempty"`
	PositionSeconds uint64  `json:"position_seconds,omitempty"`
	RuntimeSeconds  uint64  `json:"runtime_seconds,omitempty"`
	Percent         float64 `json:"percent,omitempty" jsonschema:"how far through the item the viewer is, 0-100"`

	TranscodeBitrate   uint64  `json:"transcode_bitrate,omitempty" jsonschema:"bits per second of the stream being produced"`
	TranscodeFramerate float64 `json:"transcode_framerate,omitempty" jsonschema:"frames per second ffmpeg is managing; below the source's rate means the encode cannot keep up and playback will buffer"`
	TranscodeWidth     int     `json:"transcode_width,omitempty"`
	TranscodeHeight    int     `json:"transcode_height,omitempty"`

	LastActivitySecondsAgo uint64 `json:"last_activity_seconds_ago,omitempty" jsonschema:"since the client last spoke to the server at all"`
	LastCheckInSecondsAgo  uint64 `json:"last_check_in_seconds_ago,omitempty" jsonschema:"since the client last reported playback progress"`

	Stale bool `json:"stale,omitempty" jsonschema:"true when the session claims to be playing but has not reported progress in minutes — the viewer is gone and whatever the playback costs is still being paid"`
}

type Sessions struct {
	Items []Session `json:"items"`

	TotalCount   int `json:"total_count" jsonschema:"sessions Jellyfin reported inside the activity window"`
	PlayingCount int `json:"playing_count"`
	IdleCount    int `json:"idle_count" jsonschema:"sessions connected but playing nothing; only listed when include_idle was set"`

	DirectCount   int `json:"direct_count" jsonschema:"streams costing nothing but disk and network"`
	RemuxCount    int `json:"remux_count" jsonschema:"streams where only the container or the audio is being rewritten"`
	HardwareCount int `json:"hardware_transcode_count"`
	SoftwareCount int `json:"software_transcode_count" jsonschema:"video re-encodes running on the CPU — each one is roughly a core, and this is what a load average is made of"`

	Warnings []string `json:"warnings,omitempty"`
}

type SessionOptions struct {
	IncludeIdle bool
}

// GetSessions reports what Jellyfin is serving right now.
func GetSessions(ctx context.Context, opts SessionOptions) (Sessions, error) {
	c, err := newClient()
	if err != nil {
		return Sessions{}, err
	}

	var raw []sessionJSON
	query := url.Values{"activeWithinSeconds": {strconv.Itoa(activeWindowSeconds)}}
	if err := c.get(ctx, "/Sessions", query, &raw); err != nil {
		return Sessions{}, err
	}

	s := Sessions{Items: make([]Session, 0, len(raw)), TotalCount: len(raw)}

	for _, r := range raw {
		item := r.toSession()

		switch item.Work {
		case WorkIdle:
			s.IdleCount++
		case WorkSoftware:
			s.SoftwareCount++
			s.PlayingCount++
		case WorkHardware:
			s.HardwareCount++
			s.PlayingCount++
		case WorkRemux:
			s.RemuxCount++
			s.PlayingCount++
		default:
			s.DirectCount++
			s.PlayingCount++
		}

		if item.Work == WorkIdle && !opts.IncludeIdle {
			continue
		}
		s.Items = append(s.Items, item)
	}

	// Most expensive first, then furthest from finishing: the stream that is
	// costing the machine is read before the one that is merely happening.
	slices.SortFunc(s.Items, func(a, b Session) int {
		if d := workSeverity(a) - workSeverity(b); d != 0 {
			return d
		}
		if a.TranscodeBitrate != b.TranscodeBitrate {
			if a.TranscodeBitrate > b.TranscodeBitrate {
				return -1
			}
			return 1
		}
		return strings.Compare(a.User, b.User)
	})

	s.Warnings = sessionWarnings(s)

	return s, nil
}

// workSeverity ranks sessions for display order. Lower sorts first.
func workSeverity(s Session) int {
	if s.Stale {
		return 0
	}
	switch s.Work {
	case WorkSoftware:
		return 1
	case WorkHardware:
		return 2
	case WorkRemux:
		return 3
	case WorkDirect:
		return 4
	default:
		return 5
	}
}

// Built here rather than in the renderer so a client reading only
// structuredContent sees them too.
func sessionWarnings(s Sessions) []string {
	var out []string

	for _, i := range s.Items {
		if !i.Stale {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s has not reported playback progress in %s but is still holding a %s of %s — "+
				"nobody is watching it and the server is still paying for it",
			i.who(), compactSeconds(i.LastCheckInSecondsAgo), i.Work, i.NowPlaying))
	}

	for _, i := range s.Items {
		if i.Work != WorkSoftware || i.Stale {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s is watching %s as a software transcode%s — the video is being re-encoded on "+
				"the CPU, which is roughly one saturated core per stream and is what a high "+
				"load average on a media server usually is",
			i.who(), i.NowPlaying, becauseOf(i.TranscodeReasons)))
	}

	// The one avoidable cause, and it is avoidable per file rather than per
	// server: a subtitle format the client cannot render is burned into the
	// picture, which forces a full re-encode of a file that would otherwise have
	// been sent untouched.
	for _, i := range s.Items {
		if i.Work != WorkSoftware && i.Work != WorkHardware {
			continue
		}
		if !slices.Contains(i.TranscodeReasons, "SubtitleCodecNotSupported") {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s is re-encoding %s only to burn subtitles into the picture — converting that "+
				"subtitle track to a text format, or turning it off, would make this stream "+
				"cost nothing",
			i.who(), i.NowPlaying))
	}

	if transcodes := s.SoftwareCount + s.HardwareCount; transcodes > 1 {
		out = append(out, fmt.Sprintf(
			"%d streams are being re-encoded at once (%d on the CPU, %d on hardware) — "+
				"concurrent transcodes are the load, and each new viewer adds another",
			transcodes, s.SoftwareCount, s.HardwareCount))
	}

	return out
}

// becauseOf renders Jellyfin's reasons into the sentence, because "it is
// transcoding" without them leaves nothing to act on.
func becauseOf(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	return " (" + strings.Join(reasons, ", ") + ")"
}

// who names a session the way a person would: the viewer, and the device when
// one user is watching on two of them.
func (s Session) who() string {
	switch {
	case s.User != "" && s.Device != "":
		return s.User + " on " + s.Device
	case s.User != "":
		return s.User
	case s.Device != "":
		return s.Device
	default:
		return "a session"
	}
}

// --- wire types -----------------------------------------------------------

type sessionJSON struct {
	ID                 string `json:"Id"`
	UserName           string `json:"UserName"`
	Client             string `json:"Client"`
	DeviceName         string `json:"DeviceName"`
	ApplicationVersion string `json:"ApplicationVersion"`
	RemoteEndPoint     string `json:"RemoteEndPoint"`

	LastActivityDate    string `json:"LastActivityDate"`
	LastPlaybackCheckIn string `json:"LastPlaybackCheckIn"`

	NowPlayingItem *struct {
		Name              string `json:"Name"`
		Type              string `json:"Type"`
		Container         string `json:"Container"`
		SeriesName        string `json:"SeriesName"`
		IndexNumber       *int   `json:"IndexNumber"`
		ParentIndexNumber *int   `json:"ParentIndexNumber"`
		ProductionYear    *int   `json:"ProductionYear"`
		RunTimeTicks      int64  `json:"RunTimeTicks"`
	} `json:"NowPlayingItem"`

	PlayState *struct {
		PositionTicks int64  `json:"PositionTicks"`
		IsPaused      bool   `json:"IsPaused"`
		PlayMethod    string `json:"PlayMethod"`
	} `json:"PlayState"`

	TranscodingInfo *struct {
		VideoCodec               string   `json:"VideoCodec"`
		AudioCodec               string   `json:"AudioCodec"`
		Container                string   `json:"Container"`
		IsVideoDirect            bool     `json:"IsVideoDirect"`
		IsAudioDirect            bool     `json:"IsAudioDirect"`
		Bitrate                  int64    `json:"Bitrate"`
		Framerate                float64  `json:"Framerate"`
		Width                    int      `json:"Width"`
		Height                   int      `json:"Height"`
		HardwareAccelerationType string   `json:"HardwareAccelerationType"`
		TranscodeReasons         []string `json:"TranscodeReasons"`
	} `json:"TranscodingInfo"`
}

func (r sessionJSON) toSession() Session {
	s := Session{
		ID:                     r.ID,
		User:                   r.UserName,
		Client:                 r.Client,
		Device:                 r.DeviceName,
		AppVersion:             r.ApplicationVersion,
		RemoteAddr:             r.RemoteEndPoint,
		LastActivitySecondsAgo: secondsSince(r.LastActivityDate),
		LastCheckInSecondsAgo:  secondsSince(r.LastPlaybackCheckIn),
	}

	if r.NowPlayingItem == nil {
		s.Work = WorkIdle
		return s
	}

	n := r.NowPlayingItem
	s.NowPlaying = itemName(n.SeriesName, n.Name, n.ParentIndexNumber, n.IndexNumber, n.ProductionYear)
	s.ItemType = n.Type
	s.Container = n.Container
	s.RuntimeSeconds = ticksToSeconds(n.RunTimeTicks)

	if r.PlayState != nil {
		s.Paused = r.PlayState.IsPaused
		s.PlayMethod = r.PlayState.PlayMethod
		s.PositionSeconds = ticksToSeconds(r.PlayState.PositionTicks)
		if s.RuntimeSeconds > 0 {
			s.Percent = round1(float64(s.PositionSeconds) / float64(s.RuntimeSeconds) * 100)
		}
	}

	if t := r.TranscodingInfo; t != nil {
		s.TranscodeReasons = t.TranscodeReasons
		s.TranscodeBitrate = uint64(max(t.Bitrate, 0))
		s.TranscodeFramerate = round1(t.Framerate)
		s.TranscodeWidth = t.Width
		s.TranscodeHeight = t.Height

		if hw := strings.TrimSpace(t.HardwareAccelerationType); !strings.EqualFold(hw, "none") {
			s.HardwareAccel = hw
		}
	}

	s.Work = classifyWork(r)

	if !s.Paused && s.LastCheckInSecondsAgo > staleCheckInSeconds {
		s.Stale = true
	}

	return s
}

// classifyWork is the judgement this whole file exists for. PlayMethod says
// what protocol was chosen; the transcode's own IsVideoDirect says whether any
// frame is actually being re-encoded, and the acceleration type says on what.
func classifyWork(r sessionJSON) string {
	t := r.TranscodingInfo
	if t == nil {
		if r.PlayState != nil && r.PlayState.PlayMethod == "DirectStream" {
			return WorkRemux
		}
		return WorkDirect
	}

	if t.IsVideoDirect {
		return WorkRemux
	}
	if strings.TrimSpace(t.HardwareAccelerationType) != "" &&
		!strings.EqualFold(t.HardwareAccelerationType, "none") {
		return WorkHardware
	}
	return WorkSoftware
}

// itemName is the item as a person would say it out loud: a series with its
// episode code, a film with its year.
func itemName(series, name string, season, episode, year *int) string {
	if series != "" && season != nil && episode != nil {
		out := fmt.Sprintf("%s S%02dE%02d", series, *season, *episode)
		if name != "" {
			out += " — " + name
		}
		return out
	}
	if series != "" && name != "" {
		return series + " — " + name
	}
	if year != nil && *year > 0 && name != "" {
		return fmt.Sprintf("%s (%d)", name, *year)
	}
	return name
}
