package qa

import (
	"encoding/json"
	"testing"
)

// TestAgentTools guards the tool wiring: the model must be offered exactly the
// read-only tools plus submit_verdict, and every input schema must be valid
// JSON (the API rejects malformed schemas).
func TestAgentTools(t *testing.T) {
	tools := agentTools()
	want := map[string]bool{
		"fleet_request": true, "browser_eval": true, "grep_code": true,
		"code_at_rev": true, "is_in_build": true, "log_search": true, "submit_verdict": true,
	}
	got := map[string]bool{}
	for _, tl := range tools {
		got[tl.Name] = true
		if tl.Description == "" {
			t.Errorf("tool %q has no description", tl.Name)
		}
		if _, err := json.Marshal(tl.InputSchema); err != nil {
			t.Errorf("tool %q schema not marshalable: %v", tl.Name, err)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing expected tool %q", name)
		}
	}
	// No write/confirm-style tool should ever be exposed to the model.
	for _, tl := range tools {
		if tl.Name == "fleet_request" {
			b, _ := json.Marshal(tl.InputSchema)
			if json.Valid(b) && contains(string(b), "confirm") {
				t.Error("fleet_request tool must not expose a confirm/write parameter")
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestEnsureSlash(t *testing.T) {
	for in, want := range map[string]string{"": "/", "software": "/software", "/policies": "/policies"} {
		if got := ensureSlash(in); got != want {
			t.Errorf("ensureSlash(%q) = %q, want %q", in, got, want)
		}
	}
}
