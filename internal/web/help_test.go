package web

import (
	"net/http"
	"slices"
	"testing"

	"github.com/drewsokol/job-hunt-harness/internal/playbooks"
)

func (f *fixture) help() []helpJSON {
	f.t.Helper()
	var page struct {
		Entries []helpJSON `json:"entries"`
	}
	f.ok("GET", "/api/help", nil, &page)
	return page.Entries
}

func names(es []helpJSON) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Name)
	}
	return out
}

// TestHelpPage checks the "Ask your agent" page lists every playbook with
// its phrase, help text and slash command, in the stated order, after the
// same sign-in as the rest of the API.
func TestHelpPage(t *testing.T) {
	f := newFixture(t)
	req, _ := http.NewRequest("GET", f.base+"/api/help", nil)
	resp, err := f.browser().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("signed out: status %d, want 401", resp.StatusCode)
	}

	es := f.help()
	want := []string{"initialize", "interview_me", "whats_new", "screen_new_jobs", "refresh",
		"log_application", "outreach", "interview_prep", "open_dashboard"}
	if got := names(es); !slices.Equal(got, want) {
		t.Errorf("order %v; want %v", got, want)
	}
	byName := map[string]helpJSON{}
	for _, e := range es {
		byName[e.Name] = e
		if e.Title == "" || e.Help == "" || e.Phrase == "" {
			t.Errorf("%s has no title, help text or phrase: %+v", e.Name, e)
		}
		if e.NewSession != (e.NewSessionWhy != "") {
			t.Errorf("%s: best in a new session %v, with reason %q", e.Name, e.NewSession, e.NewSessionWhy)
		}
	}
	for _, p := range playbooks.All {
		e, ok := byName[p.Name]
		if !ok {
			t.Errorf("playbook %s isn't listed", p.Name)
			continue
		}
		if e.Phrase != p.Phrase || e.Help != p.Help || e.Slash != "/mcp__jhh__"+p.Name {
			t.Errorf("%s: %+v", p.Name, e)
		}
	}
	for _, name := range []string{"interview_me", "interview_prep"} {
		if !byName[name].NewSession {
			t.Errorf("%s isn't marked best in a new session", name)
		}
	}
	if e := byName["whats_new"]; e.Slash != "" || e.NewSession {
		t.Errorf("a fixed entry has a slash command or a new session: %+v", e)
	}
}

// TestHelpPageFollowsPlaybooks checks a new playbook shows up without
// other changes, and "Log an interview" gives way to interview_prep.
func TestHelpPageFollowsPlaybooks(t *testing.T) {
	f := newFixture(t)
	saved := playbooks.All
	t.Cleanup(func() { playbooks.All = saved })

	var without []playbooks.Playbook
	for _, p := range saved {
		if p.Name != "interview_prep" {
			without = append(without, p)
		}
	}
	playbooks.All = append(without, playbooks.Playbook{Name: "made_up", Title: "A made-up playbook",
		Phrase: "do the made-up thing", Help: "Made up for a test."})
	got := names(f.help())
	want := []string{"initialize", "interview_me", "whats_new", "screen_new_jobs", "refresh",
		"log_application", "outreach", "log_interview", "open_dashboard", "made_up"}
	if !slices.Equal(got, want) {
		t.Errorf("without interview_prep and with a new playbook: %v; want %v", got, want)
	}

	playbooks.All = append(playbooks.All, playbooks.Playbook{Name: "interview_prep", Title: "Prepare", Phrase: "prepare me", Help: "Made up."})
	got = names(f.help())
	want = []string{"initialize", "interview_me", "whats_new", "screen_new_jobs", "refresh",
		"log_application", "outreach", "interview_prep", "open_dashboard", "made_up"}
	if !slices.Equal(got, want) {
		t.Errorf("with interview_prep added: %v; want %v", got, want)
	}
}
