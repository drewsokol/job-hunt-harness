package prefilter

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
)

// Status is the prefilter's decision.
type Status string

const (
	Pass Status = "pass"
	Drop Status = "drop"
	// Pending means stage 1 passed and the decision needs the description,
	// which hasn't been fetched yet.
	Pending Status = "pending"
)

// Effects of a reason.
const (
	EffectDrop    = "drop"    // the job fails this rule
	EffectPending = "pending" // the rule needs the description
	EffectFlag    = "flag"    // the job passes, and the candidate asked to see this
	EffectNote    = "note"    // the job passes, but the rule couldn't tell
)

// Reason explains one rule's effect on a job. Text never quotes the
// listing, only the filters and jhh's own vocabulary, so it is safe to show
// outside the untrusted envelope.
type Reason struct {
	Rule   string `json:"rule"`
	Effect string `json:"effect"`
	Text   string `json:"text"`
}

// Result is the prefilter's decision on one job.
type Result struct {
	Status  Status
	Reasons []Reason
	// Score orders passing jobs, highest first. It means nothing else.
	Score float64
}

// Input is what the prefilter knows about a job. All text is untrusted
// listing content, already cleaned.
type Input struct {
	Company        string
	Title          string
	Workplace      string // remote | hybrid | onsite | unknown
	Places         []normalize.Place
	EmploymentType string
	Pay            []normalize.PayRange
	Description    string
	// DescriptionKnown is false while a board that lists jobs without
	// descriptions (Greenhouse) hasn't had the description fetched.
	DescriptionKnown bool
	// SeveralRoles says the title may name several roles joined with
	// normalize.RoleSeparator, as a Hacker News ad's does. Other titles are
	// never split.
	SeveralRoles bool
}

// Compiled is a validated filter, ready to evaluate jobs.
type Compiled struct {
	f                       Filters
	require, exclude, flag  []term
	titleKeys, bodyKeys     []term
	minBody                 int
	remoteIn, onsiteIn      []normalize.Place
	levelUnknown, locPolicy string
}

// Compile validates f and prepares it for evaluation.
func Compile(f Filters) (*Compiled, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	c := &Compiled{f: f, levelUnknown: cmpOr(f.LevelUnknown, PolicyPass)}
	if t := f.Titles; t != nil {
		c.require, c.exclude = newTerms(t.RequireAny), newTerms(t.ExcludeAny)
	}
	if fl := f.Flag; fl != nil {
		c.flag = newTerms(fl.TitleAny)
	}
	if k := f.Keywords; k != nil {
		c.titleKeys, c.bodyKeys = newTerms(k.TitleAny), newTerms(k.BodyAny)
		c.minBody = k.MinBodyHitsWithoutTitleMatch
		if c.minBody == 0 && len(c.bodyKeys) > 0 {
			c.minBody = 1
		}
	}
	if l := f.Location; l != nil {
		c.locPolicy = cmpOr(l.Unknown, PolicyPass)
		for _, s := range l.RemoteIn {
			p, _ := normalize.ParsePlace(s)
			c.remoteIn = append(c.remoteIn, p)
		}
		for _, s := range l.OnsiteOrHybridIn {
			p, _ := normalize.ParsePlace(s)
			c.onsiteIn = append(c.onsiteIn, p)
		}
	}
	return c, nil
}

// Filters returns the filters c was compiled from.
func (c *Compiled) Filters() Filters { return c.f }

// Evaluate judges one job. Every failing rule is reported, not just the
// first, so the candidate can see everything a filter change would affect.
//
// A title naming several roles (Input.SeveralRoles) is judged role by role,
// and the job gets the best result: it passes if any role does. Flags from
// every role are kept, so a flagged role isn't hidden by a better one.
func (c *Compiled) Evaluate(in Input) Result {
	var roles []string
	if in.SeveralRoles {
		roles = strings.Split(in.Title, normalize.RoleSeparator)
	}
	if len(roles) < 2 {
		return c.evaluate(in)
	}
	results := make([]Result, len(roles))
	best := 0
	for i, role := range roles {
		in.Title = role
		results[i] = c.evaluate(in)
		if better(results[i], results[best]) {
			best = i
		}
	}
	out := results[best]
	out.Reasons = slices.Clone(out.Reasons)
	for i, r := range results {
		for _, reason := range r.Reasons {
			if i != best && reason.Effect == EffectFlag && !slices.Contains(out.Reasons, reason) {
				out.Reasons = append(out.Reasons, reason)
			}
		}
	}
	return out
}

