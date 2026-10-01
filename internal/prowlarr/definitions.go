package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Adding an indexer starts from one of the site definitions Prowlarr ships —
// several hundred of them, each a template with the settings that site needs.
// The whole catalogue is one request of several megabytes, so it is read once
// and kept for an hour: definitions change when Prowlarr updates them, not
// between two calls of a conversation.
//
// A definition's settings are where the credentials go, so the confirmation
// shows which settings were filled in and masks every one Prowlarr marks as
// secret. Nothing here ever reads a secret back out of Prowlarr: the values
// that are masked are the ones the caller just supplied.

const (
	definitionsTTL     = time.Hour
	definitionsTimeout = time.Minute

	defaultDefinitionLimit = 15
	maxDefinitionLimit     = 50
)

// DefinitionField is one setting a definition takes.
type DefinitionField struct {
	Name    string   `json:"name" jsonschema:"what prowlarr_indexer_add's 'settings' takes as the key"`
	Label   string   `json:"label,omitempty"`
	Type    string   `json:"type" jsonschema:"textbox, password, checkbox, select, number…"`
	Secret  bool     `json:"secret,omitempty" jsonschema:"a credential; never echoed back"`
	Options []string `json:"options,omitempty" jsonschema:"for a select: the values it accepts, by name"`
	Default string   `json:"default,omitempty"`

	// what Prowlarr stores for each option, parallel to Options
	optionValues []any
}

// Definition is one site Prowlarr knows how to talk to.
type Definition struct {
	Name        string `json:"name"`
	Definition  string `json:"definition" jsonschema:"what prowlarr_indexer_add takes"`
	Protocol    string `json:"protocol"`
	Privacy     string `json:"privacy" jsonschema:"public needs no account; semiPrivate and private need one"`
	Language    string `json:"language,omitempty"`
	Description string `json:"description,omitempty"`

	Settings []DefinitionField `json:"settings,omitempty" jsonschema:"the settings to fill in; credentials among them for a private site"`

	NeedsFlareSolverr bool `json:"needs_flaresolverr,omitempty" jsonschema:"the site sits behind Cloudflare and is unreachable without a FlareSolverr proxy"`
	AlreadyAdded      bool `json:"already_added,omitempty"`
}

type Definitions struct {
	Definitions  []Definition `json:"definitions"`
	MatchedCount int          `json:"matched_count"`
	ShownCount   int          `json:"shown_count"`
	TotalCount   int          `json:"total_count" jsonschema:"definitions Prowlarr ships"`
	Warnings     []string     `json:"warnings,omitempty"`
}

var (
	schemaMu   sync.Mutex
	schemaAt   time.Time
	schemaRaw  []json.RawMessage
	schemaMeta []definitionJSON
)

// schema reads the catalogue, from the cache when it is fresh.
func (c *client) schema(ctx context.Context) ([]json.RawMessage, []definitionJSON, error) {
	schemaMu.Lock()
	defer schemaMu.Unlock()

	if schemaRaw != nil && time.Since(schemaAt) < definitionsTTL {
		return schemaRaw, schemaMeta, nil
	}

	var raw []json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/indexer/schema", nil, nil, &raw, definitionsTimeout); err != nil {
		return nil, nil, err
	}
	meta := make([]definitionJSON, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &meta[i]); err != nil {
			return nil, nil, fmt.Errorf("prowlarr's indexer catalogue could not be read: %w", err)
		}
	}
	schemaRaw, schemaMeta, schemaAt = raw, meta, time.Now()
	return raw, meta, nil
}

