package handlers

import "backend/internal/database"

// mockHandlerTxRunner implements database.TxRunner for handler tests.
// It delegates RunInTx to the provided mock repositories without any
// real transaction — the function is simply called synchronously.
type mockHandlerTxRunner struct {
	repos database.TxRepos
}

func (m *mockHandlerTxRunner) RunInTx(fn func(repos database.TxRepos) error) error {
	return fn(m.repos)
}
