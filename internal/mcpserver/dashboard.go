package mcpserver

import (
	"context"
	"errors"

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
			URL    string `json:"url"`
			Opened bool   `json:"opened"`
			Note   string `json:"note"`
		}{URL: link, Note: "The link signs the browser in once and works for 15 minutes; after that, the browser stays signed in. Give the user the link if it didn't open."}
		if (in.OpenBrowser == nil || *in.OpenBrowser) && t.OpenBrowser != nil {
			out.Opened = t.OpenBrowser(link) == nil
		}
		return out, nil
	})
}
