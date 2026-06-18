package qa

import (
	"strings"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Self-service: editing software (\"oops\")": "self-service-editing-software-oops",
		"  Trim  Me  ": "trim-me",
		"!!!":          "regression",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slugify(strings.Repeat("x", 80)); len(got) > 50 {
		t.Errorf("slugify did not cap length: %d", len(got))
	}
}

func TestGenerateSpec(t *testing.T) {
	rep := &Report{
		Number: 47712, Title: `Self-service "categories" picker errors`,
		Group: "#g-software", Route: "/software", Version: "4.87.0-rc.123",
		Rev: "fb979d83011e66fa2992b63c4e56bb443837612e", Status: "Fixed", ReleaseStatus: "Unreleased",
	}
	path, content := GenerateSpec(rep)
	if !strings.HasPrefix(path, "tests/smoke/software/regression-47712-") || !strings.HasSuffix(path, ".spec.ts") {
		t.Errorf("unexpected path: %q", path)
	}
	for _, want := range []string{
		`import { test, expect } from "../../../helpers/authenticated-test"`,
		`#47712`,
		`await page.goto("/software")`,
		`Classification: Unreleased`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("generated spec missing %q\n---\n%s", want, content)
		}
	}
	// The title's double quotes must be escaped inside the describe() string.
	if strings.Contains(content, `describe("#47712 — Self-service "categories"`) {
		t.Error("unescaped double quote leaked into the describe() literal")
	}
}

func TestGroupToAreaFallback(t *testing.T) {
	rep := &Report{Number: 1, Title: "x", Group: "#g-unknown", Route: "/x"}
	path, _ := GenerateSpec(rep)
	if !strings.HasPrefix(path, "tests/smoke/smoke/") {
		t.Errorf("unknown group should fall back to smoke/: %q", path)
	}
}

func TestReleaseLabels(t *testing.T) {
	if got := releaseLabels(&Report{ReleaseStatus: "Unreleased"}); len(got) != 1 || got[0] != "~unreleased bug" {
		t.Errorf("unreleased labels = %v", got)
	}
	if got := releaseLabels(&Report{ReleaseStatus: "Released"}); got != nil {
		t.Errorf("released labels = %v, want nil", got)
	}
}

func TestStableTag(t *testing.T) {
	for _, ok := range []string{"fleet-v4.86.1", "fleet-v4.87.0"} {
		if !stableTag.MatchString(ok) {
			t.Errorf("%q should be a stable tag", ok)
		}
	}
	for _, no := range []string{"fleet-v4.87.0-rc1", "fleet-v4.87", "v4.86.1", "fleetctl-v1.0.0"} {
		if stableTag.MatchString(no) {
			t.Errorf("%q should NOT be a stable tag", no)
		}
	}
}
