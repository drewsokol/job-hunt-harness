package core

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/drewsokol/job-hunt-harness/internal/store"
	"github.com/drewsokol/job-hunt-harness/internal/utc"
)

// Inbound outreach rules.
const (
	maxPayNote       = 500
	maxClientCompany = 200
	maxDeclineNote   = 2_000
	maxPayAmount     = 100_000_000
	// UndisclosedClient is the client_company of a recruiter who won't
	// name the employer yet.
	UndisclosedClient = "undisclosed"
)

// OutreachChannels are the channels the descriptions suggest; any short
// word is accepted.
var OutreachChannels = []string{"linkedin_inmail", "linkedin_message", "linkedin_connection", "email", "job_site", "phone", "referral", "event", "other"}

// PayInput is pay a person mentioned, as an agent reports it: only
// numbers the person stated.
type PayInput struct {
	Min, Max float64
	Currency string // ISO 4217, required with a number
	Period   string // year (the default) or hour
	Note     string
}

// IsZero reports whether nothing was given.
func (p PayInput) IsZero() bool { return p == PayInput{} }

// checkPay checks pay and returns it as stored.
func checkPay(in PayInput) (store.OutreachPay, []string) {
	var problems []string
	p := store.OutreachPay{Min: in.Min, Max: in.Max, Currency: strings.ToUpper(strings.TrimSpace(in.Currency)),
		Period: strings.ToLower(strings.TrimSpace(in.Period)), Note: strings.TrimSpace(in.Note)}
	for _, f := range []struct {
		name string
		v    float64
	}{{"pay_min", p.Min}, {"pay_max", p.Max}} {
		if f.v < 0 || f.v > maxPayAmount {
			problems = append(problems, fmt.Sprintf("%s: a positive amount up to %d", f.name, maxPayAmount))
		}
	}
	if p.Min > 0 && p.Max > 0 && p.Min > p.Max {
		problems = append(problems, "pay_min: more than pay_max")
	}
	number := p.Min > 0 || p.Max > 0
	switch {
	case number && p.Currency == "":
		problems = append(problems, "pay_currency: an ISO code such as USD, needed with an amount")
	case p.Currency != "" && !isCurrency(p.Currency):
		problems = append(problems, fmt.Sprintf("pay_currency: %q must be a three-letter ISO code such as USD", p.Currency))
	case p.Currency != "" && !number:
		problems = append(problems, "pay_currency: give it with pay_min or pay_max")
	}
	switch {
	case p.Period == "" && number:
		p.Period = "year"
	case p.Period != "" && p.Period != "year" && p.Period != "hour":
		problems = append(problems, fmt.Sprintf("pay_period: %q must be year or hour", p.Period))
	case p.Period != "" && !number:
		problems = append(problems, "pay_period: give it with pay_min or pay_max")
	}
	if utf8.RuneCountInString(p.Note) > maxPayNote {
		problems = append(problems, fmt.Sprintf("pay_note: up to %d characters", maxPayNote))
	}
	return p, problems
}

func isCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// checkInbound checks the fields inbound outreach added: direction (when
// need, or when given), client company, decline reason and note. status
// is the thread's status after the change, for the decline rule.
func checkInbound(direction, client, reason, note, status string, need bool) []string {
	var problems []string
	switch {
	case direction == "" && need:
		problems = append(problems, "direction: inbound (they wrote first) or outbound (the user did)")
	case direction != "" && !slices.Contains(store.OutreachDirections, direction):
		problems = append(problems, fmt.Sprintf("direction: %q must be inbound or outbound", direction))
	}
	if utf8.RuneCountInString(client) > maxClientCompany {
		problems = append(problems, fmt.Sprintf("client_company: up to %d characters", maxClientCompany))
	}
	switch {
	case reason != "" && !slices.Contains(store.DeclineReasons, reason):
		problems = append(problems, fmt.Sprintf("decline_reason: %q must be one of %s", reason, strings.Join(store.DeclineReasons, ", ")))
	case reason != "" && status != "declined":
		problems = append(problems, "decline_reason: only with status declined")
	}
	if utf8.RuneCountInString(note) > maxDeclineNote {
		problems = append(problems, fmt.Sprintf("decline_note: up to %d characters", maxDeclineNote))
	}
	return problems
}

// filterInbound checks list_outreach's inbound filters into q.
func filterInbound(in OutreachFilter, q *store.OutreachQuery) []string {
	var problems []string
	q.Direction, q.Channel, q.DeclineReason = in.Direction, strings.ToLower(strings.TrimSpace(in.Channel)), in.DeclineReason
	if q.Direction != "" && !slices.Contains(store.OutreachDirections, q.Direction) {
		problems = append(problems, fmt.Sprintf("direction: %q must be inbound or outbound", q.Direction))
	}
	if q.DeclineReason != "" && !slices.Contains(store.DeclineReasons, q.DeclineReason) {
		problems = append(problems, fmt.Sprintf("decline_reason: %q must be one of %s", q.DeclineReason, strings.Join(store.DeclineReasons, ", ")))
	}
	for _, f := range []struct {
		name, v string
		dst     *time.Time
	}{{"since", in.Since, &q.Since}, {"until", in.Until, &q.Until}} {
		if f.v == "" {
			continue
		}
		t, err := utc.Parse(f.v)
		if err != nil {
			problems = append(problems, f.name+": "+err.Error())
			continue
		}
		*f.dst = t
	}
	if !q.Since.IsZero() && !q.Until.IsZero() && !q.Since.Before(q.Until) {
		problems = append(problems, "until: must be after since")
	}
	return problems
}
