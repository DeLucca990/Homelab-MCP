package jellyfin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Jellyfin's own view of itself. The container can be up, the web UI can load,
// and every playback can still fail — because the scheduled library scan has
// been erroring for a week, because a plugin did not survive the last update,
// or because the disk the transcodes are written to is full while the disk the
// media is on has terabytes free.
//
// The encoding configuration is here for a reason the version number is not: a
// server with no hardware acceleration configured is indistinguishable from a
// healthy one until the first person plays something their client cannot take
// directly, and then it is indistinguishable from a server under attack.

// Below this, a transcode has nowhere to write. Jellyfin buffers segments ahead
// of the viewer, so a 4K stream can want several gigabytes of it, and the
// failure when it runs out is a playback that stops rather than a disk error.
const lowTranscodeSpaceBytes = 5 << 30

// A scheduled task's last run older than this is reported in the warnings. Not
// a failure on its own — real-time monitoring can carry a library for weeks —
// but it is the first thing to know when something on disk is not in Jellyfin.
const staleScanSeconds = 7 * 24 * 3600

// Jellyfin's key for the "Scan Media Library" task. Matched by key rather than
// by name, which is translated into the server's own language.
const libraryScanKey = "RefreshLibrary"

type FolderSpace struct {
	Name        string `json:"name" jsonschema:"what this folder is for, e.g. transcode temp or a library's own name"`
	Path        string `json:"path"`
	FreeBytes   uint64 `json:"free_bytes,omitempty" jsonschema:"free space on the device holding this path, not a quota for the folder — two folders on one disk report the same number"`
	UsedBytes   uint64 `json:"used_bytes,omitempty"`
	StorageType string `json:"storage_type,omitempty"`
	DeviceID    string `json:"device_id,omitempty" jsonschema:"folders sharing this are on the same physical device"`
}

type Task struct {
	Name              string  `json:"name"`
	Key               string  `json:"key,omitempty"`
	State             string  `json:"state" jsonschema:"Idle, Running or Cancelling"`
	ProgressPercent   float64 `json:"progress_percent,omitempty" jsonschema:"only while running"`
	LastStatus        string  `json:"last_status,omitempty" jsonschema:"Completed, Failed, Cancelled or Aborted; empty means it has never run"`
	LastRunSecondsAgo uint64  `json:"last_run_seconds_ago,omitempty"`
	LastDuration      uint64  `json:"last_duration_seconds,omitempty"`
	ErrorMessage      string  `json:"error_message,omitempty"`
}

type Plugin struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Status  string `json:"status" jsonschema:"Active, Restart, Malfunctioned, NotSupported, Disabled, Superseded or Deleted"`
}

type Health struct {
	URL string `json:"url" jsonschema:"the address this server is talking to"`

	ServerName string `json:"server_name,omitempty"`
	Version    string `json:"version,omitempty"`
	OS         string `json:"os,omitempty"`

	PendingRestart bool `json:"pending_restart,omitempty" jsonschema:"true when Jellyfin has applied something that only takes effect after a restart"`
	ShuttingDown   bool `json:"shutting_down,omitempty"`

	HardwareAcceleration string   `json:"hardware_acceleration,omitempty" jsonschema:"the configured GPU backend — qsv, nvenc, vaapi, videotoolbox, amf, rkmpp. Empty means every transcode this server ever does will be done on the CPU"`
	HardwareEncoding     bool     `json:"hardware_encoding,omitempty" jsonschema:"whether the GPU is used for encoding as well as decoding; decode-only still leaves the encode on the CPU"`
	HardwareDecoders     []string `json:"hardware_decoders,omitempty" jsonschema:"the codecs the GPU is allowed to decode; a codec missing here is decoded on the CPU even with acceleration configured"`
	EncoderPath          string   `json:"encoder_path,omitempty" jsonschema:"the ffmpeg binary in use"`

	Folders []FolderSpace `json:"folders,omitempty"`

	Tasks            []Task `json:"tasks,omitempty" jsonschema:"only the ones worth reading: running, last-failed, and the library scan"`
	TaskCount        int    `json:"task_count,omitempty" jsonschema:"scheduled tasks in total"`
	RunningTaskCount int    `json:"running_task_count,omitempty"`
	FailedTaskCount  int    `json:"failed_task_count,omitempty"`

	Plugins     []Plugin `json:"plugins,omitempty" jsonschema:"only the ones not in the Active state"`
	PluginCount int      `json:"plugin_count,omitempty"`

	Warnings []string `json:"warnings,omitempty"`

	StandingWarnings []string `json:"standing_warnings,omitempty" jsonschema:"the subset of warnings that describe a configuration rather than a fault happening now; each also appears in warnings"`
}

