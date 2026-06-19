// Command fleet-qa-mcp is a QA toolkit for Fleet with two front-ends over one
// shared core (internal/qa):
//
//	fleet-qa-mcp                      → MCP server (stdio) for Claude Code / Cursor / VS Code
//	fleet-qa-mcp <subcommand> [args]  → deterministic CLI for scripting / CI
//
// Setup modes:
//
//	fleet-qa-mcp --install-browsers   download the Playwright Chromium driver
//	fleet-qa-mcp --auth               write a reusable browser session from the admin token
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Brajim20/fleet-qa-mcp/internal/browser"
	"github.com/Brajim20/fleet-qa-mcp/internal/fleetcfg"
	"github.com/Brajim20/fleet-qa-mcp/internal/httpapi"
	"github.com/Brajim20/fleet-qa-mcp/internal/qa"

	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/pflag"
)

const version = "0.1.0"

func main() {
	// Load .env (best-effort) so FLEET_REPO/FLEET_CONTEXT/overrides work
	// regardless of how the MCP client spawns the process.
	_ = godotenv.Load()

	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("fleet-qa-mcp", version)
		return
	}

	// HTTP/Studio mode: serve the UI + JSON API.
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		runHTTP(os.Args[2:])
		return
	}

	// CLI mode if the first arg is a known subcommand.
	if len(os.Args) > 1 && isSubcommand(os.Args[1]) {
		runCLI(os.Args[1], os.Args[2:])
		return
	}
	runServer()
}

// ---------------- HTTP / Studio mode ----------------

func runHTTP(args []string) {
	fs := pflag.NewFlagSet("serve", pflag.ExitOnError)
	ctxName := fs.String("context", ctxFromEnv(), "~/.fleet/config context")
	addr := fs.String("addr", "127.0.0.1:8799", "listen address")
	studio := fs.String("studio", defaultStudioDir(), "directory containing the Studio index.html")
	_ = fs.Parse(args)

	a, err := buildApp(*ctxName)
	must(err)

	srv := httpapi.New(a, *studio)
	fmt.Printf("Fleet QA Studio → http://%s\n", *addr)
	fmt.Printf("  instance: %s (%s)\n  studio:   %s\n", a.Inst.URL, a.Inst.Source, *studio)
	must(http.ListenAndServe(*addr, srv.Handler())) //nolint:gosec // localhost dev server
}

// defaultStudioDir locates ./studio relative to the binary or cwd, so `serve`
// works whether run from the repo root or a `go run`/installed binary.
func defaultStudioDir() string {
	for _, c := range []string{"studio", "./studio"} {
		if _, err := os.Stat(filepath.Join(c, "index.html")); err == nil {
			return c
		}
	}
	if exe, err := os.Executable(); err == nil {
		c := filepath.Join(filepath.Dir(exe), "studio")
		if _, err := os.Stat(filepath.Join(c, "index.html")); err == nil {
			return c
		}
	}
	return "studio"
}

// buildApp resolves the instance + Fleet source repo once.
func buildApp(ctxName string) (*qa.App, error) {
	inst, err := fleetcfg.Resolve(ctxName)
	if err != nil {
		return nil, err
	}
	repo, rerr := fleetcfg.ResolveRepo()
	if rerr != nil {
		fmt.Fprintf(os.Stderr, "warning: code tools disabled (%v)\n", rerr)
	}
	return &qa.App{Inst: inst, Repo: repo}, nil
}

func ctxFromEnv() string {
	if c := os.Getenv("FLEET_CONTEXT"); c != "" {
		return c
	}
	return "default"
}

// ---------------- MCP server mode ----------------

func runServer() {
	ctxName := flag.String("fleet-context", ctxFromEnv(), "~/.fleet/config context to use")
	install := flag.Bool("install-browsers", false, "download Playwright Chromium and exit")
	doAuth := flag.Bool("auth", false, "write a reusable browser session and exit")
	provision := flag.Bool("provision-repo", false, "clone fleetdm/fleet into ./.fleet-src and exit (only if you have no Fleet checkout)")
	flag.Parse()

	if *install {
		must(browser.Install())
		fmt.Println("Playwright Chromium installed.")
		return
	}
	if *provision {
		p, err := fleetcfg.ProvisionRepo()
		must(err)
		fmt.Println("Provisioned Fleet source at", p)
		return
	}
	inst, err := fleetcfg.Resolve(*ctxName)
	must(err)
	if *doAuth {
		if inst.Token == "" {
			fatal("no admin token resolved; run `fleetctl login` or set FLEET_TOKEN")
		}
		must(browser.SaveAuthState(inst.URL, inst.Token))
		fmt.Printf("Wrote browser session for %s\n", inst.URL)
		return
	}

	a, err := buildApp(*ctxName)
	must(err)
	s := server.NewMCPServer("fleet-qa", version)
	registerMCP(s, a)
	must(server.ServeStdio(s))
}

