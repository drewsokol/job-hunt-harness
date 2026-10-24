package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/core"
	"github.com/drewsokol/job-hunt-harness/internal/keys"
	"github.com/drewsokol/job-hunt-harness/internal/normalize"
	"github.com/drewsokol/job-hunt-harness/internal/prefilter"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// The JSON API. Every change is made as the user, through the same core
// functions the MCP tools call. Listing text is sent as data; the page
// shows it as text only.

// maxList caps one list on the page.
const maxList = 500

// by is who every change through the dashboard is recorded as.
const by = store.ActorUser

func (s *server) routes(mux *http.ServeMux) {
	api := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !s.signedIn(r) {
				writeJSON(w, http.StatusUnauthorized, errorJSON{Error: "signed out", SignedOut: true})
				return
			}
			h(w, r)
		})
	}
	api("GET /api/events", s.events)
	api("GET /api/state", s.state)
	api("GET /api/jobs", s.listJobs)
	api("POST /api/jobs", s.addJob)
	api("GET /api/jobs/{id}", s.getJob)
	api("PATCH /api/jobs/{id}", s.updateJob)
	api("POST /api/jobs/status", s.setStatuses)
	api("POST /api/jobs/{id}/screening", s.sendToScreening)
	api("POST /api/flags/{id}", s.reviewFlag)
	api("GET /api/outreach", s.listOutreach)
	api("POST /api/outreach", s.logOutreach)
	api("PATCH /api/outreach/{id}", s.updateOutreach)
	api("GET /api/profile", s.profile)
	api("GET /api/sources", s.listSources)
	api("PATCH /api/sources/{name}", s.setSource)
	api("PUT /api/sources/{name}/key", s.saveKey)
	api("DELETE /api/sources/{name}/key", s.removeKey)
}

