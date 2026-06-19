// Package browser drives a real Chromium via playwright-go for repros, DOM
// measurement, screenshots, and per-frame sampling of timing/visual bugs.
//
// Auth: reuses a storageState file keyed per-hostname, so switching instances
// doesn't cross sessions. Bootstrap it once with `--auth`.
package browser

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/playwright-community/playwright-go"
)

// Install downloads the Chromium driver (one-time setup).
func Install() error { return playwright.Install() }

// SaveAuthState injects the admin token as the __Host-token cookie and writes a
// reusable storageState for this instance (keyed per-host). Mirrors e2e setup.
func SaveAuthState(instanceURL, token string) error {
	pw, err := playwright.Run()
	if err != nil {
		return err
	}
	defer pw.Stop()
	b, err := pw.Chromium.Launch()
	if err != nil {
		return err
	}
	defer b.Close()
	ctx, err := b.NewContext(playwright.BrowserNewContextOptions{
		ExtraHttpHeaders: map[string]string{"ngrok-skip-browser-warning": "true"},
	})
	if err != nil {
		return err
	}
	u, _ := url.Parse(instanceURL)
	if err := ctx.AddCookies([]playwright.OptionalCookie{{
		Name:     "__Host-token",
		Value:    token,
		Domain:   playwright.String(u.Hostname()),
		Path:     playwright.String("/"),
		Secure:   playwright.Bool(true),
		SameSite: playwright.SameSiteAttributeLax,
	}}); err != nil {
		return err
	}
	_, err = ctx.StorageState(authStatePath(instanceURL))
	return err
}

func authStatePath(instanceURL string) string {
	u, _ := url.Parse(instanceURL)
	h := sha1.Sum([]byte(u.Host)) //nolint:gosec // not security-sensitive, just a cache key
	_ = os.MkdirAll(".auth", 0o755)
	return filepath.Join(".auth", "state-"+hex.EncodeToString(h[:6])+".json")
}

// Session is an open browser context for one instance.
type Session struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	page    playwright.Page
}

// Open launches headless Chromium with the per-host stored session and the
// ngrok bypass header, and navigates to pageURL (waiting for networkidle).
func Open(instanceURL, pageURL string) (*Session, error) {
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("playwright run (did you `make qa-setup`?): %w", err)
	}
	b, err := pw.Chromium.Launch()
	if err != nil {
		pw.Stop()
		return nil, err
	}
	opts := playwright.BrowserNewContextOptions{
		ExtraHttpHeaders: map[string]string{"ngrok-skip-browser-warning": "true"},
		Viewport:         &playwright.Size{Width: 1400, Height: 900},
	}
	if sp := authStatePath(instanceURL); fileExists(sp) {
		opts.StorageStatePath = playwright.String(sp)
	}
	ctx, err := b.NewContext(opts)
	if err != nil {
		b.Close()
		pw.Stop()
		return nil, err
	}
	page, err := ctx.NewPage()
	if err != nil {
		b.Close()
		pw.Stop()
		return nil, err
	}
	if pageURL != "" {
		// Wait for "load", NOT networkidle: Fleet is an SPA that polls
		// continuously, so networkidle never settles — Goto would hang and
		// leave the page mid-navigation (which then breaks Screenshot/Eval).
		if _, err := page.Goto(pageURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateLoad,
			Timeout:   playwright.Float(20000),
		}); err != nil {
			_ = err // non-fatal: caller may still want to inspect
		}
		// Brief settle for client-side render.
		page.WaitForTimeout(1500)
	}
	return &Session{pw: pw, browser: b, page: page}, nil
}

// Eval runs a JS expression in the page and returns the (JSON-able) result.
func (s *Session) Eval(js string) (interface{}, error) { return s.page.Evaluate(js) }

// sampleJS runs a requestAnimationFrame loop for durationMs, recording the
// chosen computed-style props (or "text") of each selector every frame.
// Captures a baseline, fires the optional trigger, then samples across the
// transition — ideal for flashes/desyncs that single screenshots miss.
const sampleJS = `(args) => new Promise((resolve) => {
  const {selectors, props, durationMs, trigger} = args;
  const samples = [];
  const t0 = performance.now();
  const snap = () => {
    const row = {t: Math.round(performance.now() - t0)};
    for (const sel of selectors) {
      const e = document.querySelector(sel);
      if (!e) { row[sel] = null; continue; }
      const cs = getComputedStyle(e);
      const o = {};
      for (const p of props) o[p] = (p === 'text') ? (e.textContent||'').trim().slice(0,50) : cs.getPropertyValue(p);
      row[sel] = o;
    }
    samples.push(row);
  };
  snap();                                   // baseline (~t=0)
  if (trigger) { try { eval(trigger); } catch (e) { samples.push({trigger_error: String(e)}); } }
  const loop = () => {
    snap();
    if (performance.now() - t0 < durationMs) requestAnimationFrame(loop);
    else resolve(samples);
  };
  requestAnimationFrame(loop);
})`

// SampleFrames records per-frame values of selectors over durationMs, optionally
// after firing a JS trigger (e.g. a theme toggle or a click).
func (s *Session) SampleFrames(selectors, props []string, durationMs int, trigger string) (interface{}, error) {
	return s.page.Evaluate(sampleJS, map[string]interface{}{
		"selectors": selectors, "props": props, "durationMs": durationMs, "trigger": trigger,
	})
}

// Screenshot writes a PNG and returns its path. The default (empty selector,
// fullPage=false) captures the current viewport — which misses bugs that are
// below the fold or are a small element lost in a full-page shot. The options
// make the image actually show the bug:
//   - selector set, highlight=false: scroll the element into view and crop the
//     shot to JUST that element ("the actual bug itself").
//   - selector set, highlight=true: scroll it into view, draw a red outline
//     around it, and capture the viewport (the bug in context).
//   - selector empty: capture the viewport, or the whole scrollable page when
//     fullPage=true.
func (s *Session) Screenshot(path, selector string, fullPage, highlight bool) (string, error) {
	if selector != "" {
		loc := s.page.Locator(selector).First()
		// Scroll it in so it's rendered/visible before we capture it.
		_ = loc.ScrollIntoViewIfNeeded(playwright.LocatorScrollIntoViewIfNeededOptions{
			Timeout: playwright.Float(4000),
		})
		if highlight {
			// Outline the element, then shoot the viewport so the bug is shown
			// in context (useful when the surrounding layout matters).
			_, _ = s.page.Evaluate(`(sel) => {
				const e = document.querySelector(sel);
				if (e) { e.style.outline = '3px solid #ff0044'; e.style.outlineOffset = '2px'; e.scrollIntoView({block:'center'}); }
			}`, selector)
			s.page.WaitForTimeout(150)
			_, err := s.page.Screenshot(playwright.PageScreenshotOptions{Path: playwright.String(path)})
			return path, err
		}
		// Crop to just the element.
		if _, err := loc.Screenshot(playwright.LocatorScreenshotOptions{Path: playwright.String(path)}); err != nil {
			return path, err
		}
		return path, nil
	}
	_, err := s.page.Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(path),
		FullPage: playwright.Bool(fullPage),
	})
	return path, err
}

// Close releases the browser and driver.
func (s *Session) Close() {
	if s.browser != nil {
		s.browser.Close()
	}
	if s.pw != nil {
		s.pw.Stop()
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
