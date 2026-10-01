package prowlarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
)

// Prowlarr's reason to exist is pushing indexers into Radarr and Sonarr, and
// that push has three ways to quietly not happen:
//
//   - the sync level. Full Sync keeps each *arr's copy identical to Prowlarr;
//     Add Only pushes new indexers and never touches them again, so disabling
//     or re-prioritising one in Prowlarr changes nothing downstream; Disabled
//     pushes nothing at all.
//   - tags. An application with tags only receives indexers that share one of
//     them, so a new indexer without the tag reaches nobody.
//   - reachability. Prowlarr has to reach the *arr's API to push anything.
//
// None of those is an error anywhere. This view works out, per application,
// which indexers actually reach it — the same rule Prowlarr applies — so the
// gap shows as a gap.

// Application is one app Prowlarr syncs indexers into.
type Application struct {
	ID             int      `json:"id"`
	Name           string   `json:"name"`
	Implementation string   `json:"implementation" jsonschema:"Radarr, Sonarr, Lidarr, Readarr…"`
	SyncLevel      string   `json:"sync_level" jsonschema:"fullSync keeps the app identical to Prowlarr; addOnly pushes new indexers and never updates or removes them; disabled pushes nothing"`
	Tags           []string `json:"tags,omitempty" jsonschema:"when set, the app only receives indexers sharing one of these"`
	BaseURL        string   `json:"base_url,omitempty" jsonschema:"where Prowlarr reaches the app"`

	Receives []string `json:"receives" jsonschema:"the enabled indexers that sync to this app under Prowlarr's rules"`

	Tested     bool     `json:"tested,omitempty"`
	TestPassed bool     `json:"test_passed,omitempty"`
	TestErrors []string `json:"test_errors,omitempty"`
}

type Applications struct {
	Applications []Application `json:"applications"`
	Profiles     []AppProfile  `json:"sync_profiles" jsonschema:"what each sync profile lets the apps use an indexer for"`

	Unreached []string `json:"unreached,omitempty" jsonschema:"enabled indexers that reach no application at all"`

	Warnings []string `json:"warnings,omitempty"`
}

// GetApplications reads the applications and works out what reaches each.
// With test set, it also asks Prowlarr to test the connection to every one.
func GetApplications(ctx context.Context, test bool) (Applications, error) {
	c, err := newClient()
	if err != nil {
		return Applications{}, err
	}

	var (
		apps     []applicationJSON
		indexers []indexerJSON
		tags     []tagJSON
		profiles []AppProfile

		appsErr, indexersErr, tagsErr, profilesErr error
		wg                                         sync.WaitGroup
	)
	wg.Add(4)
	go func() { defer wg.Done(); appsErr = c.get(ctx, "/applications", nil, &apps) }()
	go func() { defer wg.Done(); indexersErr = c.get(ctx, "/indexer", nil, &indexers) }()
	go func() { defer wg.Done(); tagsErr = c.get(ctx, "/tag", nil, &tags) }()
	go func() { defer wg.Done(); profiles, profilesErr = c.appProfiles(ctx) }()
	wg.Wait()

	if appsErr != nil {
		return Applications{}, appsErr
	}

	labels := tagLabels(tags)
	out := Applications{Profiles: profiles}

	reached := map[int]bool{}
	for _, a := range apps {
		app := Application{
			ID:             a.ID,
			Name:           a.Name,
			Implementation: a.Implementation,
			SyncLevel:      a.SyncLevel,
			Tags:           labelsFor(a.Tags, labels),
			BaseURL:        a.field("baseUrl"),
			Receives:       []string{},
		}
		for _, ix := range indexers {
			if a.SyncLevel != "disabled" && ix.Enable && tagsIntersect(a.Tags, ix.Tags) {
				app.Receives = append(app.Receives, ix.Name)
				reached[ix.ID] = true
			}
		}
		slices.Sort(app.Receives)
		out.Applications = append(out.Applications, app)
	}

	if indexersErr == nil {
		for _, ix := range indexers {
			if ix.Enable && !reached[ix.ID] {
				out.Unreached = append(out.Unreached, ix.Name)
			}
		}
	}

	if test && len(out.Applications) > 0 {
		results, err := c.testAll(ctx, "/applications/testall")
		if err != nil {
			out.Warnings = append(out.Warnings, "could not test the applications: "+err.Error())
		}
		for i := range out.Applications {
			if r, ok := results[out.Applications[i].ID]; ok {
				out.Applications[i].Tested = true
				out.Applications[i].TestPassed = r.passed
				out.Applications[i].TestErrors = r.errors
			}
		}
	}

	out.Warnings = append(out.Warnings, appWarnings(out)...)
	if indexersErr != nil {
		out.Warnings = append(out.Warnings, "could not read the indexers, so what reaches each "+
			"app is unknown: "+indexersErr.Error())
	}
	if tagsErr != nil {
		out.Warnings = append(out.Warnings, "could not read the tags: "+tagsErr.Error())
	}
	if profilesErr != nil {
		out.Warnings = append(out.Warnings, "could not read the sync profiles: "+profilesErr.Error())
	}

	return out, nil
}

