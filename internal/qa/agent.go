package qa

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Brajim20/fleet-qa-mcp/internal/ghissue"
	"github.com/Brajim20/fleet-qa-mcp/internal/llm"
)

const agentMaxTurns = 14

// agentSystem tells Claude to behave like a Fleet QA engineer: gather real
// evidence against the deployed build, then finish with a verdict a human
// confirms. The read-only invariant is enforced in code (the tools never expose
// writes), not just by instruction.
const agentSystem = `You are a Fleet QA engineer investigating a GitHub issue against a LIVE deployed Fleet build.

Work like a careful human QA: reproduce the report, find the root cause in the DEPLOYED source, and decide a verdict backed by evidence — never guess.

Method:
- Reproduce against the live instance: call the API the issue describes, and/or open the relevant page in a real browser.
- Root-cause in code pinned to the DEPLOYED revision (grep_code / code_at_rev) — never reason about a file you haven't read at that rev.
- If the issue or a comment names a fix PR/commit, check whether it is in the build (is_in_build).
- For a real bug, determine whether it is RELEASED or UNRELEASED: use log_search to find the commit that introduced the buggy code, then classify_release on that commit. Released bugs reached customers (patch release); unreleased were caught in time.
- Prefer the most specific identifier (a filename, snake_case symbol, API field) over generic words.

All tools are READ-ONLY and safe. You cannot and must not attempt any write.

When you have enough evidence, call submit_verdict exactly once with:
- verdict: one of "Fixed", "Confirmed bug", "Not a bug", "Cannot reproduce"
- qa_comment: a concise comment citing the concrete evidence (API status/shape, code at rev, build-check), suitable to paste on the issue.

Be efficient: a handful of well-chosen tool calls, not dozens.`

// agentTools are the read-only tools the model may call. They map 1:1 to App
// methods; arguments are validated by the methods themselves.
func agentTools() []llm.Tool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	return []llm.Tool{
		{Name: "fleet_request", Description: "GET a Fleet REST API path on the live deployed instance (read-only). e.g. /api/latest/fleet/software/titles?team_id=14",
			InputSchema: obj(map[string]any{"path": str("API path beginning with /api/")}, "path")},
		{Name: "browser_eval", Description: "Open a page on the live instance in real Chromium (authenticated) and run a JS expression; returns JSON and captures a screenshot. Use for UI repros.",
			InputSchema: obj(map[string]any{
				"path": str("page path, e.g. /software or /policies"),
				"js":   str("optional JS expression to evaluate in the page; defaults to a generic page probe"),
			}, "path")},
		{Name: "grep_code", Description: "git grep a pattern in the Fleet source at the DEPLOYED revision.",
			InputSchema: obj(map[string]any{
				"pattern":  str("regex/string to search for (prefer a specific identifier)"),
				"pathspec": str("optional path filter, e.g. frontend/ or server/"),
			}, "pattern")},
		{Name: "code_at_rev", Description: "Read a file at the DEPLOYED revision.",
			InputSchema: obj(map[string]any{"path": str("repo-relative file path")}, "path")},
		{Name: "is_in_build", Description: "Is a commit/PR-merge/cherry-pick in the deployed build? (merge-base --is-ancestor)",
			InputSchema: obj(map[string]any{"commit": str("commit SHA")}, "commit")},
		{Name: "log_search", Description: "Find which commit added/removed a string (which PR introduced it).",
			InputSchema: obj(map[string]any{
				"needle":   str("string whose introduction you want to find"),
				"pathspec": str("optional path filter"),
			}, "needle")},
		{Name: "classify_release", Description: "Is a commit shipped in a stable Fleet release? Returns Released (+earliest fleet-v* tag) or Unreleased. Use the introducing commit to classify a bug as released vs unreleased.",
			InputSchema: obj(map[string]any{"commit": str("commit SHA (e.g. from log_search)")}, "commit")},
		{Name: "submit_verdict", Description: "Finish the investigation with a verdict and a QA comment citing the evidence.",
			InputSchema: obj(map[string]any{
				"verdict":    map[string]any{"type": "string", "enum": []string{"Fixed", "Confirmed bug", "Not a bug", "Cannot reproduce"}},
				"qa_comment": str("concise, evidence-backed comment to paste on the issue"),
			}, "verdict", "qa_comment")},
	}
}

