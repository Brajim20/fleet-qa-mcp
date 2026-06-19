# fleet-qa-mcp

A QA toolkit for [Fleet](https://github.com/fleetdm/fleet) with **three front-ends over one
shared core**: an **MCP server** (for Claude Code / Cursor / VS Code), a **deterministic
CLI** (for scripting / CI), and **Fleet QA Studio** — a web app that runs full
investigations end-to-end. It reproduces issues, root-causes in the *deployed* code,
checks whether a PR/cherry-pick is in the running build, drives a real browser, and drafts
prefilled GitHub issues — the manual QA workflow, packaged so anyone can reuse it.

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

- **New investigation** — paste a GitHub issue; it reproduces against the live build, root-causes in the deployed source, classifies released vs unreleased, and proposes a verdict you confirm.
- **QA queue** — open Fleet `bug`/`story` issues filtered by **type · product group · milestone · status** (real GitHub Project board statuses); investigate any of them.
- **Reproduce / Run test plan** — execute a ticket's own *Steps to reproduce* (bugs) or *Test plan* (stories) against the live build.
- **Smoke tests** — run your Playwright smoke suite per product group and see the pass/fail matrix (with test titles). Click a **Passed/Failed/Skipped** card to filter the list. Picking a suite **auto-loads its test plan** — a step-by-step outline of what each test does (read from the spec source, no run); suites with no specs show an empty state.
- On a verdict: **prefilled bug draft** (Fleet template, never auto-posted) or a **generated Playwright regression test**.
- The full GitHub issue body is shown untruncated in the evidence timeline; click the **Fleet logo** to return to the dashboard.

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
| `fleet_request` | `request` | authenticated REST (read-only unless `confirm`) |
| `browser_eval` | `browser-eval` | run JS in real Chromium; screenshot the **buggy element** (`--shot-selector`), highlight it in context (`--shot-highlight`), or capture the whole page (`--full-page`) |
| `browser_sample_frames` | `sample-frames` | per-frame sampler for timing/visual bugs |
| `build_issue_url` | `issue` | **prefilled** GitHub issue URL (never submits) |
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
fleet-qa-mcp smoke software --status failed       # only the red software smokes (with test titles)
fleet-qa-mcp plan software/scripts.spec.ts        # what each test in one spec actually does
# screenshot the actual bug element, scrolled into view + outlined:
fleet-qa-mcp browser-eval https://your.instance/policies/new '() => ({})' \
  --screenshot bug.png --shot-selector ".modal__background" --shot-highlight
```

## Investigations: AI agent or heuristic engine

`make studio` runs full investigations end-to-end. Two engines, picked automatically:

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
