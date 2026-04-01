package handlers

// mock_domain_repositories_test.go provides in-memory mock implementations of
// all domain-specific repository interfaces for use in handler tests.
// All mocks are thread-safe and support configuring errors for negative-path tests.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"
)

// ---- UserRepository mock ----

type MockUserRepository struct {
	mu         sync.RWMutex
	users      map[string]*models.User // by ID
	byName     map[string]*models.User // by Username
	createErr  error
	findErr    error
	updateErr  error
	createFunc func(user *models.User) error // optional override for Create; called under lock
}

func NewMockUserRepository() *MockUserRepository {
	return &MockUserRepository{
		users:  make(map[string]*models.User),
		byName: make(map[string]*models.User),
	}
}

func (m *MockUserRepository) Create(user *models.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createFunc != nil {
		return m.createFunc(user)
	}
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.byName[user.Username]; exists {
		return errors.New("already exists")
	}
	m.users[user.ID] = user
	m.byName[user.Username] = user
	return nil
}

func (m *MockUserRepository) FindByID(id string) (*models.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	u, ok := m.users[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *u
	return &cp, nil
}

func (m *MockUserRepository) FindByUsername(username string) (*models.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	u, ok := m.byName[username]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *u
	return &cp, nil
}

func (m *MockUserRepository) FindByExternalID(provider, externalID string) (*models.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	for _, u := range m.users {
		if u.AuthProvider == provider && u.ExternalID != nil && *u.ExternalID == externalID {
			cp := *u
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *MockUserRepository) Update(user *models.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.updateErr != nil {
		return m.updateErr
	}
	if _, ok := m.users[user.ID]; !ok {
		return errors.New("not found")
	}
	// Remove old username mapping.
	for uname, u := range m.byName {
		if u.ID == user.ID {
			delete(m.byName, uname)
			break
		}
	}
	m.users[user.ID] = user
	m.byName[user.Username] = user
	return nil
}

func (m *MockUserRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return errors.New("not found")
	}
	delete(m.byName, u.Username)
	delete(m.users, id)
	return nil
}

func (m *MockUserRepository) List() ([]models.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]models.User, 0, len(m.users))
	for _, u := range m.users {
		out = append(out, *u)
	}
	return out, nil
}

func (m *MockUserRepository) SetCreateError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createErr = err
}

func (m *MockUserRepository) SetFindError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findErr = err
}

func (m *MockUserRepository) SetCreateFunc(fn func(user *models.User) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createFunc = fn
}

// ---- StackTemplateRepository mock ----

type MockStackTemplateRepository struct {
	mu       sync.RWMutex
	items    map[string]*models.StackTemplate
	err      error
	fetchErr error
}

func NewMockStackTemplateRepository() *MockStackTemplateRepository {
	return &MockStackTemplateRepository{items: make(map[string]*models.StackTemplate)}
}

func (m *MockStackTemplateRepository) Create(t *models.StackTemplate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[t.ID] = t
	return nil
}

func (m *MockStackTemplateRepository) FindByID(id string) (*models.StackTemplate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	t, ok := m.items[id]
	if !ok {
		return nil, dberrors.NewDatabaseError("FindByID", dberrors.ErrNotFound)
	}
	cp := *t
	return &cp, nil
}

func (m *MockStackTemplateRepository) Update(t *models.StackTemplate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[t.ID]; !ok {
		return errors.New("not found")
	}
	m.items[t.ID] = t
	return nil
}

func (m *MockStackTemplateRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[id]; !ok {
		return errors.New("not found")
	}
	delete(m.items, id)
	return nil
}

func (m *MockStackTemplateRepository) List() ([]models.StackTemplate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]models.StackTemplate, 0, len(m.items))
	for _, t := range m.items {
		out = append(out, *t)
	}
	return out, nil
}

