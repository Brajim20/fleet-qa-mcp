// Package fleetcfg resolves the target Fleet instance (URL + token) and the
// Fleet source checkout, so the same committed .mcp.json works for every user.
//
// Resolution precedence for the instance:
//  1. FLEET_URL / FLEET_TOKEN env (CI / explicit override)
//  2. ~/.fleet/config (the file `fleetctl` writes) — the selected context
//  3. https://localhost:8080 fallback (a fresh `make serve`)
package fleetcfg

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Instance is a resolved Fleet target.
type Instance struct {
	URL      string
	Token    string
	SkipTLS  bool
	Source   string // where the values came from, for `whoami`
	httpc    *http.Client
}

type fleetCtxFile struct {
	Contexts map[string]struct {
		Address       string `yaml:"address"`
		Token         string `yaml:"token"`
		TLSSkipVerify bool   `yaml:"tls-skip-verify"`
	} `yaml:"contexts"`
}

// Resolve picks the instance using the precedence above. ctxName selects which
// ~/.fleet/config context to read (default "default").
func Resolve(ctxName string) (*Instance, error) {
	if ctxName == "" {
		ctxName = "default"
	}
	inst := &Instance{}

	if u := os.Getenv("FLEET_URL"); u != "" {
		inst.URL, inst.Token, inst.Source = u, os.Getenv("FLEET_TOKEN"), "env FLEET_URL"
	} else if u, t, skip, ok := fromFleetctlConfig(ctxName); ok {
		inst.URL, inst.Token, inst.SkipTLS, inst.Source = u, t, skip, "~/.fleet/config["+ctxName+"]"
	} else {
		inst.URL, inst.SkipTLS, inst.Source = "https://localhost:8080", true, "default fallback"
	}

	inst.httpc = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: inst.SkipTLS}, //nolint:gosec // dev tunnels use self-signed/ngrok
		},
	}
	return inst, nil
}

func fromFleetctlConfig(ctxName string) (url, token string, skipTLS, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", false, false
	}
	b, err := os.ReadFile(filepath.Join(home, ".fleet", "config"))
	if err != nil {
		return "", "", false, false
	}
	var c fleetCtxFile
	if err := yaml.Unmarshal(b, &c); err != nil {
		return "", "", false, false
	}
	cx, ok := c.Contexts[ctxName]
	if !ok || cx.Address == "" {
		return "", "", false, false
	}
	return cx.Address, cx.Token, cx.TLSSkipVerify, true
}

// Version is the subset of GET /api/latest/fleet/version we care about.
type Version struct {
	Version  string `json:"version"`
	Branch   string `json:"branch"`
	Revision string `json:"revision"`
}

// DeployedVersion returns the running build's version + revision. Pin all code
// reads to .Revision — never analyze `main` blindly.
func (i *Instance) DeployedVersion() (*Version, error) {
	body, status, err := i.Request("GET", "/api/latest/fleet/version", nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("version endpoint returned %d (token expired? run `make qa-auth`)", status)
	}
	var v Version
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Request performs an authenticated REST call against the instance and returns
// the raw body + HTTP status. A 401 means the admin token expired.
func (i *Instance) Request(method, path string, body io.Reader) ([]byte, int, error) {
	req, err := http.NewRequest(method, i.URL+path, body)
	if err != nil {
		return nil, 0, err
	}
	if i.Token != "" {
		req.Header.Set("Authorization", "Bearer "+i.Token)
	}
	// ngrok free shows an interstitial to browsers; this header bypasses it.
	req.Header.Set("ngrok-skip-browser-warning", "true")
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.httpc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

const managedRepoDir = ".fleet-src"

// ResolveRepo returns a path to a Fleet source checkout for the code tools, or
// "" (with a nil error) if none is available — code tools then report a clear
// "set FLEET_REPO" message instead of the server hanging.
//
// It NEVER clones implicitly: a silent multi-GB clone of fleetdm/fleet mid-call
// is a terrible surprise. Use ProvisionRepo (an explicit setup step) for that.
// Precedence: FLEET_REPO env -> an existing managed clone under ./.fleet-src.
func ResolveRepo() (string, error) {
	if r := os.Getenv("FLEET_REPO"); r != "" {
		if _, err := os.Stat(filepath.Join(r, ".git")); err == nil {
			return r, nil
		}
		return "", fmt.Errorf("FLEET_REPO=%q is not a git checkout", r)
	}
	if _, err := os.Stat(filepath.Join(managedRepoDir, ".git")); err == nil {
		return managedRepoDir, nil
	}
	return "", nil // no repo; code tools will say "set FLEET_REPO or run --provision-repo"
}

// ProvisionRepo clones fleetdm/fleet into the managed dir (explicit, opt-in —
// e.g. `fleet-qa-mcp --provision-repo`). Slow; only for users with no checkout.
func ProvisionRepo() (string, error) {
	if _, err := os.Stat(filepath.Join(managedRepoDir, ".git")); err == nil {
		return managedRepoDir, nil // already provisioned
	}
	cmd := exec.Command("git", "clone", "https://github.com/fleetdm/fleet.git", managedRepoDir)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("clone failed (set FLEET_REPO to an existing checkout instead): %w", err)
	}
	return managedRepoDir, nil
}
