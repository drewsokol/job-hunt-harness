// Package normalize turns what job boards send into consistent data: plain
// text from HTML, structured pay ranges, and workplace and places from
// location text. It is shared by every source.
//
// Everything here works on untrusted text. It parses and never executes, and
// it never fetches anything.
package normalize

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"github.com/drewsokol/job-hunt-harness/internal/untrusted"
)

// RoleSeparator joins the roles in the title of a listing that names
// several, such as a Hacker News ad ("Senior Engineer · Product Designer").
// The prefilter judges each role on its own.
const RoleSeparator = " · "

// skipContent lists elements whose content is never shown as text.
var skipContent = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "head": true, "title": true}

// blockElements end a line of text.
var blockElements = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "ul": true, "ol": true, "tr": true, "table": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"section": true, "article": true, "header": true, "footer": true, "blockquote": true, "pre": true, "hr": true,
}

// voidElements never have an end tag.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

var (
	spaceRun = regexp.MustCompile(`[ \t\x{00a0}\r\f\v]+`)
	blankRun = regexp.MustCompile(`\n\s*\n+`)
)

// HTMLToText converts an HTML fragment to the plain text a reader sees.
// Comments, scripts, styles and elements hidden with the hidden attribute
// or inline CSS are dropped (untrusted.Detect reports hidden content
// separately), block elements become line breaks, list items get a "- "
// prefix, and runs of whitespace collapse.
func HTMLToText(fragment string) string {
	var b strings.Builder
	type open struct {
		tag    string
		hidden bool
	}
	var stack []open
	hiddenDepth := 0 // open elements that hide their content
	z := html.NewTokenizer(strings.NewReader(fragment))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return tidy(b.String())
		case html.TextToken:
			if hiddenDepth == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if blockElements[tag] {
				b.WriteByte('\n')
			}
			if tag == "li" && hiddenDepth == 0 {
				b.WriteString("- ")
			}
			if voidElements[tag] || tt == html.SelfClosingTagToken {
				continue
			}
			o := open{tag: tag, hidden: skipContent[tag]}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				if string(k) == "hidden" || (string(k) == "style" && untrusted.HiddenStyle(string(v))) {
					o.hidden = true
				}
			}
			if o.hidden {
				hiddenDepth++
			}
			stack = append(stack, o)
		case html.EndTagToken:
			name, _ := z.TagName()
			if blockElements[string(name)] {
				b.WriteByte('\n')
			}
			// Close down to the matching element, as browsers do with
			// misnested tags. A stray end tag is ignored.
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag != string(name) {
					continue
				}
				for _, o := range stack[i:] {
					if o.hidden {
						hiddenDepth--
					}
				}
				stack = stack[:i]
				break
			}
		}
	}
}

// tidy collapses whitespace: spaces within a line, and blank lines between them.
func tidy(s string) string {
	lines := strings.Split(spaceRun.ReplaceAllString(s, " "), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(blankRun.ReplaceAllString(strings.Join(lines, "\n"), "\n"))
}
