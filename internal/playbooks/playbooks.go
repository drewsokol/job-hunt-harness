// Package playbooks holds the instructions jhh gives agents for its main
// workflows: setup, the interview that builds the profile, screening, and
// preparing for a job interview. Each is a markdown file
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
	// Phrase is the one thing a user would say to start it, and Help is the
	// dashboard's "Ask your agent" text about it, written for the user.
	// Description is the agent's.
	Phrase, Help string
	// NewSession, when set, says why the playbook is best started in a new
	// session.
	NewSession string
	// Args are the arguments the text takes, written {{name}} in it, with
	// their defaults.
	Args []Arg
}

// Arg is a playbook argument.
type Arg struct {
	Name, Description, Default string
	// Fill, when set, turns the value (or the default) into the text that
	// replaces {{name}}, so a playbook can say something different when
	// an argument is left out.
	Fill func(v string) string
	// Number says the value is a whole number from 1 to Max (no limit when
	// Max is 0).
	Number bool
	Max    int
}

// All are the playbooks, in the order a user meets them.
var All = []Playbook{
	{
		Name:        "initialize",
		Title:       "Set up Job Hunt Harness",
		Description: "Setup: the profile (through the interview playbook), filters, sources, companies, the first refresh and a calibration round. Safe to run again; it builds on what is stored.",
		Phrase:      "initialize job hunt harness",
		Help:        "Start here. Your agent interviews you about what you want next, writes your profile and filters with your OK, suggests companies to follow, finds jobs and screens a first batch with you. Safe to say again later; it builds on what jhh has.",
	},
	{
		Name:        "interview_me",
		Title:       "Interview me",
		Description: "Interview the user about their job search, and build or update their profile from it. Run it at any time, such as when the search changes; it builds on what is stored.",
		Phrase:      "interview me again about my job search",
		Help:        "Your agent asks what has changed in your search, such as the roles, the level, the pay or where you'd work, and updates your profile, memories and filters to match. Use it whenever your search shifts, rather than changing the profile a line at a time.",
		NewSession:  "The interview fills a long chat, and jhh keeps every answer, so your other sessions see the result.",
	},
	{
		Name:        "screen_new_jobs",
		Title:       "Screen new jobs",
		Description: "Rate the jobs that passed the filters against the profile, and save the ratings.",
		Phrase:      "screen new jobs",
		Help:        "Your agent rates the jobs that passed your filters against your profile. The ones that fit land in To review with its reasons and the gaps; the rest go to Archive, with why.",
		Args:        []Arg{{Name: "limit", Description: "how many jobs to screen", Default: "30", Number: true, Max: 200}},
	},
	{
		Name:        "interview_prep",
		Title:       "Prepare for an interview",
		Description: "Prepare the user for one interview (research, pitch, stories, questions, practice), and record the debrief afterwards. Best run in a session of its own.",
		Phrase:      "help me prepare for my interview with Exampleco",
		Help:        "Before an interview, your agent researches the company, plans for the stage, drafts your pitch, picks stories from your story bank, practises with you and saves the prep notes. Afterwards, tell it how it went (\"debrief my interview with Exampleco\"): it records the outcome, the next step, how each story landed and what to work on. To log an interview without prepping, just tell your agent when it is (\"I have an interview with Exampleco next Tuesday at 2pm\").",
		NewSession:  "Prep fills a long chat, and jhh keeps the notes and stories, so the debrief works from any session.",
		Args: []Arg{{Name: "interview_id", Description: "the interview to prepare for, from get_tracker or get_job; leave out to ask the user",
			Number: true, Fill: func(v string) string {
				if v == "" {
					return "No interview was named. Ask the user which one, offering the upcoming ones from `get_tracker` (view upcoming_interviews), soonest first."
				}
				return "The interview is interview_id " + v + "."
			}}},
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
		if a.Fill != nil {
			v = a.Fill(v)
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
