package handlers

// mock_domain_repositories_test.go provides in-memory mock implementations of
// all domain-specific repository interfaces for use in handler tests.
// All mocks are thread-safe and support configuring errors for negative-path tests.

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"backend/internal/models"
	"backend/pkg/dberrors"
)

// ---- UserRepository mock ----

type MockUserRepository struct {
	mu        sync.RWMutex
	users     map[string]*models.User // by ID
	byName    map[string]*models.User // by Username
	createErr error
	findErr   error
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

func (m *MockUserRepository) Update(user *models.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
		return nil, errors.New("not found")
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
	mu       sync.RWMutex
	items    map[string]*models.StackInstance
	err      error
	fetchErr error
}

func NewMockStackInstanceRepository() *MockStackInstanceRepository {
	return &MockStackInstanceRepository{items: make(map[string]*models.StackInstance)}
}

func (m *MockStackInstanceRepository) Create(i *models.StackInstance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *MockAuditLogRepository) List(filters models.AuditLogFilters) ([]models.AuditLog, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.err != nil {
		return nil, 0, m.err
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

	// Apply offset.
	offset := filters.Offset
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]

	// Apply limit.
	if filters.Limit > 0 && filters.Limit < len(out) {
		out = out[:filters.Limit]
	}

	return out, total, nil
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
