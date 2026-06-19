package main

// Higher-level CLI workflow commands — the same orchestrations the Studio web
// app exposes (investigate, queue, smoke, spec, milestones), over the shared qa
// core. For scripting / CI parity with the UI.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Brajim20/fleet-qa-mcp/internal/ghissue"
	"github.com/Brajim20/fleet-qa-mcp/internal/qa"
	"github.com/Brajim20/fleet-qa-mcp/internal/smoke"
)

func shortSHA(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// cmdInvestigate runs an investigation (mode ""|reproduce|testplan) and prints
// the report: evidence steps, released/unreleased, proposed verdict, draft URL.
func cmdInvestigate(a *qa.App, ref, mode string) (string, error) {
	rep := a.Investigate(ref, mode, "", "") // no screenshots in CLI
	var b strings.Builder
	fmt.Fprintf(&b, "#%d  %s\n", rep.Number, rep.Title)
	if rep.Instance != "" {
		fmt.Fprintf(&b, "%s · Fleet %s (rev %s, %s)\n", rep.Instance, rep.Version, shortSHA(rep.Rev), rep.Tier)
	}
	if rep.Group != "" || rep.Reporter != "" {
		fmt.Fprintf(&b, "%s · reported by %s\n", orDash(rep.Group), orDash(rep.Reporter))
	}
	b.WriteString("\nEvidence:\n")
	for _, s := range rep.Steps {
		fmt.Fprintf(&b, "  [%-5s] %-26s %s\n", s.Status, s.Title, oneline(s.Summary))
	}
	switch rep.ReleaseStatus {
	case "Released":
		fmt.Fprintf(&b, "\nClassification: RELEASED — shipped since %s (likely needs a patch).\n", rep.FirstRelease)
	case "Unreleased":
		b.WriteString("\nClassification: UNRELEASED — caught before release (~unreleased bug).\n")
	}
	fmt.Fprintf(&b, "Proposed verdict: %s  (a human confirms)\n", rep.Status)
	if rep.DraftURL != "" {
		fmt.Fprintf(&b, "\nPrefilled issue (review before submitting):\n%s\n", rep.DraftURL)
	}
	if rep.Error != "" {
		fmt.Fprintf(&b, "\nnote: %s\n", rep.Error)
	}
	return b.String(), nil
}

// cmdQueue lists the QA backlog with the same filters as the Studio queue.
func cmdQueue(a *qa.App, typ, group, milestone, status string) (string, error) {
	var parts []string
	switch typ {
	case "", "bug":
		parts = append(parts, "bug")
	case "story":
		parts = append(parts, "story")
	case "all":
		// no type label
	default:
		parts = append(parts, typ)
	}
	if group != "" {
		parts = append(parts, group)
	}
	ms, err := resolveMilestone(milestone)
	if err != nil {
		return "", err
	}
	issues, err := ghissue.List(strings.Join(parts, ","), ms, 50)
	if err != nil {
		return "", err
	}
	nums := make([]int, len(issues))
	for i, is := range issues {
		nums[i] = is.Number
	}
	board := ghissue.ProjectStatuses(nums, group)

	var b strings.Builder
	n := 0
	for _, i := range issues {
		st := board[i.Number]
		if st == "" {
			st = ghissue.WorkflowStatus(i.Labels)
		}
		if status != "" && !strings.EqualFold(st, status) {
			continue
		}
		ms := ""
		if i.Milestone != "" {
			ms = " · " + i.Milestone
		}
		fmt.Fprintf(&b, "  #%-6d %-18s %-18s %s%s\n", i.Number, oneline(st), orDash(i.ProductGroup()), oneline(i.Title), ms)
		n++
	}
	if n == 0 {
		return "(no issues match this filter)", nil
	}
	return fmt.Sprintf("%d issue(s):\n%s", n, b.String()), nil
}

// resolveMilestone turns a milestone title (e.g. "4.88.0") into its number;
// passes a numeric value through; "" → no filter.
func resolveMilestone(m string) (string, error) {
	if m == "" {
		return "", nil
	}
	if _, err := strconv.Atoi(m); err == nil {
		return m, nil
	}
	list, err := ghissue.Milestones()
	if err != nil {
		return "", err
	}
	for _, ms := range list {
		if strings.EqualFold(ms.Title, m) {
			return strconv.Itoa(ms.Number), nil
		}
	}
	return "", fmt.Errorf("milestone %q not found (try `milestones`)", m)
}

// cmdSmoke runs the Playwright smoke suite (group, or all) and prints the matrix.
func cmdSmoke(a *qa.App, group string) (string, error) {
	dir := smoke.Dir(a.Repo)
	if ok, msg := smoke.Available(dir); !ok {
		return "", fmt.Errorf("%s", msg)
	}
	ctx, cancel := context.WithTimeout(context.Background(), smoke.DefaultTimeout)
	defer cancel()
	run := smoke.RunGroup(ctx, dir, group, a.Inst.URL, a.Inst.Token)
	if run.Error != "" {
		return "", fmt.Errorf("%s", run.Error)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %d passed, %d failed, %d skipped (%ds)\n\n",
		orAll(group), run.Passed, run.Failed, run.Skipped, run.Duration/1000)
	for _, r := range run.Results {
		line := fmt.Sprintf("  [%-7s] %s", r.Status, strings.TrimPrefix(r.File, "tests/smoke/"))
		if r.Error != "" {
			line += " — " + oneline(r.Error)
		}
		b.WriteString(line + "\n")
	}
	return b.String(), nil
}

// cmdMilestones lists open repo milestones.
func cmdMilestones(a *qa.App) (string, error) {
	list, err := ghissue.Milestones()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, m := range list {
		fmt.Fprintf(&b, "  #%-5d %-16s %d open\n", m.Number, m.Title, m.Open)
	}
	return b.String(), nil
}

// cmdSpec investigates an issue and prints a generated Playwright regression test.
func cmdSpec(a *qa.App, ref string) (string, error) {
	rep := a.Investigate(ref, "", "", "")
	path, content := qa.GenerateSpec(rep)
	return "# " + path + "\n\n" + content, nil
}

func oneline(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return strings.TrimSpace(s)
}
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
func orAll(s string) string {
	if s == "" {
		return "all smoke tests"
	}
	return s
}
