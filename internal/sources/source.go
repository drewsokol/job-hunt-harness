// Package sources defines how jhh reads job boards. Each board (Greenhouse,
// Lever, Ashby) is a subpackage implementing Source.
//
// A source maps its board's fields onto Posting and handles that board's
// quirks (for example, Ashby marks hybrid roles as remote). Everything shared
// by all boards (HTML to text, location and pay parsing, cleaning untrusted
// text) happens afterwards in the normalize and untrusted packages, so a new
// source only has to describe its own board.
//
// Every source must pass the shared conformance suite in sources/conformance.
//
// Search sources (The Muse) implement Searcher instead: they find postings
// across employers, and each result names its employer.
package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/normalize"
)

// Board identifies one company's job board on a source. It is decoded from
// the company's source_config.
type Board struct {
	Slug string `json:"slug"`
}

// slugPattern is what a board slug may contain. Slugs go into URLs, so
// anything else is rejected before a request is made.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)

// ParseBoard decodes and validates a company's source_config.
func ParseBoard(config json.RawMessage) (Board, error) {
	var b Board
	if err := json.Unmarshal(config, &b); err != nil {
		return Board{}, fmt.Errorf("source config: %w", err)
	}
	if err := ValidSlug(b.Slug); err != nil {
		return Board{}, err
	}
	return b, nil
}

// ValidSlug reports whether slug is safe to put in a board URL.
func ValidSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("board slug %q must be lowercase letters, digits, - or _", slug)
	}
	return nil
}

// Posting is one listing as a source read it. Text fields are untrusted:
// they come from the company's board.
type Posting struct {
	ExternalID string // the board's id for this posting
	OpeningKey string // the board's id for the opening behind it, if it has one (Greenhouse internal_job_id)
	Title      string
	// LocationRaw is the board's location text, possibly several places
	// joined with " | ".
	LocationRaw string
	// Workplace is the board's own remote/hybrid/onsite field, already
	// corrected for the board's quirks: "remote", "hybrid", "onsite" or "".
	Workplace string
	// Country is an ISO 3166 code or country name, when the board gives one.
	Country   string
	URL       string
	PostedAt  time.Time // UTC
	UpdatedAt time.Time // UTC; when the board says the posting last changed
	PayRaw    string
	// Pay is the board's structured pay, when it has any. Pay found in the
	// description text is parsed later, from DescriptionHTML.
	Pay             []normalize.PayRange
	EmploymentType  string
	DescriptionHTML string // as the board sent it, for injection detection
	DescriptionText string // the board's own plain text, when it has one
	// Partial is true when the listing omitted the description; a Detailer
	// can fetch it.
	Partial bool
	// Company is the employer's name, set by search sources, whose results
	// come from many employers. Board sources leave it empty.
	Company string
}

// ListResult is what Source.List returns.
type ListResult struct {
	Postings    []Posting
	NotModified bool   // the board hasn't changed since the ETag passed in
	ETag        string // send this next time
}

// PostingRef identifies a posting from its URL.
type PostingRef struct {
	Source     string
	Slug       string
	ExternalID string
}

// Source reads one kind of job board.
type Source interface {
	// Name is the source's id, used in company records and posting ids.
	Name() string
	// Prefix is the short form used in posting ids ("gh" in "gh:exampleco:123").
	Prefix() string
	// List returns every open posting on the board. If etag matches the
	// board's current version, it returns NotModified and no postings.
	List(ctx context.Context, b Board, etag string) (ListResult, error)
	// Probe returns the number of open postings on the board with this slug,
	// or ErrBoardNotFound.
	Probe(ctx context.Context, slug string) (int, error)
	// MatchURL recognizes a posting URL from this source, including URLs on
	// a company's own careers site that carry the source's posting id.
	MatchURL(u *url.URL) (PostingRef, bool)
}

// Detailer is implemented by sources whose List omits descriptions
// (Greenhouse). Refresh calls Detail only for postings worth the request.
type Detailer interface {
	Detail(ctx context.Context, b Board, externalID string) (Posting, error)
}

// Query is one search, from the filters' search section. A posting matches
// when it is in any of the categories, at any of the levels and in any of
// the locations; an empty field doesn't narrow.
type Query struct {
	Categories []string
	Levels     []string
	Locations  []string
}

// SearchResult is one page of a search.
type SearchResult struct {
	Postings []Posting // each with Company set
	// Pages is how many pages the whole search has.
	Pages int
}

// Searcher finds postings across employers (The Muse). Each call is one
// request, which search APIs count against a rate limit.
type Searcher interface {
	Name() string
	// Prefix is the short form used in posting ids ("tm" in "tm:22008828").
	Prefix() string
	// Check returns what is wrong with q in this searcher's terms, such as
	// a category it doesn't have, one problem per entry.
	Check(q Query) []string
	// Search returns one page of q; pages are numbered from 0.
	Search(ctx context.Context, q Query, page int) (SearchResult, error)
	// Lookup returns one posting, or ErrPostingNotFound once it's gone.
	Lookup(ctx context.Context, externalID string) (Posting, error)
}

// ErrRateLimited means a search source refused a request because too many
// were made; searching should stop until later.
var ErrRateLimited = errors.New("rate limit reached")

// ErrPostingNotFound means a search source no longer has the posting.
var ErrPostingNotFound = errors.New("posting not found")

// SearchPostingID builds jhh's id for a posting found by search: "tm:22008828".
func SearchPostingID(s Searcher, externalID string) string {
	return s.Prefix() + ":" + externalID
}

// ErrBoardNotFound means the board doesn't exist on this source.
var ErrBoardNotFound = errors.New("board not found")

// PostingID builds jhh's id for a posting: "gh:exampleco:123".
func PostingID(s Source, b Board, externalID string) string {
	return s.Prefix() + ":" + b.Slug + ":" + externalID
}

// ParseTime reads a board timestamp (RFC 3339 with any offset) as UTC. A
// missing or unreadable time becomes the zero time: board dates are
// informational, and a bad one shouldn't drop the posting.
func ParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// UnixMillis converts a board's milliseconds-since-epoch timestamp to UTC.
// Zero stays the zero time.
func UnixMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
