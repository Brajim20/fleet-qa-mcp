package smoke

// Test-plan extraction: read the smoke spec SOURCE (never executes it) and turn
// each test into a human-readable, step-by-step outline — the describe + test
// title, the file's top-of-file summary comment, and the ordered Playwright
// actions/assertions the test performs. This answers "what does this test
// actually do?" without running it or reading TypeScript.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// TestPlan is one test's extracted outline.
type TestPlan struct {
	File        string   `json:"file"`        // path relative to tests/smoke
	FileSummary string   `json:"fileSummary"` // top-of-file JSDoc, condensed
	Describe    string   `json:"describe"`    // enclosing test.describe(...) title
	Title       string   `json:"title"`       // the test(...) title (raw, may contain ${})
	Steps       []string `json:"steps"`       // ordered, humanized actions/assertions
}

var (
	reDescribe = regexp.MustCompile(`test\.describe\(\s*["'` + "`" + `]([^"'` + "`" + `]+)`)
	// Match real tests (test(...), test.only/skip/fixme), NOT test.describe / hooks.
	reTest = regexp.MustCompile(`(?m)^\s*test(?:\.(?:only|skip|fixme))?\(\s*[` + "`" + `"']([^` + "`" + `"']+)`)
	reGoto = regexp.MustCompile(`goto\(\s*[` + "`" + `"']([^` + "`" + `"']+)`)
	reName = regexp.MustCompile(`name:\s*[` + "`" + `"']?/?([^,"'` + "`" + `})/]+)`)
	reArg  = regexp.MustCompile(`[` + "`" + `"']([^` + "`" + `"']+)`)
	// Lines that represent a meaningful step.
	reAction = regexp.MustCompile(`\b(goto|getByRole|getByText|getByLabel|getByPlaceholder|getByTestId|click|dblclick|fill|type|selectOption|hover|press|check|uncheck|setInputFiles|waitForURL|waitForLoadState|waitForEvent|waitForResponse|expect|reload|goBack|test\.skip)\b`)
)

// Plan reads the spec files for a group (""=all, or a "group" dir, or a
// "group/file.spec.ts") under dir/tests/smoke and returns one TestPlan per test.
func Plan(dir, group string) ([]TestPlan, error) {
	base := filepath.Join(dir, "tests", "smoke")
	var files []string
	target := base
	if group != "" {
		target = filepath.Join(base, group)
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		_ = filepath.WalkDir(target, func(p string, d os.DirEntry, _ error) error {
			if d != nil && !d.IsDir() && strings.HasSuffix(p, ".spec.ts") {
				files = append(files, p)
			}
			return nil
		})
	} else {
		files = []string{target}
	}
	sort.Strings(files)

	var plans []TestPlan
	for _, f := range files {
		src, rerr := os.ReadFile(f)
		if rerr != nil {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(f, base), string(os.PathSeparator))
		summary := fileSummary(string(src))
		describe := ""
		if m := reDescribe.FindStringSubmatch(string(src)); m != nil {
			describe = m[1]
		}
		for _, tb := range splitTests(string(src)) {
			plans = append(plans, TestPlan{
				File:        rel,
				FileSummary: summary,
				Describe:    describe,
				Title:       tb.title,
				Steps:       steps(tb.body),
			})
		}
	}
	return plans, nil
}

type testBlock struct {
	title string
	body  string
}

// splitTests finds each test(...) and takes its body as everything up to the
// next test(...) (or EOF). Approximate but good enough for an outline.
func splitTests(src string) []testBlock {
	locs := reTest.FindAllStringSubmatchIndex(src, -1)
	var out []testBlock
	for i, m := range locs {
		title := src[m[2]:m[3]]
		start := m[1]
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out = append(out, testBlock{title: title, body: src[start:end]})
	}
	return out
}

// fileSummary condenses the first /** ... */ block into a one/two-line summary.
func fileSummary(src string) string {
	i := strings.Index(src, "/**")
	if i < 0 {
		return ""
	}
	j := strings.Index(src[i:], "*/")
	if j < 0 {
		return ""
	}
	block := src[i+3 : i+j]
	var parts []string
	for _, ln := range strings.Split(block, "\n") {
		ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "*"))
		// Keep the meaningful prose lines; drop empty, checklist, and ref lines.
		if ln == "" || strings.HasPrefix(ln, "✅") || strings.HasPrefix(ln, "🚫") ||
			strings.HasPrefix(ln, "Reference") || strings.HasPrefix(ln, "⚠") || strings.HasPrefix(ln, "@") {
			continue
		}
		parts = append(parts, ln)
		if len(parts) >= 2 {
			break
		}
	}
	return strings.Join(parts, " — ")
}

