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

### Two engines (picked automatically)
- **AI agent** — set `ANTHROPIC_API_KEY` in `.env` and Claude drives the tools: it reads
  the issue and decides which API to call, which page to open, what to grep, and which
  commit to build-check — a handful of well-chosen steps, then a proposed verdict. The New
  investigation page shows an "AI agent" badge; each tool call becomes a timeline step.
- **Heuristic engine** — with no key (or if the agent errors), a deterministic pipeline
  derives the step inputs from the issue text with regex. Same tools, same safety.

The agent is only ever given the **read-only** tools (its `fleet_request` is GET-only and
never receives `confirm`), so the "reads free, writes gated" invariant holds even when an
autonomous agent is in the loop.

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

### Smoke tests
The **Smoke tests** view runs your existing Playwright suite — it does **not** ship its
own copy. It resolves the suite at `$FLEET_REPO/tools/qa/playwright` (override with
`SMOKE_DIR`), lists the groups under `tests/smoke/`, and runs the chosen group (or all)
via `playwright test --project=e2e tests/smoke/<group>` against your resolved instance,
then renders the pass/fail matrix. Read-only on the repo (only ephemeral Playwright
artifacts are written); auth uses your instance token (passed as `FLEET_API_TOKEN`).

This is the same suite every QA/dev runs: point `FLEET_REPO` at your checkout (your branch)
and `FLEET_URL` at your build, and "Run smokes" catches regressions you introduced — no
per-user copies. (Requires the suite to exist in that checkout, i.e. merged into Fleet or
on your branch, with `npm install` + `npx playwright install` done once.)

### Reproduce / Run test plan
The queue's action button is type-aware, and the detail view offers the same:
- **Reproduce** (bugs) — parses the ticket's *Steps to reproduce* and performs each
  against the live build, in order.
- **Run test plan** (stories) — parses the story's *Test plan* and executes each item.

With an `ANTHROPIC_API_KEY`, the agent actually follows each prose step via the tools
and reports pass/fail. Without one, it performs the automatable parts of each step
(any API call or page nav it names, with a screenshot) and lists the rest as manual
checks — honest about what it could and couldn't drive.

### The QA loop
Beyond investigating one issue, LIVE mode covers the workflow end-to-end:

- **QA queue** — the sidebar "QA queue" lists real open `bug` issues from fleetdm/fleet
  (`GET /api/queue?label=bug`); click **Investigate** on any of them.
- **Released vs unreleased** — every investigation traces the commit that introduced the
  buggy code and runs `git tag --contains` against the `fleet-v*` release tags: **Released**
  (shipped to customers → needs a patch) or **Unreleased** (caught pre-release → auto-adds
  the `~unreleased bug` label). Shown as a timeline step and a pill on the detail header.
- **Verdict-driven output** — once you confirm a verdict:
  - **Confirmed bug / Cannot reproduce** → a prefilled GitHub issue (root cause + released/
    unreleased + labels baked in), opened for review — never auto-posted.
  - **Fixed** → a **Playwright regression test** matching this repo's conventions
    (`authenticated-test` fixture, `tests/smoke/<area>/<slug>.spec.ts`). Preview it, then
    **Save to repo** writes it into your Fleet checkout under `tools/qa/playwright/` (the one
    file-mutating action — explicit click only, path-guarded to the tests directory).

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
