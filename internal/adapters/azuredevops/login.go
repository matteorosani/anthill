package azuredevops

import "context"

// Login returns the authenticated user's login, the first part of the
// instance id (spec work-claiming, "Instance identity"). It is read once
// and then reused.
func (a *Adapter) Login(ctx context.Context) (string, error) {
	return a.currentLogin(ctx)
}
