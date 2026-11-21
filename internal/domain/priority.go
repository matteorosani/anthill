package domain

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"
)

// NoPriority is what the proposal list shows for an item that has
// neither a provider priority nor a priority label.
const NoPriority = "-"

// PriorityLabel finds the highest-ranked priority label the item carries
// (spec task-eligibility, "Proposal list"). priorityLabels runs from the
// highest priority to the lowest, and labels match without regard to
// case, as they do on both providers. It returns the label's index in
// priorityLabels and the label as configured, or -1 and "" when the item
// carries none.
func PriorityLabel(labels, priorityLabels []string) (int, string) {
	for i, p := range priorityLabels {
		if HasLabel(labels, p) {
			return i, p
		}
	}
	return -1, ""
}

// ShowPriority is the priority the proposal list shows for an item:
// P<n> for a provider priority, otherwise its highest-ranked priority
// label, otherwise NoPriority.
func ShowPriority(item WorkItem, priorityLabels []string) string {
	if item.Priority > 0 {
		return "P" + strconv.Itoa(item.Priority)
	}
	if _, label := PriorityLabel(item.Labels, priorityLabels); label != "" {
		return label
	}
	return NoPriority
}

// SortProposals orders proposals by priority (spec task-eligibility,
// "Proposal list"): first by provider priority, items that have one
// before items that do not and a lower number first; then by priority
// label, items that carry one before items that do not and a label
// earlier in the item's priority-labels before a later one; then by age,
// oldest first. Items equal in all three keep the order of their
// references. priorityLabels gives the priority-labels setting of an
// item's target repository; nil means none.
func SortProposals(items []WorkItem, priorityLabels func(WorkItem) []string) {
	type key struct {
		item     WorkItem
		provider int
		label    int
	}
	// rank maps "none" (0 or -1) after every real value.
	rank := func(v, none int) int {
		if v == none {
			return math.MaxInt
		}
		return v
	}
	keys := make([]key, len(items))
	for i, item := range items {
		var labels []string
		if priorityLabels != nil {
			labels = priorityLabels(item)
		}
		label, _ := PriorityLabel(item.Labels, labels)
		keys[i] = key{item: item, provider: rank(max(item.Priority, 0), 0), label: rank(label, -1)}
	}
	slices.SortStableFunc(keys, func(a, b key) int {
		if c := cmp.Compare(a.provider, b.provider); c != 0 {
			return c
		}
		if c := cmp.Compare(a.label, b.label); c != 0 {
			return c
		}
		if c := a.item.CreatedAt.Compare(b.item.CreatedAt); c != 0 {
			return c
		}
		if c := strings.Compare(a.item.Ref.Repo.String(), b.item.Ref.Repo.String()); c != 0 {
			return c
		}
		return cmp.Compare(a.item.Ref.Number, b.item.Ref.Number)
	})
	for i, k := range keys {
		items[i] = k.item
	}
}
