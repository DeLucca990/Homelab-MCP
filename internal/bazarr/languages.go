package bazarr

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Bazarr identifies a subtitle language by a two-letter code, and that code is
// not always ISO 639-1. Where the standard has no code for a variant people
// care about, Bazarr made one up: Brazilian Portuguese is "pb", Traditional
// Chinese "zt", Latin American Spanish "ea". Sending "pt" for a Brazilian
// subtitle is not an error — it is a request for the European one, which
// Bazarr will happily go and find.
//
// So a language is never passed through as typed. It is resolved against the
// list this Bazarr actually has, by code, three-letter code or name, and the
// few spellings people use for the invented codes are mapped onto them.

// Language is one subtitle language as Bazarr knows it.
type Language struct {
	Code2   string `json:"code2" jsonschema:"Bazarr's two-letter code — the one every other call takes. Not always ISO 639-1: Brazilian Portuguese is 'pb'"`
	Code3   string `json:"code3,omitempty"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled" jsonschema:"whether the language is switched on in Bazarr's settings; only enabled languages can be put in a language profile"`
}

// The spellings people actually use for the languages Bazarr gave its own
// codes. Keys are compared after lower-casing and turning '_' into '-'.
var languageAliases = map[string]string{
	"pt-br":                "pb",
	"ptbr":                 "pb",
	"pob":                  "pb",
	"brazilian":            "pb",
	"brazilian portuguese": "pb",
	"portuguese brazil":    "pb",
	"português brasileiro": "pb",
	"portugues brasileiro": "pb",
	"pt-pt":                "pt",
	"zh-tw":                "zt",
	"zh-hant":              "zt",
	"traditional chinese":  "zt",
	"es-419":               "ea",
	"es-la":                "ea",
	"latin spanish":        "ea",
	"spanish latino":       "ea",
}

// GetLanguages lists every language this Bazarr knows, enabled or not.
func GetLanguages(ctx context.Context) ([]Language, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	return c.languages(ctx)
}

func (c *client) languages(ctx context.Context) ([]Language, error) {
	var out []Language
	if err := c.get(ctx, "/system/languages", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveLanguage turns what someone typed into the language Bazarr means by
// it. It refuses rather than guesses: a subtitle downloaded in the wrong
// language is a file on disk nobody wanted.
func ResolveLanguage(ctx context.Context, input string) (Language, error) {
	c, err := newClient()
	if err != nil {
		return Language{}, err
	}
	known, err := c.languages(ctx)
	if err != nil {
		return Language{}, err
	}
	return resolveLanguage(known, input)
}

func resolveLanguage(known []Language, input string) (Language, error) {
	want := strings.ToLower(strings.TrimSpace(input))
	want = strings.ReplaceAll(want, "_", "-")
	want = strings.Trim(want, "()")
	if want == "" {
		return Language{}, fmt.Errorf("a language is required — a two-letter code such as " +
			"'en', or 'pb' for Brazilian Portuguese")
	}
	if alias, ok := languageAliases[want]; ok {
		want = alias
	}

	for _, l := range known {
		if strings.EqualFold(l.Code2, want) || strings.EqualFold(l.Code3, want) ||
			strings.EqualFold(l.Name, want) {
			return l, nil
		}
	}

	// A name typed a little differently — "portuguese (brazil)" against
	// "Portuguese (Brazil)" is caught above; "brazil" is caught here, but only
	// when it names exactly one language.
	var partial []Language
	for _, l := range known {
		if len(want) >= 4 && strings.Contains(strings.ToLower(l.Name), want) {
			partial = append(partial, l)
		}
	}
	if len(partial) == 1 {
		return partial[0], nil
	}

	var enabled []string
	for _, l := range known {
		if l.Enabled {
			enabled = append(enabled, fmt.Sprintf("%s (%s)", l.Code2, l.Name))
		}
	}
	slices.Sort(enabled)

	msg := fmt.Sprintf("bazarr has no language matching %q", input)
	if len(partial) > 1 {
		names := make([]string, 0, len(partial))
		for _, l := range partial {
			names = append(names, fmt.Sprintf("%s (%s)", l.Code2, l.Name))
		}
		msg = fmt.Sprintf("%q matches more than one language: %s", input, strings.Join(names, ", "))
	}
	if len(enabled) > 0 {
		msg += " — the languages enabled on this Bazarr are " + strings.Join(enabled, ", ")
	}
	return Language{}, fmt.Errorf("%s", msg)
}

// languageName renders a code for a human, falling back to the code itself.
func languageName(known []Language, code2 string) string {
	for _, l := range known {
		if l.Code2 == code2 {
			return l.Name
		}
	}
	return code2
}