// GetDefinitions searches the catalogue by name.
func GetDefinitions(ctx context.Context, term, protocol, privacy string, limit int) (Definitions, error) {
	c, err := newClient()
	if err != nil {
		return Definitions{}, err
	}

	switch {
	case limit <= 0:
		limit = defaultDefinitionLimit
	case limit > maxDefinitionLimit:
		limit = maxDefinitionLimit
	}

	_, meta, err := c.schema(ctx)
	if err != nil {
		return Definitions{}, err
	}
	added, _ := c.addedDefinitions(ctx)

	want := strings.ToLower(strings.TrimSpace(term))
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	privacy = strings.ToLower(strings.TrimSpace(privacy))

	out := Definitions{TotalCount: len(meta)}
	var matched []Definition
	for _, m := range meta {
		d := m.toDefinition()
		d.AlreadyAdded = added[strings.ToLower(d.Definition)]

		if want != "" && !strings.Contains(strings.ToLower(d.Name), want) &&
			!strings.Contains(strings.ToLower(d.Definition), want) &&
			!strings.Contains(strings.ToLower(d.Description), want) {
			continue
		}
		if protocol != "" && strings.ToLower(d.Protocol) != protocol {
			continue
		}
		if privacy != "" && strings.ToLower(d.Privacy) != privacy {
			continue
		}
		matched = append(matched, d)
	}

	// An exact name first: "1337x" should not be buried under every
	// description that mentions it.
	slices.SortStableFunc(matched, func(a, b Definition) int {
		ae, be := strings.EqualFold(a.Name, term), strings.EqualFold(b.Name, term)
		switch {
		case ae && !be:
			return -1
		case be && !ae:
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	out.MatchedCount = len(matched)
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out.Definitions = matched
	out.ShownCount = len(matched)

	switch {
	case out.MatchedCount == 0:
		out.Warnings = append(out.Warnings, fmt.Sprintf("no definition matches %q — Prowlarr "+
			"ships %d; try part of the site's name", term, out.TotalCount))
	case out.MatchedCount > out.ShownCount:
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d definitions matched and %d are "+
			"shown — narrow 'term', or filter by 'protocol' or 'privacy'", out.MatchedCount, out.ShownCount))
	}
	return out, nil
}

// addedDefinitions is the set of definitions already in use, lower-cased.
func (c *client) addedDefinitions(ctx context.Context) (map[string]bool, error) {
	var raw []indexerJSON
	if err := c.get(ctx, "/indexer", nil, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(raw))
	for _, r := range raw {
		out[strings.ToLower(r.DefinitionName)] = true
	}
	return out, nil
}

// --- adding ----------------------------------------------------------------------

type AddRequest struct {
	Definition string
	Name       string
	AppProfile string
	Priority   int
	Tags       []string
	Settings   map[string]string
	Disabled   bool
}

// SettingValue is one setting as the confirmation shows it.
type SettingValue struct {
	Name  string `json:"name"`
	Value string `json:"value" jsonschema:"masked for a credential"`
}

type AddPlan struct {
	Definition Definition `json:"definition"`

	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	AppProfile string         `json:"sync_profile"`
	Priority   int            `json:"priority"`
	Tags       []string       `json:"tags,omitempty"`
	Settings   []SettingValue `json:"settings,omitempty"`

	ReachesApps []string `json:"reaches_apps,omitempty" jsonschema:"applications that will receive it"`

	Warnings []string `json:"warnings,omitempty"`

	body map[string]any
	key  string
}

// Key is what the fingerprint covers: every value that will be sent,
// credentials included, hashed rather than shown.
func (p AddPlan) Key() string { return p.key }

type AddResult struct {
	Indexer  Indexer  `json:"indexer"`
	Warnings []string `json:"warnings,omitempty"`
}

// PlanAdd builds an indexer from a definition without creating it.
func PlanAdd(ctx context.Context, req AddRequest) (AddPlan, error) {
	if strings.TrimSpace(req.Definition) == "" {
		return AddPlan{}, fmt.Errorf("a 'definition' is required — prowlarr_indexer_definitions " +
			"finds one")
	}
	if req.Priority != 0 && (req.Priority < 1 || req.Priority > 50) {
		return AddPlan{}, fmt.Errorf("'priority' runs from 1 to 50, lower preferred; got %d", req.Priority)
	}

	c, err := newClient()
	if err != nil {
		return AddPlan{}, err
	}
	raw, meta, err := c.schema(ctx)
	if err != nil {
		return AddPlan{}, err
	}

	idx := -1
	for i, m := range meta {
		if strings.EqualFold(m.DefinitionName, req.Definition) || strings.EqualFold(m.Name, req.Definition) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return AddPlan{}, fmt.Errorf("prowlarr has no definition %q — prowlarr_indexer_definitions "+
			"with a 'term' finds the exact name", req.Definition)
	}
	def := meta[idx].toDefinition()

	added, _ := c.addedDefinitions(ctx)
	if added[strings.ToLower(def.Definition)] {
		return AddPlan{}, fmt.Errorf("%s is already added — prowlarr_indexer_status shows it, and "+
			"prowlarr_indexer_update changes it", def.Name)
	}

	var body map[string]any
	if err := json.Unmarshal(raw[idx], &body); err != nil {
		return AddPlan{}, err
	}
	delete(body, "presets")
	delete(body, "id")

	p := AddPlan{Definition: def, Name: nonEmpty(req.Name, def.Name), Enabled: !req.Disabled}
	body["name"] = p.Name
	body["enable"] = p.Enabled

	// The sync profile: named, or the only one there is.
	profiles, err := c.appProfiles(ctx)
	if err != nil {
		return AddPlan{}, err
	}
	var prof AppProfile
	switch {
	case strings.TrimSpace(req.AppProfile) != "":
		if prof, err = resolveAppProfile(profiles, req.AppProfile); err != nil {
			return AddPlan{}, err
		}
	case len(profiles) == 1:
		prof = profiles[0]
	default:
		return AddPlan{}, fmt.Errorf("this Prowlarr has %d sync profiles, so 'sync_profile' has to "+
			"name one: %s", len(profiles), profileList(profiles))
	}
	body["appProfileId"] = prof.ID
	p.AppProfile = fmt.Sprintf("%s (%s)", prof.Name, prof.Describe())

	p.Priority = 25
	if req.Priority != 0 {
		p.Priority = req.Priority
	}
	body["priority"] = p.Priority

	tagIDs, labels, err := c.resolveTags(ctx, req.Tags)
	if err != nil {
		return AddPlan{}, err
	}
	body["tags"] = tagIDs
	p.Tags = labels

	fields, _ := body["fields"].([]any)
	keyParts := []string{p.Name, strconv.FormatBool(p.Enabled), strconv.Itoa(prof.ID),
		strconv.Itoa(p.Priority), fmt.Sprint(tagIDs)}

	names := make([]string, 0, len(req.Settings))
	for k := range req.Settings {
		names = append(names, k)
	}
	slices.Sort(names)
	for _, name := range names {
		value := req.Settings[name]
		f, spec, err := findField(fields, def, name)
		if err != nil {
			return AddPlan{}, err
		}
		v, err := settingValue(spec, value)
		if err != nil {
			return AddPlan{}, err
		}
		f["value"] = v
		shown := value
		if spec.Secret {
			shown = "••••••"
		}
		p.Settings = append(p.Settings, SettingValue{Name: spec.Name, Value: shown})
		keyParts = append(keyParts, spec.Name+"="+value)
	}
	p.key = strings.Join(keyParts, "\x00")
	p.body = body

	// Credentials a private site will almost certainly want and did not get.
	if def.Privacy != "public" {
		var missing []string
		for _, s := range def.Settings {
			if s.Secret && s.Default == "" && req.Settings[s.Name] == "" {
				missing = append(missing, s.Name)
			}
		}
		if len(missing) > 0 {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s is %s and none of %s was given — "+
				"Prowlarr tests the login before saving, so this will most likely be refused",
				def.Name, def.Privacy, strings.Join(missing, ", ")))
		}
	}
	if def.NeedsFlareSolverr {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s sits behind Cloudflare: it needs a "+
			"FlareSolverr proxy in Prowlarr sharing a tag with it, or every query fails", def.Name))
	}

	if full, addOnly, err := c.syncTargets(ctx, tagIDs); err == nil {
		p.ReachesApps = append(full, addOnly...)
		if len(p.ReachesApps) == 0 {
			p.Warnings = append(p.Warnings, "no application will receive this indexer — "+
				"its tags match none of them, so Radarr and Sonarr will never use it")
		}
	}

	return p, nil
}

