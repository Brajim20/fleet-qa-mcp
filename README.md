# fleet-qa-mcp

A QA toolkit for [Fleet](https://github.com/fleetdm/fleet) with **three front-ends over one
shared core**: an **MCP server** (for Claude Code / Cursor / VS Code), a **deterministic
CLI** (for scripting / CI), and **Fleet QA Studio** — a web app for the QA queue and smoke
runs. It reproduces issues, root-causes in the *deployed* code, checks whether a
PR/cherry-pick is in the running build, drives a real browser, and drafts prefilled GitHub
issues — the manual QA workflow, packaged so anyone can reuse it.

## Quick start

```bash
git clone https://github.com/Brajim20/fleet-qa-mcp && cd fleet-qa-mcp
fleetctl login                 # so ~/.fleet/config has your instance URL + token
gh auth login                  # GitHub access (for the QA queue / reading issues)
make qa-setup                  # deps + Playwright Chromium (one-time)
make qa-mcp                    # build ./build/fleet-qa-mcp
export FLEET_REPO=~/path/to/fleet   # your Fleet checkout (code tools + smoke suite)
make qa-auth                   # reusable browser session from your admin token
```

Then any of:
- **MCP**: open the repo in Claude Code (auto-detects `.mcp.json`) / Cursor / VS Code, enable `fleet-qa`. Run the `whoami` tool first.
- **CLI**: `./build/fleet-qa-mcp help`
- **Web app (Fleet QA Studio)**: `make studio` → <http://127.0.0.1:8799>. See below.

It's built so **anyone runs it locally with their own creds** — no shared server. See **[ONBOARDING.md](ONBOARDING.md)** for full setup, per-user config, the human-in-the-loop steps, and limits.

## Fleet QA Studio (web app)

`make studio` → <http://127.0.0.1:8799> — the web front-end over the same core:

- **QA queue** — open Fleet `bug`/`story` issues filtered by **type · product group · milestone · status** (real GitHub Project board statuses); investigate any of them.
- **Reproduce / Run test plan** — execute a ticket's own *Steps to reproduce* (bugs) or *Test plan* (stories) against the live build.
- **Smoke tests** — run your Playwright smoke suite per product group and see the pass/fail matrix (with test titles). Click a **Passed/Failed/Skipped** card to filter the list. Picking a suite **auto-loads its test plan** — a step-by-step outline of what each test does (read from the spec source, no run); suites with no specs show an empty state.
- On a verdict: **prefilled bug draft** (Fleet template, never auto-posted) or a **generated Playwright regression test**.
- The full GitHub issue body is shown untruncated in the evidence timeline; click the **Fleet logo** to return to the dashboard.

> To run a free-form investigation, use the `/investigate` skill in Claude Code (see below) or the `investigate` CLI command instead.

### Per-feature setup (each user, runs locally)

| Feature | Needs |
|---|---|
| Investigations / target instance | `fleetctl login` (`~/.fleet/config`) or `FLEET_URL` + `FLEET_TOKEN` |
| Code tools + released/unreleased | `FLEET_REPO` → your Fleet checkout |
| Browser repro / smoke runs | `make qa-setup` (Playwright Chromium) + `make qa-auth` |
| **QA queue** | GitHub auth — `gh auth login` (or `GITHUB_TOKEN`) |
| **QA queue → board statuses** | `gh auth refresh -s read:project` (else falls back to the label-derived status) |
| **Smoke tests** | the Playwright suite present at `$FLEET_REPO/tools/qa/playwright` (override with `SMOKE_DIR`) |
| **AI agent** (optional) | `ANTHROPIC_API_KEY` in `.env` (else the deterministic heuristic engine) |

> The QA queue and Smoke tests tabs appear only in **live mode** (i.e. when `make studio` is serving the backend). The static GitHub Pages build is a **demo** (mock data) — the functional tool runs locally.

## Tools / commands

| Tool (MCP) | CLI | Purpose |
|---|---|---|
| `whoami` | `whoami` | instance + deployed version/rev + repo |
| `code_at_rev` | `code-at-rev` | read a file at the **deployed** revision |
| `grep_code` | `grep` | git grep at the deployed revision |
| `is_in_build` | `is-in-build` | is a commit/PR/cherry-pick in the running build? |
| `log_search` | `log-search` | which commit introduced a string |
| `released_in` | `released-in` | when did this bug ship? finds the introducing commit then reports the first stable `fleet-vX.Y.Z` release |
| `fleet_request` | `request` | authenticated REST (read-only unless `confirm`) |
| `browser_eval` | `browser-eval` | run JS in real Chromium; screenshot the **buggy element** (`--shot-selector`), highlight it in context (`--shot-highlight`), or capture the whole page (`--full-page`) |
| `browser_sample_frames` | `sample-frames` | per-frame sampler for timing/visual bugs |
| `build_issue_url` | `issue` | **prefilled** GitHub issue URL (never submits) |
| `investigate` | `investigate` | full investigation pipeline (see below) |
| `smoke_run` | `smoke` | run the Playwright smoke suite; pass/fail matrix with titles, `--status` filter |
| `smoke_plan` | `plan` | step-by-step outline of what each smoke test does (no run) |

### Workflow commands (CLI — same orchestrations as the Studio web app)

| CLI | Purpose |
|---|---|
| `investigate <issue> [--mode reproduce\|testplan]` | run a full investigation; print evidence, released/unreleased, proposed verdict, draft URL |
| `queue [--type bug\|story\|all] [--group #g-*] [--milestone V] [--status S]` | list the QA backlog with board statuses |
| `smoke [group] [--status passed\|failed\|skipped]` | run the Playwright smoke suite; pass/fail matrix with test titles, optionally filtered by status |
| `plan [group\|spec]` | step-by-step outline of what each smoke test does — reads the spec source, never runs it |
| `milestones` | list open release milestones |
| `spec <issue>` | generate a Playwright regression test |

e.g. `fleet-qa-mcp queue --group '#g-software' --milestone 4.87.0 --status 'Ready for release'`

```bash
fleet-qa-mcp released-in "someSymbolOrString"          # when did this bug ship?
fleet-qa-mcp smoke software --status failed            # only the red software smokes (with test titles)
fleet-qa-mcp plan software/scripts.spec.ts             # what each test in one spec actually does
# screenshot the actual bug element, scrolled into view + outlined:
fleet-qa-mcp browser-eval https://your.instance/policies/new '() => ({})' \
  --screenshot bug.png --shot-selector ".modal__background" --shot-highlight
```

## /investigate skill

The server ships a built-in `/investigate` skill available in **any** Claude Code project
that has `fleet-qa` configured — no need to have this repo open.

In Claude Code, type `/mcp__fleet-qa__investigate` (or just `/investigate` if it resolves),
pass the issue number or URL, and Claude will:

1. Call `whoami` to confirm the live instance and deployed revision.
2. Run the `investigate` tool — fetches the issue, hits the relevant API, opens the page in
   real Chromium, greps the deployed source, classifies released/unreleased.
3. Deepen with individual tools as needed (`code_at_rev`, `grep_code`, `is_in_build`, `released_in`, …).
4. Produce a verdict block and a prefilled bug-report URL for your review.

The same pipeline is also available as `./build/fleet-qa-mcp investigate <issue>` from the
CLI, or via the **QA queue** tab in Fleet QA Studio.

## Claude Code skills

This repo ships a full reference of all Claude Code skills used by the Fleet QA team — see **[SKILLS.md](SKILLS.md)**.

Skills are slash commands you type in Claude Code (e.g. `/lint`, `/review-pr`, `/fix-ci`). There are two kinds:

- **Project skills** — live in `.claude/skills/` and are shared automatically when teammates clone the Fleet repo.
- **Global user skills** — must be installed once per user via `claude /find-skills playwright`. These include the Playwright skills (`/playwright-cli`, `/playwright-generate-test`, `/playwright-best-practices`).

Quick reference: type `/` in Claude Code to see all available skills, or open [SKILLS.md](SKILLS.md) for descriptions of all 29 skills.

## Investigations: AI agent or heuristic engine

Two engines, picked automatically:

- **AI agent** (when `ANTHROPIC_API_KEY` is set): Claude reads the issue and drives the
  read-only tools in a loop — calling the API, opening a real browser, grepping the
  deployed source, checking the build — then proposes a verdict you confirm. It decides
  *what* to investigate, the way a human QA engineer would.
- **Heuristic engine** (no key, or on agent error): a deterministic pipeline that derives
  the step inputs (API path, grep keyword, commit SHA) from the issue text with regex.

Either way the tools are read-only, code is pinned to the deployed revision, and the
verdict is human-confirmed. See [studio/README.md](studio/README.md).

## Design

- **Per-user config**: instance URL + token resolved from each user's `~/.fleet/config`, so the committed `.mcp.json` carries no shared URL. Auto-refreshes the token on a 401 (with `FLEET_PASSWORD`).
- **Deployed-rev pinning**: code tools default to the running build's revision — never analyze `main` by mistake.
- **Safety**: read-only by default; writes require `confirm` (the AI agent is never given it); GitHub issues are prefilled URLs you review, never auto-posted.

## License

MIT — see [LICENSE](LICENSE).
