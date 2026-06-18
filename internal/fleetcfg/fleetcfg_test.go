package fleetcfg

import "testing"

func TestResolveEnvOverride(t *testing.T) {
	t.Setenv("FLEET_URL", "https://example.test")
	t.Setenv("FLEET_TOKEN", "tok-123")

	inst, err := Resolve("default")
	if err != nil {
		t.Fatal(err)
	}
	if inst.URL != "https://example.test" {
		t.Errorf("URL = %q, want https://example.test", inst.URL)
	}
	if inst.Token != "tok-123" {
		t.Errorf("Token = %q", inst.Token)
	}
	if inst.Source != "env FLEET_URL" {
		t.Errorf("Source = %q", inst.Source)
	}
}

func TestCanRefresh(t *testing.T) {
	if !(&Instance{Email: "e@x", Password: "p"}).canRefresh() {
		t.Error("email+password should enable refresh")
	}
	if (&Instance{Email: "e@x"}).canRefresh() {
		t.Error("missing password should disable refresh")
	}
	if (&Instance{Password: "p"}).canRefresh() {
		t.Error("missing email should disable refresh")
	}
}
