package ghissue

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Issue is the subset of a GitHub issue we use during an investigation.
type Issue struct {
	Number   int      `json:"number"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Reporter string   `json:"reporter"`
	State    string   `json:"state"`
	Labels   []string `json:"labels"`
	HTMLURL  string   `json:"html_url"`
}

// ghIssueResp mirrors the GitHub REST API issue payload.
type ghIssueResp struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

var ghClient = &http.Client{Timeout: 20 * time.Second}

// Fetch reads a public fleetdm/fleet issue by number. No token is required for
// public repos; GITHUB_TOKEN (if set) raises the rate limit and is sent as a
// bearer. Read-only.
func Fetch(number int) (*Issue, error) {
	if number <= 0 {
		return nil, fmt.Errorf("invalid issue number %d", number)
	}
	url := fmt.Sprintf("https://api.github.com/repos/fleetdm/fleet/issues/%d", number)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := ghClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case 200:
		// ok
	case 404:
		return nil, fmt.Errorf("issue #%d not found", number)
	case 403:
		return nil, fmt.Errorf("GitHub rate limit hit (set GITHUB_TOKEN to raise it)")
	default:
		return nil, fmt.Errorf("GitHub returned HTTP %d", resp.StatusCode)
	}
	var r ghIssueResp
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(r.Labels))
	for _, l := range r.Labels {
		labels = append(labels, l.Name)
	}
	return &Issue{
		Number:   r.Number,
		Title:    r.Title,
		Body:     strings.ReplaceAll(r.Body, "\r\n", "\n"),
		Reporter: r.User.Login,
		State:    r.State,
		Labels:   labels,
		HTMLURL:  r.HTMLURL,
	}, nil
}

// ProductGroup returns the first "#g-" group label (Fleet's owning-group
// convention), or "" if none.
func (i *Issue) ProductGroup() string {
	for _, l := range i.Labels {
		if strings.HasPrefix(l, "#g-") {
			return l
		}
	}
	return ""
}