// better reports whether a is a better result for the job than b.
func better(a, b Result) bool {
	if ra, rb := statusRank(a.Status), statusRank(b.Status); ra != rb {
		return ra > rb
	}
	return a.Score > b.Score
}

func statusRank(s Status) int {
	switch s {
	case Pass:
		return 2
	case Pending:
		return 1
	}
	return 0
}

func (c *Compiled) evaluate(in Input) Result {
	var drops, waits, passes []Reason
	reason := func(rule, effect, format string, args ...any) {
		r := Reason{Rule: rule, Effect: effect, Text: fmt.Sprintf(format, args...)}
		switch effect {
		case EffectDrop:
			drops = append(drops, r)
		case EffectPending:
			waits = append(waits, r)
		default:
			passes = append(passes, r)
		}
	}
	title := newText(in.Title)
	var body *text
	if in.DescriptionKnown {
		body = newText(normalize.TrimBoilerplate(in.Description, in.Company))
	}

	// Titles. A listing without a title (a Hacker News ad whose header
	// names no role) isn't dropped for it; the keywords still apply.
	switch {
	case len(c.require) == 0:
	case strings.TrimSpace(in.Title) == "":
		reason("titles.require_any", EffectNote, "the listing names no role in its title")
	case len(title.matching(c.require, true)) == 0:
		reason("titles.require_any", EffectDrop, "title has none of %s", list(texts(c.require)))
	}
	if hit := title.matching(c.exclude, true); len(hit) > 0 {
		reason("titles.exclude_any", EffectDrop, "title has %s", list(hit))
	}

	// Levels.
	levels := levelsIn(title)
	topLevel := ""
	for _, l := range levels {
		if slices.Contains(c.f.Levels, l) {
			topLevel = l
		}
	}
	if len(c.f.Levels) > 0 {
		switch {
		case len(levels) == 0 && c.levelUnknown == PolicyDrop:
			reason("level_unknown", EffectDrop, "title states no level")
		case len(levels) == 0:
			reason("level_unknown", EffectNote, "title states no level")
		case topLevel == "":
			reason("levels", EffectDrop, "level %s is not one of %s", strings.Join(levels, "/"), list(c.f.Levels))
		}
	}

	// Employment type. A kind the title states is the role's own. A field
	// listing several kinds offers the job as any of them, so it drops only
	// when every kind it lists is excluded, and otherwise notes the rest.
	fieldKinds, titleKinds := employmentField(in.EmploymentType), employmentTitle(title)
	var listed []string
	for _, e := range c.f.EmploymentExclude {
		switch {
		case titleKinds[e]:
			reason("employment_exclude", EffectDrop, "%s", e)
		case fieldKinds[e]:
			listed = append(listed, e)
		}
	}
	switch {
	case len(listed) == 0:
	case len(listed) == len(fieldKinds):
		for _, e := range listed {
			reason("employment_exclude", EffectDrop, "%s", e)
		}
	default:
		reason("employment_exclude", EffectNote, "also offered as %s", strings.Join(listed, ", "))
	}

	// Location.
	if c.f.Location != nil {
		c.location(in, body, reason)
	}

	// Keywords.
	titleHits := title.matching(c.titleKeys, true)
	var bodyHits []string
	if body != nil {
		bodyHits = body.matching(c.bodyKeys, true)
	}
	if len(c.titleKeys)+len(c.bodyKeys) > 0 && len(titleHits) == 0 {
		switch {
		case len(c.bodyKeys) == 0 && strings.TrimSpace(in.Title) == "":
			reason("keywords", EffectNote, "the listing names no role in its title")
		case len(c.bodyKeys) == 0:
			reason("keywords", EffectDrop, "title has none of %s", list(texts(c.titleKeys)))
		case body == nil:
			reason("keywords", EffectPending, "title has no keyword; waiting for the description")
		case len(bodyHits) < c.minBody:
			found := "none"
			if len(bodyHits) > 0 {
				found = list(bodyHits)
			}
			reason("keywords", EffectDrop, "title has no keyword, and the description has %d of the %d needed (found %s)",
				len(bodyHits), c.minBody, found)
		}
	}

	// Pay floor.
	if pf := c.f.PayFloor; pf != nil && pf.Amount > 0 {
		if top, ok := topPay(in.Pay, pf.Currency); ok && top < pf.Amount {
			reason("pay_floor", EffectDrop, "pay tops out at %s %s a year, below the floor of %s",
				money(top), pf.Currency, money(pf.Amount))
		}
	}

	// Flags.
	if hit := title.matching(c.flag, true); len(hit) > 0 {
		reason("flag.title_any", EffectFlag, "title has %s", list(hit))
	}

	res := Result{Status: Pass, Score: c.score(topLevel, len(titleHits) > 0, len(bodyHits))}
	switch {
	case len(drops) > 0:
		res.Status = Drop
	case len(waits) > 0:
		res.Status = Pending
	}
	res.Reasons = slices.Concat(drops, waits, passes)
	return res
}

