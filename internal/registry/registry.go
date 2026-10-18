// Package registry is the starter list of companies with known job board
// feeds. jhh adds them on the first refresh so a new user has jobs to look
// at before adding their own companies.
package registry

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/drewsokol/job-hunt-harness/internal/sources"
)

//go:embed companies.json
var companiesJSON []byte

// Entry is one company in the registry.
type Entry struct {
	Name   string `json:"name"`
	Source string `json:"ats"` // greenhouse | lever | ashby
	Slug   string `json:"slug"`
}

// BoardKey is the company's unique board id, "greenhouse:exampleco".
func (e Entry) BoardKey() string { return e.Source + ":" + e.Slug }

// Starter returns the registry, checked: every entry has a name, a known
// source and a valid slug, and no board appears twice.
func Starter(known func(source string) bool) ([]Entry, error) {
	var es []Entry
	if err := json.Unmarshal(companiesJSON, &es); err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range es {
		switch {
		case e.Name == "":
			return nil, fmt.Errorf("registry: entry %+v has no name", e)
		case !known(e.Source):
			return nil, fmt.Errorf("registry: %s uses unknown source %q", e.Name, e.Source)
		case seen[e.BoardKey()]:
			return nil, fmt.Errorf("registry: %s is listed twice", e.BoardKey())
		}
		if err := sources.ValidSlug(e.Slug); err != nil {
			return nil, fmt.Errorf("registry: %s: %w", e.Name, err)
		}
		seen[e.BoardKey()] = true
	}
	return es, nil
}
