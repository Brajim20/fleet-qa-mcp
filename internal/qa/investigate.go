package qa

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Brajim20/fleet-qa-mcp/internal/ghissue"
	"github.com/Brajim20/fleet-qa-mcp/internal/gitcode"
	"github.com/Brajim20/fleet-qa-mcp/internal/llm"
)

// StepResult is one row of the investigation evidence timeline. The shape
// mirrors the UI's step renderer: a titled, tool-tagged, expandable block.
type StepResult struct {
	Kind    string `json:"kind"`    // issue|target|reproduce|rootcause|buildcheck|draft
	Sub     string `json:"sub"`     // api|browser|"" — disambiguates reproduce steps
	Title   string `json:"title"`   // e.g. "Reproduce in live browser"
	Tool    string `json:"tool"`    // which underlying function ran
	Summary string `json:"summary"` // one-line headline
	Detail  string `json:"detail"`  // preformatted evidence (code/JSON/text)
	Status  string `json:"status"`  // ok|warn|error|info
	Image   string `json:"image"`   // URL of a captured screenshot, if any
}

// Report is the full result of an investigation — everything the UI renders.
type Report struct {
	Number    int          `json:"number"`
	Title     string       `json:"title"`
	Reporter  string       `json:"reporter"`
	Group     string       `json:"group"`
	Labels    []string     `json:"labels"`
	IssueURL  string       `json:"issueUrl"`
	Instance  string       `json:"instance"`
	Version   string       `json:"version"`
	Rev       string       `json:"rev"`
	Tier      string       `json:"tier"`
	Status    string       `json:"status"` // suggested verdict — human confirms in the UI
	Steps     []StepResult `json:"steps"`
	QAComment string       `json:"qaComment"`
	DraftURL  string       `json:"draftUrl"`
	Error     string       `json:"error,omitempty"`

	// Release classification (released vs unreleased), when we can trace the
	// introducing commit. Drives the prefilled-issue labels.
	ReleaseStatus string `json:"releaseStatus"` // "Released" | "Unreleased" | ""
	FirstRelease  string `json:"firstRelease"`  // earliest fleet-v* tag containing the bug, if released
	IntroCommit   string `json:"introCommit"`   // commit that introduced the buggy code

	Route string `json:"route"` // UI route reproduced against (for generated Playwright tests)
}

// Investigate runs an investigation for one issue against the live deployed
// build and returns a structured report. When an Anthropic API key is set, it
// runs the agentic path (Claude drives the tools like a human QA); otherwise —
// or if the agent errors — it falls back to the deterministic heuristic engine.
//
// shotDir is where browser screenshots are written; shotURLBase is the URL
// prefix the HTTP layer serves them under (e.g. "/shots"). Pass "" for both to
// skip screenshots.
func (a *App) Investigate(ref, shotDir, shotURLBase string) *Report {
	if c, ok := llm.NewFromEnv(); ok {
		if rep, err := a.investigateAgentic(c, ref, shotDir, shotURLBase); err == nil {
			return rep
		} else {
			// Agent failed (bad key, rate limit, no verdict) — degrade to the
			// deterministic engine rather than returning nothing.
			rep = a.investigateHeuristic(ref, shotDir, shotURLBase)
			rep.Error = strings.TrimSpace("agent fell back to heuristic engine: " + err.Error())
			return rep
		}
	}
	return a.investigateHeuristic(ref, shotDir, shotURLBase)
}

