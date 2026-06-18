// Package qa is the shared core behind both front-ends (MCP server + CLI).
// Each method returns a formatted string + error; the front-ends only handle
// argument parsing and output. This is the "one core, multiple front-ends"
// boundary — keep MCP/CLI specifics out of here.
package qa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fleetdm/fleet-qa-mcp/internal/browser"
	"github.com/fleetdm/fleet-qa-mcp/internal/fleetcfg"
	"github.com/fleetdm/fleet-qa-mcp/internal/ghissue"
	"github.com/fleetdm/fleet-qa-mcp/internal/gitcode"
)

// App holds the resolved instance + Fleet source repo.
type App struct {
	Inst *fleetcfg.Instance
	Repo string
}

// ResolveRev returns the explicit rev, or the deployed build's revision after a
// fetch. The returned rev may be non-empty WITH an error (e.g. rev not in the
// local checkout) so callers can warn but proceed.
func (a *App) ResolveRev(explicit string) (string, error) {
	if a.Repo == "" {
		return "", fmt.Errorf("no Fleet source repo (set FLEET_REPO or run --provision-repo)")
	}
	// Already-local explicit rev → no network fetch needed.
	if explicit != "" {
		if gitcode.HasRev(a.Repo, explicit) {
			return explicit, nil
		}
		_ = gitcode.Fetch(a.Repo)
		return explicit, nil
	}
	_ = gitcode.Fetch(a.Repo) // resolving the deployed rev — make sure it's reachable
	v, err := a.Inst.DeployedVersion()
	if err != nil {
		return "", err
	}
	if !gitcode.HasRev(a.Repo, v.Revision) {
		return v.Revision, fmt.Errorf("deployed rev %s not in this checkout (locally-built/unpushed? point FLEET_REPO at the build tree)", short(v.Revision))
	}
	return v.Revision, nil
}

func (a *App) Whoami() (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "instance: %s (from %s)\n", a.Inst.URL, a.Inst.Source)
	if v, err := a.Inst.DeployedVersion(); err != nil {
		fmt.Fprintf(&b, "version:  <error: %v>\n", err)
	} else {
		fmt.Fprintf(&b, "version:  %s\nbranch:   %s\nrevision: %s\n", v.Version, v.Branch, v.Revision)
	}
	fmt.Fprintf(&b, "repo:     %s\n", orNone(a.Repo))
	return b.String(), nil
}

func (a *App) CodeAtRev(path, revArg string) (string, error) {
	rev, rerr := a.ResolveRev(revArg)
	if rev == "" || rerr != nil {
		return "", rerr // can't operate on a rev that isn't in the local object store
	}
	out, err := gitcode.ShowAtRev(a.Repo, rev, path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# %s @ %s\n%s", path, short(rev), out), nil
}

func (a *App) GrepCode(pattern, pathspec, revArg string) (string, error) {
	rev, rerr := a.ResolveRev(revArg)
	if rev == "" || rerr != nil {
		return "", rerr // can't operate on a rev that isn't in the local object store
	}
	out, err := gitcode.GrepAtRev(a.Repo, rev, pattern, pathspec)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "(no matches)", nil
	}
	return out, nil
}

func (a *App) IsInBuild(commit string) (string, error) {
	rev, rerr := a.ResolveRev("")
	if rev == "" || rerr != nil {
		return "", rerr // can't operate on a rev that isn't in the local object store
	}
	in, err := gitcode.IsAncestor(a.Repo, commit, rev)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s in deployed build %s: %v", short(commit), short(rev), in), nil
}

func (a *App) LogSearch(needle, ref, pathspec string) (string, error) {
	out, err := gitcode.LogSearch(a.Repo, ref, needle, pathspec)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "(no commits matched)", nil
	}
	return out, nil
}

func (a *App) FleetRequest(method, path, body string, confirm bool) (string, error) {
	method = strings.ToUpper(method)
	if method != "GET" && !confirm {
		return "", fmt.Errorf("non-GET request requires confirm=true (safety: writes are gated)")
	}
	out, status, err := a.Inst.Request(method, path, bytes.NewReader([]byte(body)))
	if err != nil {
		return "", err
	}
	hint := ""
	if status == 401 {
		hint = "  (401 — admin token expired; run `fleet-qa --auth` or `fleetctl login`)"
	}
	return fmt.Sprintf("[HTTP %d]%s\n%s", status, hint, string(out)), nil
}

func (a *App) BrowserEval(pageURL, js, screenshot string) (string, error) {
	sess, err := browser.Open(a.Inst.URL, pageURL)
	if err != nil {
		return "", err
	}
	defer sess.Close()
	val, err := sess.Eval(js)
	if err != nil {
		return "", err
	}
	note := ""
	if screenshot != "" {
		if p, serr := sess.Screenshot(screenshot); serr == nil {
			note = "\n(screenshot: " + p + ")"
		} else {
			note = "\n(screenshot FAILED: " + serr.Error() + ")" // don't swallow it
		}
	}
	b, _ := json.MarshalIndent(val, "", "  ")
	return string(b) + note, nil
}

func (a *App) BuildIssueURL(title, actual, steps, discovered, toFix, moreInfo string, labels []string) (string, error) {
	if discovered == "" {
		if v, err := a.Inst.DeployedVersion(); err == nil {
			discovered = v.Version
		}
	}
	br := ghissue.BugReport{
		Title: title, Actual: actual, Steps: steps,
		Discovered: discovered, Reproduced: discovered,
		BrowserOS: "Chromium / macOS",
		ToFix:     toFix, MoreInfo: moreInfo, Labels: labels,
	}
	return "Prefilled issue (review before submitting):\n" + br.URL(), nil
}

func short(s string) string {
	if len(s) >= 40 { // full SHA → abbreviate; leave branch names/short refs intact
		return s[:10]
	}
	return s
}
func orNone(s string) string {
	if s == "" {
		return "<none — set FLEET_REPO>"
	}
	return s
}
