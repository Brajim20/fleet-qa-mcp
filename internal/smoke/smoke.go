// Package smoke runs the Fleet Playwright smoke suite that lives in the user's
// Fleet checkout (tools/qa/playwright) against their resolved instance — the
// same `npm run test:smoke` workflow, surfaced through the QA Studio. It never
// modifies the specs; it executes them and parses the pass/fail matrix.
package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Result is one spec's outcome.
type Result struct {
	File     string `json:"file"`
	Title    string `json:"title"`
	Status   string `json:"status"` // passed|failed|skipped
	Error    string `json:"error"`
	Duration int    `json:"durationMs"`
}

// Run is the result of running a group (or all) of the smoke suite.
type Run struct {
	Group    string   `json:"group"`
	Dir      string   `json:"dir"`
	Passed   int      `json:"passed"`
	Failed   int      `json:"failed"`
	Skipped  int      `json:"skipped"`
	Duration int      `json:"durationMs"`
	Results  []Result `json:"results"`
	Error    string   `json:"error,omitempty"`
}

// Dir resolves the Playwright suite location: $SMOKE_DIR, else
// <repo>/tools/qa/playwright. Returns "" if neither is usable.
func Dir(repo string) string {
	if d := os.Getenv("SMOKE_DIR"); d != "" {
		return d
	}
	if repo == "" {
		return ""
	}
	return filepath.Join(repo, "tools", "qa", "playwright")
}

// Available reports whether a runnable suite exists (smoke dir + the playwright
// binary). The message explains what's missing when it isn't.
func Available(dir string) (bool, string) {
	if dir == "" {
		return false, "no Fleet repo resolved (set FLEET_REPO) — can't find the smoke suite"
	}
	if _, err := os.Stat(filepath.Join(dir, "tests", "smoke")); err != nil {
		return false, "no smoke suite in this checkout (" + dir + "/tests/smoke missing — merge tools/qa/playwright into Fleet, or set SMOKE_DIR)"
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", ".bin", "playwright")); err != nil {
		return false, "Playwright not installed — run `npm install` and `npx playwright install` in " + dir
	}
	return true, ""
}

// Groups lists the smoke subdirectories (each a product area) under tests/smoke.
func Groups(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, "tests", "smoke"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// RunGroup executes the smoke specs for one group ("" = all of tests/smoke)
// against instanceURL, authenticating with token (passed as FLEET_API_TOKEN so
// the suite's e2e-setup logs in). Playwright exits non-zero on failures — that's
// expected; we parse stdout regardless and only error if it produced no report.
func RunGroup(ctx context.Context, dir, group, instanceURL, token string) *Run {
	run := &Run{Group: group, Dir: dir}
	testPath := "tests/smoke"
	if group != "" {
		testPath = "tests/smoke/" + group
	}
	bin := filepath.Join(dir, "node_modules", ".bin", "playwright")
	cmd := exec.CommandContext(ctx, bin, "test", "--project=e2e", testPath, "--reporter=json")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "FLEET_URL="+instanceURL)
	if token != "" {
		cmd.Env = append(cmd.Env, "FLEET_API_TOKEN="+token)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run() // playwright exits non-zero on failures — expected

	if ctx.Err() != nil {
		run.Error = "smoke run timed out after " + DefaultTimeout.String()
		return run
	}
	report := extractJSON(stdout.Bytes())
	if len(report) == 0 {
		run.Error = "smoke run produced no report — Playwright output:\n" + tail(stderr.String(), 600)
		if runErr != nil && stderr.Len() == 0 {
			run.Error = "could not start the smoke run: " + runErr.Error()
		}
		return run
	}
	setupFailed := parseReport(report, run)
	// No specs ran → almost always the e2e auth/setup step failed.
	if run.Passed+run.Failed+run.Skipped == 0 {
		if setupFailed {
			run.Error = "the e2e auth setup failed, so no smoke specs ran — check the instance is reachable and the admin token is valid.\n\n" + tail(stderr.String(), 600)
		} else {
			run.Error = "no smoke specs matched/ran for this selection.\n\n" + tail(stderr.String(), 400)
		}
	}
	return run
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// --- Playwright JSON reporter parsing ---

type pwReport struct {
	Suites []pwSuite `json:"suites"`
	Stats  struct {
		Duration float64 `json:"duration"`
	} `json:"stats"`
}
type pwSuite struct {
	Specs  []pwSpec  `json:"specs"`
	Suites []pwSuite `json:"suites"`
}
type pwSpec struct {
	Title string `json:"title"`
	File  string `json:"file"`
	Ok    bool   `json:"ok"`
	Tests []struct {
		Status  string `json:"status"` // expected|unexpected|flaky|skipped
		Results []struct {
			Status   string `json:"status"` // passed|failed|timedOut|skipped
			Duration int    `json:"duration"`
			Error    *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
	} `json:"tests"`
}

// parseReport fills run from the Playwright JSON; returns true if the auth setup
// spec failed (which means the smoke specs were skipped).
func parseReport(b []byte, run *Run) (setupFailed bool) {
	var r pwReport
	if err := json.Unmarshal(b, &r); err != nil {
		run.Error = "could not parse the Playwright report: " + err.Error()
		return false
	}
	run.Duration = int(r.Stats.Duration)
	var walk func(s pwSuite)
	walk = func(s pwSuite) {
		for _, sp := range s.Specs {
			if strings.HasPrefix(sp.File, "setup/") { // the auth setup project
				if !sp.Ok {
					setupFailed = true
				}
				continue
			}
			res := Result{File: sp.File, Title: sp.Title}
			skipped := len(sp.Tests) > 0
			for _, t := range sp.Tests {
				if t.Status != "skipped" {
					skipped = false
				}
				for _, rr := range t.Results {
					res.Duration += rr.Duration
					if rr.Error != nil && res.Error == "" {
						res.Error = firstLine(rr.Error.Message)
					}
				}
			}
			switch {
			case skipped:
				res.Status = "skipped"
				run.Skipped++
			case sp.Ok:
				res.Status = "passed"
				run.Passed++
			default:
				res.Status = "failed"
				run.Failed++
			}
			run.Results = append(run.Results, res)
		}
		for _, c := range s.Suites {
			walk(c)
		}
	}
	for _, s := range r.Suites {
		walk(s)
	}
	return setupFailed
}

// extractJSON returns the JSON object from stdout (from the first '{').
func extractJSON(b []byte) []byte {
	if i := strings.IndexByte(string(b), '{'); i >= 0 {
		return b[i:]
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// DefaultTimeout bounds a smoke run (the whole suite can take minutes).
const DefaultTimeout = 12 * time.Minute
