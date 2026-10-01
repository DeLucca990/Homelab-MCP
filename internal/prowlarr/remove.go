package prowlarr

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Removing an indexer from Prowlarr removes it from every application Prowlarr
// syncs to — Add Only included, unlike every other change — because Prowlarr
// treats a deleted indexer as one it must clean up after. That reach is what
// the confirmation states.

type RemovePlan struct {
	Indexer Indexer `json:"indexer"`

	RemovedFrom []string `json:"removed_from,omitempty" jsonschema:"apps that will lose this indexer too"`

	Warnings []string `json:"warnings,omitempty"`
}

type RemoveResult struct {
	Removed     Indexer  `json:"removed"`
	RemovedFrom []string `json:"removed_from,omitempty"`
	Remaining   int      `json:"remaining_enabled" jsonschema:"enabled indexers left in Prowlarr"`
	Warnings    []string `json:"warnings,omitempty"`
}

// PlanRemove resolves an indexer removal without making it.
func PlanRemove(ctx context.Context, input string) (RemovePlan, error) {
	ix, err := ResolveIndexer(ctx, input)
	if err != nil {
		return RemovePlan{}, err
	}
	c, err := newClient()
	if err != nil {
		return RemovePlan{}, err
	}

	p := RemovePlan{Indexer: ix}

	var apps []applicationJSON
	if err := c.get(ctx, "/applications", nil, &apps); err != nil {
		p.Warnings = append(p.Warnings, "could not read the applications, so which of them lose "+
			"this indexer too is unknown: "+err.Error())
	} else {
		for _, a := range apps {
			if a.SyncLevel != "disabled" && tagsIntersect(a.Tags, ix.tagIDs) {
				p.RemovedFrom = append(p.RemovedFrom, a.Name)
			}
		}
	}

	var raw []indexerJSON
	if err := c.get(ctx, "/indexer", nil, &raw); err == nil {
		others := 0
		for _, r := range raw {
			if r.ID != ix.ID && r.Enable {
				others++
			}
		}
		if others == 0 {
			p.Warnings = append(p.Warnings, "this is the last enabled indexer — without it Radarr "+
				"and Sonarr have nothing to search")
		}
	}
	if ix.Privacy == "private" {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s is a private tracker; its credentials "+
			"are deleted with it and have to be entered again to add it back", ix.Name))
	}
	if ix.Enabled && !ix.Failing {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s is working — disabling it with "+
			"prowlarr_indexer_update keeps it and its settings for later", ix.Name))
	}

	return p, nil
}

// Remove deletes a planned indexer.
func Remove(ctx context.Context, p RemovePlan) (RemoveResult, error) {
	c, err := newClient()
	if err != nil {
		return RemoveResult{}, err
	}
	if err := c.do(ctx, http.MethodDelete, "/indexer/"+strconv.Itoa(p.Indexer.ID), nil, nil, nil,
		requestTimeout); err != nil {
		return RemoveResult{}, err
	}

	res := RemoveResult{Removed: p.Indexer, RemovedFrom: p.RemovedFrom}

	var raw []indexerJSON
	if err := c.get(ctx, "/indexer", nil, &raw); err == nil {
		for _, r := range raw {
			if r.ID == p.Indexer.ID {
				return res, fmt.Errorf("prowlarr accepted the removal but %s is still listed", p.Indexer.Name)
			}
			if r.Enable {
				res.Remaining++
			}
		}
	}
	if len(p.RemovedFrom) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("prowlarr removes it from %s in the "+
			"background", strings.Join(p.RemovedFrom, " and ")))
	}
	return res, nil
}
