package ghissue

import (
	"net/url"
	"strings"
	"testing"
)

func TestBugReportURL(t *testing.T) {
	br := BugReport{
		Title: "Card corners poke out", Actual: "corners stick out", Steps: "1. open page",
		Discovered: "4.87.0-rc", Reproduced: "4.87.0-rc", BrowserOS: "Chromium/macOS",
		Labels: []string{"~frontend", "#g-software"},
	}
	u := br.URL()
	if !strings.HasPrefix(u, "https://github.com/fleetdm/fleet/issues/new?") {
		t.Fatalf("unexpected prefix: %s", u)
	}
	p, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	q := p.Query()
	if q.Get("title") != "Card corners poke out" {
		t.Errorf("title = %q", q.Get("title"))
	}
	labels := q.Get("labels")
	for _, want := range []string{"bug", ":product", "~frontend", "#g-software"} {
		if !strings.Contains(labels, want) {
			t.Errorf("labels %q missing %q", labels, want)
		}
	}
	body := q.Get("body")
	for _, want := range []string{"4.87.0-rc", "Actual behavior", "corners stick out", "Steps to reproduce", "1. open page"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
