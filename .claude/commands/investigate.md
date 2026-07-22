# /investigate — Fleet QA issue investigation

Run a full QA investigation for a Fleet GitHub issue number or URL.

## Usage

```
/investigate <issue-number-or-url> [mode]
```

**mode** (optional): `reproduce` | `testplan` | *(blank = general)*

## Workflow

### Step 1 — Orient yourself

Call `whoami` first to confirm the connected instance, deployed version, and git revision.
All tool calls implicitly target this instance and rev.

### Step 2 — Run the automated pipeline

Call `investigate` with the issue reference (and mode if specified).
This runs the full agentic pipeline:
- Fetches the issue from GitHub (title, labels, group, reporter, steps-to-reproduce)
- Hits Fleet API endpoints relevant to the issue
- Opens the affected UI route in real Chromium and runs JS probes
- Greps the deployed source for the root-cause code path
- Classifies the fix as Released / Unreleased
- Proposes a QA verdict (confirmed / cannot-reproduce / WAI / need-info)
- Builds a prefilled GitHub bug-report URL

The result is a structured report:
```
#<N>  <title>
<instance> · Fleet <version> (rev <sha>, <tier>)
<group> · reported by <user>

Evidence:
  [pass ] <step-title>                 <one-line-summary>
  [fail ] <step-title>                 <one-line-summary>
  ...

Classification: RELEASED — shipped since <version>
Proposed verdict: confirmed  (a human confirms)

Prefilled issue:
https://github.com/fleetdm/fleet/issues/new?...
```

### Step 3 — Deepen with individual tools (as needed)

Use the automated report as a foundation, then call individual tools to fill gaps:

| Situation | Tool |
|-----------|------|
| Need to read the buggy code at the deployed rev | `code_at_rev` |
| Need to find where a symbol is defined | `grep_code` |
| Need to know when a commit was introduced | `log_search` |
| Need to check if a fix PR is already in the build | `is_in_build` |
| Need to hit an API endpoint manually | `fleet_request` |
| Need to observe a UI element or run JS in the browser | `browser_eval` |
| Debugging a timing/flash/visual bug | `browser_sample_frames` |

### Step 4 — Synthesize verdict and QA comment

After collecting evidence, write a clear verdict block:

```
**Verdict**: confirmed | cannot-reproduce | WAI | need-info
**Tested on**: Fleet <version> (rev <sha>) — <instance-url>
**Summary**: <1-2 sentences on what you observed>
**Root cause** (if confirmed): <file:line — what the code does wrong>
**Released**: yes (since <version>) | no (caught pre-release)
```

### Step 5 — Draft the GitHub issue (if it's a new bug)

For newly found bugs (not ones you were given a GitHub issue number for),
call `build_issue_url` with the evidence you gathered to get a prefilled report URL:

```
build_issue_url(
  title    = "<concise bug title>",
  actual   = "<what actually happens>",
  steps    = "<numbered repro steps>",
  discovered = "<Fleet version, e.g. 4.87.0>",
  to_fix   = "<file and change needed, if known>",
  labels   = "bug,#g-<group>"
)
```

Present the URL to the user for review — **never open or submit it automatically**.

## Tips

- Always call `whoami` first — investigation tools use the deployed rev, not main.
- If `investigate` returns `Error: ...`, check if the issue number is correct and
  the instance is reachable (`fleet_request path=/api/v1/fleet/version`).
- For UI bugs, `browser_eval` with `shot_selector` produces a cropped screenshot
  of the specific element — much easier to read than a full-page screenshot.
- `browser_sample_frames` is the right tool for flicker/timing bugs: it records
  computed CSS props frame-by-frame so you can see the exact transition.
- When in doubt about whether a fix is already shipped, use `is_in_build <commit-sha>`.