func registerMCP(s *server.MCPServer, a *qa.App) {
	wrap := func(fn func() (string, error)) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, err := fn()
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(out), nil
		}
	}

	s.AddTool(mcp.NewTool("whoami",
		mcp.WithDescription("Resolved instance URL + deployed version/revision + Fleet source repo. Run first.")),
		func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			out, _ := a.Whoami()
			return mcp.NewToolResultText(out), nil
		})

	s.AddTool(mcp.NewTool("code_at_rev",
		mcp.WithDescription("Read a file at the DEPLOYED revision (default) — use instead of reading main."),
		mcp.WithString("path", mcp.Required()),
		mcp.WithString("rev", mcp.Description("defaults to deployed revision"))),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, bad := req(r, "path")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) { return a.CodeAtRev(p, mcp.ParseString(r, "rev", "")) })(context.Background(), r)
		})

	s.AddTool(mcp.NewTool("grep_code",
		mcp.WithDescription("git grep at the deployed revision."),
		mcp.WithString("pattern", mcp.Required()),
		mcp.WithString("pathspec", mcp.DefaultString(".")),
		mcp.WithString("rev")),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, bad := req(r, "pattern")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) {
				return a.GrepCode(p, mcp.ParseString(r, "pathspec", "."), mcp.ParseString(r, "rev", ""))
			})(context.Background(), r)
		})

	s.AddTool(mcp.NewTool("is_in_build",
		mcp.WithDescription("Is a commit/PR-merge/cherry-pick in the deployed build? (merge-base --is-ancestor)."),
		mcp.WithString("commit", mcp.Required())),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, bad := req(r, "commit")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) { return a.IsInBuild(c) })(context.Background(), r)
		})

	s.AddTool(mcp.NewTool("log_search",
		mcp.WithDescription("Find commits whose diff added/removed a string (which PR introduced it)."),
		mcp.WithString("needle", mcp.Required()),
		mcp.WithString("ref", mcp.DefaultString("origin/main")),
		mcp.WithString("pathspec", mcp.DefaultString(""))),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			n, bad := req(r, "needle")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) {
				return a.LogSearch(n, mcp.ParseString(r, "ref", "origin/main"), mcp.ParseString(r, "pathspec", ""))
			})(context.Background(), r)
		})

	s.AddTool(mcp.NewTool("fleet_request",
		mcp.WithDescription("Authenticated REST call. Read-only unless confirm=true."),
		mcp.WithString("method", mcp.DefaultString("GET")),
		mcp.WithString("path", mcp.Required()),
		mcp.WithString("body"),
		mcp.WithBoolean("confirm")),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p, bad := req(r, "path")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) {
				return a.FleetRequest(mcp.ParseString(r, "method", "GET"), p, mcp.ParseString(r, "body", ""), mcp.ParseBoolean(r, "confirm", false))
			})(context.Background(), r)
		})

	s.AddTool(mcp.NewTool("browser_eval",
		mcp.WithDescription("Open a URL in real Chromium with the stored session, run JS, return JSON. For repros/DOM measurement."),
		mcp.WithString("url", mcp.Required()),
		mcp.WithString("js", mcp.Required()),
		mcp.WithString("screenshot"),
		mcp.WithString("shot_selector", mcp.Description("CSS selector of the buggy element; the screenshot scrolls to it and outlines/crops it so the image shows the actual bug (not just the viewport)")),
		mcp.WithBoolean("full_page", mcp.Description("capture the whole scrollable page instead of just the viewport")),
		mcp.WithBoolean("shot_highlight", mcp.Description("with shot_selector: outline the element and capture the viewport (bug in context) instead of cropping to it"))),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u, bad := req(r, "url")
			if bad != nil {
				return bad, nil
			}
			j, bad := req(r, "js")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) {
				return a.BrowserEval(u, j, mcp.ParseString(r, "screenshot", ""), qa.ShotOpts{
					Selector:  mcp.ParseString(r, "shot_selector", ""),
					FullPage:  mcp.ParseBoolean(r, "full_page", false),
					Highlight: mcp.ParseBoolean(r, "shot_highlight", false),
				})
			})(context.Background(), r)
		})

	s.AddTool(mcp.NewTool("browser_sample_frames",
		mcp.WithDescription("Per-frame sampler for timing/visual bugs (flashes, theme desync). Records computed-style props of selectors every frame for duration_ms, optionally after a JS trigger; returns a collapsed transition log."),
		mcp.WithString("url", mcp.Required()),
		mcp.WithString("selectors", mcp.Required(), mcp.Description("comma-separated CSS selectors")),
		mcp.WithString("props", mcp.DefaultString("background-color,color"), mcp.Description("comma-separated computed CSS props (kebab-case), or 'text'")),
		mcp.WithNumber("duration_ms", mcp.DefaultNumber(1500)),
		mcp.WithString("trigger", mcp.Description("JS run after the baseline frame to start a transition, e.g. a click or theme toggle"))),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u, bad := req(r, "url")
			if bad != nil {
				return bad, nil
			}
			sel, bad := req(r, "selectors")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) {
				return a.SampleFrames(u, splitCSV(sel), splitCSV(mcp.ParseString(r, "props", "background-color,color")),
					mcp.ParseInt(r, "duration_ms", 1500), mcp.ParseString(r, "trigger", ""))
			})(context.Background(), r)
		})

	s.AddTool(mcp.NewTool("build_issue_url",
		mcp.WithDescription("Build a PREFILLED GitHub issue URL (Fleet bug-report template). Never submits."),
		mcp.WithString("title", mcp.Required()),
		mcp.WithString("actual", mcp.Required()),
		mcp.WithString("steps", mcp.Required()),
		mcp.WithString("discovered"),
		mcp.WithString("to_fix"),
		mcp.WithString("more_info"),
		mcp.WithString("labels", mcp.Description("comma-separated extra labels"))),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			t, bad := req(r, "title")
			if bad != nil {
				return bad, nil
			}
			ac, bad := req(r, "actual")
			if bad != nil {
				return bad, nil
			}
			st, bad := req(r, "steps")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) {
				return a.BuildIssueURL(t, ac, st, mcp.ParseString(r, "discovered", ""),
					mcp.ParseString(r, "to_fix", ""), mcp.ParseString(r, "more_info", ""), splitCSV(mcp.ParseString(r, "labels", "")))
			})(context.Background(), r)
		})
}