// investigateHeuristic runs the deterministic evidence pipeline: it derives the
// step inputs (API path, grep keyword, commit SHA) from the issue text with
// regex heuristics. Each step captures its own error into the report rather
// than aborting — a failed browser repro shouldn't blow away the API evidence
// that already succeeded.
func (a *App) investigateHeuristic(ref, shotDir, shotURLBase string) *Report {
	num := parseIssueNumber(ref)
	rep := &Report{Number: num, Status: "In progress", Instance: hostOf(a.Inst.URL)}

	// 1. Fetch the real issue.
	issue, ierr := ghissue.Fetch(num)
	if ierr != nil {
		rep.Steps = append(rep.Steps, StepResult{
			Kind: "issue", Title: "Fetch issue", Tool: "github.issue", Status: "error",
			Summary: ierr.Error(),
		})
		rep.Title = fmt.Sprintf("Issue #%d", num)
		rep.Error = ierr.Error()
		return rep // without the issue text there's nothing to drive the rest
	}
	rep.Title = issue.Title
	rep.Reporter = issue.Reporter
	rep.Labels = issue.Labels
	rep.Group = issue.ProductGroup()
	rep.IssueURL = issue.HTMLURL
	rep.Steps = append(rep.Steps, StepResult{
		Kind: "issue", Title: "Fetch issue", Tool: "github.issue", Status: "ok",
		Summary: firstLine(issue.Title),
		Detail:  fmt.Sprintf("#%d · %s · reported by %s\nLabels: %s\n\n%s", issue.Number, issue.State, issue.Reporter, strings.Join(issue.Labels, ", "), clip(issue.Body, 1200)),
	})

	// 2. Resolve the live target (version / rev / tier).
	rep.Steps = append(rep.Steps, a.stepTarget(rep))

	// 3a. Reproduce against the live API (first API path mentioned in the issue).
	if paths := extractAPIPaths(issue.Body); len(paths) > 0 {
		rep.Steps = append(rep.Steps, a.stepAPI(paths[0]))
	} else {
		rep.Steps = append(rep.Steps, StepResult{
			Kind: "reproduce", Sub: "api", Title: "Reproduce via API", Tool: "fleet.request", Status: "info",
			Summary: "No API path referenced in the issue — skipped",
		})
	}

	// 3b. Reproduce in a real browser against the relevant page.
	rep.Route = guessRoute(issue.Body)
	rep.Steps = append(rep.Steps, a.stepBrowser(issue, shotDir, shotURLBase))

	// 4. Root cause — grep the deployed source for an identifier from the issue.
	kw := extractKeywords(issue.Title + "\n" + issue.Body)
	rep.Steps = append(rep.Steps, a.stepRootCause(issue))

	// 5. Released or unreleased? Trace the introducing commit for the top
	// identifier and check whether it shipped in a stable release.
	topSymbol := ""
	if len(kw) > 0 {
		topSymbol = kw[0]
	}
	rep.Steps = append(rep.Steps, a.stepRelease(rep, topSymbol, "."))

	// 6. Build check — is any referenced commit in the deployed build?
	rep.Steps = append(rep.Steps, a.stepBuildCheck(issue))

	// 7. Draft a prefilled issue URL from the gathered evidence.
	rep.QAComment = buildQAComment(rep)
	draft, derr := a.BuildIssueURL(
		issue.Title,
		"See investigation evidence below (live API + browser + source at deployed rev).",
		"Investigated against "+rep.Instance+" — Fleet "+rep.Version+" (rev "+short(rep.Rev)+").",
		rep.Version, "", rep.QAComment, append(issue.Labels, releaseLabels(rep)...))
	if derr == nil {
		// BuildIssueURL prefixes a human note; keep just the URL for the UI.
		if idx := strings.Index(draft, "https://"); idx >= 0 {
			rep.DraftURL = strings.TrimSpace(draft[idx:])
		}
	}
	rep.Steps = append(rep.Steps, StepResult{
		Kind: "draft", Title: "Draft GitHub issue", Tool: "build_issue_url", Status: "ok",
		Summary: "Prefilled bug report ready — review before submitting",
	})
	return rep
}

func (a *App) stepTarget(rep *Report) StepResult {
	v, err := a.Inst.DeployedVersion()
	if err != nil {
		return StepResult{Kind: "target", Title: "Resolve target", Tool: "fleet.version", Status: "error", Summary: err.Error()}
	}
	rep.Version = v.Version
	rep.Rev = v.Revision
	rep.Tier = a.LicenseTier()
	tier := rep.Tier
	if tier == "" {
		tier = "unknown"
	}
	return StepResult{
		Kind: "target", Title: "Resolve target", Tool: "fleet.version", Status: "ok",
		Summary: fmt.Sprintf("%s · %s · rev %s", rep.Instance, shortVer(v.Version), short(v.Revision)),
		Detail:  fmt.Sprintf("Instance: %s\nDeployed version: %s\nGit revision: %s\nBranch: %s\nTier: %s\n\nAll API reads + code lookups below are pinned to this exact deployed revision.", rep.Instance, v.Version, v.Revision, v.Branch, tier),
	}
}

func (a *App) stepAPI(path string) StepResult {
	out, status, err := a.Inst.Request("GET", path, nil)
	if err != nil {
		return StepResult{Kind: "reproduce", Sub: "api", Title: "Reproduce via API", Tool: "fleet.request", Status: "error", Summary: "GET " + path + " — " + err.Error()}
	}
	st := "ok"
	if status >= 400 {
		st = "warn"
	}
	return StepResult{
		Kind: "reproduce", Sub: "api", Title: "Reproduce via API", Tool: "fleet.request", Status: st,
		Summary: fmt.Sprintf("GET %s → HTTP %d", path, status),
		Detail:  fmt.Sprintf("GET %s\nHTTP %d\n\n%s", path, status, prettyJSON(out, 1600)),
	}
}

