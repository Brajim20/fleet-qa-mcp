package ghissue

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// authToken resolves a GitHub token: GITHUB_TOKEN env first, else the local
// `gh` CLI's token. Board statuses need a token with the read:project scope.
func authToken() string {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ProjectStatuses returns each issue's GitHub Project board Status (the column:
// "In progress", "Awaiting QA", "Done", …), keyed by issue number. Requires a
// token with read:project; returns an empty map (no error surfaced to the user)
// when the token is missing or lacks the scope, so the caller falls back to the
// label-derived status. Best-effort — a board an issue isn't on simply yields
// no status for it.
func ProjectStatuses(numbers []int, preferGroup string) map[int]string {
	out := map[int]string{}
	token := authToken()
	if token == "" || len(numbers) == 0 {
		return out
	}
	// Chunk to keep each GraphQL query within complexity limits.
	for start := 0; start < len(numbers); start += 25 {
		end := start + 25
		if end > len(numbers) {
			end = len(numbers)
		}
		mergeProjectStatuses(out, token, numbers[start:end], preferGroup)
	}
	return out
}

func mergeProjectStatuses(into map[int]string, token string, numbers []int, preferGroup string) {
	var q strings.Builder
	q.WriteString(`query { repository(owner:"fleetdm", name:"fleet") {`)
	for _, n := range numbers {
		fmt.Fprintf(&q, ` i%d: issue(number:%d){ projectItems(first:8){ nodes { project{title} s: fieldValueByName(name:"Status"){ ... on ProjectV2ItemFieldSingleSelectValue { name } } } } }`, n, n)
	}
	q.WriteString(` } }`)

	body, _ := json.Marshal(map[string]string{"query": q.String()})
	req, err := http.NewRequest("POST", "https://api.github.com/graphql", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ghClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var r struct {
		Data struct {
			Repository map[string]struct {
				ProjectItems struct {
					Nodes []struct {
						Project struct {
							Title string `json:"title"`
						} `json:"project"`
						S *struct {
							Name string `json:"name"`
						} `json:"s"`
					} `json:"nodes"`
				} `json:"projectItems"`
			} `json:"repository"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	for alias, item := range r.Data.Repository {
		n := 0
		if _, err := fmt.Sscanf(alias, "i%d", &n); err != nil || n == 0 {
			continue
		}
		// Prefer the selected group's board (an issue can sit on several);
		// otherwise take the first board that has a Status.
		first := ""
		for _, node := range item.ProjectItems.Nodes {
			if node.S == nil || node.S.Name == "" {
				continue
			}
			st := normalizeBoardStatus(node.S.Name)
			if first == "" {
				first = st
			}
			if preferGroup != "" && strings.Contains(node.Project.Title, preferGroup) {
				first = st
				break
			}
		}
		if first != "" {
			into[n] = first
		}
	}
}

// normalizeBoardStatus drops the leading emoji/symbol prefix Fleet uses on its
// Project board statuses (plus any zero-width / variation-selector marks, which
// all sort before the first ASCII letter), leaving clean text:
// "✅ Ready for release" → "Ready for release", "🦤 In review" → "In review".
func normalizeBoardStatus(s string) string {
	for i, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return strings.TrimSpace(s[i:])
		}
	}
	return strings.TrimSpace(s)
}
