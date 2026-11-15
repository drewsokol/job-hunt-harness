package mcpserver

import (
	"context"
	"errors"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Dashboard serves the web UI and hands out links that sign a browser in.
type Dashboard interface {
	URL(ctx context.Context) (string, error)
}

func (t *tools) addDashboardTools(s *mcp.Server) {
	type openIn struct {
		OpenBrowser *bool `json:"open_browser,omitempty" jsonschema:"open the link in the user's browser; default true"`
	}
	add(t, s, "open_dashboard", reads, func(ctx context.Context, _ call, in openIn) (any, error) {
		if t.Dashboard == nil {
			return nil, shownError{errors.New("this jhh process has no dashboard")}
		}
		link, err := t.Dashboard.URL(ctx)
		if err != nil {
			return nil, shownError{err}
		}
		out := struct {
			URL     string `json:"url"`
			HelpURL string `json:"help_url,omitempty"`
			Opened  bool   `json:"opened"`
			Note    string `json:"note"`
		}{URL: link, HelpURL: helpURL(link),
			Note: "The link signs the browser in once and works for 15 minutes; after that, the browser stays signed in. help_url, the \"Ask your agent\" page, works once the browser has signed in through url. Give the user the link if it didn't open."}
		if (in.OpenBrowser == nil || *in.OpenBrowser) && t.OpenBrowser != nil {
			out.Opened = t.OpenBrowser(link) == nil
		}
		return out, nil
	})
}

// helpURL is the plain address of the dashboard's "Ask your agent" page,
// on the port the sign-in link uses.
func helpURL(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/", Fragment: "help"}).String()
}