// location judges where the job can be done against remote_in and
// onsite_or_hybrid_in. Unclear cases follow location.unknown.
func (c *Compiled) location(in Input, body *text, reason func(rule, effect, format string, args ...any)) {
	unclear := func(format string, args ...any) {
		if c.locPolicy == PolicyDrop {
			reason("location.unknown", EffectDrop, format, args...)
		} else {
			reason("location.unknown", EffectNote, format, args...)
		}
	}
	places := in.Places
	switch in.Workplace {
	case normalize.Remote:
		if len(places) == 0 {
			unclear("remote, but it doesn't say where")
			return
		}
		switch best(places, c.remoteIn, overlaps) {
		case none:
			reason("location", EffectDrop, "remote in %s, outside remote_in", summary(places))
		case unclearRel:
			unclear("remote in %s; can't tell if that's within remote_in", summary(places))
		}
	case normalize.Hybrid, normalize.Onsite:
		// With nowhere to go in, an office job is out wherever it is.
		if len(c.onsiteIn) == 0 {
			reason("location", EffectDrop, "%s, and onsite_or_hybrid_in is empty", in.Workplace)
			return
		}
		if len(places) == 0 {
			unclear("%s, but it doesn't say where", in.Workplace)
			return
		}
		switch best(places, c.onsiteIn, within) {
		case none:
			reason("location", EffectDrop, "%s in %s, outside onsite_or_hybrid_in", in.Workplace, summary(places))
		case unclearRel:
			unclear("%s in %s; can't tell if that's within onsite_or_hybrid_in", in.Workplace, summary(places))
		}
	default:
		if len(places) == 0 {
			unclear("the listing doesn't say where or whether it's remote")
			return
		}
		onsite, remote := best(places, c.onsiteIn, within), best(places, c.remoteIn, overlaps)
		switch {
		case onsite == match:
			return
		case onsite == none && remote == none:
			reason("location", EffectDrop, "in %s, outside both remote_in and onsite_or_hybrid_in", summary(places))
		case remote == match && body != nil && slices.ContainsFunc(remoteInDescription, func(t term) bool { return body.has(t, false) }):
			reason("location", EffectNote, "remote, according to the description")
		case body == nil && c.locPolicy == PolicyDrop:
			reason("location", EffectPending, "doesn't say whether it's remote; waiting for the description")
		default:
			unclear("in %s, and doesn't say whether it's remote", summary(places))
		}
	}
}

// score orders passing jobs: the level (higher in the filters' levels
// scores more), a keyword in the title, and keywords in the description.
func (c *Compiled) score(level string, titleHit bool, bodyHits int) float64 {
	s := 0.0
	if level != "" {
		levels := slices.Clone(c.f.Levels)
		slices.SortFunc(levels, func(a, b string) int { return levelRank(a) - levelRank(b) })
		levels = slices.Compact(levels)
		if 2*slices.Index(levels, level) >= len(levels)-1 {
			s += 3
		} else {
			s++
		}
	}
	if titleHit {
		s += 3
	}
	s += float64(min(bodyHits, 6)) / 2
	return math.Round(s*10) / 10
}

