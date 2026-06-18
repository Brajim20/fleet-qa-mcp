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
		if _, err := page.Goto(pageURL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateNetworkidle,
		}); err != nil {
			// non-fatal: caller may still want to inspect
			_ = err
		}
	}
	return &Session{pw: pw, browser: b, page: page}, nil
}

// Eval runs a JS expression in the page and returns the (JSON-able) result.
func (s *Session) Eval(js string) (interface{}, error) { return s.page.Evaluate(js) }

// Screenshot writes a PNG and returns its path.
func (s *Session) Screenshot(path string) (string, error) {
	_, err := s.page.Screenshot(playwright.PageScreenshotOptions{Path: playwright.String(path)})
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