func appWarnings(a Applications) []string {
	var w []string

	if len(a.Applications) == 0 {
		return []string{"prowlarr syncs to no application, so nothing configured here reaches " +
			"Radarr or Sonarr — each needs adding under Settings → Apps"}
	}

	for _, app := range a.Applications {
		switch app.SyncLevel {
		case "disabled":
			w = append(w, fmt.Sprintf("sync to %s is disabled — it receives no indexer from Prowlarr", app.Name))
		case "addOnly":
			w = append(w, fmt.Sprintf("%s is on Add Only: new indexers reach it, but disabling, "+
				"re-prioritising or re-profiling one in Prowlarr never does — its copy keeps the "+
				"settings it was added with", app.Name))
		}
		if app.SyncLevel != "disabled" && len(app.Receives) == 0 {
			msg := fmt.Sprintf("%s receives no indexer at all", app.Name)
			if len(app.Tags) > 0 {
				msg += fmt.Sprintf(" — it is tagged %s and no enabled indexer shares a tag with it",
					strings.Join(app.Tags, ", "))
			}
			w = append(w, msg)
		}
		if app.Tested && !app.TestPassed {
			w = append(w, fmt.Sprintf("prowlarr cannot reach %s: %s — nothing it changes is "+
				"pushed there until it can", app.Name, strings.Join(app.TestErrors, "; ")))
		}
	}

	if len(a.Unreached) > 0 {
		w = append(w, fmt.Sprintf("%d enabled %s no application: %s — their tags match no app's",
			len(a.Unreached), plural(len(a.Unreached), "indexer reaches", "indexers reach"),
			strings.Join(a.Unreached, ", ")))
	}

	return w
}

// tagsIntersect is Prowlarr's own rule: an app with no tags takes every
// indexer, and one with tags takes the indexers sharing at least one.
func tagsIntersect(appTags, indexerTags []int) bool {
	if len(appTags) == 0 {
		return true
	}
	for _, t := range appTags {
		if slices.Contains(indexerTags, t) {
			return true
		}
	}
	return false
}

// syncTargets splits the apps an indexer change would reach by how they take
// it: the ones kept identical, and the ones that will never hear about it.
func (c *client) syncTargets(ctx context.Context, indexerTags []int) (full, addOnly []string, err error) {
	var apps []applicationJSON
	if err := c.get(ctx, "/applications", nil, &apps); err != nil {
		return nil, nil, err
	}
	for _, a := range apps {
		if !tagsIntersect(a.Tags, indexerTags) {
			continue
		}
		switch a.SyncLevel {
		case "fullSync":
			full = append(full, a.Name)
		case "addOnly":
			addOnly = append(addOnly, a.Name)
		}
	}
	return full, addOnly, nil
}

// --- testing ------------------------------------------------------------------

type testResult struct {
	passed bool
	errors []string
}

// testAll runs a testall endpoint. Prowlarr answers it with 400 as soon as any
// one provider fails, and the body of that 400 is the full list of results —
// the same as a 200 would have carried — so it is read either way.
func (c *client) testAll(ctx context.Context, path string) (map[int]testResult, error) {
	var raw []testAllJSON
	err := c.do(ctx, http.MethodPost, path, nil, nil, &raw, indexerTimeout)

	var se *statusError
	if errors.As(err, &se) && se.status == http.StatusBadRequest {
		if jsonErr := json.Unmarshal(se.body, &raw); jsonErr == nil {
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}

	out := make(map[int]testResult, len(raw))
	for _, r := range raw {
		res := testResult{passed: r.IsValid}
		for _, f := range r.ValidationFailures {
			if f.IsWarning {
				continue
			}
			if m := f.String(); m != "" {
				res.errors = append(res.errors, m)
			}
		}
		if len(res.errors) > 0 {
			res.passed = false
		}
		out[r.ID] = res
	}
	return out, nil
}

// --- wire types -----------------------------------------------------------

type fieldJSON struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Value   any    `json:"value"`
	Type    string `json:"type"`
	Privacy string `json:"privacy"`
	Hidden  string `json:"hidden"`

	SelectOptions []struct {
		Value any    `json:"value"`
		Name  string `json:"name"`
	} `json:"selectOptions"`
}

type applicationJSON struct {
	ID             int         `json:"id"`
	Name           string      `json:"name"`
	Implementation string      `json:"implementation"`
	SyncLevel      string      `json:"syncLevel"`
	Tags           []int       `json:"tags"`
	Fields         []fieldJSON `json:"fields"`
}

// field reads one non-secret setting. Anything Prowlarr marks as a password,
// API key or user name is never returned, whatever it is asked for.
func (a applicationJSON) field(name string) string {
	for _, f := range a.Fields {
		if f.Name != name || isSecret(f) {
			continue
		}
		if s, ok := f.Value.(string); ok {
			return s
		}
	}
	return ""
}

func isSecret(f fieldJSON) bool {
	switch strings.ToLower(f.Privacy) {
	case "", "normal":
	default:
		return true
	}
	switch strings.ToLower(f.Type) {
	case "password":
		return true
	}
	name := strings.ToLower(f.Name)
	for _, s := range []string{"apikey", "password", "cookie", "passkey", "token", "secret", "rsskey"} {
		if strings.Contains(name, s) {
			return true
		}
	}
	return false
}

type testAllJSON struct {
	ID                 int                     `json:"id"`
	IsValid            bool                    `json:"isValid"`
	ValidationFailures []validationFailureJSON `json:"validationFailures"`
}
