# Fleet QA Studio — web app

A web UI for the `fleet-qa-mcp` toolkit, implemented from the Claude Design file
`Fleet QA Studio.dc.html`. Paste a GitHub issue → reproduce on the live deployed
build → root-cause in the deployed-revision source → build-membership check →
human-confirmed verdict → prefilled GitHub issue.

The same `index.html` runs in two modes — it detects which automatically:

## LIVE mode (real investigations)
Served by the `fleet-qa-mcp` backend, every investigation actually runs against
your deployed instance: it fetches the GitHub issue, hits the live API, drives a
real headless Chromium (and captures a screenshot), greps the deployed source at
the deployed revision, and runs the build-membership check.

```bash
make studio          # → http://127.0.0.1:8799   (or: ./build/fleet-qa-mcp serve)
```

The instance + Fleet source repo are resolved exactly like the MCP/CLI front-ends
(`~/.fleet/config`, `FLEET_URL`/`FLEET_TOKEN`, `FLEET_REPO`). One-time setup for the
browser repro: `make qa-setup` then `make qa-auth`. Reads are free; live-instance
writes and issue submission always require an explicit confirm — nothing is auto-posted.

### How the engine maps to the UI
| Timeline step | Backend function (`internal/qa`) |
|---|---|
| Fetch issue | `ghissue.Fetch` (GitHub API) |
| Resolve target | `Inst.DeployedVersion` + `LicenseTier` |
| Reproduce via API | `FleetRequest` (first `/api/...` path in the issue) |
| Reproduce in live browser | `BrowserEval` (real Chromium + screenshot) |
| Root cause | `GrepCode` at the deployed revision |
| Build check | `IsInBuild` (merge-base `--is-ancestor`) |
| Draft GitHub issue | `BuildIssueURL` (prefilled; never submits) |

What to grep / which API to hit is auto-derived from the issue text (heuristics in
`internal/qa/investigate.go`); the human owns the verdict.

## DEMO mode (mock data)
Opened as a plain static file (e.g. GitHub Pages), it falls back to a scripted
walkthrough with mock investigations (#47712, #43310, #46920, …) — no backend needed.

```bash
cd studio && python3 -m http.server 8745   # → http://localhost:8745  (demo)
```

## What's here
- `index.html` — the whole app (React, single file): dashboard, new-investigation,
  detail with the evidence timeline, sticky verdict panel, draft modal, dark+light.
- `vendor/` — React 18, ReactDOM 18, Babel standalone (vendored so it runs offline).