func (a *App) stepBrowser(issue *ghissue.Issue, shotDir, shotURLBase string) StepResult {
	route := guessRoute(issue.Body)
	pageURL := strings.TrimRight(a.Inst.URL, "/") + route
	var shotPath string
	if shotDir != "" {
		shotPath = filepath.Join(shotDir, fmt.Sprintf("issue-%d.png", issue.Number))
	}
	js := `() => ({ title: document.title, url: location.href, hasErrorBanner: !!document.querySelector('[class*="flash"],[class*="error"],[role="alert"]') })`
	out, err := a.BrowserEval(pageURL, js, shotPath)
	if err != nil {
		return StepResult{
			Kind: "reproduce", Sub: "browser", Title: "Reproduce in live browser", Tool: "browser.eval", Status: "warn",
			Summary: "Browser repro unavailable — " + firstLine(err.Error()),
			Detail:  "Navigated to: " + pageURL + "\n\n" + err.Error() + "\n\n(Run `fleet-qa-mcp --install-browsers` and `--auth` to enable live browser repros.)",
		}
	}
	step := StepResult{
		Kind: "reproduce", Sub: "browser", Title: "Reproduce in live browser", Tool: "browser.eval", Status: "ok",
		Summary: "Loaded " + route + " in real Chromium",
		Detail:  "Navigated to: " + pageURL + "\n\n" + out,
	}
	if shotDir != "" && shotURLBase != "" {
		step.Image = shotURLBase + "/" + fmt.Sprintf("issue-%d.png", issue.Number)
	}
	return step
}

func (a *App) stepRootCause(issue *ghissue.Issue) StepResult {
	kw := extractKeywords(issue.Title + "\n" + issue.Body)
	if len(kw) == 0 {
		return StepResult{Kind: "rootcause", Title: "Root cause", Tool: "grep_code", Status: "info", Summary: "No distinctive identifier to grep"}
	}
	out, err := a.GrepCode(kw[0], ".", "")
	if err != nil {
		return StepResult{
			Kind: "rootcause", Title: "Root cause", Tool: "grep_code", Status: "info",
			Summary: "Code search unavailable — " + firstLine(err.Error()),
			Detail:  "Tried: grep \"" + kw[0] + "\" at deployed rev\n\n" + err.Error(),
		}
	}
	hits := strings.Count(out, "\n")
	return StepResult{
		Kind: "rootcause", Title: "Root cause", Tool: "grep_code", Status: "ok",
		Summary: fmt.Sprintf("grep %q at deployed rev — %d matches", kw[0], hits),
		Detail:  "Candidate identifiers: " + strings.Join(kw, ", ") + "\n\n" + clip(out, 1600),
	}
}

func (a *App) stepBuildCheck(issue *ghissue.Issue) StepResult {
	shas := extractSHAs(issue.Body)
	// Only check SHAs that resolve to a real object in the checkout — most hex
	// runs in an issue body are UUIDs/IDs, not commits.
	var sha string
	for _, c := range shas {
		if a.Repo != "" && gitcode.HasRev(a.Repo, c) {
			sha = c
			break
		}
	}
	if sha == "" {
		return StepResult{Kind: "buildcheck", Title: "Build check", Tool: "is_in_build", Status: "info", Summary: "No referenced commit resolves in the deployed checkout"}
	}
	out, err := a.IsInBuild(sha)
	if err != nil {
		return StepResult{Kind: "buildcheck", Title: "Build check", Tool: "is_in_build", Status: "info", Summary: "Build check unavailable — " + firstLine(err.Error())}
	}
	st := "ok"
	if strings.Contains(out, "false") {
		st = "warn"
	}
	return StepResult{Kind: "buildcheck", Title: "Build check", Tool: "is_in_build", Status: st, Summary: out}
}

// LicenseTier reads license.tier from the live config (premium/free), or "".
func (a *App) LicenseTier() string {
	out, status, err := a.Inst.Request("GET", "/api/latest/fleet/config", nil)
	if err != nil || status != 200 {
		return ""
	}
	var c struct {
		License struct {
			Tier string `json:"tier"`
		} `json:"license"`
	}
	if json.Unmarshal(out, &c) == nil {
		return c.License.Tier
	}
	return ""
}

