# Fleet QA Studio — product prototype

A self-contained, runnable prototype of the **Fleet QA Studio** product UI, implemented
from the Claude Design file `Fleet QA Studio.dc.html`.

It mirrors what the `fleet-qa-mcp` toolkit does, as a product: paste a GitHub issue →
reproduce on the live deployed build → root-cause in the deployed-revision source →
build-membership check → human-confirmed verdict → prefilled GitHub issue.

## Run it
Just open `index.html` in a browser (no build step, no network — React/ReactDOM/Babel
are vendored under `vendor/`). Or serve it:

```bash
cd studio && python3 -m http.server 8745   # → http://localhost:8745
```

## What's here
- `index.html` — the whole app (React, single file): dashboard, new-investigation,
  detail with a 7-step evidence timeline (issue / target / reproduce / frame-sampler /
  root-cause / build-check / verdict / draft), sticky verdict panel, draft modal, dark+light.
- `vendor/` — React 18, ReactDOM 18, Babel standalone (vendored so it runs offline).

All data is mocked (issues #47712, #43310, #46920, …) — this is a UI prototype, not wired
to a live instance.
