package ghissue

import "testing"

func TestWorkflowStatus(t *testing.T) {
	cases := []struct {
		labels []string
		want   string
	}{
		{[]string{"bug", ":release", "#g-software"}, "Release"},
		{[]string{"bug", ":product", ":reproduce"}, "Product"}, // furthest-along wins
		{[]string{"bug", ":reproduce"}, "Reproduce"},
		{[]string{"bug", ":incoming"}, "Incoming"},
		{[]string{"bug", "~assisting qa"}, "In QA"},
		{[]string{"bug", "~frontend"}, "Triage"}, // no process label
		{nil, "Triage"},
	}
	for _, c := range cases {
		if got := WorkflowStatus(c.labels); got != c.want {
			t.Errorf("WorkflowStatus(%v) = %q, want %q", c.labels, got, c.want)
		}
	}
}

func TestProductGroup(t *testing.T) {
	i := &Issue{Labels: []string{"bug", "~frontend", "#g-mdm"}}
	if got := i.ProductGroup(); got != "#g-mdm" {
		t.Errorf("ProductGroup = %q, want #g-mdm", got)
	}
	if got := (&Issue{Labels: []string{"bug"}}).ProductGroup(); got != "" {
		t.Errorf("ProductGroup with no group = %q, want empty", got)
	}
}
