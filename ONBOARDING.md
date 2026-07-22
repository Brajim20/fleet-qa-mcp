# Fleet QA MCP

An MCP server of QA tools for Fleet devs/QA — reproduce issues, root-cause in the
**deployed** code, check whether a PR/cherry-pick is in the running build, hit the
API, drive a real browser, and draft prefilled GitHub issues.

It's the toolkit from our manual QA sessions, packaged so anyone can use it the same way.

---

## Setup (≈3 commands)

```bash
git clone <this-repo> && cd fleet-qa-mcp
make qa-setup        # deps + download Playwright Chromium (one-time)
make qa-mcp          # build ./build/fleet-qa-mcp
make qa-auth         # write a reusable browser session from your admin token
```

Then open this repo in your MCP client (Claude Code auto-detects the committed
`.mcp.json`; Cursor/VS Code use `.cursor/mcp.json` / `.vscode/mcp.json`). Enable the
`fleet-qa` server when prompted.

**First thing to run:** the `whoami` tool — it prints which instance you're pointed at,
the deployed version + revision, and the Fleet source repo in use.

---

## Configuration (no secrets in git)

You don't set a URL in `.mcp.json`. The server resolves the instance like this:

1. `FLEET_URL` / `FLEET_TOKEN` env (CI / override)
2. **`~/.fleet/config`** — your existing `fleetctl` context (URL + token). This is the
   default, so your own instance "just works" with zero extra config.
3. `https://localhost:8080` fallback.

**Token auto-refresh:** the admin token expires ~hourly. Set **`FLEET_PASSWORD`** in your
`.env` (email comes from `~/.fleet/config`, or set `FLEET_EMAIL`) and the server
transparently re-logs-in on a 401 and retries — no mid-session interruption. Without a
password it falls back to a clear "run `fleetctl login`" message.

For the **code tools** (read deployed code, check build membership) the server needs a
Fleet source checkout:

- Set **`FLEET_REPO`** to your Fleet clone — ideally **the tree you `make build` from**,
  so locally-built/unpushed revisions are reachable.
- If unset, the server self-provisions a managed clone of `fleetdm/fleet` under
  `./.fleet-src`.

Copy `.env.example` → `.env` (gitignored) for any overrides.

---

## Tools

| Tool | Use |
|---|---|
| `whoami` | resolved instance URL + deployed version/rev + repo. Run first. |
| `code_at_rev` | read a file at the **deployed revision** (not `main`) |
| `grep_code` | git grep at the deployed revision |
| `is_in_build` | is a commit / PR-merge / cherry-pick in the running build? |
| `log_search` | which commit/PR introduced a string |
| `fleet_request` | authenticated REST (read-only unless `confirm=true`) |
| `browser_eval` | open a URL in real Chromium, run JS, optional screenshot — for repros & DOM measurement. `shot_selector` scrolls to + outlines the buggy element so the image shows the actual bug; `full_page` captures the whole page |
| `browser_sample_frames` | per-frame sampler for timing/visual bugs (flashes, theme desync) — records computed-style props across an optional trigger, returns a collapsed transition log |
| `build_issue_url` | **prefilled** GitHub issue URL from the bug-report template |
| `smoke_run` | run the Playwright smoke suite against the live instance; pass/fail matrix with test titles, optional `status` filter (passed/failed/skipped) |
| `smoke_plan` | step-by-step outline of what each smoke test does, from the spec source — never runs anything |

---

## Safety model

- **Read-only by default.** `fleet_request` refuses non-GET unless `confirm=true`.
- **Prefilled, never submitted.** `build_issue_url` returns a URL you review and click —
  the tool never posts to GitHub.
- **Pinned to the deployed revision.** Code tools default to the running build's rev, so
  you never analyze `main` by mistake.

---

## Things the tool can't do (human-in-the-loop)

These came up constantly in real sessions — the tool surfaces them, you act:

- **Device tokens** (`/device/{token}/...`): UUIDs with a ~hourly TTL, **not** fetchable
  via the admin API. Grab one from the **My Device page** network tab, or set it in the DB
  yourself:
  ```bash
  docker compose exec -T mysql bash -c 'echo "INSERT INTO fleet.host_device_auth (host_id, token) VALUES (<id>, \"<tok>\") ON DUPLICATE KEY UPDATE token=VALUES(token)" | MYSQL_PWD=toor mysql -uroot'
  ```
- **Tier switch (Free ↔ Premium):** resets sessions — re-run `make qa-auth` after.
- **GitOps tools need a matching `fleetctl`.** A version-mismatched `fleetctl` (e.g. 4.86
  client vs 4.87 server) produces garbage — build `fleetctl` from the same tree as the
  server.
- **Deployed build can be ahead of the DB schema** (missing migrations) → you may see raw
  DB errors; run `prepare db` / apply the missing migration. Don't misread it as a product bug.

## Limits to keep in mind

- **Headless ≠ real Chrome.** Some visual/timing bugs (sub-frame flashes, classic
  scrollbars) need a human eye in a real browser. The tool gathers evidence; you + the
  model own the verdict.
- **Locally-built, unpushed revisions** aren't in any clone — point `FLEET_REPO` at the
  build tree for `is_in_build`/`code_at_rev` to work on those.