func req(r mcp.CallToolRequest, key string) (string, *mcp.CallToolResult) {
	v := mcp.ParseString(r, key, "")
	if strings.TrimSpace(v) == "" {
		return "", mcp.NewToolResultError(fmt.Sprintf("missing required argument %q", key))
	}
	return v, nil
}

// ---------------- CLI mode ----------------

var subcommands = map[string]bool{
	"whoami": true, "code-at-rev": true, "grep": true, "is-in-build": true,
	"log-search": true, "request": true, "browser-eval": true, "sample-frames": true,
	"issue": true, "help": true,
	// workflow commands (parity with the Studio web app)
	"investigate": true, "queue": true, "smoke": true, "milestones": true, "spec": true,
}

func isSubcommand(s string) bool { return subcommands[s] }

// runCLI dispatches a subcommand. Convention: flags come BEFORE positionals.
func runCLI(name string, args []string) {
	if name == "help" {
		printCLIHelp()
		return
	}
	// pflag parses flags interspersed with positionals, so users can put
	// --flags before OR after positional args (stdlib flag can't).
	fs := pflag.NewFlagSet(name, pflag.ExitOnError)
	ctxName := fs.String("context", ctxFromEnv(), "~/.fleet/config context")

	// per-subcommand flags
	rev := fs.String("rev", "", "git revision (default: deployed)")
	pathspec := fs.String("pathspec", ".", "path filter")
	ref := fs.String("ref", "origin/main", "git ref for log-search")
	method := fs.String("method", "GET", "HTTP method")
	body := fs.String("body", "", "request body")
	confirm := fs.Bool("confirm", false, "allow non-GET writes")
	shot := fs.String("screenshot", "", "screenshot path")
	shotSel := fs.String("shot-selector", "", "browser-eval: CSS selector of the buggy element — the screenshot scrolls to it and crops/outlines it so the image shows the actual bug")
	shotFull := fs.Bool("full-page", false, "browser-eval: capture the whole scrollable page instead of just the viewport")
	shotHi := fs.Bool("shot-highlight", false, "browser-eval: with --shot-selector, outline the element and capture the viewport (bug in context) instead of cropping to it")
	selectors := fs.String("selectors", "", "comma-separated CSS selectors (sample-frames)")
	props := fs.String("props", "background-color,color", "comma-separated computed CSS props or 'text'")
	duration := fs.Int("duration", 1500, "sampling duration in ms")
	trigger := fs.String("trigger", "", "JS to fire after baseline (sample-frames)")
	title := fs.String("title", "", "issue title")
	actual := fs.String("actual", "", "actual behavior")
	steps := fs.String("steps", "", "repro steps")
	discovered := fs.String("discovered", "", "Fleet version")
	toFix := fs.String("to-fix", "", "to fix")
	moreInfo := fs.String("more-info", "", "more info")
	labels := fs.String("labels", "", "comma-separated extra labels")
	// workflow flags
	mode := fs.String("mode", "", "investigate: reproduce | testplan")
	qtype := fs.String("type", "bug", "queue: bug | story | all")
	qgroup := fs.String("group", "", "queue: product group label, e.g. #g-software")
	qmilestone := fs.String("milestone", "", "queue: milestone title or number")
	qstatus := fs.String("status", "", "queue: filter by status")
	_ = fs.Parse(args)
	pos := fs.Args()

	a, err := buildApp(*ctxName)
	must(err)

	var out string
	switch name {
	case "whoami":
		out, err = a.Whoami()
	case "code-at-rev":
		out, err = a.CodeAtRev(arg(pos, 0, "path"), *rev)
	case "grep":
		out, err = a.GrepCode(arg(pos, 0, "pattern"), *pathspec, *rev)
	case "is-in-build":
		out, err = a.IsInBuild(arg(pos, 0, "commit"))
	case "log-search":
		out, err = a.LogSearch(arg(pos, 0, "needle"), *ref, *pathspec)
	case "request":
		// Accept both `request <method> <path>` and `request <path>` (method via --method).
		if len(pos) >= 2 {
			out, err = a.FleetRequest(pos[0], pos[1], *body, *confirm)
		} else {
			out, err = a.FleetRequest(*method, arg(pos, 0, "path"), *body, *confirm)
		}
	case "browser-eval":
		out, err = a.BrowserEval(arg(pos, 0, "url"), arg(pos, 1, "js"), *shot,
			qa.ShotOpts{Selector: *shotSel, FullPage: *shotFull, Highlight: *shotHi})
	case "sample-frames":
		out, err = a.SampleFrames(arg(pos, 0, "url"), splitCSV(*selectors), splitCSV(*props), *duration, *trigger)
	case "issue":
		out, err = a.BuildIssueURL(*title, *actual, *steps, *discovered, *toFix, *moreInfo, splitCSV(*labels))
	case "investigate":
		out, err = cmdInvestigate(a, arg(pos, 0, "issue"), *mode)
	case "queue":
		out, err = cmdQueue(a, *qtype, *qgroup, *qmilestone, *qstatus)
	case "smoke":
		g := ""
		if len(pos) > 0 {
			g = pos[0]
		}
		out, err = cmdSmoke(a, g)
	case "milestones":
		out, err = cmdMilestones(a)
	case "spec":
		out, err = cmdSpec(a, arg(pos, 0, "issue"))
	}
	if err != nil {
		fatal(err.Error())
	}
	fmt.Println(out)
}

