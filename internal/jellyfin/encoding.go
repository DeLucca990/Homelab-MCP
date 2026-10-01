package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// Hardware acceleration is the setting with the widest blast radius on this
// server: every transcode goes through it. Set to a backend the machine does
// not have — or has, but the container cannot see — and every stream that
// needs a transcode fails, for everyone, until it is set back.
//
// So the change is narrow (the backend, its device, which codecs are decoded
// in hardware, whether encoding is too), the confirmation states the current
// values as the way back, and the result says how to prove it worked: play
// something that needs a transcode and check jellyfin_active_sessions reports
// a hardware transcode rather than a software one.

var accelBackends = []string{"none", "qsv", "vaapi", "nvenc", "amf", "videotoolbox", "rkmpp", "v4l2m2m"}

var decodeCodecs = []string{"h264", "hevc", "mpeg2video", "mpeg4", "vc1", "vp8", "vp9", "av1"}

type EncodingRequest struct {
	Acceleration     string
	Device           string
	DecodeCodecs     []string
	HardwareEncoding *bool
	Tonemapping      *bool
}

type EncodingPlan struct {
	Changes []Change `json:"changes"`
	Revert  []Change `json:"revert" jsonschema:"the values to set to undo this"`

	Warnings []string `json:"warnings,omitempty"`

	options map[string]any
}

// Summary is the changes as one line, for the fingerprint and the log.
func (p EncodingPlan) Summary() string { return summarize(p.Changes) }

type EncodingResult struct {
	Changes  []Change `json:"changes"`
	Revert   []Change `json:"revert"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanEncoding resolves a transcoding change without making it.
func PlanEncoding(ctx context.Context, req EncodingRequest) (EncodingPlan, error) {
	if req.Acceleration == "" && req.Device == "" && req.DecodeCodecs == nil &&
		req.HardwareEncoding == nil && req.Tonemapping == nil {
		return EncodingPlan{}, fmt.Errorf("nothing to change — pass 'acceleration', 'device', " +
			"'decode_codecs', 'hardware_encoding' or 'tonemapping'")
	}

	c, err := newClient()
	if err != nil {
		return EncodingPlan{}, err
	}
	var opts map[string]any
	if err := c.get(ctx, "/System/Configuration/encoding", nil, &opts); err != nil {
		return EncodingPlan{}, err
	}

	p := EncodingPlan{options: opts}
	change := func(field, key string, from, to any) {
		f, t := display(from), display(to)
		if f == t {
			return
		}
		p.Changes = append(p.Changes, Change{field, f, t})
		p.Revert = append(p.Revert, Change{field, t, f})
		opts[key] = to
	}

	accel := strings.ToLower(nonEmpty(str(opts["HardwareAccelerationType"]), "none"))
	if req.Acceleration != "" {
		want := strings.ToLower(strings.TrimSpace(req.Acceleration))
		if !slices.Contains(accelBackends, want) {
			return EncodingPlan{}, fmt.Errorf("'acceleration' is one of %s — qsv for Intel, "+
				"nvenc for NVIDIA, vaapi for AMD or Intel on Linux, amf for AMD on Windows",
				strings.Join(accelBackends, ", "))
		}
		change("hardware acceleration", "HardwareAccelerationType", accel, want)
		accel = want
	}

	if req.Device != "" {
		dev := strings.TrimSpace(req.Device)
		if !strings.HasPrefix(dev, "/dev/") {
			return EncodingPlan{}, fmt.Errorf("'device' is a device node such as /dev/dri/renderD128")
		}
		switch accel {
		case "vaapi":
			change("VA-API device", "VaapiDevice", opts["VaapiDevice"], dev)
		case "qsv":
			change("QSV device", "QsvDevice", opts["QsvDevice"], dev)
		default:
			return EncodingPlan{}, fmt.Errorf("a device is only set for vaapi or qsv, not %s", accel)
		}
	}

	if req.DecodeCodecs != nil {
		var want []string
		for _, codec := range req.DecodeCodecs {
			codec = strings.ToLower(strings.TrimSpace(codec))
			if codec == "h265" {
				codec = "hevc"
			}
			if !slices.Contains(decodeCodecs, codec) {
				return EncodingPlan{}, fmt.Errorf("'decode_codecs' takes %s, not %q",
					strings.Join(decodeCodecs, ", "), codec)
			}
			if !slices.Contains(want, codec) {
				want = append(want, codec)
			}
		}
		change("hardware decoding", "HardwareDecodingCodecs", opts["HardwareDecodingCodecs"], want)
	}

	if req.HardwareEncoding != nil {
		change("hardware encoding", "EnableHardwareEncoding", opts["EnableHardwareEncoding"], *req.HardwareEncoding)
	}
	if req.Tonemapping != nil {
		change("tone mapping", "EnableTonemapping", opts["EnableTonemapping"], *req.Tonemapping)
	}

	if len(p.Changes) == 0 {
		return EncodingPlan{}, fmt.Errorf("the transcoding settings already have those values — nothing to change")
	}

	switch accel {
	case "none":
		p.Warnings = append(p.Warnings, "with no hardware acceleration every video transcode "+
			"runs on the CPU — roughly one saturated core per stream")
	case "vaapi", "qsv":
		p.Warnings = append(p.Warnings, "in Docker, the container needs the GPU passed through "+
			"(devices: /dev/dri) and its user in the render group; without it every transcode fails")
	case "nvenc":
		p.Warnings = append(p.Warnings, "in Docker, the container needs the NVIDIA runtime and "+
			"NVIDIA_VISIBLE_DEVICES; without it every transcode fails")
	}
	if accel != "none" {
		codecs := toStrings(opts["HardwareDecodingCodecs"])
		if !slices.Contains(codecs, "hevc") {
			p.Warnings = append(p.Warnings, "HEVC is not decoded in hardware, so 4K and most "+
				"recent releases still decode on the CPU — add it to 'decode_codecs' if the GPU supports it")
		}
		p.Warnings = append(p.Warnings, "if the GPU does not support this backend, every "+
			"transcode fails until it is set back — the 'revert' values undo it")
	}
	return p, nil
}

// SetEncoding applies a planned transcoding change.
func SetEncoding(ctx context.Context, p EncodingPlan) (EncodingResult, error) {
	c, err := newClient()
	if err != nil {
		return EncodingResult{}, err
	}
	if err := c.send(ctx, http.MethodPost, "/System/Configuration/encoding", nil, p.options); err != nil {
		return EncodingResult{}, err
	}
	return EncodingResult{
		Changes: p.Changes,
		Revert:  p.Revert,
		Warnings: []string{"to prove it works, play something that needs a transcode and check " +
			"jellyfin_active_sessions: 'hardware transcode' means it took, 'software transcode' " +
			"means it fell back to the CPU, and a playback error means the backend is wrong"},
	}, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// toStrings reads a list that is []any when it came from Jellyfin and
// []string when this package just set it.
func toStrings(v any) []string {
	if typed, ok := v.([]string); ok {
		return typed
	}
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func display(v any) string {
	switch t := v.(type) {
	case nil:
		return "none"
	case string:
		return nonEmpty(t, "none")
	case bool:
		return yesNo(t)
	case []string:
		return nonEmpty(strings.Join(t, ", "), "none")
	case []any:
		return nonEmpty(strings.Join(toStrings(t), ", "), "none")
	}
	return fmt.Sprint(v)
}
