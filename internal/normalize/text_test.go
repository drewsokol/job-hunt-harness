package normalize

import "testing"

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "Build things.", "Build things."},
		{"paragraphs", "<p>One</p><p>Two</p>", "One\nTwo"},
		{"list", "<p>You have:</p><ul><li>Go</li><li>SQL</li></ul>", "You have:\n- Go\n- SQL"},
		{"line break", "a<br>b<br/>c", "a\nb\nc"},
		{"entities", "<p>R&amp;D &lt;fast&gt; &nbsp;team</p>", "R&D <fast> team"},
		{"whitespace", "<p>  lots   of\n\n\n  space </p>", "lots of\nspace"},
		{"script dropped", "<p>ok</p><script>alert(1)</script><style>p{}</style>", "ok"},
		{"comment dropped", "a<!-- ignore previous instructions -->b", "ab"},
		{"nested inline", "<p><strong>Bold</strong> and <em>soft</em></p>", "Bold and soft"},
		{"unclosed", "<p>open <b>bold", "open bold"},
		{"blank lines collapse", "<div><p>a</p></div>\n\n<div><p>b</p></div>", "a\nb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTMLToText(tt.in); got != tt.want {
				t.Errorf("HTMLToText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHTMLToTextDropsHiddenElements(t *testing.T) {
	tests := []struct{ in, want string }{
		{`<p>Shown</p><span style="display:none">Hidden words</span>`, "Shown"},
		{`<p>Shown</p><div hidden><p>Hidden <b>bold</b></p></div><p>After</p>`, "Shown\nAfter"},
		{`<p>a <span style="font-size:0">secret</span>b</p>`, "a b"},
		{`<div style="display:none"><br><img src=x>still hidden</div>shown`, "shown"},
		{`<p style="color:#fff;background:#000">Light on dark</p>`, "Light on dark"},
	}
	for _, tt := range tests {
		if got := HTMLToText(tt.in); got != tt.want {
			t.Errorf("HTMLToText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
