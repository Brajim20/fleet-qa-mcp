// Package ghissue builds *prefilled* GitHub issue URLs from the Fleet
// bug-report template. It never submits — the user reviews and clicks Submit.
package ghissue

import "net/url"

// BugReport mirrors the Fleet bug-report.md template fields.
type BugReport struct {
	Title      string
	Discovered string // Fleet version observed on
	Reproduced string
	BrowserOS  string
	Actual     string // 💥 Actual behavior
	ToFix      string // 🛠️ To fix (optional)
	Steps      string // 🧑‍💻 Steps to reproduce
	MoreInfo   string // 🕯️ More info (optional)
	Labels     []string
}

// URL renders the prefilled "new issue" URL for fleetdm/fleet.
func (b BugReport) URL() string {
	body := "**Fleet versions**\n" +
		"  - *Discovered:* " + b.Discovered + "\n" +
		"  - *Reproduced:* " + b.Reproduced + "\n\n" +
		"**Web browser and operating system**: " + b.BrowserOS + "\n\n" +
		"<hr/>\n\n### 💥  Actual behavior\n" + b.Actual + "\n\n"
	if b.ToFix != "" {
		body += "### 🛠️ To fix\n" + b.ToFix + "\n\n"
	}
	body += "### 🧑‍💻  Steps to reproduce\n" + b.Steps + "\n\n"
	body += "### 🕯️ More info _(optional)_\n"
	if b.MoreInfo != "" {
		body += b.MoreInfo
	} else {
		body += "N/A"
	}

	labels := "bug,:product"
	for _, l := range b.Labels {
		labels += "," + l
	}

	q := url.Values{}
	q.Set("title", b.Title)
	q.Set("labels", labels)
	q.Set("body", body)
	return "https://github.com/fleetdm/fleet/issues/new?" + q.Encode()
}
