// Package fleetcfg resolves the target Fleet instance (URL + token) and the
// Fleet source checkout, so the same committed .mcp.json works for every user.
//
// Resolution precedence for the instance:
//  1. FLEET_URL / FLEET_TOKEN env (CI / explicit override)
//  2. ~/.fleet/config (the file `fleetctl` writes) — the selected context
//  3. https://localhost:8080 fallback (a fresh `make serve`)
package fleetcfg

import (
	"bytes"
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
	Email    string // for token auto-refresh on 401
	Password string // from FLEET_PASSWORD; never persisted
	SkipTLS  bool
	Source   string
	httpc    *http.Client
}

type fleetCtxFile struct {
	Contexts map[string]struct {
		Address       string `yaml:"address"`
		Email         string `yaml:"email"`
		Token         string `yaml:"token"`
		TLSSkipVerify bool   `yaml:"tls-skip-verify"`
	} `yaml:"contexts"`
}

// Resolve picks the instance using the precedence above.
func Resolve(ctxName string) (*Instance, error) {
	if ctxName == "" {
		ctxName = "default"
	}
	inst := &Instance{Password: os.Getenv("FLEET_PASSWORD")}

	if u := os.Getenv("FLEET_URL"); u != "" {
		inst.URL, inst.Token, inst.Source = u, os.Getenv("FLEET_TOKEN"), "env FLEET_URL"
		inst.Email = os.Getenv("FLEET_EMAIL")
	} else if c, ok := fromFleetctlConfig(ctxName); ok {
		inst.URL, inst.Token, inst.Email, inst.SkipTLS = c.addr, c.token, c.email, c.skip
		inst.Source = "~/.fleet/config[" + ctxName + "]"
	} else {
		inst.URL, inst.SkipTLS, inst.Source = "https://localhost:8080", true, "default fallback"
	}
	if e := os.Getenv("FLEET_EMAIL"); e != "" {
		inst.Email = e // explicit override
	}

	inst.httpc = &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: inst.SkipTLS}}, //nolint:gosec // dev tunnels
	}
	return inst, nil
}

type ctxVals struct {
	addr, email, token string
	skip               bool
}

func fromFleetctlConfig(ctxName string) (ctxVals, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return ctxVals{}, false
	}
	b, err := os.ReadFile(filepath.Join(home, ".fleet", "config"))
	if err != nil {
		return ctxVals{}, false
	}
	var c fleetCtxFile
	if err := yaml.Unmarshal(b, &c); err != nil {
		return ctxVals{}, false
	}
	cx, ok := c.Contexts[ctxName]
	if !ok || cx.Address == "" {
		return ctxVals{}, false
	}
	return ctxVals{addr: cx.Address, email: cx.Email, token: cx.Token, skip: cx.TLSSkipVerify}, true
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
		return nil, fmt.Errorf("version endpoint returned %d", status)
	}
	var v Version
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// Request performs an authenticated REST call. On a 401 it transparently
// re-logs-in (if email+FLEET_PASSWORD are available) and retries once, so a
// teammate's hourly token expiry doesn't interrupt a session.
func (i *Instance) Request(method, path string, body io.Reader) ([]byte, int, error) {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = io.ReadAll(body)
	}
	out, status, err := i.do(method, path, bodyBytes)
	if err != nil {
		return nil, status, err
	}
	if status == 401 && i.canRefresh() {
		if lerr := i.login(); lerr == nil {
			out, status, err = i.do(method, path, bodyBytes)
		}
	}
	return out, status, err
}

func (i *Instance) canRefresh() bool { return i.Email != "" && i.Password != "" }

func (i *Instance) do(method, path string, body []byte) ([]byte, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, i.URL+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if i.Token != "" {
		req.Header.Set("Authorization", "Bearer "+i.Token)
	}
	req.Header.Set("ngrok-skip-browser-warning", "true") // bypass ngrok interstitial
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.httpc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode, nil
}

// login exchanges email+password for a fresh session token (in-memory only).
func (i *Instance) login() error {
	payload, _ := json.Marshal(map[string]string{"email": i.Email, "password": i.Password})
	b, status, err := i.do("POST", "/api/latest/fleet/login", payload)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("re-login failed: HTTP %d", status)
	}
	var r struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Token == "" {
		return fmt.Errorf("re-login: no token in response")
	}
	i.Token = r.Token
	return nil
}

const managedRepoDir = ".fleet-src"

// ResolveRepo returns a Fleet source checkout for the code tools, or "" (nil
// error) if none — code tools then report a clear message. NEVER clones
// implicitly (a silent multi-GB clone mid-call is a terrible surprise); use
// ProvisionRepo for that.
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
	return "", nil
}

// ProvisionRepo clones fleetdm/fleet into the managed dir (explicit, opt-in).
func ProvisionRepo() (string, error) {
	if _, err := os.Stat(filepath.Join(managedRepoDir, ".git")); err == nil {
		return managedRepoDir, nil
	}
	cmd := exec.Command("git", "clone", "https://github.com/fleetdm/fleet.git", managedRepoDir)
	cmd.Stderr, cmd.Stdout = os.Stderr, os.Stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("clone failed (set FLEET_REPO to an existing checkout instead): %w", err)
	}
	return managedRepoDir, nil
}
