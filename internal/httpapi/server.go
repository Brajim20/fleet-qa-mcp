// Package httpapi is the third front-end over the shared qa core (alongside the
// MCP server and the CLI). It serves the Fleet QA Studio UI same-origin and
// exposes the QA functions as JSON so the UI can run REAL investigations against
// the live deployed build — fetch the issue, hit the API, drive a browser, grep
// the deployed source, check the build, and draft a prefilled issue.
//
// Same-origin (UI + API on one port) means no CORS. Writes stay gated: the
// generic /api/request proxy refuses non-GET unless confirm=true, exactly like
// the MCP/CLI front-ends.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Brajim20/fleet-qa-mcp/internal/ghissue"
	"github.com/Brajim20/fleet-qa-mcp/internal/llm"
	"github.com/Brajim20/fleet-qa-mcp/internal/qa"
)

// Server wires the qa core to HTTP. It keeps an in-memory log of investigations
// run this session so the dashboard reflects real runs (there's no database —
// the engine is stateless; this is just session memory).
type Server struct {
	app       *qa.App
	studioDir string
	shotDir   string

	mu   sync.Mutex
	runs map[int]*storedRun // issue number -> latest report
}

type storedRun struct {
	Report  *qa.Report `json:"report"`
	RanAt   time.Time  `json:"ranAt"`
	Verdict string     `json:"verdict"`
}

// New builds the HTTP handler. studioDir holds index.html; screenshots are
// written under <studioDir>/.shots and served at /shots/.
func New(app *qa.App, studioDir string) *Server {
	shotDir := filepath.Join(studioDir, ".shots")
	_ = os.MkdirAll(shotDir, 0o755)
	return &Server{app: app, studioDir: studioDir, shotDir: shotDir, runs: map[int]*storedRun{}}
}

// Handler returns the configured mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/queue", s.handleQueue)
	mux.HandleFunc("/api/investigations", s.handleList)
	mux.HandleFunc("/api/investigate", s.handleInvestigate)
	mux.HandleFunc("/api/report", s.handleReport)
	mux.HandleFunc("/api/verdict", s.handleVerdict)
	mux.HandleFunc("/api/spec", s.handleSpec)          // generate a Playwright test (preview)
	mux.HandleFunc("/api/spec/save", s.handleSpecSave) // write it into the repo (gated)
	mux.HandleFunc("/api/request", s.handleRequest)    // ad-hoc REST proxy (read-only unless confirm)

	// Screenshots captured during browser repros.
	mux.Handle("/shots/", http.StripPrefix("/shots/", http.FileServer(http.Dir(s.shotDir))))
	// The Studio UI (index.html + vendor/). Served last as the catch-all.
	mux.Handle("/", http.FileServer(http.Dir(s.studioDir)))
	return logRequests(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, agent := llm.NewFromEnv()
	out := map[string]any{"ok": true, "instance": hostOf(s.app.Inst.URL), "source": s.app.Inst.Source, "repo": s.app.Repo != "", "agent": agent}
	if v, err := s.app.Inst.DeployedVersion(); err == nil {
		out["version"] = v.Version
		out["rev"] = v.Revision
		out["tier"] = s.app.LicenseTier()
		out["connected"] = true
	} else {
		out["connected"] = false
		out["error"] = err.Error()
	}
	writeJSON(w, 200, out)
}

// handleQueue lists the QA backlog — open fleetdm/fleet issues labeled with the
// given label (default "bug"), most-recently-updated first.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("label")
	if label == "" {
		label = "bug"
	}
	// Optional product-group filter (#g-*). GitHub treats comma-separated labels
	// as AND, so "bug,#g-software" = open software bugs.
	if g := r.URL.Query().Get("group"); g != "" {
		label = label + "," + g
	}
	issues, err := ghissue.List(label, 50)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	// Prefer the real GitHub Project board status (needs a read:project token);
	// fall back to the label-derived status per issue when unavailable.
	nums := make([]int, len(issues))
	for i, is := range issues {
		nums[i] = is.Number
	}
	board := ghissue.ProjectStatuses(nums)

	list := make([]map[string]any, 0, len(issues))
	for _, i := range issues {
		status := board[i.Number]
		source := "board"
		if status == "" {
			status, source = ghissue.WorkflowStatus(i.Labels), "label"
		}
		list = append(list, map[string]any{
			"number": i.Number, "title": i.Title, "reporter": i.Reporter,
			"group": i.ProductGroup(), "labels": i.Labels, "status": status, "statusSource": source,
		})
	}
	writeJSON(w, 200, map[string]any{"label": label, "issues": list})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]map[string]any, 0, len(s.runs))
	for _, run := range s.runs {
		list = append(list, runSummary(run))
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i]["_ts"].(int64) > list[j]["_ts"].(int64)
	})
	writeJSON(w, 200, map[string]any{"investigations": list})
}

