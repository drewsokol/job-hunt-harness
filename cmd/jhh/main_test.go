package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/drewsokol/job-hunt-harness/internal/datadir"
	"github.com/drewsokol/job-hunt-harness/internal/web"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioStdoutCarriesOnlyProtocol drives the stdio server in-process with
// raw JSON-RPC, prints to os.Stdout while it runs, and checks that the print
// lands on stderr and that stdout holds nothing but JSON-RPC messages.
func TestStdioStdoutCarriesOnlyProtocol(t *testing.T) {
	t.Setenv(datadir.EnvVar, t.TempDir())
	t.Setenv(web.PortEnv, "0") // never the real dashboard's port
	afterStdoutRedirect = func() { fmt.Println("stray print") }
	t.Cleanup(func() { afterStdoutRedirect = func() {} })

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	stderrDone := make(chan struct{})
	go func() { io.Copy(&stderr, errR); close(stderrDone) }()

	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exit := make(chan int, 1)
	go func() { exit <- run(ctx, nil, inR, outW, errW) }()

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}` + "\n"
	if _, err := io.WriteString(inW, initialize); err != nil {
		t.Fatal(err)
	}

	var stdout []string
	select {
	case line := <-lines:
		stdout = append(stdout, line)
	case <-ctx.Done():
		t.Fatal("no response to initialize")
	}
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			ServerInfo   mcp.Implementation `json:"serverInfo"`
			Instructions string             `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout[0]), &resp); err != nil {
		t.Fatalf("initialize response %q: %v", stdout[0], err)
	}
	if resp.ID != 1 || resp.Result.ServerInfo.Name != "jhh" {
		t.Errorf("initialize response = %+v, want id 1 from jhh", resp)
	}
	if !strings.Contains(resp.Result.Instructions, "UTC") {
		t.Errorf("instructions should state the UTC rule: %q", resp.Result.Instructions)
	}

	inW.Close() // the client disconnects
	select {
	case code := <-exit:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-ctx.Done():
		t.Fatal("server did not stop after stdin closed")
	}
	outW.Close()
	errW.Close()
	for line := range lines {
		stdout = append(stdout, line)
	}
	<-stderrDone

	for _, line := range stdout {
		var msg struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil || msg.JSONRPC != "2.0" {
			t.Errorf("stdout line is not JSON-RPC: %q", line)
		}
	}
	if !strings.Contains(stderr.String(), "stray print") {
		t.Errorf("stray print should be on stderr; stderr = %q", stderr.String())
	}
}

