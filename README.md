# fleet-qa-mcp

A QA toolkit for [Fleet](https://github.com/fleetdm/fleet) with **three front-ends over one
shared core**: an **MCP server** (for Claude Code / Cursor / VS Code), a **deterministic
CLI** (for scripting / CI), and **Fleet QA Studio** — a web app that runs full
investigations end-to-end. It reproduces issues, root-causes in the *deployed* code,
checks whether a PR/cherry-pick is in the running build, drives a real browser, and drafts
prefilled GitHub issues — the manual QA workflow, packaged so anyone can reuse it.

## Quick start

```bash
git clone <this-repo> && cd fleet-qa-mcp
fleetctl login                 # so ~/.fleet/config has your instance URL + token
make qa-setup                  # deps + Playwright Chromium (one-time)
make qa-mcp                    # build ./build/fleet-qa-mcp
export FLEET_REPO=~/path/to/fleet   # your Fleet checkout (for code tools)
make qa-auth                   # reusable browser session from your admin token
```

Then any of:
- **MCP**: open the repo in Claude Code (auto-detects `.mcp.json`) / Cursor / VS Code, enable `fleet-qa`. Run the `whoami` tool first.
- **CLI**: `./build/fleet-qa-mcp help`
- **Web app (Fleet QA Studio)**: `make studio` → <http://127.0.0.1:8799>. Paste a GitHub issue and it runs the whole investigation against your live build. See [studio/README.md](studio/README.md).

See **[ONBOARDING.md](ONBOARDING.md)** for full setup, per-user config, the human-in-the-loop steps, and limits.

## Tools / commands

| Tool (MCP) | CLI | Purpose |
|---|---|---|
| `whoami` | `whoami` | instance + deployed version/rev + repo |
| `code_at_rev` | `code-at-rev` | read a file at the **deployed** revision |
| `grep_code` | `grep` | git grep at the deployed revision |
| `is_in_build` | `is-in-build` | is a commit/PR/cherry-pick in the running build? |
| `log_search` | `log-search` | which commit introduced a string |
| `fleet_request` | `request` | authenticated REST (read-only unless `confirm`) |
| `browser_eval` | `browser-eval` | run JS in real Chromium, optional screenshot |
| `browser_sample_frames` | `sample-frames` | per-frame sampler for timing/visual bugs |
| `build_issue_url` | `issue` | **prefilled** GitHub issue URL (never submits) |

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