func (m *MockStackTemplateRepository) ListPublished() ([]models.StackTemplate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.StackTemplate
	for _, t := range m.items {
		if t.IsPublished {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (m *MockStackTemplateRepository) ListByOwner(ownerID string) ([]models.StackTemplate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.StackTemplate
	for _, t := range m.items {
		if t.OwnerID == ownerID {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (m *MockStackTemplateRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockStackTemplateRepository) SetFetchError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchErr = err
}

// ---- TemplateChartConfigRepository mock ----

type MockTemplateChartConfigRepository struct {
	mu    sync.RWMutex
	items map[string]*models.TemplateChartConfig
	err   error
}

func NewMockTemplateChartConfigRepository() *MockTemplateChartConfigRepository {
	return &MockTemplateChartConfigRepository{items: make(map[string]*models.TemplateChartConfig)}
}

func (m *MockTemplateChartConfigRepository) Create(c *models.TemplateChartConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[c.ID] = c
	return nil
}

func (m *MockTemplateChartConfigRepository) FindByID(id string) (*models.TemplateChartConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *c
	return &cp, nil
}

func (m *MockTemplateChartConfigRepository) Update(c *models.TemplateChartConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[c.ID]; !ok {
		return errors.New("not found")
	}
	m.items[c.ID] = c
	return nil
}

func (m *MockTemplateChartConfigRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return errors.New("not found")
	}
	delete(m.items, id)
	return nil
}

func (m *MockTemplateChartConfigRepository) ListByTemplate(templateID string) ([]models.TemplateChartConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.TemplateChartConfig
	for _, c := range m.items {
		if c.StackTemplateID == templateID {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (m *MockTemplateChartConfigRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- StackDefinitionRepository mock ----

type MockStackDefinitionRepository struct {
	mu       sync.RWMutex
	items    map[string]*models.StackDefinition
	err      error
	fetchErr error
}

func NewMockStackDefinitionRepository() *MockStackDefinitionRepository {
	return &MockStackDefinitionRepository{items: make(map[string]*models.StackDefinition)}
}

func (m *MockStackDefinitionRepository) Create(d *models.StackDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[d.ID] = d
	return nil
}

func (m *MockStackDefinitionRepository) FindByID(id string) (*models.StackDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	d, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *d
	return &cp, nil
}

func (m *MockStackDefinitionRepository) Update(d *models.StackDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[d.ID]; !ok {
		return errors.New("not found")
	}
	m.items[d.ID] = d
	return nil
}

func (m *MockStackDefinitionRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[id]; !ok {
		return errors.New("not found")
	}
	delete(m.items, id)
	return nil
}

func (m *MockStackDefinitionRepository) List() ([]models.StackDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]models.StackDefinition, 0, len(m.items))
	for _, d := range m.items {
		out = append(out, *d)
	}
	return out, nil
}

func (m *MockStackDefinitionRepository) ListByOwner(ownerID string) ([]models.StackDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.StackDefinition
	for _, d := range m.items {
		if d.OwnerID == ownerID {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (m *MockStackDefinitionRepository) ListByTemplate(templateID string) ([]models.StackDefinition, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.StackDefinition
	for _, d := range m.items {
		if d.SourceTemplateID == templateID {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (m *MockStackDefinitionRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockStackDefinitionRepository) SetFetchError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchErr = err
}

// ---- ChartConfigRepository mock ----

type MockChartConfigRepository struct {
	mu    sync.RWMutex
	items map[string]*models.ChartConfig
	err   error
}

func NewMockChartConfigRepository() *MockChartConfigRepository {
	return &MockChartConfigRepository{items: make(map[string]*models.ChartConfig)}
}

func (m *MockChartConfigRepository) Create(c *models.ChartConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[c.ID] = c
	return nil
}

func (m *MockChartConfigRepository) FindByID(id string) (*models.ChartConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *c
	return &cp, nil
}

func (m *MockChartConfigRepository) Update(c *models.ChartConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[c.ID]; !ok {
		return errors.New("not found")
	}
	m.items[c.ID] = c
	return nil
}

func (m *MockChartConfigRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return errors.New("not found")
	}
	delete(m.items, id)
	return nil
}

func (m *MockChartConfigRepository) ListByDefinition(definitionID string) ([]models.ChartConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.ChartConfig
	for _, c := range m.items {
		if c.StackDefinitionID == definitionID {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (m *MockChartConfigRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- StackInstanceRepository mock ----

type MockStackInstanceRepository struct {
	mu        sync.RWMutex
	items     map[string]*models.StackInstance
	err       error
	fetchErr  error
	createErr error
}

func NewMockStackInstanceRepository() *MockStackInstanceRepository {
	return &MockStackInstanceRepository{items: make(map[string]*models.StackInstance)}
}

func (m *MockStackInstanceRepository) Create(i *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	if m.err != nil {
		return m.err
	}
	m.items[i.ID] = i
	return nil
}

func (m *MockStackInstanceRepository) FindByID(id string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	i, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *i
	return &cp, nil
}

func (m *MockStackInstanceRepository) FindByNamespace(namespace string) (*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	for _, i := range m.items {
		if i.Namespace == namespace {
			cp := *i
			return &cp, nil
		}
	}
	return nil, dberrors.NewDatabaseError("FindByNamespace", dberrors.ErrNotFound)
}

func (m *MockStackInstanceRepository) Update(i *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[i.ID]; !ok {
		return errors.New("not found")
	}
	m.items[i.ID] = i
	return nil
}

func (m *MockStackInstanceRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[id]; !ok {
		return errors.New("not found")
	}
	delete(m.items, id)
	return nil
}

func (m *MockStackInstanceRepository) List() ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]models.StackInstance, 0, len(m.items))
	for _, i := range m.items {
		out = append(out, *i)
	}
	return out, nil
}

func (m *MockStackInstanceRepository) ListByOwner(ownerID string) ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.StackInstance
	for _, i := range m.items {
		if i.OwnerID == ownerID {
			out = append(out, *i)
		}
	}
	return out, nil
}

func (m *MockStackInstanceRepository) FindByCluster(clusterID string) ([]models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.StackInstance
	for _, i := range m.items {
		if i.ClusterID == clusterID {
			out = append(out, *i)
		}
	}
	return out, nil
}

func (m *MockStackInstanceRepository) CountByClusterAndOwner(clusterID, ownerID string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return 0, m.err
	}
	count := 0
	for _, i := range m.items {
		if i.ClusterID == clusterID && i.OwnerID == ownerID {
			count++
		}
	}
	return count, nil
}

func (m *MockStackInstanceRepository) ListPaged(limit, offset int) ([]models.StackInstance, int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, 0, m.err
	}
	all := make([]models.StackInstance, 0, len(m.items))
	for _, i := range m.items {
		all = append(all, *i)
	}
	total := len(all)
	if offset > 0 && offset < total {
		all = all[offset:]
	} else if offset >= total {
		return []models.StackInstance{}, total, nil
	}
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

func (m *MockStackInstanceRepository) CountAll() (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return 0, m.err
	}
	return len(m.items), nil
}

func (m *MockStackInstanceRepository) CountByStatus(status string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return 0, m.err
	}
	count := 0
	for _, i := range m.items {
		if i.Status == status {
			count++
		}
	}
	return count, nil
}

func (m *MockStackInstanceRepository) ExistsByDefinitionAndStatus(definitionID, status string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return false, m.err
	}
	for _, i := range m.items {
		if i.StackDefinitionID == definitionID && i.Status == status {
			return true, nil
		}
	}
	return false, nil
}

func (m *MockStackInstanceRepository) ListExpired() ([]*models.StackInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	now := time.Now()
	var out []*models.StackInstance
	for _, i := range m.items {
		if i.Status == models.StackStatusRunning && i.ExpiresAt != nil && i.ExpiresAt.Before(now) {
			cp := *i
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockStackInstanceRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockStackInstanceRepository) SetFetchError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchErr = err
}

func (m *MockStackInstanceRepository) SetCreateError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createErr = err
}

// ---- ValueOverrideRepository mock ----

type MockValueOverrideRepository struct {
	mu    sync.RWMutex
	items map[string]*models.ValueOverride
	err   error
}

func NewMockValueOverrideRepository() *MockValueOverrideRepository {
	return &MockValueOverrideRepository{items: make(map[string]*models.ValueOverride)}
}

func (m *MockValueOverrideRepository) Create(v *models.ValueOverride) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[v.ID] = v
	return nil
}

func (m *MockValueOverrideRepository) FindByID(id string) (*models.ValueOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.items[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *v
	return &cp, nil
}

func (m *MockValueOverrideRepository) FindByInstanceAndChart(instanceID, chartConfigID string) (*models.ValueOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.items {
		if v.StackInstanceID == instanceID && v.ChartConfigID == chartConfigID {
			cp := *v
			return &cp, nil
		}
	}
	return nil, errors.New("not found")
}

func (m *MockValueOverrideRepository) Update(v *models.ValueOverride) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[v.ID]; !ok {
		return errors.New("not found")
	}
	m.items[v.ID] = v
	return nil
}

func (m *MockValueOverrideRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return errors.New("not found")
	}
	delete(m.items, id)
	return nil
}

func (m *MockValueOverrideRepository) ListByInstance(instanceID string) ([]models.ValueOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.ValueOverride
	for _, v := range m.items {
		if v.StackInstanceID == instanceID {
			out = append(out, *v)
		}
	}
	return out, nil
}

func (m *MockValueOverrideRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- AuditLogRepository mock ----

type MockAuditLogRepository struct {
	mu      sync.RWMutex
	entries []models.AuditLog
	err     error
}

func NewMockAuditLogRepository() *MockAuditLogRepository {
	return &MockAuditLogRepository{}
}

func (m *MockAuditLogRepository) Create(log *models.AuditLog) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.entries = append(m.entries, *log)
	return nil
}

func (m *MockAuditLogRepository) List(filters models.AuditLogFilters) (*models.AuditLogResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.AuditLog
	for _, e := range m.entries {
		if filters.UserID != "" && e.UserID != filters.UserID {
			continue
		}
		if filters.EntityType != "" && e.EntityType != filters.EntityType {
			continue
		}
		if filters.EntityID != "" && e.EntityID != filters.EntityID {
			continue
		}
		if filters.Action != "" && e.Action != filters.Action {
			continue
		}
		out = append(out, e)
	}
	total := int64(len(out))

	// Cursor-based pagination: decode opaque cursor and skip entries until we pass the cursor ID.
	if filters.Cursor != "" {
		cursorID := filters.Cursor
		// Try to decode as base64 opaque token (pk|rk format where rk is the ID).
		if data, err := base64.StdEncoding.DecodeString(filters.Cursor); err == nil {
			parts := strings.SplitN(string(data), "|", 2)
			if len(parts) == 2 {
				cursorID = parts[1]
			}
		}
		found := false
		for i, e := range out {
			if e.ID == cursorID {
				out = out[i+1:]
				found = true
				break
			}
		}
		if !found {
			out = nil
		}
		total = -1 // unknown total in cursor mode
	} else {
		// Apply offset.
		offset := filters.Offset
		if offset > len(out) {
			offset = len(out)
		}
		out = out[offset:]
	}

	// Detect next page and apply limit.
	var nextCursor string
	if filters.Limit > 0 && filters.Limit < len(out) {
		out = out[:filters.Limit]
		if filters.Cursor != "" {
			lastID := out[filters.Limit-1].ID
			nextCursor = base64.StdEncoding.EncodeToString([]byte("mock|" + lastID))
		}
	}

	return &models.AuditLogResult{
		Data:       out,
		Total:      total,
		NextCursor: nextCursor,
	}, nil
}

func (m *MockAuditLogRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- APIKeyRepository mock ----

type MockAPIKeyRepository struct {
	mu        sync.RWMutex
	keys      map[string]*models.APIKey   // by ID
	byPrefix  map[string][]*models.APIKey // by Prefix (slice for collision support)
	createErr error
	findErr   error
	deleteErr error
	listErr   error
}

func NewMockAPIKeyRepository() *MockAPIKeyRepository {
	return &MockAPIKeyRepository{
		keys:     make(map[string]*models.APIKey),
		byPrefix: make(map[string][]*models.APIKey),
	}
}

func (m *MockAPIKeyRepository) Create(key *models.APIKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	if key.ID == "" {
		key.ID = fmt.Sprintf("key-%d", len(m.keys)+1)
	}
	cp := *key
	m.keys[key.ID] = &cp
	m.byPrefix[key.Prefix] = append(m.byPrefix[key.Prefix], &cp)
	return nil
}

func (m *MockAPIKeyRepository) FindByID(userID, keyID string) (*models.APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	k, ok := m.keys[keyID]
	if !ok || k.UserID != userID {
		return nil, errors.New("not found")
	}
	cp := *k
	return &cp, nil
}

func (m *MockAPIKeyRepository) FindByPrefix(prefix string) ([]*models.APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.findErr != nil {
		return nil, m.findErr
	}
	ks, ok := m.byPrefix[prefix]
	if !ok || len(ks) == 0 {
		return nil, errors.New("not found")
	}
	out := make([]*models.APIKey, len(ks))
	for i, k := range ks {
		cp := *k
		out[i] = &cp
	}
	return out, nil
}

func (m *MockAPIKeyRepository) ListByUser(userID string) ([]*models.APIKey, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []*models.APIKey
	for _, k := range m.keys {
		if k.UserID == userID {
			cp := *k
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockAPIKeyRepository) UpdateLastUsed(userID, keyID string, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.keys[keyID]
	if !ok || k.UserID != userID {
		return errors.New("not found")
	}
	k.LastUsedAt = &t
	return nil
}

func (m *MockAPIKeyRepository) Delete(userID, keyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	k, ok := m.keys[keyID]
	if !ok || k.UserID != userID {
		return errors.New("not found")
	}
	// Remove from byPrefix slice.
	if ks, exists := m.byPrefix[k.Prefix]; exists {
		for i, entry := range ks {
			if entry.ID == keyID {
				m.byPrefix[k.Prefix] = append(ks[:i], ks[i+1:]...)
				break
			}
		}
		if len(m.byPrefix[k.Prefix]) == 0 {
			delete(m.byPrefix, k.Prefix)
		}
	}
	delete(m.keys, keyID)
	return nil
}

func (m *MockAPIKeyRepository) SetCreateError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createErr = err
}

func (m *MockAPIKeyRepository) SetFindError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.findErr = err
}

func (m *MockAPIKeyRepository) SetDeleteError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteErr = err
}

func (m *MockAPIKeyRepository) SetListError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.listErr = err
}

// ---- ClusterRepository mock ----

type MockClusterRepository struct {
	mu         sync.RWMutex
	clusters   map[string]*models.Cluster
	defaultID  string
	err        error
	fetchErr   error
	defaultErr error
}

func NewMockClusterRepository() *MockClusterRepository {
	return &MockClusterRepository{clusters: make(map[string]*models.Cluster)}
}

func (m *MockClusterRepository) Create(cl *models.Cluster) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if cl.ID == "" {
		cl.ID = fmt.Sprintf("cluster-%d", len(m.clusters)+1)
	}
	cl.CreatedAt = time.Now()
	cl.UpdatedAt = time.Now()
	m.clusters[cl.ID] = cl
	if cl.IsDefault {
		m.defaultID = cl.ID
	}
	return nil
}

func (m *MockClusterRepository) FindByID(id string) (*models.Cluster, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	cl, ok := m.clusters[id]
	if !ok {
		return nil, dberrors.NewDatabaseError("FindByID", dberrors.ErrNotFound)
	}
	cp := *cl
	return &cp, nil
}

func (m *MockClusterRepository) Update(cl *models.Cluster) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.clusters[cl.ID]; !ok {
		return dberrors.NewDatabaseError("Update", dberrors.ErrNotFound)
	}
	cl.UpdatedAt = time.Now()
	m.clusters[cl.ID] = cl
	return nil
}

func (m *MockClusterRepository) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.clusters[id]; !ok {
		return dberrors.NewDatabaseError("Delete", dberrors.ErrNotFound)
	}
	delete(m.clusters, id)
	return nil
}