// Add creates a planned indexer. Prowlarr tests an enabled indexer before
// saving it, so a refusal here is the site's own answer.
func Add(ctx context.Context, p AddPlan) (AddResult, error) {
	c, err := newClient()
	if err != nil {
		return AddResult{}, err
	}

	var created struct {
		ID int `json:"id"`
	}
	if err := c.do(ctx, http.MethodPost, "/indexer", nil, p.body, &created, indexerTimeout); err != nil {
		return AddResult{}, fmt.Errorf("%w%s", err, testHint(err.Error()))
	}

	res := AddResult{Indexer: Indexer{ID: created.ID, Name: p.Name}}
	if ix, err := ResolveIndexer(ctx, strconv.Itoa(created.ID)); err == nil {
		res.Indexer = ix
	}
	if len(p.ReachesApps) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("prowlarr pushes it to %s in the background",
			strings.Join(p.ReachesApps, " and ")))
	}
	return res, nil
}

func (c *client) resolveTags(ctx context.Context, want []string) ([]int, []string, error) {
	ids := []int{}
	var labels []string
	if len(want) == 0 {
		return ids, nil, nil
	}
	var tags []tagJSON
	if err := c.get(ctx, "/tag", nil, &tags); err != nil {
		return nil, nil, err
	}
	for _, w := range want {
		found := false
		for _, t := range tags {
			if strings.EqualFold(t.Label, strings.TrimSpace(w)) {
				ids = append(ids, t.ID)
				labels = append(labels, t.Label)
				found = true
				break
			}
		}
		if !found {
			existing := make([]string, 0, len(tags))
			for _, t := range tags {
				existing = append(existing, t.Label)
			}
			return nil, nil, fmt.Errorf("prowlarr has no tag %q — it has %s. Tags are created in "+
				"Prowlarr's settings, not here", w, nonEmpty(strings.Join(existing, ", "), "none"))
		}
	}
	return ids, labels, nil
}

