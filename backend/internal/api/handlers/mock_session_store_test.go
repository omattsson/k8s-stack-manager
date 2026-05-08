package handlers

// mock_session_store_test.go provides an in-memory mock of sessionstore.SessionStore
// for handler tests. It records calls to BlockUser and UnblockUser so tests can
// assert that the correct methods were invoked.

import (
	"context"
	"sync"
	"time"

	"backend/internal/sessionstore"
)

type mockSessionStore struct {
	mu               sync.Mutex
	blockUserCalls   []string // user IDs passed to BlockUser
	unblockUserCalls []string // user IDs passed to UnblockUser
	blockedUsers     map[string]bool
	blockUserErr     error
	unblockUserErr   error
}

func newMockHandlerSessionStore() *mockSessionStore {
	return &mockSessionStore{
		blockedUsers: make(map[string]bool),
	}
}

func (m *mockSessionStore) BlockToken(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *mockSessionStore) IsTokenBlocked(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (m *mockSessionStore) BlockUser(_ context.Context, userID string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blockUserErr != nil {
		return m.blockUserErr
	}
	m.blockUserCalls = append(m.blockUserCalls, userID)
	m.blockedUsers[userID] = true
	return nil
}

func (m *mockSessionStore) IsUserBlocked(_ context.Context, userID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blockedUsers[userID], nil
}

func (m *mockSessionStore) UnblockUser(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unblockUserErr != nil {
		return m.unblockUserErr
	}
	m.unblockUserCalls = append(m.unblockUserCalls, userID)
	delete(m.blockedUsers, userID)
	return nil
}

func (m *mockSessionStore) SaveOIDCState(_ context.Context, _ string, _ sessionstore.OIDCStateData, _ time.Duration) error {
	return nil
}

func (m *mockSessionStore) ConsumeOIDCState(_ context.Context, _ string) (*sessionstore.OIDCStateData, error) {
	return nil, nil
}

func (m *mockSessionStore) SaveCLIAuth(_ context.Context, _ string, _ sessionstore.CLIAuthData, _ time.Duration) error {
	return nil
}

func (m *mockSessionStore) GetCLIAuth(_ context.Context, _ string) (*sessionstore.CLIAuthData, error) {
	return nil, nil
}

func (m *mockSessionStore) UpdateCLIAuth(_ context.Context, _ string, _ sessionstore.CLIAuthData) error {
	return nil
}

func (m *mockSessionStore) Cleanup(_ context.Context) error { return nil }
func (m *mockSessionStore) Stop()                           {}

// wasBlockUserCalledFor returns true if BlockUser was called with the given userID.
func (m *mockSessionStore) wasBlockUserCalledFor(userID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.blockUserCalls {
		if id == userID {
			return true
		}
	}
	return false
}

// wasUnblockUserCalledFor returns true if UnblockUser was called with the given userID.
func (m *mockSessionStore) wasUnblockUserCalledFor(userID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.unblockUserCalls {
		if id == userID {
			return true
		}
	}
	return false
}
