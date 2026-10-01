package prowlarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// A test is the question the failure counts cannot answer: not "has this been
// failing" but "does it work right now, and if not, why". Prowlarr logs in,
// runs a query and reports what went wrong in its own words — a Cloudflare
// challenge, an expired cookie, a site that moved domain.
//
// A test changes nothing in Prowlarr. It does reach the indexer, so it is not
// free for a private tracker that counts requests, which is why the default is
// every enabled indexer at once rather than a loop over them.

type IndexerTest struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	Passed bool     `json:"passed"`
	Errors []string `json:"errors,omitempty" jsonschema:"why it failed, in Prowlarr's words"`
}

type TestReport struct {
	Results []IndexerTest `json:"results" jsonschema:"failures first"`

	TestedCount  int `json:"tested_count"`
	FailedCount  int `json:"failed_count"`
	SkippedCount int `json:"skipped_count,omitempty" jsonschema:"disabled indexers, which a test of all of them does not reach"`

	Warnings []string `json:"warnings,omitempty"`
}

// TestIndexers tests one indexer, by id or name, or every enabled one when
// input is empty.
func TestIndexers(ctx context.Context, input string) (TestReport, error) {
	c, err := newClient()
	if err != nil {
		return TestReport{}, err
	}

	var raw []indexerJSON
	if err := c.get(ctx, "/indexer", nil, &raw); err != nil {
		return TestReport{}, err
	}

	if strings.TrimSpace(input) != "" {
		all := make([]Indexer, 0, len(raw))
		for _, r := range raw {
			all = append(all, r.toIndexer(nil, nil))
		}
		ix, err := resolveIndexer(all, input)
		if err != nil {
			return TestReport{}, err
		}
		res, err := c.testOne(ctx, ix)
		if err != nil {
			return TestReport{}, err
		}
		rep := TestReport{Results: []IndexerTest{res}, TestedCount: 1}
		if !res.Passed {
			rep.FailedCount = 1
		}
		if !ix.Enabled {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s is disabled, so even a passing "+
				"test changes nothing until it is enabled — prowlarr_indexer_update does that", ix.Name))
		}
		rep.Warnings = append(rep.Warnings, testWarnings(rep)...)
		return rep, nil
	}

	results, err := c.testAll(ctx, "/indexer/testall")
	if err != nil {
		return TestReport{}, err
	}

	var rep TestReport
	for _, r := range raw {
		res, tested := results[r.ID]
		if !tested {
			if !r.Enable {
				rep.SkippedCount++
			}
			continue
		}
		rep.Results = append(rep.Results, IndexerTest{ID: r.ID, Name: r.Name, Passed: res.passed, Errors: res.errors})
	}
	slices.SortStableFunc(rep.Results, func(a, b IndexerTest) int {
		switch {
		case a.Passed == b.Passed:
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case !a.Passed:
			return -1
		default:
			return 1
		}
	})
	rep.TestedCount = len(rep.Results)
	for _, r := range rep.Results {
		if !r.Passed {
			rep.FailedCount++
		}
	}
	rep.Warnings = testWarnings(rep)
	return rep, nil
}

// testOne sends the indexer back exactly as Prowlarr stored it, which is what
// its own Test button does.
func (c *client) testOne(ctx context.Context, ix Indexer) (IndexerTest, error) {
	var stored json.RawMessage
	if err := c.get(ctx, "/indexer/"+strconv.Itoa(ix.ID), nil, &stored); err != nil {
		return IndexerTest{}, err
	}

	res := IndexerTest{ID: ix.ID, Name: ix.Name, Passed: true}
	err := c.do(ctx, http.MethodPost, "/indexer/test", nil, stored, nil, indexerTimeout)

	var se *statusError
	switch {
	case err == nil:
		return res, nil
	case errors.As(err, &se) && se.status == http.StatusBadRequest:
		res.Passed = false
		res.Errors = validationMessages(se.body)
		if len(res.Errors) == 0 {
			res.Errors = []string{err.Error()}
		}
		return res, nil
	default:
		return IndexerTest{}, err
	}
}

func testWarnings(rep TestReport) []string {
	var w []string
	if rep.TestedCount == 0 {
		return []string{"no indexer was tested — Prowlarr only tests the enabled ones"}
	}
	for _, r := range rep.Results {
		if r.Passed {
			continue
		}
		reason := strings.Join(r.Errors, "; ")
		w = append(w, fmt.Sprintf("%s failed: %s%s", r.Name, reason, testHint(reason)))
	}
	return w
}

// testHint turns the few failures with a known fix into that fix.
func testHint(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.Contains(r, "cloudflare") || strings.Contains(r, "flaresolverr"):
		return " — the site is behind Cloudflare; it needs a FlareSolverr proxy, and the " +
			"indexer and the proxy need a tag in common"
	case strings.Contains(r, "cookie"):
		return " — the login cookie has expired; a fresh one has to be copied from a browser " +
			"session into the indexer's settings"
	case strings.Contains(r, "401") || strings.Contains(r, "unauthorized") ||
		strings.Contains(r, "login") || strings.Contains(r, "credentials"):
		return " — the credentials in the indexer's settings are being refused"
	case strings.Contains(r, "429") || strings.Contains(r, "too many"):
		return " — the site is rate-limiting; testing again right away makes it worse"
	case strings.Contains(r, "name or service not known") || strings.Contains(r, "no such host") ||
		strings.Contains(r, "resolve"):
		return " — the site's domain does not resolve; it may have moved, and a newer base URL " +
			"may be listed in the indexer's settings"
	}
	return ""
}
