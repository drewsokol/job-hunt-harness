package web

import (
	"net/http"

	"github.com/drewsokol/job-hunt-harness/internal/playbooks"
)

// The "Ask your agent" page lists what the user can say to their agent.
// Playbooks bring their own text, so a new one shows up without anyone
// remembering to add it; the workflows that aren't playbooks are here.

// helpEntry is a workflow that isn't a playbook.
type helpEntry struct {
	Name, Title, Help, Phrase string
	// NewSession, when set, says why it's best started in a new session.
	NewSession string
	// ReplacedBy names a playbook that takes this entry's place on the
	// page while it exists.
	ReplacedBy string
}

var fixedHelp = []helpEntry{
	{Name: "whats_new", Title: "Check in on what's new",
		Phrase: "what's new?",
		Help:   "The daily check-in: jobs that arrived or closed, follow-ups that are due, interviews coming up and anything that needs you. A good way to start each day."},
	{Name: "refresh", Title: "Look for new jobs",
		Phrase: "refresh my jobs",
		Help:   "Your agent fetches the latest jobs from your companies and sources, and your filters sort them. Follow it with \"screen new jobs\" to have the new ones rated."},
	{Name: "log_application", Title: "Log an application",
		Phrase: "I applied to the Backend Engineer role at Exampleco",
		Help:   "Tell your agent when you apply, including to a role jhh didn't find, and it marks the job applied. You can also mark it applied here, under To review."},
	{Name: "outreach", Title: "Track outreach",
		Phrase: "a recruiter at Exampleco wrote to me; log it and remind me to follow up in a week",
		Help:   "Your agent logs messages you send and ones people send you, the people behind them and when to follow up. Due follow-ups show in the check-in and under Outreach."},
	{Name: "log_interview", Title: "Log an interview", ReplacedBy: "interview_prep",
		Phrase: "I have an interview with Exampleco next Tuesday at 2pm; log it",
		Help:   "Your agent records the interview, its stage and who you're meeting. Afterwards, tell it how it went (\"debrief my interview with Exampleco\"): it saves your notes, the outcome and the next step."},
	{Name: "open_dashboard", Title: "Open the dashboard",
		Phrase: "open my dashboard",
		Help:   "Your agent opens this dashboard and signs your browser in. Ask for it when the dashboard says you're signed out."},
}

// helpOrder is the page's order, by playbook or fixed entry name. A
// playbook that isn't named here goes at the end.
var helpOrder = []string{"initialize", "interview_me", "whats_new", "screen_new_jobs", "refresh",
	"log_application", "outreach", "log_interview", "open_dashboard"}

type helpJSON struct {
	Name          string `json:"name"`
	Title         string `json:"title"`
	Help          string `json:"help"`
	Phrase        string `json:"phrase"`
	Slash         string `json:"slash,omitempty"` // a playbook's Claude Code slash command, when jhh was added as "jhh"
	NewSession    bool   `json:"new_session"`
	NewSessionWhy string `json:"new_session_why,omitempty"`
}

// helpEntries lists the page's entries in order for these playbooks.
func helpEntries(pbs []playbooks.Playbook) []helpJSON {
	byName := map[string]playbooks.Playbook{}
	for _, p := range pbs {
		byName[p.Name] = p
	}
	out := []helpJSON{}
	listed := map[string]bool{}
	addPlaybook := func(p playbooks.Playbook) {
		if listed[p.Name] {
			return
		}
		listed[p.Name] = true
		out = append(out, helpJSON{Name: p.Name, Title: p.Title, Help: p.Help, Phrase: p.Phrase, Slash: "/mcp__jhh__" + p.Name,
			NewSession: p.NewSession != "", NewSessionWhy: p.NewSession})
	}
	for _, name := range helpOrder {
		if p, ok := byName[name]; ok {
			addPlaybook(p)
			continue
		}
		for _, e := range fixedHelp {
			if e.Name != name {
				continue
			}
			if p, ok := byName[e.ReplacedBy]; ok {
				addPlaybook(p)
			} else {
				out = append(out, helpJSON{Name: e.Name, Title: e.Title, Help: e.Help, Phrase: e.Phrase,
					NewSession: e.NewSession != "", NewSessionWhy: e.NewSession})
			}
		}
	}
	for _, p := range pbs {
		addPlaybook(p)
	}
	return out
}

func (s *server) help(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, struct {
		Entries []helpJSON `json:"entries"`
	}{helpEntries(playbooks.All)})
}