// GetHealth reports whether Jellyfin itself is in a state to do its job.
//
// Four of the five reads are administrator-only, and a key without those rights
// fails them one at a time. Each failure becomes a warning rather than an error,
// on the same rule the *arr health tools follow: a check that could not run must
// not withhold the ones that did.
func GetHealth(ctx context.Context) (Health, error) {
	c, err := newClient()
	if err != nil {
		return Health{}, err
	}

	h := Health{URL: c.base}

	var (
		info     systemInfoJSON
		storage  storageJSON
		tasks    []taskJSON
		plugins  []pluginJSON
		encoding encodingJSON

		infoErr, storageErr, tasksErr, pluginsErr, encodingErr error

		wg sync.WaitGroup
	)

	wg.Add(5)
	go func() { defer wg.Done(); infoErr = c.get(ctx, "/System/Info", nil, &info) }()
	go func() { defer wg.Done(); storageErr = c.get(ctx, "/System/Info/Storage", nil, &storage) }()
	go func() { defer wg.Done(); tasksErr = c.get(ctx, "/ScheduledTasks", nil, &tasks) }()
	go func() { defer wg.Done(); pluginsErr = c.get(ctx, "/Plugins", nil, &plugins) }()
	go func() {
		defer wg.Done()
		encodingErr = c.get(ctx, "/System/Configuration/encoding", nil, &encoding)
	}()
	wg.Wait()

	if infoErr != nil && storageErr != nil && tasksErr != nil && pluginsErr != nil && encodingErr != nil {
		return Health{}, infoErr
	}

	if infoErr == nil {
		h.ServerName = info.ServerName
		h.Version = info.Version
		h.OS = strings.TrimSpace(nonEmpty(info.OperatingSystemDisplayName, info.OperatingSystem))
		h.PendingRestart = info.HasPendingRestart
		h.ShuttingDown = info.IsShuttingDown
	} else {
		h.Warnings = append(h.Warnings, "could not read Jellyfin's version: "+infoErr.Error())
	}

	if encodingErr == nil {
		if hw := strings.TrimSpace(encoding.HardwareAccelerationType); !strings.EqualFold(hw, "none") {
			h.HardwareAcceleration = hw
		}
		h.HardwareEncoding = encoding.EnableHardwareEncoding
		h.HardwareDecoders = encoding.HardwareDecodingCodecs
		h.EncoderPath = nonEmpty(encoding.EncoderAppPathDisplay, encoding.EncoderAppPath)
	} else {
		h.Warnings = append(h.Warnings, describeReadFailure("the encoding settings", encodingErr))
	}

	if storageErr == nil {
		h.Folders = storage.folders()
	} else {
		h.Warnings = append(h.Warnings, describeReadFailure("free space per folder", storageErr))
	}

	if tasksErr == nil {
		h.TaskCount = len(tasks)
		h.Tasks, h.RunningTaskCount, h.FailedTaskCount = notableTasks(tasks)
	} else {
		h.Warnings = append(h.Warnings, describeReadFailure("the scheduled tasks", tasksErr))
	}

	if pluginsErr == nil {
		h.PluginCount = len(plugins)
		for _, p := range plugins {
			if strings.EqualFold(p.Status, "Active") {
				continue
			}
			h.Plugins = append(h.Plugins, Plugin{Name: p.Name, Version: p.Version, Status: p.Status})
		}
	} else {
		h.Warnings = append(h.Warnings, describeReadFailure("the installed plugins", pluginsErr))
	}

	// What is wrong now, then how the server is set up. A reader who stops after
	// the first warning should have stopped on an incident.
	h.StandingWarnings = standingWarnings(h)
	h.Warnings = append(h.Warnings, healthWarnings(h)...)
	h.Warnings = append(h.Warnings, h.StandingWarnings...)

	return h, nil
}

// standingWarnings describe how the server is set up. They are true every
// second of its life rather than at this moment, which is what separates them
// from everything in healthWarnings — see Health.StandingWarnings.
//
// The encoding settings are the whole of this category today. They are stated
// even when nothing is transcoding, because that is the point: the cost is
// invisible until the first stream that needs it, and by then it is a fire
// rather than a setting.
func standingWarnings(h Health) []string {
	var out []string

	if h.HardwareAcceleration == "" {
		out = append(out, "no hardware acceleration is configured, so every transcode this "+
			"server does is re-encoded on the CPU — roughly one saturated core per stream")
	} else if !h.HardwareEncoding {
		out = append(out, fmt.Sprintf(
			"hardware acceleration is set to %s for decoding but hardware encoding is off, "+
				"so the encode half of every transcode still runs on the CPU", h.HardwareAcceleration))
	}

	return out
}