// topPay returns the highest yearly pay among ranges in currency, and
// whether any range could be compared. Ranges without a period are taken
// as yearly only when the figures look yearly.
func topPay(ranges []normalize.PayRange, currency string) (float64, bool) {
	top, ok := 0.0, false
	for _, r := range ranges {
		if r.Currency != currency {
			continue
		}
		v := max(r.Max, r.Min)
		switch r.Period {
		case "hour":
			v *= 2080
		case "month":
			v *= 12
		case "year":
		default:
			if v < 15000 {
				continue
			}
		}
		top, ok = max(top, v), true
	}
	return top, ok
}

// relation is how a job's place relates to a place in the filters.
type relation int

const (
	none relation = iota
	unclearRel
	match
)

// best returns the strongest relation between any job place and any
// filter place.
func best(places, wanted []normalize.Place, rel func(p, w normalize.Place) relation) relation {
	r := none
	for _, p := range places {
		for _, w := range wanted {
			r = max(r, rel(p, w))
		}
	}
	return r
}

// within says whether job place p lies inside filter place w: a job in
// Columbus is within "Ohio", a job somewhere in the US might or might not
// be within "Columbus, OH".
func within(p, w normalize.Place) relation {
	if isRegionOnly(w) {
		switch {
		case p.Country != "":
			return boolRel(normalize.InRegion(p.Country, w.Region))
		case isRegionOnly(p):
			return regionRel(p.Region, w.Region)
		}
		return unclearRel
	}
	switch {
	case isRegionOnly(p):
		if normalize.InRegion(w.Country, p.Region) {
			return unclearRel
		}
		return none
	case p.Country == "":
		return unclearRel // a city jhh doesn't know
	case p.Country != w.Country:
		return none
	case w.Region != "" && p.Region == "":
		return unclearRel
	case w.Region != "" && p.Region != w.Region:
		return none
	case w.City != "" && p.City == "":
		return unclearRel
	case w.City != "" && !strings.EqualFold(p.City, w.City):
		return none
	}
	return match
}

// overlaps says whether remote work in job place p is open to someone in
// filter place w. "Remote - US" is open to someone in Ohio, and "Remote -
// California" is taken as open to someone who said "US".
func overlaps(p, w normalize.Place) relation {
	return max(within(p, w), within(w, p))
}

// regionRel relates region a to region b: all of a in b is a match, some
// of it is unclear.
func regionRel(a, b string) relation {
	switch {
	case a == b || b == normalize.Worldwide:
		return match
	case a == normalize.Worldwide:
		return unclearRel
	}
	inB, total := 0, 0
	for _, c := range normalize.RegionMembers(a) {
		total++
		if normalize.InRegion(c, b) {
			inB++
		}
	}
	switch {
	case total > 0 && inB == total:
		return match
	case inB > 0:
		return unclearRel
	}
	return none
}

func isRegionOnly(p normalize.Place) bool { return p.Country == "" && normalize.IsRegion(p.Region) }

func boolRel(b bool) relation {
	if b {
		return match
	}
	return none
}

// summary names a job's places by country and region only. City names can
// be raw listing text, so they are left out of reasons.
func summary(places []normalize.Place) string {
	var names []string
	unknown := false
	for _, p := range places {
		var n string
		switch {
		case p.Country != "" && p.Region != "":
			n = p.Region + ", " + p.Country
		case p.Country != "":
			n = p.Country
		case p.Region != "":
			n = p.Region
		default:
			unknown = true
			continue
		}
		if !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	if unknown {
		names = append(names, "a place jhh doesn't recognize")
	}
	if len(names) > 4 {
		names = append(names[:3], fmt.Sprintf("%d more", len(names)-3))
	}
	return strings.Join(names, "; ")
}

// list quotes terms for a reason, shortening long lists.
func list(ss []string) string {
	q := make([]string, 0, min(len(ss), 6))
	for i, s := range ss {
		if i == 5 && len(ss) > 6 {
			q = append(q, fmt.Sprintf("%d more", len(ss)-5))
			break
		}
		q = append(q, strconv.Quote(s))
	}
	return strings.Join(q, ", ")
}

func texts(ts []term) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.text
	}
	return out
}

// money formats a yearly amount with thousands separators: 150,000.
func money(v float64) string {
	s := strconv.FormatInt(int64(math.Round(v)), 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func cmpOr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