type errorJSON struct {
	Error     string `json:"error"`
	SignedOut bool   `json:"signed_out,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// fail answers with an error the page can show. Input and not-found errors
// say what was wrong; anything else is logged and reported generically.
func (s *server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var in *core.InputError
	switch {
	case errors.As(err, &in):
		writeJSON(w, http.StatusBadRequest, errorJSON{Error: in.Error()})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorJSON{Error: "not found; it may have been merged or removed"})
	default:
		s.Logger.Error("dashboard request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorJSON{Error: "jhh couldn't do that; its log has the details"})
	}
}

// decode reads a JSON body, refusing fields the endpoint doesn't take.
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return &core.InputError{Problems: []string{"the request isn't valid JSON for this endpoint: " + err.Error()}}
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, &core.InputError{Problems: []string{"id: a positive number"}}
	}
	return id, nil
}

// The page's lists of jobs.
var lists = map[string]store.BoardQuery{
	"review":       {Statuses: []store.JobStatus{store.StatusReview}},
	"unscreened":   {Statuses: []store.JobStatus{store.StatusUnscreened}},
	"applied":      {Statuses: []store.JobStatus{store.StatusApplied, store.StatusInterviewing, store.StatusOffer}, Recent: true},
	"archive":      {Statuses: []store.JobStatus{store.StatusSkipped, store.StatusRejected, store.StatusWithdrawn}, Recent: true},
	"screened_out": {Statuses: []store.JobStatus{store.StatusScreenedOut}, Recent: true},
	"filtered_out": {Statuses: []store.JobStatus{store.StatusFilteredOut}},
}

type stateJSON struct {
	Now      string         `json:"now"`
	Summary  summaryJSON    `json:"summary"`
	Counts   map[string]int `json:"counts"`
	Setup    setupJSON      `json:"setup"`
	Outreach int            `json:"outreach"`
	// Paused are the search sources turned off, by their names for people
	// (a job's via), so their jobs can say so.
	Paused []string `json:"paused_sources"`
}

type summaryJSON struct {
	ToReview      int    `json:"to_review"`
	Unscreened    int    `json:"unscreened"`
	Applications  int    `json:"applications"`
	AppliedLast7  int    `json:"applied_last_7"`
	Interviewing  int    `json:"interviewing"`
	Offers        int    `json:"offers"`
	FollowUpsDue  int    `json:"follow_ups_due"`
	OpenFlags     int    `json:"open_flags"`
	SuspectFeeds  int    `json:"suspect_feeds"`
	LastRefreshAt string `json:"last_refresh_at,omitempty"`
}

type setupJSON struct {
	Profile   bool `json:"profile"`
	Filters   bool `json:"filters"`
	Companies int  `json:"companies"`
}

// state is the summary strip, the tab counts and what is set up.
func (s *server) state(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.Store.Tracker(ctx, utc.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := stateJSON{
		Now: utc.Format(utc.Now()),
		Summary: summaryJSON{ToReview: t.ToReview, Unscreened: t.Unscreened, Applications: t.Applications, AppliedLast7: t.AppliedLast7,
			Interviewing: t.Interviewing, Offers: t.Offers, FollowUpsDue: t.FollowUpsDue, OpenFlags: t.OpenFlags,
			SuspectFeeds: t.SuspectFeeds, LastRefreshAt: timeText(t.LastRefreshAt)},
		Counts: map[string]int{},
	}
	for name, q := range lists {
		q.Limit = 0
		if _, out.Counts[name], err = s.Store.BoardJobs(ctx, q); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if _, out.Outreach, err = s.Store.ListOutreach(ctx, store.OutreachQuery{}); err != nil {
		s.fail(w, r, err)
		return
	}
	c, err := s.Store.SetupCounts(ctx)
	if err == nil {
		_, out.Setup.Profile, err = s.Store.Profile(ctx)
	}
	if err == nil {
		_, out.Setup.Filters, err = s.Store.ActiveFilters(ctx)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out.Setup.Companies = c.BoardCompanies
	ss, err := core.ListSources(ctx, s.Store, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out.Paused = append([]string{}, core.PausedSearches(ss)...)
	writeJSON(w, http.StatusOK, out)
}

// sourceJSON is one source on the Sources tab. LastError may quote the
// service's reply.
type sourceJSON struct {
	Name             string          `json:"name"`
	Title            string          `json:"title"`
	Kind             string          `json:"kind"`
	Status           string          `json:"status,omitempty"`
	Setup            string          `json:"setup,omitempty"`
	Settings         json.RawMessage `json:"settings,omitempty"`
	Companies        int             `json:"companies"`
	EnabledCompanies int             `json:"enabled_companies"`
	LastRun          string          `json:"last_run,omitempty"`
	LastPostings     int             `json:"last_postings"`
	LastError        string          `json:"last_error,omitempty"`
	By               string          `json:"by,omitempty"`
	At               string          `json:"at,omitempty"`
	Reason           string          `json:"reason,omitempty"`
	// Whether the source takes an optional key, and whether one is saved:
	// never the key.
	TakesKey   bool `json:"takes_key,omitempty"`
	KeySaved   bool `json:"key_saved,omitempty"`
	KeyRefused bool `json:"key_refused,omitempty"`
}

func newSourceJSON(info core.SourceInfo) sourceJSON {
	o := sourceJSON{Name: info.Name, Title: info.Title, Kind: info.Kind, Status: info.Status, Setup: info.Setup, Settings: info.Settings,
		Companies: info.Companies, EnabledCompanies: info.EnabledCompanies, LastRun: timeText(info.LastRunAt),
		LastPostings: info.LastPostings, LastError: info.LastError,
		TakesKey: info.TakesKey, KeySaved: info.KeySaved, KeyRefused: info.KeySaved && info.KeyRefused}
	if info.UpdatedBy != store.ActorSystem && info.UpdatedBy != "" {
		o.By, o.At, o.Reason = string(info.UpdatedBy), timeText(info.UpdatedAt), info.Reason
	}
	return o
}

func (s *server) boardNames() []string {
	names := make([]string, 0, len(s.Sources))
	for name := range s.Sources {
		names = append(names, name)
	}
	return names
}

func (s *server) listSources(w http.ResponseWriter, r *http.Request) {
	ss, err := core.ListSources(r.Context(), s.Store, s.boardNames())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	core.MarkKeys(ss, s.Keys)
	out := make([]sourceJSON, 0, len(ss))
	for _, info := range ss {
		out = append(out, newSourceJSON(info))
	}
	writeJSON(w, http.StatusOK, struct {
		Sources []sourceJSON `json:"sources"`
	}{out})
}

// setSource turns a search source on or off, or replaces its settings.
func (s *server) setSource(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled  *bool           `json:"enabled"`
		Settings json.RawMessage `json:"settings"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	info, err := core.SetSource(r.Context(), s.Store, by, r.PathValue("name"), core.SourceUpdate{Enabled: in.Enabled, Settings: in.Settings})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeSource(w, info)
}