func (m *MockClusterRepository) List() ([]models.Cluster, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	out := make([]models.Cluster, 0, len(m.clusters))
	for _, cl := range m.clusters {
		out = append(out, *cl)
	}
	return out, nil
}

func (m *MockClusterRepository) FindDefault() (*models.Cluster, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultErr != nil {
		return nil, m.defaultErr
	}
	if m.defaultID == "" {
		return nil, dberrors.NewDatabaseError("FindDefault", dberrors.ErrNotFound)
	}
	cl, ok := m.clusters[m.defaultID]
	if !ok {
		return nil, dberrors.NewDatabaseError("FindDefault", dberrors.ErrNotFound)
	}
	cp := *cl
	return &cp, nil
}

func (m *MockClusterRepository) SetDefault(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.clusters[id]; !ok {
		return dberrors.NewDatabaseError("SetDefault", dberrors.ErrNotFound)
	}
	m.defaultID = id
	return nil
}

func (m *MockClusterRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockClusterRepository) SetFetchError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchErr = err
}

// ---- helpers ----

// ensure unused import doesn't cause compile error
var _ = fmt.Sprintf

// ---- UserFavoriteRepository mock ----

type MockUserFavoriteRepository struct {
	mu    sync.RWMutex
	items map[string]*models.UserFavorite // key = userID + ":" + entityType + ":" + entityID
	err   error
}