func (s *Server) handleInvestigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "POST required")
		return
	}
	var in struct {
		Issue string `json:"issue"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Issue == "" {
		writeErr(w, 400, "missing 'issue'")
		return
	}
	rep := s.app.Investigate(in.Issue, s.shotDir, "/shots")
	if rep.Number > 0 {
		s.mu.Lock()
		s.runs[rep.Number] = &storedRun{Report: rep, RanAt: time.Now(), Verdict: rep.Status}
		s.mu.Unlock()
	}
	writeJSON(w, 200, rep)
}

// handleReport returns a stored full report by ?number=N (survives a page reload
// for the session). 404 if it hasn't been run this session.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("number"))
	s.mu.Lock()
	run, ok := s.runs[n]
	s.mu.Unlock()
	if !ok {
		writeErr(w, 404, "no investigation for that issue this session")
		return
	}
	writeJSON(w, 200, run.Report)
}

// handleVerdict records the human's verdict for an investigation (session-only).
func (s *Server) handleVerdict(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Number  int    `json:"number"`
		Verdict string `json:"verdict"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	if run, ok := s.runs[in.Number]; ok {
		run.Verdict = in.Verdict
		run.Report.Status = in.Verdict
	}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true})
}

// handleSpec generates (but does not write) a Playwright regression test for a
// stored investigation, for preview in the UI.
func (s *Server) handleSpec(w http.ResponseWriter, r *http.Request) {
	rep := s.reportFromBody(r)
	if rep == nil {
		writeErr(w, 404, "no investigation for that issue this session")
		return
	}
	relPath, content := qa.GenerateSpec(rep)
	exists := false
	if dir := s.playwrightDir(); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, relPath)); err == nil {
			exists = true
		}
	}
	writeJSON(w, 200, map[string]any{"path": "tools/qa/playwright/" + relPath, "relPath": relPath, "content": content, "exists": exists})
}

// handleSpecSave writes the generated test into the Fleet checkout's Playwright
// suite. This is the one file-mutating action — it only ever runs on an explicit
// "Save" click, and the target is constrained to the tests directory.
func (s *Server) handleSpecSave(w http.ResponseWriter, r *http.Request) {
	dir := s.playwrightDir()
	if dir == "" {
		writeErr(w, 400, "no Fleet repo resolved (set FLEET_REPO) — can't save the test")
		return
	}
	rep := s.reportFromBody(r)
	if rep == nil {
		writeErr(w, 404, "no investigation for that issue this session")
		return
	}
	relPath, content := qa.GenerateSpec(rep)
	target := filepath.Join(dir, relPath)
	// Safety: the resolved path must stay inside the Playwright tests dir.
	if rel, err := filepath.Rel(dir, target); err != nil || strings.HasPrefix(rel, "..") {
		writeErr(w, 400, "refusing to write outside the Playwright directory")
		return
	}
	_, existed := os.Stat(target)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"saved": "tools/qa/playwright/" + relPath, "overwrote": existed == nil})
}

func (s *Server) reportFromBody(r *http.Request) *qa.Report {
	var in struct {
		Number int `json:"number"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.mu.Lock()
	defer s.mu.Unlock()
	if run, ok := s.runs[in.Number]; ok {
		return run.Report
	}
	return nil
}

func (s *Server) playwrightDir() string {
	if s.app.Repo == "" {
		return ""
	}
	return filepath.Join(s.app.Repo, "tools", "qa", "playwright")
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Method, Path, Body string
		Confirm            bool
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Method == "" {
		in.Method = "GET"
	}
	out, err := s.app.FleetRequest(in.Method, in.Path, in.Body, in.Confirm)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"result": out})
}

func runSummary(run *storedRun) map[string]any {
	rep := run.Report
	return map[string]any{
		"number":    rep.Number,
		"title":     rep.Title,
		"reporter":  rep.Reporter,
		"group":     rep.Group,
		"status":    run.Verdict,
		"steps":     len(rep.Steps),
		"updatedAt": humanAgo(run.RanAt),
		"_ts":       run.RanAt.Unix(),
	}
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func hostOf(u string) string {
	for _, p := range []string{"https://", "http://"} {
		if len(u) >= len(p) && u[:len(p)] == p {
			u = u[len(p):]
		}
	}
	for len(u) > 0 && u[len(u)-1] == '/' {
		u = u[:len(u)-1]
	}
	return u
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
			fmt.Fprintf(os.Stderr, "[qa-studio] %s %s\n", r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}
