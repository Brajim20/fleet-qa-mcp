package qa

import (
	"reflect"
	"testing"
)

func TestParseIssueNumber(t *testing.T) {
	cases := map[string]int{
		"47712": 47712,
		"https://github.com/fleetdm/fleet/issues/47712": 47712,
		"#43310":    43310,
		"fleet#999": 999,
		"":          0,
		"no digits": 0,
	}
	for in, want := range cases {
		if got := parseIssueNumber(in); got != want {
			t.Errorf("parseIssueNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestExtractAPIPaths(t *testing.T) {
	body := "Backend: `GET /api/latest/fleet/software/self_service_categories?team_id=14` returns null.\n" +
		"Also see /api/latest/fleet/config for the tier."
	got := extractAPIPaths(body)
	want := []string{
		"/api/latest/fleet/software/self_service_categories?team_id=14",
		"/api/latest/fleet/config",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractAPIPaths = %v, want %v", got, want)
	}
}

func TestExtractSHAs(t *testing.T) {
	// Real-ish: a commit SHA (mixed hex) should match; a plain decimal id and a
	// UUID-with-dashes fragment should not pollute the result as one token.
	body := "Introduced by commit e4f1d2a9. Version 4.87.0. Pure number 1234567 is not a SHA."
	got := extractSHAs(body)
	if len(got) != 1 || got[0] != "e4f1d2a9" {
		t.Errorf("extractSHAs = %v, want [e4f1d2a9]", got)
	}
}

func TestExtractKeywordsPrefersCodeIdentifiers(t *testing.T) {
	// The component filename should rank above the snake_case symbol, and a
	// common English word like "returns" must never win.
	text := "Editing software errors the picker\n" +
		"`SoftwareOptionsSelector.tsx` calls res.self_service_categories.map and returns an error in showEmptyState."
	got := extractKeywords(text)
	if len(got) == 0 {
		t.Fatal("expected keywords, got none")
	}
	if got[0] != "SoftwareOptionsSelector" {
		t.Errorf("top keyword = %q, want SoftwareOptionsSelector (got %v)", got[0], got)
	}
	for _, k := range got {
		if k == "returns" {
			t.Errorf("common word %q should be filtered out (got %v)", k, got)
		}
	}
}

func TestGuessRoute(t *testing.T) {
	if r := guessRoute("the bug is on the /policies page"); r != "/policies" {
		t.Errorf("guessRoute = %q, want /policies", r)
	}
	if r := guessRoute("no route mentioned here"); r != "/dashboard" {
		t.Errorf("guessRoute fallback = %q, want /dashboard", r)
	}
}

func TestHostOf(t *testing.T) {
	for in, want := range map[string]string{
		"https://brayan.ngrok.app/":     "brayan.ngrok.app",
		"http://localhost:8080":         "localhost:8080",
		"https://dogfood.fleetdm.com//": "dogfood.fleetdm.com",
	} {
		if got := hostOf(in); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", in, got, want)
		}
	}
}