func NewMockUserFavoriteRepository() *MockUserFavoriteRepository {
	return &MockUserFavoriteRepository{items: make(map[string]*models.UserFavorite)}
}

func favKey(userID, entityType, entityID string) string {
	return userID + ":" + entityType + ":" + entityID
}

func (m *MockUserFavoriteRepository) Add(fav *models.UserFavorite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if fav.ID == "" {
		fav.ID = fmt.Sprintf("fav-%d", len(m.items)+1)
	}
	key := favKey(fav.UserID, fav.EntityType, fav.EntityID)
	// Idempotent — overwrite if exists.
	m.items[key] = fav
	return nil
}

func (m *MockUserFavoriteRepository) Remove(userID, entityType, entityID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	key := favKey(userID, entityType, entityID)
	if _, ok := m.items[key]; !ok {
		return errors.New("not found")
	}
	delete(m.items, key)
	return nil
}

func (m *MockUserFavoriteRepository) List(userID string) ([]*models.UserFavorite, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []*models.UserFavorite
	for _, fav := range m.items {
		if fav.UserID == userID {
			cp := *fav
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockUserFavoriteRepository) IsFavorite(userID, entityType, entityID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return false, m.err
	}
	key := favKey(userID, entityType, entityID)
	_, ok := m.items[key]
	return ok, nil
}

func (m *MockUserFavoriteRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// ---- TemplateVersionRepository mock ----

type MockTemplateVersionRepository struct {
	mu       sync.RWMutex
	items    map[string]*models.TemplateVersion
	err      error
	fetchErr error
}

func NewMockTemplateVersionRepository() *MockTemplateVersionRepository {
	return &MockTemplateVersionRepository{items: make(map[string]*models.TemplateVersion)}
}

func (m *MockTemplateVersionRepository) Create(_ context.Context, v *models.TemplateVersion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.items[v.ID] = v
	return nil
}

func (m *MockTemplateVersionRepository) ListByTemplate(_ context.Context, templateID string) ([]models.TemplateVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []models.TemplateVersion
	for _, v := range m.items {
		if v.TemplateID == templateID {
			out = append(out, *v)
		}
	}
	// Sort by CreatedAt descending.
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (m *MockTemplateVersionRepository) GetByID(_ context.Context, templateID, id string) (*models.TemplateVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	v, ok := m.items[id]
	if !ok || v.TemplateID != templateID {
		return nil, errors.New("not found")
	}
	cp := *v
	return &cp, nil
}

func (m *MockTemplateVersionRepository) GetLatestByTemplate(_ context.Context, templateID string) (*models.TemplateVersion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	var latest *models.TemplateVersion
	for _, v := range m.items {
		if v.TemplateID == templateID {
			if latest == nil || v.CreatedAt.After(latest.CreatedAt) {
				cp := *v
				latest = &cp
			}
		}
	}
	if latest == nil {
		return nil, errors.New("not found")
	}
	return latest, nil
}

func (m *MockTemplateVersionRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *MockTemplateVersionRepository) SetFetchError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchErr = err
}

// ---- MockInstanceQuotaOverrideRepository ----

// MockInstanceQuotaOverrideRepository is an in-memory mock for models.InstanceQuotaOverrideRepository.
type MockInstanceQuotaOverrideRepository struct {
	mu    sync.RWMutex
	items map[string]*models.InstanceQuotaOverride // keyed by StackInstanceID
	err   error
}

func NewMockInstanceQuotaOverrideRepository() *MockInstanceQuotaOverrideRepository {
	return &MockInstanceQuotaOverrideRepository{items: make(map[string]*models.InstanceQuotaOverride)}
}

func (m *MockInstanceQuotaOverrideRepository) GetByInstanceID(_ context.Context, instanceID string) (*models.InstanceQuotaOverride, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, m.err
	}
	o, ok := m.items[instanceID]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *o
	return &cp, nil
}

func (m *MockInstanceQuotaOverrideRepository) Upsert(_ context.Context, override *models.InstanceQuotaOverride) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if override.ID == "" {
		override.ID = "iqo-" + override.StackInstanceID
	}
	now := time.Now().UTC()
	if override.CreatedAt.IsZero() {
		override.CreatedAt = now
	}
	override.UpdatedAt = now
	cp := *override
	m.items[override.StackInstanceID] = &cp
	return nil
}

func (m *MockInstanceQuotaOverrideRepository) Delete(_ context.Context, instanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if _, ok := m.items[instanceID]; !ok {
		return errors.New("not found")
	}
	delete(m.items, instanceID)
	return nil
}

func (m *MockInstanceQuotaOverrideRepository) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}
