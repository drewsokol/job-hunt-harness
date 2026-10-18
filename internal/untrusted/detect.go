package untrusted

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Kinds of finding, matching content_flags.kind.
const (
	KindCanary     = "canary"      // checks for AI-written applications
	KindHostile    = "hostile"     // tries to change ratings, call tools, or get data out
	KindHiddenText = "hidden_text" // hidden from human readers
	KindUnclear    = "unclear"     // addressed to an AI, purpose unclear
)

// Detectors, matching content_flags.detector.
const (
	DetectorText      = "ai-text"         // visible text addressed to an AI
	DetectorHiddenCSS = "hidden-css"      // text hidden with inline styles
	DetectorHiddenTag = "hidden-attr"     // text in an element with the hidden attribute
	DetectorComment   = "html-comment"    // an HTML comment addressed to an AI
	DetectorInvisible = "invisible-chars" // text smuggled in Unicode tag characters
)

// Finding is one piece of a listing that needs a human look.
type Finding struct {
	Detector string
	Kind     string
	Excerpt  string // at most 200 bytes
}

var (
	// Rules are checked in this order; the first match decides the kind.
	// They are tuned against real listings from AI companies, which talk
	// about agents, prompts and tools all the time: a rule must match text
	// addressed to an AI, not text about AI.
	hostileRules = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget|override)\b[^.\n]{0,30}\b(?:previous|prior|above|earlier|all|your|other)\b[^.\n]{0,20}\b(?:instructions?|prompts?|rules|guidelines|context)\b`),
		regexp.MustCompile(`(?i)\b(?:reveal|print|show|output|repeat|leak)\s+(?:your|the)\s+(?:system prompt|instructions|developer message)\b`),
		regexp.MustCompile(`(?i)\b(?:rate|rank|score|mark|classify|label)\s+(?:this|the|me|my|our)\s+(?:\w+\s+){0,3}(?:as|a)\s+(?:an?\s+|the\s+)?(?:strong|perfect|excellent|top|best|10/10|100%|good)\b`),
		regexp.MustCompile(`(?i)\b(?:send|email|forward|upload|post|share|exfiltrate)\b[^.\n]{0,40}\b(?:resume|cv|profile|salary|contacts?|personal (?:data|information)|memories|api key|credentials|conversation)\b[^.\n]{0,40}\b(?:to|at)\b`),
		// "call the tool", "invoke record_screening": a tool call, not a
		// listing that mentions tools or an on-call function.
		regexp.MustCompile(`(?i)(?:^|[^-\w])(?:call|invoke)\s+(?:the|a|this|your)\s+(?:\w+\s+)?(?:tool|function)s?\b|\b(?:call|invoke|run|execute)\s+\x60?[a-z]+_[a-z_]+\b`),
		regexp.MustCompile(`(?i)\byou are now (?:a|an|in)\b|\bnew instructions\s*:`),
	}
	canaryRules = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bif you(?:'re| are)\s+(?:an?\s+)?(?:ai|a\.i\.|llm|large language model|language model|chatbot|chatgpt|claude|gpt|gemini|bot|automated (?:system|tool|agent))\b`),
		regexp.MustCompile(`(?i)\b(?:ai|llm|chatgpt|gpt|claude|automated tools?)\b[^.\n]{0,60}\b(?:include|mention|insert|add|write|start with|begin with|end with)\s+(?:the|a|this)\s+(?:word|phrase|code ?word|keyword|emoji)\b`),
		regexp.MustCompile(`(?i)\b(?:include|mention|use)\s+the\s+(?:word|phrase|keyword|code ?word)\s+["'“‘]`),
	}
	addressRules = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:dear|attention|note to|hello|hi|hey)\s*,?\s+(?:the\s+)?(?:ai|llm|chatgpt|claude|gpt|language model|ai assistant|bot)s?\s*[,:!]`),
		regexp.MustCompile(`(?i)\b(?:ai|llm|language model|automated)\s+(?:assistants?|agents?|screeners?|tools?|models?|systems?)\s+(?:reading|reviewing|screening|processing|evaluating|parsing|summarizing)\s+(?:this|these)\b`),
		regexp.MustCompile(`(?i)\bas an (?:ai|llm|language model)(?: assistant| model)?\s*,\s*(?:you|please|ignore|do|make|always|never)\b|\binstructions? (?:for|to) (?:the\s+|any\s+)?(?:ai|llm|language model|ai assistant)s?\b`),
	}
)

// classify returns the kind of the first rule that matches text, and where.
func classify(text string) (kind string, start, end int, ok bool) {
	for _, set := range []struct {
		kind  string
		rules []*regexp.Regexp
	}{{KindHostile, hostileRules}, {KindCanary, canaryRules}, {KindUnclear, addressRules}} {
		for _, re := range set.rules {
			if m := re.FindStringIndex(text); m != nil {
				return set.kind, m[0], m[1], true
			}
		}
	}
	return "", 0, 0, false
}

// Detect looks through a listing's raw HTML (or plain text) for content
// aimed at AI agents and for text hidden from human readers. It must run on
// the raw content, before it is cleaned and stripped, so hidden text is still
// there to find. Findings are labels for review; nothing is removed.
func Detect(raw string) []Finding {
	var out []Finding
	seen := map[Finding]bool{}
	add := func(f Finding) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	// checkAI adds a finding when text is addressed to an AI.
	checkAI := func(detector, text string) bool {
		if kind, s, e, ok := classify(text); ok {
			add(Finding{Detector: detector, Kind: kind, Excerpt: excerpt(text, s, e)})
			return true
		}
		return false
	}
	// checkHidden adds a finding for text a reader can't see: its AI kind if
	// it is addressed to an AI, otherwise hidden_text.
	checkHidden := func(detector, text string) {
		text = strings.TrimSpace(text)
		if len(strings.Fields(text)) < 3 {
			return // a stray icon label or a spacer, not hidden content
		}
		if !checkAI(detector, text) {
			add(Finding{Detector: detector, Kind: KindHiddenText, Excerpt: excerpt(text, 0, 0)})
		}
	}

	if s := Smuggled(raw); s != "" {
		if !checkAI(DetectorInvisible, s) {
			add(Finding{Detector: DetectorInvisible, Kind: KindHiddenText, Excerpt: excerpt(s, 0, 0)})
		}
	}

	visible, hidden, comments := split(Clean(raw))
	for _, h := range hidden {
		checkHidden(h.detector, h.text)
	}
	for _, c := range comments {
		checkAI(DetectorComment, c)
	}
	checkAI(DetectorText, visible)
	return out
}

type hiddenText struct{ detector, text string }

// voidElements never have an end tag, so they are not tracked as open.
var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true,
	"input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

// split separates raw HTML into visible text, hidden text and comments.
func split(raw string) (visible string, hidden []hiddenText, comments []string) {
	var (
		vis    strings.Builder
		hidBuf strings.Builder // text inside the outermost open hidden element
		stack  []openElement
	)
	// closeTo pops stack down to length n, collecting the text of a hidden
	// element when the outermost one closes.
	closeTo := func(n int) {
		for len(stack) > n {
			o := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if o.detector != "" && hiddenBy(stack) == "" {
				hidden = append(hidden, hiddenText{o.detector, hidBuf.String()})
				hidBuf.Reset()
			}
		}
	}
	z := html.NewTokenizer(strings.NewReader(raw))
	skip := 0
	for {
		switch z.Next() {
		case html.ErrorToken:
			closeTo(0)
			return vis.String(), hidden, comments
		case html.CommentToken:
			comments = append(comments, string(z.Text()))
		case html.TextToken:
			if skip > 0 {
				continue
			}
			if hiddenBy(stack) != "" {
				hidBuf.Write(z.Text())
				hidBuf.WriteByte(' ')
			} else {
				vis.Write(z.Text())
			}
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if tag == "script" || tag == "style" {
				skip++
				continue
			}
			if lineBreaks[tag] {
				vis.WriteByte('\n')
			}
			if voidElements[tag] {
				continue
			}
			o := openElement{tag: tag}
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				switch string(k) {
				case "hidden":
					o.detector = DetectorHiddenTag
				case "style":
					if HiddenStyle(string(v)) {
						o.detector = DetectorHiddenCSS
					}
				}
			}
			stack = append(stack, o)
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if tag == "script" || tag == "style" {
				skip = max(skip-1, 0)
				continue
			}
			if lineBreaks[tag] {
				vis.WriteByte('\n')
			}
			// Close everything down to the matching open element, as browsers
			// do with misnested tags. A stray end tag is ignored.
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == tag {
					closeTo(i)
					break
				}
			}
		}
	}
}

type openElement struct {
	tag      string
	detector string // why the element is hidden, or ""
}

// hiddenBy returns the detector of the innermost hidden element in stack.
func hiddenBy(stack []openElement) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].detector != "" {
			return stack[i].detector
		}
	}
	return ""
}

var lineBreaks = map[string]bool{"br": true, "p": true, "div": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true, "h4": true}

var (
	hiddenStyleRules = regexp.MustCompile(`(?i)(?:display\s*:\s*none|visibility\s*:\s*hidden|font-size\s*:\s*0(?:\.0+)?(?:px|pt|em|rem|%)?\s*(?:;|$|!)|opacity\s*:\s*0(?:\.0+)?\s*(?:;|$|!)|(?:^|;)\s*(?:max-)?height\s*:\s*0(?:px)?\s*(?:;|$)[^"]*overflow\s*:\s*hidden|clip\s*:\s*rect\(\s*0|left\s*:\s*-\d{3,}px|text-indent\s*:\s*-\d{3,})`)
	whiteText        = regexp.MustCompile(`(?i)(?:^|;)\s*color\s*:\s*(?:#fff(?:fff)?|white|rgb\(\s*255\s*,\s*255\s*,\s*255\s*\))\s*(?:;|$|!)`)
	setsBackground   = regexp.MustCompile(`(?i)background(?:-color)?\s*:`)
)

// HiddenStyle reports whether an inline style hides its element's text.
// White text counts only when the element doesn't set its own background,
// since job descriptions are shown on white pages.
func HiddenStyle(style string) bool {
	return hiddenStyleRules.MatchString(style) || (whiteText.MatchString(style) && !setsBackground.MatchString(style))
}