// investigateAgentic runs the LLM-driven investigation. Claude chooses which
// tools to call; each call is recorded as a timeline step so the UI shows
// exactly what it did. Returns an error only on a hard failure (e.g. the model
// never finished) so the caller can fall back to the heuristic engine.
func (a *App) investigateAgentic(c *llm.Client, ref, shotDir, shotURLBase string) (*Report, error) {
	num := parseIssueNumber(ref)
	rep := &Report{Number: num, Status: "In progress", Instance: hostOf(a.Inst.URL)}

	issue, ierr := ghissue.Fetch(num)
	if ierr != nil {
		return nil, ierr
	}
	rep.Title = issue.Title
	rep.Reporter = issue.Reporter
	rep.Labels = issue.Labels
	rep.Group = issue.ProductGroup()
	rep.IssueURL = issue.HTMLURL
	rep.Steps = append(rep.Steps, StepResult{
		Kind: "issue", Title: "Fetch issue", Tool: "github.issue", Status: "ok",
		Summary: firstLine(issue.Title),
		Detail:  fmt.Sprintf("#%d · %s · reported by %s\nLabels: %s\n\n%s", issue.Number, issue.State, issue.Reporter, strings.Join(issue.Labels, ", "), clip(issue.Body, 1500)),
	})
	rep.Steps = append(rep.Steps, a.stepTarget(rep)) // resolve target (version/rev/tier)

	verdict, qaComment := "", ""
	shotN := 0
	dispatch := func(name string, input json.RawMessage) (string, error) {
		switch name {
		case "submit_verdict":
			var in struct {
				Verdict   string `json:"verdict"`
				QAComment string `json:"qa_comment"`
			}
			_ = json.Unmarshal(input, &in)
			verdict, qaComment = in.Verdict, in.QAComment
			return "recorded", nil
		case "fleet_request":
			var in struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(input, &in)
			out, err := a.FleetRequest("GET", in.Path, "", false) // GET-only: writes can't be requested
			a.recordStep(rep, "reproduce", "api", "Reproduce via API", "fleet.request", "GET "+in.Path, out, err, "")
			return outOrErr(out, err), nil
		case "browser_eval":
			var in struct {
				Path string `json:"path"`
				JS   string `json:"js"`
			}
			_ = json.Unmarshal(input, &in)
			if in.JS == "" {
				in.JS = `() => ({ title: document.title, url: location.href })`
			}
			shotN++
			var shotPath, shotURL string
			if shotDir != "" {
				shotPath = filepath.Join(shotDir, fmt.Sprintf("agent-%d-%d.png", num, shotN))
				if shotURLBase != "" {
					shotURL = shotURLBase + "/" + fmt.Sprintf("agent-%d-%d.png", num, shotN)
				}
			}
			rep.Route = ensureSlash(in.Path)
			pageURL := strings.TrimRight(a.Inst.URL, "/") + ensureSlash(in.Path)
			out, err := a.BrowserEval(pageURL, in.JS, shotPath)
			a.recordStep(rep, "reproduce", "browser", "Reproduce in live browser", "browser.eval", "open "+in.Path, out, err, shotURL)
			return outOrErr(out, err), nil
		case "grep_code":
			var in struct {
				Pattern  string `json:"pattern"`
				Pathspec string `json:"pathspec"`
			}
			_ = json.Unmarshal(input, &in)
			if in.Pathspec == "" {
				in.Pathspec = "."
			}
			out, err := a.GrepCode(in.Pattern, in.Pathspec, "")
			a.recordStep(rep, "rootcause", "", "Root cause", "grep_code", "grep "+in.Pattern, out, err, "")
			return outOrErr(out, err), nil
		case "code_at_rev":
			var in struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(input, &in)
			out, err := a.CodeAtRev(in.Path, "")
			a.recordStep(rep, "rootcause", "", "Read source at deployed rev", "code_at_rev", in.Path, out, err, "")
			return outOrErr(out, err), nil
		case "is_in_build":
			var in struct {
				Commit string `json:"commit"`
			}
			_ = json.Unmarshal(input, &in)
			out, err := a.IsInBuild(in.Commit)
			a.recordStep(rep, "buildcheck", "", "Build check", "is_in_build", in.Commit, out, err, "")
			return outOrErr(out, err), nil
		case "log_search":
			var in struct {
				Needle   string `json:"needle"`
				Pathspec string `json:"pathspec"`
			}
			_ = json.Unmarshal(input, &in)
			out, err := a.LogSearch(in.Needle, "origin/main", in.Pathspec)
			a.recordStep(rep, "rootcause", "", "Find introducing commit", "log_search", in.Needle, out, err, "")
			return outOrErr(out, err), nil
		case "classify_release":
			var in struct {
				Commit string `json:"commit"`
			}
			_ = json.Unmarshal(input, &in)
			status, first, err := a.ClassifyRelease(in.Commit)
			out := status
			if status == "Released" {
				out = "Released (shipped since " + first + ")"
				rep.ReleaseStatus, rep.FirstRelease, rep.IntroCommit = status, first, in.Commit
			} else if status == "Unreleased" {
				out = "Unreleased (not in any fleet-v* release)"
				rep.ReleaseStatus, rep.IntroCommit = status, in.Commit
			}
			a.recordStep(rep, "release", "", "Released or unreleased?", "tag --contains", short(in.Commit), out, err, "")
			return outOrErr(out, err), nil
		}
		return "", fmt.Errorf("unknown tool %q", name)
	}

	userMsg := fmt.Sprintf("Investigate this issue:\n\nTitle: %s\nLabels: %s\nReporter: %s\nURL: %s\n\nBody:\n%s\n\nTarget: %s — Fleet %s (rev %s, %s).",
		issue.Title, strings.Join(issue.Labels, ", "), issue.Reporter, issue.HTMLURL, clip(issue.Body, 4000),
		rep.Instance, rep.Version, short(rep.Rev), rep.Tier)

	_, _, err := c.RunToolLoop(context.Background(), agentSystem,
		[]llm.Message{{Role: "user", Content: []llm.Block{{Type: "text", Text: userMsg}}}},
		agentTools(), dispatch, agentMaxTurns)
	if err != nil {
		return nil, err
	}
	if verdict == "" {
		return nil, fmt.Errorf("agent finished without a verdict")
	}

	rep.Status = verdict
	rep.QAComment = qaComment
	rep.Steps = append(rep.Steps, StepResult{
		Kind: "verdict", Title: "Verdict", Tool: "agent", Status: "ok",
		Summary: "Proposed: " + verdict + " — confirm in the panel",
		Detail:  qaComment,
	})
	draft, derr := a.BuildIssueURL(issue.Title,
		"See investigation evidence below (agent-driven, against the live deployed build).",
		"Investigated against "+rep.Instance+" — Fleet "+rep.Version+" (rev "+short(rep.Rev)+").",
		rep.Version, "", qaComment, append(issue.Labels, releaseLabels(rep)...))
	if derr == nil {
		if idx := strings.Index(draft, "https://"); idx >= 0 {
			rep.DraftURL = strings.TrimSpace(draft[idx:])
		}
	}
	rep.Steps = append(rep.Steps, StepResult{
		Kind: "draft", Title: "Draft GitHub issue", Tool: "build_issue_url", Status: "ok",
		Summary: "Prefilled bug report ready — review before submitting",
	})
	return rep, nil
}

// recordStep appends a tool call to the evidence timeline.
func (a *App) recordStep(rep *Report, kind, sub, title, tool, summary, out string, err error, image string) {
	st := StepResult{Kind: kind, Sub: sub, Title: title, Tool: tool, Summary: summary, Image: image, Status: "ok"}
	if err != nil {
		st.Status = "warn"
		st.Summary = summary + " — " + firstLine(err.Error())
		st.Detail = err.Error()
	} else {
		st.Detail = clip(out, 1800)
	}
	rep.Steps = append(rep.Steps, st)
}

func outOrErr(out string, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return clip(out, 4000)
}

func ensureSlash(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}
