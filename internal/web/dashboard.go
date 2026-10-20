package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultPort is where the dashboard is served unless JHH_UI_PORT says
// otherwise.
const DefaultPort = 7878

// watchEvery is how often the serving process checks the database for
// changes from other processes.
const watchEvery = time.Second

// Dashboard is one process's part in serving the dashboard. Every jhh
// process has one; whichever gets the port first serves, and the others
// hand out links to it. When the serving process exits, the next URL call
// from any other process takes the port over.
type Dashboard struct {
	deps Deps
	port int // as configured; 0 picks a free port (tests)
	ctx  context.Context

	mu      sync.Mutex
	srv     *http.Server
	serving int                // the port this process serves on, or 0
	stop    context.CancelFunc // ends the watcher and open event streams
	done    chan struct{}      // closed when the watcher returns
}

// NewDashboard returns a process's dashboard, which serves on port while
// ctx lasts. Call Start to try for the port now.
func NewDashboard(ctx context.Context, d Deps, port int) *Dashboard {
	if d.Logger == nil {
		d.Logger = slog.New(slog.DiscardHandler)
	}
	return &Dashboard{deps: d, port: port, ctx: ctx}
}

// ErrPortTaken means another program, or a jhh with other data, holds the
// dashboard's port.
var ErrPortTaken = errors.New("port taken")

// Start serves the dashboard if the port is free. It returns nil when this
// process serves it or another jhh with the same data already does.
func (d *Dashboard) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.ensure()
	return err
}

// URL makes sure the dashboard is being served, here or by another jhh
// process with the same data, and returns a link that signs the browser
// in once.
func (d *Dashboard) URL(ctx context.Context) (string, error) {
	d.mu.Lock()
	port, err := d.ensure()
	d.mu.Unlock()
	if err != nil {
		return "", err
	}
	return newLoginLink(ctx, d.deps.Store, port)
}

// ensure serves the dashboard if nobody does, and returns its port.
func (d *Dashboard) ensure() (int, error) {
	if d.serving != 0 {
		return d.serving, nil
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(d.port)))
	if err != nil {
		return d.port, d.servedElsewhere()
	}
	d.serve(ln)
	return d.serving, nil
}

// servedElsewhere checks that the program on the port is jhh with this
// process's data.
func (d *Dashboard) servedElsewhere() error {
	ctx, cancel := context.WithTimeout(d.ctx, 2*time.Second)
	defer cancel()
	addr := "127.0.0.1:" + strconv.Itoa(d.port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/hello", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: port %d is in use and isn't answering; set JHH_UI_PORT to use another", ErrPortTaken, d.port)
	}
	defer resp.Body.Close()
	var h helloJSON
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil || h.App != "jhh" {
		return fmt.Errorf("%w: another program is using port %d; set JHH_UI_PORT to use another", ErrPortTaken, d.port)
	}
	if h.Instance != d.deps.Instance {
		return fmt.Errorf("%w: a jhh with a different data directory is serving port %d; set JHH_UI_PORT to use another", ErrPortTaken, d.port)
	}
	return nil
}

// serve starts the server and the database watcher on ln.
func (d *Dashboard) serve(ln net.Listener) {
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, stop := context.WithCancel(d.ctx)
	h := newHub()
	srv := &http.Server{
		Handler:           newServer(d.deps, port, h).handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          slog.NewLogLogger(d.deps.Logger.Handler(), slog.LevelWarn),
	}
	d.srv, d.serving, d.stop, d.done = srv, port, stop, make(chan struct{})
	d.deps.Logger.Info("serving the dashboard", "port", port)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.deps.Logger.Error("dashboard server stopped", "err", err)
		}
	}()
	go func() {
		defer close(d.done)
		if err := d.deps.Store.Watch(ctx, watchEvery, h.notify); err != nil {
			d.deps.Logger.Error("watching the database", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.srv == srv { // not one started since
			d.shutdown()
		}
	}()
}

// Close stops serving, if this process was, and frees the port for the
// next jhh process.
func (d *Dashboard) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.srv != nil {
		d.shutdown()
	}
}

func (d *Dashboard) shutdown() {
	d.stop() // ends event streams, which would otherwise hold Shutdown up
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.srv.Shutdown(ctx); err != nil {
		d.srv.Close()
	}
	<-d.done
	d.srv, d.serving = nil, 0
}

// Serving returns the port this process serves the dashboard on, or 0.
func (d *Dashboard) Serving() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.serving
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// Instance names a data directory by its database's path, for Deps.
func Instance(dbPath string) string {
	if abs, err := filepath.Abs(dbPath); err == nil {
		dbPath = abs
	}
	if runtime.GOOS == "windows" { // paths ignore case
		dbPath = strings.ToLower(dbPath)
	}
	return hash(filepath.Clean(dbPath))[:16]
}

// PortEnv names the environment variable that moves the dashboard.
const PortEnv = "JHH_UI_PORT"

// Port returns the dashboard's port: JHH_UI_PORT, or DefaultPort.
func Port() (int, error) {
	v := strings.TrimSpace(os.Getenv(PortEnv))
	if v == "" {
		return DefaultPort, nil
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 0 || p > 65535 {
		return 0, fmt.Errorf("%s=%q: give a port number", PortEnv, v)
	}
	return p, nil
}
