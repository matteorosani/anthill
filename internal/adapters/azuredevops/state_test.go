package azuredevops

import (
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestItemState(t *testing.T) {
	tests := []struct {
		category, state, reason string
		want                    domain.ItemState
	}{
		{"Proposed", "New", "", domain.StateOpen},
		{"InProgress", "Active", "", domain.StateOpen},
		{"Resolved", "Resolved", "Fixed", domain.StateOpen},
		{"Completed", "Closed", "Fixed", domain.StateClosedCompleted},
		{"Completed", "Done", "Work finished", domain.StateClosedCompleted},
		{"Completed", "Closed", "Duplicate", domain.StateClosedNotPlanned},
		{"Completed", "Closed", "won't fix", domain.StateClosedNotPlanned},
		{"Completed", "Closed", "Obsolete", domain.StateClosedNotPlanned},
		{"Completed", "Closed", "Cut", domain.StateClosedNotPlanned},
		{"Completed", "Done", "Abandoned", domain.StateClosedNotPlanned},
		{"Removed", "Removed", "", domain.StateClosedNotPlanned},
		// A state the type does not list falls back on its name.
		{"", "Closed", "", domain.StateClosedCompleted},
		{"", "Removed", "", domain.StateClosedNotPlanned},
		{"", "Doing", "", domain.StateOpen},
	}
	for _, tt := range tests {
		if got := itemState(tt.category, tt.state, tt.reason); got != tt.want {
			t.Errorf("itemState(%q, %q, %q) = %q, want %q", tt.category, tt.state, tt.reason, got, tt.want)
		}
	}
}

func TestItemType(t *testing.T) {
	for in, want := range map[string]domain.ItemType{
		"Bug": domain.TypeBug, "Task": domain.TypeTask, "User Story": domain.TypeFeature,
		"Product Backlog Item": domain.TypeFeature, "Requirement": domain.TypeFeature, "Feature": domain.TypeFeature,
		"Epic": domain.TypeFeature, "Issue": domain.TypeFeature, "Test Case": domain.TypeOther,
	} {
		if got := itemType(in); got != want {
			t.Errorf("itemType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLooksLikeJWT(t *testing.T) {
	for s, want := range map[string]bool{
		"eyJhbGciOiJSUzI1NiJ9.eyJleHAiOjF9.c2ln":              true,
		"52charsofbase32patlookingxxxxxxxxxxxxxxxxxxxxxxxxxx": false,
		"eyJonly.two": false,
		"abc.def.ghi": false,
	} {
		if got := looksLikeJWT(s); got != want {
			t.Errorf("looksLikeJWT(%q) = %v", s, got)
		}
	}
}
