// Package builtin lists the sources that ship with jhh. Adding a board
// source means writing its package, passing the conformance suite, and
// adding it here; search sources go in Searchers.
package builtin

import (
	"github.com/drewsokol/job-hunt-harness/internal/keys"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/ashby"
	"github.com/drewsokol/job-hunt-harness/internal/sources/greenhouse"
	"github.com/drewsokol/job-hunt-harness/internal/sources/hackernews"
	"github.com/drewsokol/job-hunt-harness/internal/sources/himalayas"
	"github.com/drewsokol/job-hunt-harness/internal/sources/lever"
	"github.com/drewsokol/job-hunt-harness/internal/sources/themuse"
)

// All returns every built-in source by name, sharing client.
func All(client *sources.Client) map[string]sources.Source {
	m := map[string]sources.Source{}
	for _, s := range []sources.Source{greenhouse.New(client), lever.New(client), ashby.New(client)} {
		m[s.Name()] = s
	}
	return m
}

// Searchers returns every built-in search source by name, sharing client.
// Sources that take an optional key read it from k, which may be nil.
func Searchers(client *sources.Client, k *keys.File) map[string]sources.Searcher {
	m := map[string]sources.Searcher{}
	for _, s := range []sources.Searcher{themuse.New(client), hackernews.New(client), himalayas.New(client, k)} {
		m[s.Name()] = s
	}
	return m
}