func printCLIHelp() {
	fmt.Print(`fleet-qa-mcp — Fleet QA toolkit

  (no subcommand)                start the MCP server (for Claude Code / IDEs)
  whoami                         show instance + deployed version/rev + repo
  code-at-rev [--rev R] <path>   read a file at the deployed revision
  grep [--pathspec P] <pattern>  git grep at the deployed revision
  is-in-build <commit>           is a commit/PR/cherry-pick in the deployed build?
  log-search [--ref R] <needle>  which commit introduced a string
  request [--method M] [--body B] [--confirm] <path>   authenticated REST call
  browser-eval <url> <js> [--screenshot P [--shot-selector CSS [--shot-highlight]] [--full-page]]
                                                       run JS in real Chromium; --shot-selector crops/outlines the buggy element so the image shows the actual bug
  sample-frames <url> --selectors "a,b" [--props ...] [--duration N] [--trigger JS]
                                                       per-frame sampler for timing/visual bugs
  issue --title T --actual A --steps S [--labels ...]  prefilled GitHub issue URL

Workflow (same as the Studio web app):
  investigate <issue> [--mode reproduce|testplan]      run a full investigation, print the report
  queue [--type bug|story|all] [--group #g-*] [--milestone V] [--status S]   list the QA backlog
  smoke [group]                                        run the Playwright smoke suite, print pass/fail
  milestones                                           list open release milestones
  spec <issue>                                         generate a Playwright regression test

Serve the web app:  fleet-qa-mcp serve   (→ http://127.0.0.1:8799)
Setup: --install-browsers | --auth | --provision-repo ; flags may appear anywhere.
`)
}

func arg(pos []string, i int, name string) string {
	if i >= len(pos) {
		fatal(fmt.Sprintf("missing required argument <%s>", name))
	}
	return pos[i]
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func must(err error) {
	if err != nil {
		fatal(err.Error())
	}
}
func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}