func (s *server) writeSource(w http.ResponseWriter, info core.SourceInfo) {
	ss := []core.SourceInfo{info}
	core.MarkKeys(ss, s.Keys)
	writeJSON(w, http.StatusOK, newSourceJSON(ss[0]))
}

// keyVerifier is a search source that can check a key before it's saved.
type keyVerifier interface {
	Verify(ctx context.Context, key string) error
}

// saveKey checks a source's optional key with the service and saves it.
// The key is never logged, echoed or returned, only "saved".
func (s *server) saveKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Key string `json:"key"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	in.Key = strings.TrimSpace(in.Key)
	name := r.PathValue("name")
	v, ok := s.Searchers[name].(keyVerifier)
	switch {
	case !ok:
		s.fail(w, r, &core.InputError{Problems: []string{fmt.Sprintf("%s takes no key", name)}})
		return
	case in.Key == "" || len(in.Key) > keys.MaxKeyLen || strings.ContainsAny(in.Key, " \t\r\n"):
		s.fail(w, r, &core.InputError{Problems: []string{"key: paste the key as your account shows it, without spaces"}})
		return
	}
	err := v.Verify(r.Context(), in.Key)
	switch {
	case errors.Is(err, sources.ErrKeyRefused):
		s.fail(w, r, &core.InputError{Problems: []string{sources.SearchTitle(name) + " refused this key, so it wasn't saved. Check it in your account and paste it again."}})
		return
	case errors.Is(err, sources.ErrRateLimited):
		s.fail(w, r, &core.InputError{Problems: []string{sources.SearchTitle(name) + " is limiting requests right now, so the key couldn't be checked. Try again in a few minutes."}})
		return
	case err != nil:
		// Not the key's fault, and the error never holds the key.
		s.Logger.Warn("checking a key", "source", name, "err", err)
		s.fail(w, r, &core.InputError{Problems: []string{"jhh couldn't reach " + sources.SearchTitle(name) + " to check the key. Try again later."}})
		return
	}
	if err := s.Keys.Set(name, in.Key); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := core.KeyChanged(r.Context(), s.Store, by, name, true); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Logger.Info("key saved", "source", name)
	s.sourceAfterKey(w, r, name)
}

// removeKey deletes a source's optional key. The source goes on without one.
func (s *server) removeKey(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.Searchers[name].(keyVerifier); !ok {
		s.fail(w, r, &core.InputError{Problems: []string{fmt.Sprintf("%s takes no key", name)}})
		return
	}
	if err := s.Keys.Remove(name); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := core.KeyChanged(r.Context(), s.Store, by, name, false); err != nil {
		s.fail(w, r, err)
		return
	}
	s.Logger.Info("key removed", "source", name)
	s.sourceAfterKey(w, r, name)
}

func (s *server) sourceAfterKey(w http.ResponseWriter, r *http.Request, name string) {
	ss, err := core.ListSources(r.Context(), s.Store, nil)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, info := range ss {
		if info.Name == name {
			s.writeSource(w, info)
			return
		}
	}
	s.fail(w, r, store.ErrNotFound)
}

// jobJSON is a job as the page shows it. Company, title, places, pay, URL
// and description come from listings.
type jobJSON struct {
	ID                 int64              `json:"id"`
	Company            string             `json:"company"`
	CompanySuspect     bool               `json:"company_suspect,omitempty"`
	Title              string             `json:"title"`
	Status             string             `json:"status"`
	Origin             string             `json:"origin"`
	Workplace          string             `json:"workplace"`
	Places             []string           `json:"places,omitempty"`
	Pay                string             `json:"pay,omitempty"`
	Ask                string             `json:"ask,omitempty"`
	URL                string             `json:"url,omitempty"`
	Via                string             `json:"via,omitempty"`
	Posted             string             `json:"posted,omitempty"`
	FirstSeen          string             `json:"first_seen"`
	Closed             string             `json:"closed,omitempty"`
	Reposted           string             `json:"reposted,omitempty"`
	Applied            string             `json:"applied,omitempty"`
	AppliedUnconfirmed bool               `json:"applied_unconfirmed,omitempty"`
	Fit                string             `json:"fit,omitempty"`
	Reason             string             `json:"reason,omitempty"`
	Gaps               string             `json:"gaps,omitempty"`
	Flags              []string           `json:"flags,omitempty"`
	ScreenedBy         string             `json:"screened_by,omitempty"`
	OpenFlags          []string           `json:"open_flags,omitempty"`
	Prefilter          []prefilter.Reason `json:"prefilter,omitempty"`
	Notes              string             `json:"notes,omitempty"`
}

// asker computes suggested asks for a page of jobs.
type asker struct {
	currency string
	factor   float64
}

func (s *server) asker(ctx context.Context) (asker, error) {
	var a asker
	f, ok, err := s.Store.ActiveFilters(ctx)
	if err != nil || !ok {
		return a, err
	}
	var filters prefilter.Filters
	if json.Unmarshal(f.Filters, &filters) == nil && filters.PayFloor != nil {
		a.currency = filters.PayFloor.Currency
	}
	a.factor, err = core.AskFactor(ctx, s.Store)
	return a, err
}

func (a asker) job(r store.JobRow) jobJSON {
	ranges := core.PayRanges(r.PayRanges)
	j := jobJSON{
		ID: r.ID, Company: r.Company, CompanySuspect: r.CompanySuspect, Title: r.Title, Status: string(r.Status),
		Origin: r.Origin, Workplace: r.Workplace, Pay: payText(ranges, r.PayRaw), URL: webURL(r.URL), Via: sources.SearchTitle(r.Via),
		Posted: timeText(r.PostedAt), FirstSeen: timeText(r.FirstSeenAt), Closed: timeText(r.ClosedAt),
		Reposted: timeText(r.RepostedAt), Applied: timeText(r.AppliedAt), AppliedUnconfirmed: r.AppliedUnconfirmed,
		OpenFlags: r.OpenFlags, Notes: r.Notes,
	}
	for _, p := range core.Places(r.Locations) {
		j.Places = append(j.Places, p.String())
	}
	if ask := core.SuggestedAsk(ranges, a.currency, a.factor); ask > 0 {
		j.Ask = money(ask, a.currency)
	}
	if sc := r.Screening; sc != nil {
		j.Fit, j.Reason, j.Gaps, j.ScreenedBy = sc.Fit, sc.Reason, sc.Gaps, string(sc.ScreenedBy)
		_ = json.Unmarshal(sc.Flags, &j.Flags)
	}
	_ = json.Unmarshal(r.Reasons, &j.Prefilter)
	return j
}

func (s *server) listJobs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("list")
	q, ok := lists[name]
	if !ok {
		s.fail(w, r, &core.InputError{Problems: []string{fmt.Sprintf("list: %q isn't a list", name)}})
		return
	}
	q.Limit = maxList
	rows, total, err := s.Store.BoardJobs(r.Context(), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := s.asker(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	jobs := make([]jobJSON, 0, len(rows))
	for _, row := range rows {
		jobs = append(jobs, a.job(row))
	}
	writeJSON(w, http.StatusOK, struct {
		Total int       `json:"total"`
		Jobs  []jobJSON `json:"jobs"`
	}{total, jobs})
}

type jobDetailJSON struct {
	jobJSON
	Description string          `json:"description,omitempty"`
	Postings    []postingJSON   `json:"postings"`
	Screenings  []screeningJSON `json:"screenings"`
	ContentFlag []flagJSON      `json:"content_flags"`
	Events      []eventJSON     `json:"events"`
}

type postingJSON struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Title    string `json:"title"`
	Location string `json:"location,omitempty"`
	URL      string `json:"url,omitempty"`
	Closed   string `json:"closed,omitempty"`
}

type screeningJSON struct {
	Fit    string `json:"fit"`
	Reason string `json:"reason"`
	Gaps   string `json:"gaps,omitempty"`
	By     string `json:"by"`
	At     string `json:"at"`
}

type flagJSON struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Detector string `json:"detector"`
	Excerpt  string `json:"excerpt"`
	Status   string `json:"status"`
}

type eventJSON struct {
	Kind  string          `json:"kind"`
	At    string          `json:"at"`
	Actor string          `json:"actor"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// getJob returns a job with its whole description, postings, ratings,