func healthWarnings(h Health) []string {
	var out []string

	if h.ShuttingDown {
		out = append(out, "jellyfin is shutting down")
	}
	if h.PendingRestart {
		out = append(out, "jellyfin has changes waiting for a restart — a plugin or an update "+
			"is installed and not running")
	}

	seenDevice := map[string]bool{}
	for _, f := range h.Folders {
		if f.FreeBytes == 0 || f.FreeBytes >= lowTranscodeSpaceBytes {
			continue
		}
		key := f.DeviceID
		if key == "" {
			key = f.Path
		}
		if seenDevice[key] {
			continue
		}
		seenDevice[key] = true
		out = append(out, fmt.Sprintf(
			"%s (%s) has under %s free — Jellyfin buffers a transcode ahead of the viewer, "+
				"and a stream that runs out of room there stops playing rather than reporting "+
				"a disk error",
			f.Name, f.Path, compactBytes(lowTranscodeSpaceBytes)))
	}

	for _, t := range h.Tasks {
		switch {
		case t.LastStatus == "Failed":
			msg := fmt.Sprintf("the %s task failed", t.Name)
			if t.LastRunSecondsAgo > 0 {
				msg += " " + compactSeconds(t.LastRunSecondsAgo) + " ago"
			}
			if t.ErrorMessage != "" {
				msg += ": " + t.ErrorMessage
			}
			out = append(out, msg)

		case t.Key == libraryScanKey && t.LastStatus == "":
			out = append(out, "the library scan has never run on this server, so anything the "+
				"*arrs have imported may be on disk and absent from Jellyfin")

		case t.Key == libraryScanKey && t.LastRunSecondsAgo > staleScanSeconds:
			out = append(out, fmt.Sprintf(
				"the library scan last ran %s ago — if real-time monitoring is off, files "+
					"added since then are on disk and not in the library",
				compactSeconds(t.LastRunSecondsAgo)))
		}
	}

	for _, p := range h.Plugins {
		switch strings.ToLower(p.Status) {
		case "malfunctioned":
			out = append(out, fmt.Sprintf(
				"the %s plugin has malfunctioned and is not running", p.Name))
		case "notsupported":
			out = append(out, fmt.Sprintf(
				"the %s plugin does not support this Jellyfin version and is not running", p.Name))
		case "restart":
			out = append(out, fmt.Sprintf(
				"the %s plugin needs a jellyfin restart before it runs", p.Name))
		}
	}

	return out
}

// notableTasks keeps what is worth reading out of the fifteen-odd scheduled
// tasks: whatever is running, whatever last failed, and the library scan —
// which is reported even when it succeeded, because "when did Jellyfin last
// look at the disk" is a question with an answer here and nowhere else.
func notableTasks(raw []taskJSON) (out []Task, running, failed int) {
	for _, r := range raw {
		t := r.toTask()

		if t.State == "Running" || t.State == "Cancelling" {
			running++
		}
		if t.LastStatus != "" && t.LastStatus != "Completed" {
			failed++
		}

		keep := t.State != "Idle" ||
			(t.LastStatus != "" && t.LastStatus != "Completed") ||
			t.Key == libraryScanKey
		if keep {
			out = append(out, t)
		}
	}

	// Running first, then whatever failed, then the scan.
	severity := func(t Task) int {
		switch {
		case t.LastStatus == "Failed":
			return 0
		case t.State == "Running" || t.State == "Cancelling":
			return 1
		case t.LastStatus != "" && t.LastStatus != "Completed":
			return 2
		default:
			return 3
		}
	}
	slices.SortFunc(out, func(a, b Task) int { return severity(a) - severity(b) })

	return out, running, failed
}

// describeReadFailure keeps the reason a section is missing attached to the
// section. A key without admin rights fails four of the five reads with the
// same message, and saying so once per read is what makes the fix obvious.
func describeReadFailure(what string, err error) string {
	if errors.Is(err, ErrForbidden) {
		return "could not read " + what + ": this API key is not an administrator key"
	}
	return "could not read " + what + ": " + err.Error()
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// compactBytes renders a threshold inside a warning sentence. The renderer has
// its own copy for table cells; this one exists so the collector never has to
// reach into the presentation layer.
func compactBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	if v < 10 {
		return fmt.Sprintf("%.1f%c", v, "KMGTPE"[exp])
	}
	return fmt.Sprintf("%.0f%c", v, "KMGTPE"[exp])
}

