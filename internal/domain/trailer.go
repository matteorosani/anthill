package domain

import (
	"errors"
	"fmt"
	"strings"
)

// The commit trailers that make ant commits traceable (spec
// branching-policy, "Commit identity and traceability").
const (
	TrailerItem     = "Anthill-Item"
	TrailerAgent    = "Anthill-Agent"
	TrailerInstance = "Anthill-Instance"
)

// Trailers are the values an ant's commits carry, such as gh-12,
// claude-code and mrosani@LAPTOP-42.
type Trailers struct {
	Item     string
	Agent    string
	Instance string
}

// NewTrailers returns the trailers of the ant for item, run by runner on
// instance.
func NewTrailers(item Ref, runner Runner, instance string) Trailers {
	return Trailers{Item: item.String(), Agent: string(runner), Instance: instance}
}

// Validate checks that every value is set and fits on one trailer line.
func (t Trailers) Validate() error {
	var errs []error
	for _, f := range []struct{ key, value string }{
		{TrailerItem, t.Item},
		{TrailerAgent, t.Agent},
		{TrailerInstance, t.Instance},
	} {
		switch {
		case strings.TrimSpace(f.value) == "":
			errs = append(errs, fmt.Errorf("trailer %s is empty", f.key))
		case strings.ContainsAny(f.value, "\r\n") || strings.TrimSpace(f.value) != f.value:
			errs = append(errs, fmt.Errorf("trailer %s %q must be one line without surrounding spaces", f.key, f.value))
		}
	}
	return errors.Join(errs...)
}

// Owner returns who the trailers say a commit belongs to.
func (t Trailers) Owner() BranchOwner {
	return BranchOwner{Item: t.Item, Instance: t.Instance}
}

// BranchOwner is the Anthill-Item and Anthill-Instance pair found on a
// commit: the item and the instance whose ant made it.
type BranchOwner struct {
	Item     string
	Instance string
}
