// Package playbooks holds the instructions jhh gives agents for its main
// workflows: the setup interview and screening. Each is a markdown file
// embedded in the binary, served as an MCP prompt and through the
// get_playbook tool, for clients that don't support prompts.
package playbooks

import (
	"embed"
	"strings"
)

//go:embed *.md
var files embed.FS

// Playbook is one workflow's instructions.
type Playbook struct {
	Name        string
	Title       string
	Description string
	// Args are the arguments the text takes, written {{name}} in it, with
	// their defaults.
	Args []Arg
}

// Arg is a playbook argument.
type Arg struct {
	Name, Description, Default string
}

// All are the playbooks, in the order a user meets them.
var All = []Playbook{
	{
		Name:        "initialize",
		Title:       "Set up Job Hunt Harness",
		Description: "The setup interview: resume, profile, memories, filters, companies, the first refresh and a calibration round. Safe to run again; it builds on what is stored.",
	},
	{
		Name:        "screen_new_jobs",
		Title:       "Screen new jobs",
		Description: "Rate the jobs that passed the filters against the profile, and save the ratings.",
		Args:        []Arg{{Name: "limit", Description: "how many jobs to screen", Default: "30"}},
	},
}

// Get returns a playbook by name.
func Get(name string) (Playbook, bool) {
	for _, p := range All {
		if p.Name == name {
			return p, true
		}
	}
	return Playbook{}, false
}

// Text returns the playbook's text with its arguments filled in; missing
// arguments take their defaults.
func (p Playbook) Text(args map[string]string) string {
	b, err := files.ReadFile(p.Name + ".md")
	if err != nil {
		panic("playbooks: no file for " + p.Name) // the test checks every playbook has one
	}
	text := string(b)
	for _, a := range p.Args {
		v := strings.TrimSpace(args[a.Name])
		if v == "" {
			v = a.Default
		}
		text = strings.ReplaceAll(text, "{{"+a.Name+"}}", v)
	}
	return text
}

// Names lists the playbooks' names.
func Names() []string {
	out := make([]string, len(All))
	for i, p := range All {
		out[i] = p.Name
	}
	return out
}
