package handlers

import (
	"context"
	"fmt"

	"backend/internal/database"
	"backend/pkg/dberrors"
)

// mockHandlerTxRunner implements database.TxRunner for handler tests.
// It delegates RunInTx to the provided mock repositories without any
// real transaction — the function is simply called synchronously.
type mockHandlerTxRunner struct {
	repos database.TxRepos
}

// RunInTx fails like GORMTxRunner on a done ctx: before fn, and after fn
// when ctx ended during it (the commit fails). The mock cannot roll back
// the writes of fn.
func (m *mockHandlerTxRunner) RunInTx(ctx context.Context, fn func(repos database.TxRepos) error) error {
	if err := ctxDone(ctx, "transaction"); err != nil {
		return err
	}
	err := fn(m.repos)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if err == nil {
			err = ctxErr
		} else {
			err = fmt.Errorf("%w: %w", ctxErr, err)
		}
		return dberrors.NewDatabaseError("transaction", err)
	}
	return err
}
