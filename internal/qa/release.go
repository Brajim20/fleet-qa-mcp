package qa

import (
	"fmt"
	"regexp"

	"github.com/Brajim20/fleet-qa-mcp/internal/gitcode"
)

// stableTag matches a shipped Fleet release tag (fleet-v4.86.1), excluding
// rc/beta/pre-release tags — only stable tags mean "released to customers".
var stableTag = regexp.MustCompile(`^fleet-v\d+\.\d+\.\d+$`)

// ClassifyRelease decides whether a commit shipped in a stable release. Returns
// ("Released", earliest-release) if any stable fleet-v* tag contains it, else
// ("Unreleased", "").
func (a *App) ClassifyRelease(commit string) (status, firstRelease string, err error) {
	if a.Repo == "" {
		return "", "", fmt.Errorf("no Fleet source repo")
	}
	if commit == "" || !gitcode.HasRev(a.Repo, commit) {
		return "", "", fmt.Errorf("commit not in checkout")
	}
	tags, err := gitcode.TagsContaining(a.Repo, commit, "fleet-v*")
	if err != nil {
		return "", "", err
	}
	for _, t := range tags { // version-sorted ascending → first stable is the earliest shipping release
		if stableTag.MatchString(t) {
			return "Released", t, nil
		}
	}
	return "Unreleased", "", nil
}

// stepRelease traces the commit that introduced `symbol` in the deployed history
// and classifies the bug as released vs unreleased — the call QA needs to make
// before filing (patch a shipped release vs. just fix before the next one).
func (a *App) stepRelease(rep *Report, symbol, pathspec string) StepResult {
	const kind, title, tool = "release", "Released or unreleased?", "tag --contains"
	if symbol == "" || a.Repo == "" || rep.Rev == "" {
		return StepResult{Kind: kind, Title: title, Tool: tool, Status: "info", Summary: "No traceable symbol — classify manually"}
	}
	commit, err := gitcode.IntroducingCommit(a.Repo, rep.Rev, symbol, pathspec)
	if err != nil || commit == "" {
		return StepResult{Kind: kind, Title: title, Tool: tool, Status: "info", Summary: "Couldn't locate the introducing commit for " + symbol}
	}
	rep.IntroCommit = commit
	status, first, cerr := a.ClassifyRelease(commit)
	if cerr != nil {
		return StepResult{Kind: kind, Title: title, Tool: tool, Status: "info", Summary: "Classification unavailable — " + firstLine(cerr.Error())}
	}
	rep.ReleaseStatus = status
	rep.FirstRelease = first
	if status == "Released" {
		return StepResult{
			Kind: kind, Title: title, Tool: tool, Status: "warn",
			Summary: fmt.Sprintf("RELEASED — shipped since %s (introduced %s)", first, short(commit)),
			Detail:  fmt.Sprintf("Symbol %q was introduced by commit %s, which is contained in stable release %s.\nThis bug reached customers — it likely needs a patch release, not just a fix on main.", symbol, commit, first),
		}
	}
	return StepResult{
		Kind: kind, Title: title, Tool: tool, Status: "ok",
		Summary: fmt.Sprintf("UNRELEASED — introduced %s, not in any fleet-v* release", short(commit)),
		Detail:  fmt.Sprintf("Symbol %q was introduced by commit %s, which is NOT contained in any stable fleet-v* tag.\nCaught before release — file with the ~unreleased bug label.", symbol, commit),
	}
}

// releaseLabels returns labels to add to the prefilled issue based on the
// classification (~unreleased bug for pre-release finds).
func releaseLabels(rep *Report) []string {
	if rep.ReleaseStatus == "Unreleased" {
		return []string{"~unreleased bug"}
	}
	return nil
}
