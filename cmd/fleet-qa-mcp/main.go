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
	"os"
	"strings"

	"github.com/fleetdm/fleet-qa-mcp/internal/browser"
	"github.com/fleetdm/fleet-qa-mcp/internal/fleetcfg"
	"github.com/fleetdm/fleet-qa-mcp/internal/qa"

	"github.com/joho/godotenv"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	// Load .env (best-effort) so FLEET_REPO/FLEET_CONTEXT/overrides work
	// regardless of how the MCP client spawns the process.
	_ = godotenv.Load()

	// CLI mode if the first arg is a known subcommand.
	if len(os.Args) > 1 && isSubcommand(os.Args[1]) {
		runCLI(os.Args[1], os.Args[2:])
		return
	}
	runServer()
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
	s := server.NewMCPServer("fleet-qa", "0.1.0")
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
		mcp.WithString("screenshot")),
		func(_ context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			u, bad := req(r, "url")
			if bad != nil {
				return bad, nil
			}
			j, bad := req(r, "js")
			if bad != nil {
				return bad, nil
			}
			return wrap(func() (string, error) { return a.BrowserEval(u, j, mcp.ParseString(r, "screenshot", "")) })(context.Background(), r)
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
	"log-search": true, "request": true, "browser-eval": true, "issue": true, "help": true,
}

func isSubcommand(s string) bool { return subcommands[s] }

// runCLI dispatches a subcommand. Convention: flags come BEFORE positionals.
func runCLI(name string, args []string) {
	if name == "help" {
		printCLIHelp()
		return
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	ctxName := fs.String("context", ctxFromEnv(), "~/.fleet/config context")

	// per-subcommand flags
	rev := fs.String("rev", "", "git revision (default: deployed)")
	pathspec := fs.String("pathspec", ".", "path filter")
	ref := fs.String("ref", "origin/main", "git ref for log-search")
	method := fs.String("method", "GET", "HTTP method")
	body := fs.String("body", "", "request body")
	confirm := fs.Bool("confirm", false, "allow non-GET writes")
	shot := fs.String("screenshot", "", "screenshot path")
	title := fs.String("title", "", "issue title")
	actual := fs.String("actual", "", "actual behavior")
	steps := fs.String("steps", "", "repro steps")
	discovered := fs.String("discovered", "", "Fleet version")
	toFix := fs.String("to-fix", "", "to fix")
	moreInfo := fs.String("more-info", "", "more info")
	labels := fs.String("labels", "", "comma-separated extra labels")
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
		out, err = a.FleetRequest(*method, arg(pos, 0, "path"), *body, *confirm)
	case "browser-eval":
		out, err = a.BrowserEval(arg(pos, 0, "url"), arg(pos, 1, "js"), *shot)
	case "issue":
		out, err = a.BuildIssueURL(*title, *actual, *steps, *discovered, *toFix, *moreInfo, splitCSV(*labels))
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
  browser-eval <url> <js> [--screenshot P]             run JS in real Chromium
  issue --title T --actual A --steps S [--labels ...]  prefilled GitHub issue URL

Setup: --install-browsers | --auth ; flags come before positionals.
`)
}

func arg(pos []string, i int, name string) string {
	if i >= len(pos) {
		fatal(fmt.Sprintf("missing required argument <%s> (flags must precede positionals)", name))
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