func buildQAComment(rep *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Investigated against %s — Fleet %s (rev %s, %s).\n\nEvidence:\n", rep.Instance, rep.Version, short(rep.Rev), titleCase(rep.Tier))
	for _, s := range rep.Steps {
		if s.Kind == "issue" || s.Kind == "draft" {
			continue
		}
		fmt.Fprintf(&b, "• %s: %s\n", s.Title, s.Summary)
	}
	if rep.ReleaseStatus == "Released" {
		fmt.Fprintf(&b, "\nClassification: RELEASED — shipped since %s. Likely needs a patch release.\n", rep.FirstRelease)
	} else if rep.ReleaseStatus == "Unreleased" {
		fmt.Fprintf(&b, "\nClassification: UNRELEASED — caught before release (~unreleased bug).\n")
	}
	return strings.TrimSpace(b.String())
}

// --- extraction heuristics ---------------------------------------------------

var (
	reIssueNum  = regexp.MustCompile(`(\d{3,7})`)
	reAPIPath   = regexp.MustCompile(`/api/[A-Za-z0-9/_.\-{}]+(?:\?[A-Za-z0-9/_.\-=&{}]+)?`)
	reSHA       = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	reFilename  = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*\.(?:tsx|ts|jsx|js|go)`)
	reSnakeCase = regexp.MustCompile(`[a-z][a-z0-9]*(?:_[a-z0-9]+)+`)
	reCamelCase = regexp.MustCompile(`[a-z]+[A-Z][A-Za-z0-9]+`)
	reRoute     = regexp.MustCompile(`/(dashboard|hosts|software|controls|policies|queries|reports|settings|profile|labels)\b`)
)

func parseIssueNumber(ref string) int {
	ref = strings.TrimSpace(ref)
	// If a full issue URL, prefer the trailing number.
	if m := reIssueNum.FindAllString(ref, -1); len(m) > 0 {
		n, _ := strconv.Atoi(m[len(m)-1])
		return n
	}
	return 0
}

func extractAPIPaths(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reAPIPath.FindAllString(body, -1) {
		m = strings.TrimRight(m, ".,)`\"'")
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func extractSHAs(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range reSHA.FindAllString(body, -1) {
		// Require a mix of letters and digits to avoid matching plain numbers
		// and pure-decimal version fragments.
		if !(strings.ContainsAny(m, "abcdef") && strings.ContainsAny(m, "0123456789")) {
			continue
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "when": true, "from": true,
	"that": true, "this": true, "fleet": true, "page": true, "shows": true, "should": true,
	"after": true, "before": true, "https": true, "http": true, "issue": true, "error": true,
	"errors": true, "displays": true, "displayed": true, "button": true, "click": true,
}

// extractKeywords returns distinctive code identifiers to grep, most-specific
// first. A plain English word like "returns" matches thousands of lines and is
// useless for root-cause; a filename, snake_case symbol, or camelCase name
// points at the actual code. We score by that specificity.
func extractKeywords(text string) []string {
	type cand struct {
		tok   string
		score int
	}
	seen := map[string]bool{}
	var cands []cand
	add := func(tok string, score int) {
		tok = strings.Trim(tok, "._-")
		low := strings.ToLower(tok)
		if len(tok) < 5 || seen[low] || stopwords[low] {
			return
		}
		seen[low] = true
		cands = append(cands, cand{tok, score})
	}
	// Filenames are the strongest signal (e.g. SoftwareOptionsSelector.tsx).
	for _, m := range reFilename.FindAllString(text, -1) {
		add(strings.TrimSuffix(m, filepath.Ext(m)), 5)
	}
	for _, m := range reSnakeCase.FindAllString(text, -1) { // self_service_categories
		add(m, 4)
	}
	for _, m := range reCamelCase.FindAllString(text, -1) { // showEmptyState
		add(m, 3)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var out []string
	for _, c := range cands {
		out = append(out, c.tok)
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func guessRoute(body string) string {
	if m := reRoute.FindString(body); m != "" {
		return m
	}
	return "/dashboard"
}

// --- small formatters --------------------------------------------------------

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	return strings.TrimRight(u, "/")
}

func shortVer(v string) string {
	if i := strings.Index(v, "-rc"); i >= 0 {
		return v[:i+3]
	}
	return v
}

func titleCase(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "\n… (truncated)"
	}
	return s
}

func prettyJSON(b []byte, max int) string {
	var v interface{}
	if json.Unmarshal(b, &v) == nil {
		if pretty, err := json.MarshalIndent(v, "", "  "); err == nil {
			return clip(string(pretty), max)
		}
	}
	return clip(string(b), max)
}
