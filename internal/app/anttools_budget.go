package app

import (
	"context"

	"github.com/matteorosani/anthill/internal/ports"
)

// WatchBudgets sets who is told when an ant's budget stops counting,
// counts again or grows through the ant tools: when request_extension
// starts or stops waiting, and when an extension is granted. The
// supervisor's timekeeper watches them to move the run's deadline
// (design D21). It is set after both services exist, as each needs the
// other.
func (t *AntTools) WatchBudgets(w BudgetWatcher) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.budgets = w
}

// budgetChanged tells the watcher, if any. Its error is not the tool
// call's: the call has done what it was asked, and the executor still
// ends the run at the deadline it had.
func (t *AntTools) budgetChanged(ctx context.Context, id ports.AntID) {
	t.mu.Lock()
	w := t.budgets
	t.mu.Unlock()
	if w != nil {
		_ = w.BudgetChanged(ctx, id)
	}
}
