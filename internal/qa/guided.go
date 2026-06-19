package qa

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Brajim20/fleet-qa-mcp/internal/ghissue"
)

// Guided investigations follow the ticket's OWN instructions: a bug's
// "Steps to reproduce" or a story's "Test plan". The agent (when a key is set)
// executes each step against the live build; without a key we run a best-effort
// walk — performing any API/route a step names, and listing the rest for a human.

var (
	// Steps-to-reproduce section of the bug template, up to the next heading.
	reStepsSection = regexp.MustCompile(`(?is)#+\s*[^\n]*steps to reproduce[^\n]*\n(.*?)(?:\n#+\s|\z)`)
	// Test plan section of the story template, up to the next ##-level heading.
	reTestPlanSection = regexp.MustCompile(`(?is)\n#+\s*test plan[^\n]*\n(.*?)(?:\n##\s|\z)`)
	reListMarker      = regexp.MustCompile(`^(\d+[.)]|[-*])\s+`)
	reCheckbox        = regexp.MustCompile(`^\[[ xX]\]\s*`)
)

// guidedSteps extracts the relevant step list from an issue body for the mode
// ("reproduce" → steps to reproduce, "testplan" → test plan). Sub-headings in a
// test plan become section labels prefixed to following items.
func guidedSteps(body, mode string) (label string, steps []string) {
	var re *regexp.Regexp
	if mode == "testplan" {
		re, label = reTestPlanSection, "Test plan"
	} else {
		re, label = reStepsSection, "Steps to reproduce"
	}
	m := re.FindStringSubmatch(body)
	if m == nil {
		return label, nil
	}
	section := ""
	for _, line := range strings.Split(m[1], "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "#") { // a sub-heading like "#### Core flow"
			section = strings.TrimSpace(strings.TrimLeft(t, "# "))
			continue
		}
		t = reListMarker.ReplaceAllString(t, "")
		t = reCheckbox.ReplaceAllString(t, "")
		t = strings.TrimSpace(t)
		if t == "" || t == "These steps:" || strings.HasPrefix(t, "Have been confirmed") || strings.HasPrefix(t, "Describe the workflow") {
			continue // skip the bug-template boilerplate
		}
		if section != "" {
			t = section + ": " + t
		}
		steps = append(steps, t)
	}
	return label, steps
}

// investigateGuidedHeuristic walks the ticket's own steps without an LLM: it
// performs any API call or page nav a step names (capturing evidence) and lists
// the rest as manual checks. Honest about what it could and couldn't automate.
func (a *App) investigateGuidedHeuristic(ref, mode, shotDir, shotURLBase string) *Report {
	num := parseIssueNumber(ref)
	rep := &Report{Number: num, Status: "In progress", Instance: hostOf(a.Inst.URL)}

	issue, ierr := ghissue.Fetch(num)
	if ierr != nil {
		rep.Steps = append(rep.Steps, StepResult{Kind: "issue", Title: "Fetch issue", Tool: "github.issue", Status: "error", Summary: ierr.Error()})
		rep.Title, rep.Error = fmt.Sprintf("Issue #%d", num), ierr.Error()
		return rep
	}
	rep.Title, rep.Reporter, rep.Labels, rep.Group, rep.IssueURL = issue.Title, issue.Reporter, issue.Labels, issue.ProductGroup(), issue.HTMLURL
	rep.Steps = append(rep.Steps, StepResult{
		Kind: "issue", Title: "Fetch issue", Tool: "github.issue", Status: "ok", Summary: firstLine(issue.Title),
		Detail: fmt.Sprintf("#%d · %s · reported by %s\nLabels: %s", issue.Number, issue.State, issue.Reporter, strings.Join(issue.Labels, ", ")),
	})
	rep.Steps = append(rep.Steps, a.stepTarget(rep))

	label, steps := guidedSteps(issue.Body, mode)
	if len(steps) == 0 {
		rep.Steps = append(rep.Steps, StepResult{Kind: "plan", Title: label, Tool: "parse", Status: "warn",
			Summary: "No " + strings.ToLower(label) + " section found in the ticket"})
		rep.QAComment = "No " + strings.ToLower(label) + " found to execute."
		return rep
	}

	var planBody strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&planBody, "%d. %s\n", i+1, s)
	}
	rep.Steps = append(rep.Steps, StepResult{Kind: "plan", Title: label, Tool: "parse", Status: "ok",
		Summary: fmt.Sprintf("%d steps from the ticket — performing each", len(steps)), Detail: planBody.String()})

	automated := 0
	for i, step := range steps {
		title := fmt.Sprintf("Step %d", i+1)
		if paths := extractAPIPaths(step); len(paths) > 0 {
			s := a.stepAPI(paths[0])
			s.Kind, s.Title, s.Summary = "reproduce", title, step
			s.Detail = step + "\n\n" + s.Detail
			rep.Steps = append(rep.Steps, s)
			automated++
		} else if route := reRoute.FindString(step); route != "" {
			rep.Route = route
			s := a.stepBrowserAt(route, fmt.Sprintf("step-%d", i+1), shotDir, shotURLBase)
			s.Title, s.Summary = title, step
			s.Detail = step + "\n\n" + s.Detail
			rep.Steps = append(rep.Steps, s)
			automated++
		} else {
			rep.Steps = append(rep.Steps, StepResult{Kind: "reproduce", Title: title, Tool: "manual", Status: "info",
				Summary: step, Detail: "No API/route to automate in this step — verify manually in the live browser."})
		}
	}

	rep.QAComment = fmt.Sprintf("Walked the ticket's %s against %s (Fleet %s, rev %s): %d of %d steps had an API/route to perform automatically; the rest need a human. Set the verdict from the evidence.",
		strings.ToLower(label), rep.Instance, rep.Version, short(rep.Rev), automated, len(steps))
	draft, derr := a.BuildIssueURL(issue.Title, "Guided "+strings.ToLower(label)+" run — see evidence.",
		"Ran the ticket's "+strings.ToLower(label)+" against "+rep.Instance+".", rep.Version, "", rep.QAComment, append(issue.Labels, releaseLabels(rep)...))
	if derr == nil {
		if idx := strings.Index(draft, "https://"); idx >= 0 {
			rep.DraftURL = strings.TrimSpace(draft[idx:])
		}
	}
	return rep
}

// stepBrowserAt opens a specific route and screenshots it (used by guided steps).
func (a *App) stepBrowserAt(route, shotName, shotDir, shotURLBase string) StepResult {
	pageURL := strings.TrimRight(a.Inst.URL, "/") + route
	var shotPath, shotURL string
	if shotDir != "" {
		shotPath = shotDir + "/" + shotName + ".png"
		if shotURLBase != "" {
			shotURL = shotURLBase + "/" + shotName + ".png"
		}
	}
	js := `() => ({ title: document.title, url: location.href })`
	out, err := a.BrowserEval(pageURL, js, shotPath, ShotOpts{})
	if err != nil {
		return StepResult{Kind: "reproduce", Sub: "browser", Tool: "browser.eval", Status: "warn",
			Summary: "open " + route, Detail: "Navigated to: " + pageURL + "\n\n" + err.Error()}
	}
	return StepResult{Kind: "reproduce", Sub: "browser", Tool: "browser.eval", Status: "ok",
		Summary: "opened " + route, Detail: "Navigated to: " + pageURL + "\n\n" + out, Image: shotURL}
}