// --- wire types -----------------------------------------------------------

type systemInfoJSON struct {
	ServerName                 string `json:"ServerName"`
	Version                    string `json:"Version"`
	OperatingSystem            string `json:"OperatingSystem"`
	OperatingSystemDisplayName string `json:"OperatingSystemDisplayName"`
	HasPendingRestart          bool   `json:"HasPendingRestart"`
	IsShuttingDown             bool   `json:"IsShuttingDown"`
	TranscodingTempPath        string `json:"TranscodingTempPath"`
}

type folderJSON struct {
	Path        string `json:"Path"`
	FreeSpace   int64  `json:"FreeSpace"`
	UsedSpace   int64  `json:"UsedSpace"`
	StorageType string `json:"StorageType"`
	DeviceID    string `json:"DeviceId"`
}

type storageJSON struct {
	ProgramDataFolder      *folderJSON `json:"ProgramDataFolder"`
	CacheFolder            *folderJSON `json:"CacheFolder"`
	LogFolder              *folderJSON `json:"LogFolder"`
	InternalMetadataFolder *folderJSON `json:"InternalMetadataFolder"`
	TranscodingTempFolder  *folderJSON `json:"TranscodingTempFolder"`

	Libraries []struct {
		Name    string       `json:"Name"`
		Folders []folderJSON `json:"Folders"`
	} `json:"Libraries"`
}

// folders flattens Jellyfin's five named folders and every library path into
// one list. The transcode temp comes first: it is the one whose filling up
// breaks playback while every other reading still looks healthy.
func (s storageJSON) folders() []FolderSpace {
	var out []FolderSpace

	add := func(name string, f *folderJSON) {
		if f == nil || f.Path == "" {
			return
		}
		out = append(out, FolderSpace{
			Name:        name,
			Path:        f.Path,
			FreeBytes:   uint64(max(f.FreeSpace, 0)),
			UsedBytes:   uint64(max(f.UsedSpace, 0)),
			StorageType: f.StorageType,
			DeviceID:    f.DeviceID,
		})
	}

	add("transcode temp", s.TranscodingTempFolder)
	add("metadata", s.InternalMetadataFolder)
	add("cache", s.CacheFolder)
	add("logs", s.LogFolder)
	add("program data", s.ProgramDataFolder)

	for _, lib := range s.Libraries {
		for i := range lib.Folders {
			add("library: "+lib.Name, &lib.Folders[i])
		}
	}

	return out
}

type taskJSON struct {
	Name                      string   `json:"Name"`
	Key                       string   `json:"Key"`
	State                     string   `json:"State"`
	CurrentProgressPercentage *float64 `json:"CurrentProgressPercentage"`

	LastExecutionResult *struct {
		StartTimeUtc string `json:"StartTimeUtc"`
		EndTimeUtc   string `json:"EndTimeUtc"`
		Status       string `json:"Status"`
		ErrorMessage string `json:"ErrorMessage"`
	} `json:"LastExecutionResult"`
}

func (r taskJSON) toTask() Task {
	t := Task{Name: r.Name, Key: r.Key, State: r.State}

	if r.CurrentProgressPercentage != nil {
		t.ProgressPercent = round1(*r.CurrentProgressPercentage)
	}

	if e := r.LastExecutionResult; e != nil {
		t.LastStatus = e.Status
		t.ErrorMessage = e.ErrorMessage
		t.LastRunSecondsAgo = secondsSince(e.EndTimeUtc)

		start, end := secondsSince(e.StartTimeUtc), secondsSince(e.EndTimeUtc)
		if start > end {
			t.LastDuration = start - end
		}
	}

	return t
}

type pluginJSON struct {
	Name    string `json:"Name"`
	Version string `json:"Version"`
	Status  string `json:"Status"`
}

type encodingJSON struct {
	HardwareAccelerationType string   `json:"HardwareAccelerationType"`
	EnableHardwareEncoding   bool     `json:"EnableHardwareEncoding"`
	HardwareDecodingCodecs   []string `json:"HardwareDecodingCodecs"`
	EncoderAppPath           string   `json:"EncoderAppPath"`
	EncoderAppPathDisplay    string   `json:"EncoderAppPathDisplay"`
	TranscodingTempPath      string   `json:"TranscodingTempPath"`
}