// findField finds a setting by name or label, returning the object to edit
// and its description.
func findField(fields []any, def Definition, name string) (map[string]any, DefinitionField, error) {
	for _, spec := range def.Settings {
		if !strings.EqualFold(spec.Name, name) && !strings.EqualFold(spec.Label, name) {
			continue
		}
		for _, f := range fields {
			m, ok := f.(map[string]any)
			if ok && m["name"] == spec.Name {
				return m, spec, nil
			}
		}
	}
	settable := make([]string, 0, len(def.Settings))
	for _, s := range def.Settings {
		settable = append(settable, s.Name)
	}
	return nil, DefinitionField{}, fmt.Errorf("%s has no setting %q — it takes %s", def.Name, name,
		strings.Join(settable, ", "))
}

// settingValue converts what was typed into the type the setting holds. A
// select is given by the option's name, which is how a person names it, and
// sent as the option's value, which is what Prowlarr stores.
func settingValue(spec DefinitionField, value string) (any, error) {
	switch spec.Type {
	case "checkbox":
		b, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s is a checkbox and takes true or false, not %q", spec.Name, value)
		}
		return b, nil
	case "number":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("%s takes a number, not %q", spec.Name, value)
		}
		return n, nil
	case "select":
		for i, opt := range spec.Options {
			if strings.EqualFold(opt, strings.TrimSpace(value)) {
				return spec.optionValues[i], nil
			}
		}
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			for _, v := range spec.optionValues {
				if fmt.Sprint(v) == strconv.Itoa(n) {
					return v, nil
				}
			}
		}
		return nil, fmt.Errorf("%s is one of %s, not %q", spec.Name, strings.Join(spec.Options, ", "), value)
	}
	return value, nil
}

func profileList(ps []AppProfile) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, fmt.Sprintf("%q (%s)", p.Name, p.Describe()))
	}
	return strings.Join(names, ", ")
}

// --- wire types -----------------------------------------------------------

type definitionJSON struct {
	Name           string      `json:"name"`
	DefinitionName string      `json:"definitionName"`
	Implementation string      `json:"implementation"`
	Protocol       string      `json:"protocol"`
	Privacy        string      `json:"privacy"`
	Language       string      `json:"language"`
	Description    string      `json:"description"`
	Fields         []fieldJSON `json:"fields"`
}

func (d definitionJSON) toDefinition() Definition {
	out := Definition{
		Name:        d.Name,
		Definition:  nonEmpty(d.DefinitionName, d.Implementation),
		Protocol:    d.Protocol,
		Privacy:     d.Privacy,
		Language:    d.Language,
		Description: d.Description,
	}
	for _, f := range d.Fields {
		if strings.Contains(strings.ToLower(f.Name), "flaresolverr") {
			out.NeedsFlareSolverr = true
		}
		// Info fields are instructions for a human in a web form; hidden ones
		// are not the caller's to set.
		if f.Type == "info" || strings.EqualFold(f.Hidden, "hidden") || strings.EqualFold(f.Hidden, "hiddenIfNotSet") {
			continue
		}
		spec := DefinitionField{Name: f.Name, Label: f.Label, Type: f.Type, Secret: isSecret(f)}
		for _, o := range f.SelectOptions {
			spec.Options = append(spec.Options, o.Name)
			spec.optionValues = append(spec.optionValues, o.Value)
		}
		if !spec.Secret && f.Value != nil {
			switch v := f.Value.(type) {
			case string:
				spec.Default = v
			case bool, float64:
				spec.Default = fmt.Sprint(v)
			}
			if f.Type == "select" {
				for i, ov := range spec.optionValues {
					if fmt.Sprint(ov) == fmt.Sprint(f.Value) {
						spec.Default = spec.Options[i]
					}
				}
			}
		}
		out.Settings = append(out.Settings, spec)
	}
	return out
}