// TestBinaryHandshake builds the real binary and connects to it the way an
// MCP client does.
func TestBinaryHandshake(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "jhh")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), datadir.EnvVar+"="+t.TempDir(), web.PortEnv+"=0")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res := session.InitializeResult()
	if res.ServerInfo.Name != "jhh" {
		t.Errorf("server name = %q, want jhh", res.ServerInfo.Name)
	}
	if !strings.Contains(res.Instructions, "untrusted") {
		t.Errorf("instructions should explain untrusted content: %q", res.Instructions)
	}

	// The tools and playbooks work over the real transport.
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) < 20 {
		t.Fatalf("list tools: %v, %d tools", err, len(tools.Tools))
	}
	call, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_setup_state"})
	if err != nil || call.IsError || len(call.Content) != 1 {
		t.Fatalf("get_setup_state: %v %+v", err, call)
	}
	if text := call.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"next_steps"`) {
		t.Errorf("get_setup_state: %s", text)
	}
	prompt, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "initialize"})
	if err != nil || len(prompt.Messages) != 1 {
		t.Fatalf("initialize prompt: %v", err)
	}

	// The process serves the dashboard, and open_dashboard's link signs a
	// browser in to it.
	call, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "open_dashboard", Arguments: map[string]any{"open_browser": false}})
	if err != nil || call.IsError {
		t.Fatalf("open_dashboard: %v %+v", err, call)
	}
	var dash struct {
		URL    string `json:"url"`
		Opened bool   `json:"opened"`
	}
	if err := json.Unmarshal([]byte(call.Content[0].(*mcp.TextContent).Text), &dash); err != nil || dash.Opened {
		t.Fatalf("open_dashboard: %v %+v", err, dash)
	}
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	resp, err := browser.Get(dash.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = browser.Get(strings.Split(dash.URL, "/?")[0] + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the dashboard after signing in: status %d", resp.StatusCode)
	}
}

func TestDoctorReportsDatabase(t *testing.T) {
	t.Setenv(datadir.EnvVar, t.TempDir())
	dir, err := datadir.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, err := openStore(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	outR, outW := io.Pipe()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { io.Copy(&out, outR); close(done) }()
	code := doctor(context.Background(), outW)
	outW.Close()
	<-done
	if code != 0 || !strings.Contains(out.String(), "schema v9, integrity ok") {
		t.Errorf("doctor exit %d, output:\n%s", code, out.String())
	}
}

func TestCommands(t *testing.T) {
	t.Setenv(datadir.EnvVar, t.TempDir())
	tests := []struct {
		args     []string
		code     int
		inStdout string
		inStderr string
	}{
		{args: []string{"version"}, code: 0, inStdout: "jhh "},
		{args: []string{"help"}, code: 0, inStdout: "claude mcp add jhh -- jhh"},
		{args: []string{"doctor"}, code: 0, inStdout: "not created yet"},
		{args: []string{"frobnicate"}, code: 2, inStderr: `unknown command "frobnicate"`},
		// Probing a URL needs no network; probing a slug does, so only its
		// validation is tested here.
		{args: []string{"probe", "https://jobs.lever.co/exampleco/0b1c2d3e-0000-4000-8000-000000000001"}, code: 0, inStdout: "lever posting 0b1c2d3e-0000-4000-8000-000000000001 on board exampleco"},
		{args: []string{"probe", "https://example.com/careers/1"}, code: 1, inStdout: "No supported board"},
		{args: []string{"probe", "../admin"}, code: 2, inStderr: "board slug"},
		{args: []string{"probe"}, code: 2, inStderr: "usage: jhh probe"},
		{args: []string{"companies"}, code: 0, inStdout: "No companies yet"},
		{args: []string{"companies", "frobnicate"}, code: 2, inStderr: "jhh companies add <name> [where]"},
		// Adding asks the boards, so only what fails before any request is
		// tested here.
		{args: []string{"companies", "add", "Made-Up Labs", "../admin"}, code: 1, inStderr: "board slug"},
		{args: []string{"companies", "add", "Made-Up Labs", "https://example.com/careers/1"}, code: 1, inStderr: "no supported board"},
		{args: []string{"refresh", "--company", "nobody"}, code: 1, inStderr: `no company named "nobody"`},
		{args: []string{"candidates"}, code: 0, inStdout: "No filters are set"},
		{args: []string{"candidates", "--status", "maybe"}, code: 2, inStderr: "usage: jhh candidates"},
		{args: []string{"filters"}, code: 0, inStdout: "No filters are set"},
		{args: []string{"filters", "set", "-"}, code: 2, inStderr: "--rationale"},
		{args: []string{"filters", "frobnicate"}, code: 2, inStderr: "usage:"},
		{args: []string{"filters", "preview", "testdata/bad-filters.json"}, code: 1, inStderr: `levels[0]: "sr" is not a known level`},
		{args: []string{"filters", "preview", "../../examples/filters.json"}, code: 0, inStdout: "Judged 0 open jobs."},
		{args: []string{"filters", "set", "../../examples/filters.json", "--rationale", "test"}, code: 0, inStdout: "Saved filters version 1."},
		{args: []string{"filters"}, code: 0, inStdout: "Filters version 1, set by user"},
		{args: []string{"candidates"}, code: 0, inStdout: "0 open jobs are unscreened."},
		{args: []string{"sources"}, code: 0, inStdout: "themuse      needs setup"},
		{args: []string{"sources", "off", "hackernews"}, code: 0, inStdout: "hackernews   off"},
		{args: []string{"sources", "set", "themuse", "../../examples/themuse.json"}, code: 0, inStdout: `themuse      on           {"queries":[{"categories":["Software Engineering","Data Science"]`},
		{args: []string{"sources", "set", "himalayas", "../../examples/himalayas.json"}, code: 0, inStdout: `himalayas    on           {"queries":[{"q":"machine learning engineer"`},
		{args: []string{"sources"}, code: 0, inStdout: "key: none (optional"},
		{args: []string{"sources", "on", "greenhouse"}, code: 1, inStderr: "set_company"},
		{args: []string{"sources", "frobnicate"}, code: 2, inStderr: "usage:"},
		{args: []string{"sources"}, code: 0, inStdout: "greenhouse   board        0 companies"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			outR, outW := io.Pipe()
			errR, errW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			done := make(chan struct{}, 2)
			go func() { io.Copy(&stdout, outR); done <- struct{}{} }()
			go func() { io.Copy(&stderr, errR); done <- struct{}{} }()

			code := run(context.Background(), tt.args, io.NopCloser(strings.NewReader("")), outW, errW)
			outW.Close()
			errW.Close()
			<-done
			<-done

			if code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tt.code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.inStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.inStdout)
			}
			if !strings.Contains(stderr.String(), tt.inStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.inStderr)
			}
		})
	}
}
