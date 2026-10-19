package dedupe

import (
	"strings"
	"testing"
)

func TestTitleKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Senior Software Engineer", "senior software engineer"},
		{"Sr. Software Engineer, Platform (Remote - US)", "senior software engineer platform"},
		{"Staff AI Engineer | Canada | Remote", "staff ai engineer"},
		{"Software Engineer - New York, NY", "software engineer"},
		{"Software Engineer II (Backend)", "software engineer 2 backend"},
		{"ML Engineer - Search & Ranking", "ml engineer search and ranking"},
		{"Engineering Mgr, Payments", "engineering manager payments"},
		{"Product Designer [Hybrid]", "product designer"},
	}
	for _, tt := range tests {
		if got := TitleKey(tt.in); got != tt.want {
			t.Errorf("TitleKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// body is a made-up description long enough to fingerprint.
const body = `About the role
You will build the systems that move invoices between our customers and their banks.
You will work with a small team on reliability, data modeling and developer tooling.
What you bring
- Five years building backend services
- Comfort owning production systems end to end
`

func TestFingerprintIgnoresCityAndPay(t *testing.T) {
	sf := "Location: San Francisco, CA\n" + body + "Our San Francisco office is near the train.\nPay: $180,000 - $220,000"
	nyc := "Location: New York, NY\n" + body + "Our New York office is near the train.\nPay: $190,000 - $230,000"
	a, b := Fingerprint(sf, "San Francisco, CA"), Fingerprint(nyc, "New York, NY")
	if a == "" || a != b {
		t.Errorf("city postings differ: %q vs %q", a, b)
	}
	if len(a) != 16 {
		t.Errorf("fingerprint %q is not 16 hex characters", a)
	}
	other := Fingerprint(strings.Replace(body, "invoices", "payroll", 1))
	if other == a {
		t.Error("different descriptions share a fingerprint")
	}
}

func TestFingerprintIgnoresFormatting(t *testing.T) {
	if Fingerprint(body) != Fingerprint(strings.ToUpper(strings.ReplaceAll(body, "\n", "  \n\n"))) {
		t.Error("case and whitespace change the fingerprint")
	}
}

func TestFingerprintNeedsEnoughText(t *testing.T) {
	if got := Fingerprint("Apply on our careers site."); got != "" {
		t.Errorf("short text got fingerprint %q", got)
	}
}

func TestCompanyKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Example Corp, Inc.", "example"},
		{"example corp", "example"},
		{"The Example Company", "example"},
		{"Exampleco GmbH", "exampleco"},
		{"Smith & Jones LLP", "smith and jones"},
		{"Made-Up Labs", "made up labs"},
		{"The Company", "the company"},
		{"Co", "co"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := CompanyKey(tt.in); got != tt.want {
			t.Errorf("CompanyKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCanonicalURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://boards.greenhouse.io/exampleco/jobs/123?gh_src=abc&utm_source=x", "https://boards.greenhouse.io/exampleco/jobs/123"},
		{"http://www.Example.com/careers/123/?gh_jid=123#apply", "https://example.com/careers/123?gh_jid=123"},
		{"https://jobs.lever.co/exampleco/abc-def/?lever-source=LinkedIn", "https://jobs.lever.co/exampleco/abc-def"},
		{"https://example.com/jobs?b=2&a=1", "https://example.com/jobs?a=1&b=2"},
		{"https://user:pass@example.com/jobs", "https://example.com/jobs"},
		{"javascript:alert(1)", ""},
		{"mailto:jobs@example.com", ""},
		{"/relative/path", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := CanonicalURL(tt.in); got != tt.want {
			t.Errorf("CanonicalURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
