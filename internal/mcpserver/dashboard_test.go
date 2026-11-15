package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDashboard struct {
	err  error
	port string // 7878 when empty
}

func (d fakeDashboard) URL(context.Context) (string, error) {
	port := d.port
	if port == "" {
		port = "7878"
	}
	return "http://127.0.0.1:" + port + "/?t=example", d.err
}

func TestOpenDashboard(t *testing.T) {
	var opened []string
	h := newHarness(t, Deps{Dashboard: fakeDashboard{}, OpenBrowser: func(url string) error { opened = append(opened, url); return nil }})
	var out struct {
		URL     string `json:"url"`
		HelpURL string `json:"help_url"`
		Opened  bool   `json:"opened"`
	}
	h.call("open_dashboard", map[string]any{}, &out)
	if out.URL != "http://127.0.0.1:7878/?t=example" || out.HelpURL != "http://127.0.0.1:7878/#help" || !out.Opened || len(opened) != 1 {
		t.Errorf("open_dashboard: %+v, opened %v", out, opened)
	}
	moved := newHarness(t, Deps{Dashboard: fakeDashboard{port: "51234"}})
	moved.call("open_dashboard", map[string]any{"open_browser": false}, &out)
	if out.URL != "http://127.0.0.1:51234/?t=example" || out.HelpURL != "http://127.0.0.1:51234/#help" {
		t.Errorf("on another port: %+v", out)
	}
	out.Opened = false
	h.call("open_dashboard", map[string]any{"open_browser": false}, &out)
	if out.Opened || len(opened) != 1 {
		t.Errorf("open_browser false still opened it: %+v", out)
	}

	busy := newHarness(t, Deps{Dashboard: fakeDashboard{err: errors.New("port taken: another program is using port 7878")}})
	if msg := busy.fail("open_dashboard", map[string]any{}); !strings.Contains(msg, "another program") {
		t.Errorf("a taken port: %s", msg)
	}
	if msg := newHarness(t, Deps{}).fail("open_dashboard", map[string]any{}); !strings.Contains(msg, "no dashboard") {
		t.Errorf("no dashboard: %s", msg)
	}
}
