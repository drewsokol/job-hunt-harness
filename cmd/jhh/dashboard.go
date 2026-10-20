package main

import (
	"context"
	"fmt"
	"io"

	"github.com/drewsokol/job-hunt-harness/internal/datadir"
	"github.com/drewsokol/job-hunt-harness/internal/sources"
	"github.com/drewsokol/job-hunt-harness/internal/sources/builtin"
	"github.com/drewsokol/job-hunt-harness/internal/web"
)

// openBrowser is web.OpenBrowser; tests replace it.
var openBrowser = web.OpenBrowser

// dashboard opens the dashboard in the browser. If no agent's jhh is
// serving it, this process does until it is interrupted.
func dashboard(ctx context.Context, stdout, stderr io.Writer) int {
	dir, err := datadir.Resolve()
	if err == nil {
		err = dir.Ensure()
	}
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	port, err := web.Port()
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 2
	}
	st, err := openStore(ctx, dir)
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	defer st.Close()
	dash := web.NewDashboard(ctx, web.Deps{Store: st, Sources: builtin.All(sources.NewClient()), Instance: web.Instance(dir.DB())}, port)
	defer dash.Close()
	link, err := dash.URL(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "jhh:", err)
		return 1
	}
	if err := openBrowser(link); err != nil {
		fmt.Fprintf(stdout, "Open this link to sign in (it works once, for 15 minutes):\n  %s\n", link)
	} else {
		fmt.Fprintln(stdout, "Opened the dashboard in your browser.")
	}
	if dash.Serving() == 0 {
		return 0 // another jhh serves it
	}
	fmt.Fprintf(stdout, "Serving it on http://127.0.0.1:%d until you press Ctrl+C.\n", dash.Serving())
	<-ctx.Done()
	return 0
}