// steps walks a test body line by line and emits a humanized step for each
// meaningful Playwright action/assertion, in source order.
func steps(body string) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		ln := strings.TrimSpace(raw)
		if ln == "" || strings.HasPrefix(ln, "//") || strings.HasPrefix(ln, "*") {
			continue
		}
		if !reAction.MatchString(ln) {
			continue
		}
		// Skip locator/variable declarations that don't perform an action —
		// e.g. `const btn = page.getByRole(...)` is setup, not a step.
		if isDecl(ln) && !hasVerb(ln) {
			continue
		}
		if s := humanize(ln); s != "" {
			out = append(out, s)
		}
		if len(out) >= 25 {
			out = append(out, "… (more steps omitted)")
			break
		}
	}
	return out
}

// humanize turns a source line into a short readable step. Falls back to a
// lightly-cleaned version of the line so nothing is misrepresented.
func humanize(ln string) string {
	clean := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "await ")
		s = strings.TrimPrefix(s, "return ")
		s = strings.TrimSuffix(s, "{")
		s = strings.TrimRight(s, " ;")
		return strings.TrimSpace(s)
	}
	switch {
	case reGoto.MatchString(ln):
		if m := reGoto.FindStringSubmatch(ln); m != nil {
			return "Open " + m[1]
		}
	case strings.Contains(ln, ".click("):
		if t := targetName(ln); t != "" {
			return "Click " + t
		}
		return "Click"
	case strings.Contains(ln, ".hover("):
		if t := targetName(ln); t != "" {
			return "Hover " + t
		}
		return "Hover"
	case strings.Contains(ln, ".fill("):
		if m := reArg.FindAllStringSubmatch(ln, -1); len(m) > 0 {
			return "Fill " + firstNonName(m)
		}
		return "Fill a field"
	case strings.Contains(ln, ".selectOption("):
		return "Select an option"
	case strings.Contains(ln, ".setInputFiles("):
		return "Upload a file"
	case strings.Contains(ln, ".press("):
		if m := reArg.FindStringSubmatch(ln); m != nil {
			return "Press " + m[1]
		}
	case strings.Contains(ln, "waitForURL"):
		if m := reArg.FindStringSubmatch(ln); m != nil {
			return "Wait for URL " + m[1]
		}
		return "Wait for navigation"
	case strings.Contains(ln, "waitForLoadState"):
		return "Wait for page to settle"
	case strings.HasPrefix(ln, "expect(") || strings.Contains(ln, " expect("):
		return "Expect: " + clean(ln)
	case strings.Contains(ln, "test.skip"):
		return "(skips when a precondition isn't met)"
	}
	return clean(ln)
}

// targetName pulls a readable element label out of a getByRole/getByText/locator chain.
func targetName(ln string) string {
	if m := reName.FindStringSubmatch(ln); m != nil {
		return cleanLabel(m[1])
	}
	if m := reArg.FindStringSubmatch(ln); m != nil {
		return cleanLabel(m[1])
	}
	return ""
}

// cleanLabel strips regex anchors/slashes that leak in from name: /^Foo$/i.
func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "/")
	s = strings.TrimPrefix(s, "^")
	s = strings.TrimSuffix(s, "$")
	return strings.TrimSpace(s)
}

func isDecl(ln string) bool {
	return strings.HasPrefix(ln, "const ") || strings.HasPrefix(ln, "let ") || strings.HasPrefix(ln, "var ")
}

// hasVerb reports whether a line performs an actual action (vs just declaring a locator).
func hasVerb(ln string) bool {
	for _, v := range []string{"goto(", ".click(", ".fill(", ".hover(", ".press(", ".selectOption(",
		".setInputFiles(", ".check(", ".uncheck(", "waitForURL", "waitForLoadState", "waitForEvent", "expect("} {
		if strings.Contains(ln, v) {
			return true
		}
	}
	return false
}

// firstNonName returns the first quoted arg that isn't a name: key (used for fill values).
func firstNonName(m [][]string) string {
	for _, g := range m {
		if !strings.Contains(g[0], "name") {
			return `"` + g[1] + `"`
		}
	}
	if len(m) > 0 {
		return `"` + m[0][1] + `"`
	}
	return "a value"
}