// content flags and timeline.
func (s *server) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.Store.GetJobDetail(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	a, err := s.asker(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := jobDetailJSON{jobJSON: a.job(d.JobRow), Description: d.Description,
		Postings: []postingJSON{}, Screenings: []screeningJSON{}, ContentFlag: []flagJSON{}, Events: []eventJSON{}}
	for _, p := range d.Postings {
		out.Postings = append(out.Postings, postingJSON{ID: p.ID, Source: sources.SearchTitle(p.Source), Title: p.Title, Location: p.LocationRaw,
			URL: webURL(p.URL), Closed: timeText(p.ClosedAt)})
	}
	for _, sc := range d.Screenings {
		out.Screenings = append(out.Screenings, screeningJSON{Fit: sc.Fit, Reason: sc.Reason, Gaps: sc.Gaps, By: string(sc.ScreenedBy), At: timeText(sc.ScreenedAt)})
	}
	for _, f := range d.Flags {
		out.ContentFlag = append(out.ContentFlag, flagJSON{ID: f.ID, Kind: f.Kind, Detector: f.Detector, Excerpt: f.Excerpt, Status: f.Status})
	}
	for _, e := range d.Events {
		out.Events = append(out.Events, eventJSON{Kind: e.Kind, At: timeText(e.At), Actor: string(e.Actor), Data: e.Data})
	}
	writeJSON(w, http.StatusOK, out)
}

// row returns one job as a list shows it, after a change.
func (s *server) row(ctx context.Context, id int64) (jobJSON, error) {
	d, err := s.Store.GetJobDetail(ctx, id)
	if err != nil {
		return jobJSON{}, err
	}
	a, err := s.asker(ctx)
	return a.job(d.JobRow), err
}

type jobChange struct {
	Status         string  `json:"status"`
	AppliedAt      string  `json:"applied_at"`
	ClearAppliedAt bool    `json:"clear_applied_at"`
	Notes          *string `json:"notes"`
}

func (s *server) updateJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var c jobChange
	if err == nil {
		err = decode(r, &c)
	}
	if err == nil {
		err = core.UpdateApplication(r.Context(), s.Store, by, id,
			core.ApplicationInput{Status: c.Status, AppliedAt: c.AppliedAt, ClearAppliedAt: c.ClearAppliedAt, Notes: c.Notes})
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j, err := s.row(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// maxBulk caps one bulk change.
const maxBulk = maxList

// setStatuses changes several jobs' statuses at once: bulk skip, and
// undoing it.
func (s *server) setStatuses(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Changes []struct {
			ID             int64  `json:"id"`
			Status         string `json:"status"`
			ClearAppliedAt bool   `json:"clear_applied_at"`
		} `json:"changes"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	if len(in.Changes) == 0 || len(in.Changes) > maxBulk {
		s.fail(w, r, &core.InputError{Problems: []string{fmt.Sprintf("changes: 1 to %d", maxBulk)}})
		return
	}
	for _, c := range in.Changes {
		if c.Status == "" {
			s.fail(w, r, &core.InputError{Problems: []string{"changes: each needs a status"}})
			return
		}
		if err := core.UpdateApplication(r.Context(), s.Store, by, c.ID,
			core.ApplicationInput{Status: c.Status, ClearAppliedAt: c.ClearAppliedAt}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (s *server) sendToScreening(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var in struct {
		On bool `json:"on"`
	}
	if err == nil {
		err = decode(r, &in)
	}
	if err == nil {
		err = core.SendToScreening(r.Context(), s.Store, by, id, in.On)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j, err := s.row(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *server) reviewFlag(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var in struct {
		Action string `json:"action"`
		Kind   string `json:"kind"`
	}
	if err == nil {
		err = decode(r, &in)
	}
	var job int64
	if err == nil {
		job, err = core.ReviewFlag(r.Context(), s.Store, by, id, in.Action, in.Kind)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	j, err := s.row(r.Context(), job)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// addJob adds a job the user found, through the same path as add_jobs.
func (s *server) addJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL         string `json:"url"`
		Company     string `json:"company"`
		Title       string `json:"title"`
		Location    string `json:"location"`
		Pay         string `json:"pay"`
		Description string `json:"description"`
		Notes       string `json:"notes"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	added, err := core.AddJobs(ctx, s.Store, s.Sources, by, []core.AgentJob{{URL: in.URL, Company: in.Company, Title: in.Title,
		Location: in.Location, Pay: in.Pay, Description: in.Description, Notes: in.Notes}})
	if err != nil {
		s.fail(w, r, inputFields(err))
		return
	}
	if a := added[0]; a.Error != "" {
		s.fail(w, r, &core.InputError{Problems: []string{a.Error}})
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID       int64  `json:"id"`
		Existing bool   `json:"existing"`
		Status   string `json:"status"`
	}{added[0].JobID, added[0].Existing, string(added[0].Status)})
}

// inputFields drops add_jobs' "jobs[0]." from field names: the page adds
// one job.
func inputFields(err error) error {
	var in *core.InputError
	if !errors.As(err, &in) {
		return err
	}
	out := &core.InputError{}
	for _, p := range in.Problems {
		out.Problems = append(out.Problems, strings.TrimPrefix(p, "jobs[0]."))
	}
	return out
}

type outreachJSON struct {
	ID       int64  `json:"id"`
	Person   string `json:"person"`
	Role     string `json:"role,omitempty"`
	Company  string `json:"company,omitempty"`
	JobID    int64  `json:"job_id,omitempty"`
	JobTitle string `json:"job_title,omitempty"`
	Channel  string `json:"channel"`
	Sent     string `json:"sent"`
	Status   string `json:"status"`
	FollowUp string `json:"follow_up,omitempty"`
	Note     string `json:"note,omitempty"`
}

func (s *server) listOutreach(w http.ResponseWriter, r *http.Request) {
	os, total, err := s.Store.ListOutreach(r.Context(), store.OutreachQuery{Limit: maxList, NewestFirst: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]outreachJSON, 0, len(os))
	for _, o := range os {
		out = append(out, outreachJSON{ID: o.ID, Person: o.Person, Role: o.Role, Company: o.Company, JobID: o.JobID, JobTitle: o.JobTitle,
			Channel: o.Channel, Sent: timeText(o.SentAt), Status: o.Status, FollowUp: timeText(o.FollowUpAt), Note: o.Note})
	}
	writeJSON(w, http.StatusOK, struct {
		Total    int            `json:"total"`
		Statuses []string       `json:"statuses"`
		Items    []outreachJSON `json:"items"`
	}{total, store.OutreachStatuses, out})
}

func (s *server) logOutreach(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Person     string `json:"person"`
		Role       string `json:"role"`
		Company    string `json:"company"`
		Channel    string `json:"channel"`
		JobID      int64  `json:"job_id"`
		SentAt     string `json:"sent_at"`
		FollowUpAt string `json:"follow_up_at"`
		Note       string `json:"note"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, r, err)
		return
	}
	o, err := core.LogOutreach(r.Context(), s.Store, by, core.OutreachInput{Person: in.Person, Role: in.Role, Company: in.Company,
		Channel: in.Channel, JobID: in.JobID, SentAt: in.SentAt, FollowUpAt: in.FollowUpAt, NoFollowUp: in.FollowUpAt == "", Note: in.Note})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID int64 `json:"id"`
	}{o.ID})
}

func (s *server) updateOutreach(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	var in struct {
		Status        string `json:"status"`
		FollowUpAt    string `json:"follow_up_at"`
		ClearFollowUp bool   `json:"clear_follow_up"`
		Note          string `json:"note"`
	}
	if err == nil {
		err = decode(r, &in)
	}
	if err == nil {
		err = core.UpdateOutreach(r.Context(), s.Store, by, id,
			core.OutreachUpdate{Status: in.Status, FollowUpAt: in.FollowUpAt, ClearFollowUp: in.ClearFollowUp, Note: in.Note})
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

type profileJSON struct {
	Profile  *versionJSON `json:"profile"`
	Resume   *resumeJSON  `json:"resume"`
	Memories []memoryJSON `json:"memories"`
	Filters  *filtersJSON `json:"filters"`
	Kinds    []string     `json:"memory_kinds"`
}

type versionJSON struct {
	Version  int64  `json:"version"`
	Markdown string `json:"markdown"`
	Summary  string `json:"summary"`
	By       string `json:"by"`
	At       string `json:"at"`
}

type resumeJSON struct {
	Version int64  `json:"version"`
	Source  string `json:"source"`
	Chars   int    `json:"chars"`
	By      string `json:"by"`
	At      string `json:"at"`
}

type memoryJSON struct {
	ID       int64  `json:"id"`
	Kind     string `json:"kind"`
	Content  string `json:"content"`
	Basis    string `json:"basis"`
	Evidence string `json:"evidence,omitempty"`
	By       string `json:"by"`
	At       string `json:"at"`
}

type filtersJSON struct {
	Version   int64           `json:"version"`
	Rationale string          `json:"rationale"`
	Filters   json.RawMessage `json:"filters"`
	By        string          `json:"by"`
	At        string          `json:"at"`
}

// profile is the read-only Profile page: what the agent has set up.
func (s *server) profile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := profileJSON{Memories: []memoryJSON{}, Kinds: store.MemoryKinds}
	p, ok, err := s.Store.Profile(ctx)
	if err == nil && ok {
		out.Profile = &versionJSON{Version: p.Version, Markdown: p.Markdown, Summary: p.Summary, By: string(p.CreatedBy), At: timeText(p.CreatedAt)}
	}
	var res store.ResumeVersion
	if err == nil {
		res, ok, err = s.Store.Resume(ctx)
	}
	if err == nil && ok {
		out.Resume = &resumeJSON{Version: res.Version, Source: res.Source, Chars: len([]rune(res.Text)), By: string(res.CreatedBy), At: timeText(res.CreatedAt)}
	}
	var ms []store.Memory
	if err == nil {
		ms, err = s.Store.Memories(ctx, store.MemoryQuery{})
	}
	for _, m := range ms {
		out.Memories = append(out.Memories, memoryJSON{ID: m.ID, Kind: m.Kind, Content: m.Content, Basis: m.Basis, Evidence: m.Evidence,
			By: string(m.UpdatedBy), At: timeText(m.UpdatedAt)})
	}
	var f store.FilterVersion
	if err == nil {
		f, ok, err = s.Store.ActiveFilters(ctx)
	}
	if err == nil && ok {
		out.Filters = &filtersJSON{Version: f.Version, Rationale: f.Rationale, Filters: f.Filters, By: string(f.CreatedBy), At: timeText(f.CreatedAt)}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return utc.Format(t)
}

// webURL keeps only http and https links: nothing else from a listing
// becomes a link.
func webURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// payText shows parsed pay ranges compactly, or the posting's own words
// when none were parsed.
func payText(ranges []normalize.PayRange, raw string) string {
	if len(ranges) == 0 {
		return raw
	}
	var parts []string
	for _, r := range ranges {
		t := money(r.Min, r.Currency)
		if r.Max > r.Min {
			t += "–" + strings.TrimPrefix(money(r.Max, r.Currency), "$")
		}
		switch r.Period {
		case "hour":
			t += "/hr"
		case "month":
			t += "/mo"
		}
		if r.Label != "" {
			t += " (" + r.Label + ")"
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, ", ")
}

// money writes an amount the short way: $158k, 95.5k EUR, $42.50.
func money(v float64, currency string) string {
	var n string
	switch {
	case v >= 1_000:
		n = strconv.FormatFloat(math.Round(v/100)/10, 'f', -1, 64) + "k"
	case v == math.Trunc(v):
		n = strconv.FormatFloat(v, 'f', 0, 64)
	default:
		n = strconv.FormatFloat(v, 'f', 2, 64)
	}
	if strings.EqualFold(currency, "USD") {
		return "$" + n
	}
	return n + " " + currency
}
