package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/internal/models"
	"backend/internal/podstream"
	"backend/internal/websocket"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRevocable struct {
	user, instance string
	// kind defaults to port_forward.
	kind string
}

func (f *fakeRevocable) Kind() string {
	if f.kind == "" {
		return "port_forward"
	}
	return f.kind
}

func (f *fakeRevocable) Identity() websocket.ClientIdentity {
	return websocket.ClientIdentity{UserID: f.user}
}
func (f *fakeRevocable) InstanceID() string { return f.instance }
func (f *fakeRevocable) Revoke(int, string) {}

func TestStreamAccessChecker(t *testing.T) {
	t.Parallel()
	live := func() *models.StackInstance {
		return &models.StackInstance{ID: "i1", OwnerID: "owner", Namespace: "ns", ClusterID: "c1", Status: models.StackStatusRunning}
	}
	tests := []struct {
		name     string
		cfgOff   bool
		inst     func() *models.StackInstance
		users    map[string]*models.User
		cluster  *models.Cluster
		sessUser string
		setup    func(*MockStackInstanceRepository, *MockUserRepository, *MockClusterRepository)
		wantLost bool
	}{
		{name: "owner keeps access", inst: live, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner"},
		{name: "feature turned off", cfgOff: true, inst: live, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner", wantLost: true},
		{name: "devops keeps access", inst: live, users: map[string]*models.User{"d": {ID: "d", Role: "devops"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "d"},
		{name: "role downgraded to user and not the owner", inst: live, users: map[string]*models.User{"d": {ID: "d", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "d", wantLost: true},
		{name: "owner changed", inst: func() *models.StackInstance { i := live(); i.OwnerID = "new"; return i }, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner", wantLost: true},
		{name: "user deleted", inst: live, users: map[string]*models.User{}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner", wantLost: true},
		{name: "user disabled", inst: live, users: map[string]*models.User{"owner": {ID: "owner", Role: "user", Disabled: true}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner", wantLost: true},
		{name: "instance deleted", inst: nil, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner", wantLost: true},
		{name: "instance stopped", inst: func() *models.StackInstance { i := live(); i.Status = models.StackStatusStopped; return i }, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner", wantLost: true},
		{name: "instance cleaning", inst: func() *models.StackInstance { i := live(); i.Status = models.StackStatusCleaning; return i }, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner", wantLost: true},
		{name: "cluster switch turned off", inst: live, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1"}, sessUser: "owner", wantLost: true},
		{name: "cluster gone", inst: live, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: nil, sessUser: "owner", wantLost: true},
		{name: "default cluster", inst: func() *models.StackInstance { i := live(); i.ClusterID = ""; return i }, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", IsDefault: true, AllowPortForward: true}, sessUser: "owner"},
		{name: "user lookup fails open", inst: live, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner",
			setup: func(_ *MockStackInstanceRepository, u *MockUserRepository, _ *MockClusterRepository) {
				u.findErr = errors.New("db down")
			}},
		{name: "instance lookup fails open", inst: live, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner",
			setup: func(i *MockStackInstanceRepository, _ *MockUserRepository, _ *MockClusterRepository) {
				i.fetchErr = errors.New("db down")
			}},
		{name: "cluster lookup fails open", inst: live, users: map[string]*models.User{"owner": {ID: "owner", Role: "user"}}, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, sessUser: "owner",
			setup: func(_ *MockStackInstanceRepository, _ *MockUserRepository, c *MockClusterRepository) {
				c.err = errors.New("db down")
			}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			instRepo, userRepo, clRepo := NewMockStackInstanceRepository(), NewMockUserRepository(), NewMockClusterRepository()
			if tt.inst != nil {
				require.NoError(t, instRepo.Create(ctx, tt.inst()))
			}
			for _, u := range tt.users {
				require.NoError(t, userRepo.Create(ctx, &models.User{ID: u.ID, Username: u.ID, Role: u.Role, Disabled: u.Disabled}))
			}
			if tt.cluster != nil {
				require.NoError(t, clRepo.Create(ctx, tt.cluster))
			}
			if tt.setup != nil {
				tt.setup(instRepo, userRepo, clRepo)
			}
			cfg := defaultStreamCfg()
			cfg.PortForward.Enabled = !tt.cfgOff
			check := NewStreamAccessChecker(instRepo, userRepo, clRepo, cfg)
			got := check(ctx, []websocket.Revocable{&fakeRevocable{user: tt.sessUser, instance: "i1"}})
			require.Len(t, got, 1)
			assert.Equal(t, tt.wantLost, got[0])
		})
	}
}

func TestStreamAccessChecker_BatchesLookups(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	instRepo, userRepo, clRepo := NewMockStackInstanceRepository(), NewMockUserRepository(), NewMockClusterRepository()
	require.NoError(t, instRepo.Create(ctx, &models.StackInstance{ID: "i1", OwnerID: "u1", Namespace: "ns", ClusterID: "c1", Status: models.StackStatusRunning}))
	require.NoError(t, userRepo.Create(ctx, &models.User{ID: "u1", Username: "u1", Role: "user"}))
	require.NoError(t, clRepo.Create(ctx, &models.Cluster{ID: "c1", AllowPortForward: true}))
	rs := make([]websocket.Revocable, 0, 20)
	for i := 0; i < 20; i++ {
		rs = append(rs, &fakeRevocable{user: "u1", instance: "i1"})
	}
	check := NewStreamAccessChecker(instRepo, userRepo, clRepo, defaultStreamCfg())
	got := check(ctx, rs)
	for _, l := range got {
		assert.False(t, l)
	}
	assert.EqualValues(t, 1, userRepo.findByIDsCalls.Load(), "one user lookup for all sessions")
	assert.EqualValues(t, 1, instRepo.findByIDsCalls.Load(), "one instance lookup for all sessions")
	assert.EqualValues(t, 1, clRepo.streamFlagCalls.Load(), "one cluster lookup for all sessions")
}

// Only the clusters of the open sessions are read, by ID, with the projected
// call (no List, so no decryption of the other clusters).
func TestStreamAccessChecker_ReadsOnlyClustersInUse(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	instRepo, userRepo, clRepo := NewMockStackInstanceRepository(), NewMockUserRepository(), NewMockClusterRepository()
	require.NoError(t, instRepo.Create(ctx, &models.StackInstance{ID: "i1", OwnerID: "u1", Namespace: "ns", ClusterID: "c1", Status: models.StackStatusRunning}))
	require.NoError(t, instRepo.Create(ctx, &models.StackInstance{ID: "i2", OwnerID: "u1", Namespace: "ns", ClusterID: "c2", Status: models.StackStatusRunning}))
	require.NoError(t, userRepo.Create(ctx, &models.User{ID: "u1", Username: "u1", Role: "user"}))
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		require.NoError(t, clRepo.Create(ctx, &models.Cluster{ID: id, AllowPortForward: id != "c2"}))
	}
	check := NewStreamAccessChecker(instRepo, userRepo, clRepo, defaultStreamCfg())
	got := check(ctx, []websocket.Revocable{
		&fakeRevocable{user: "u1", instance: "i1"}, &fakeRevocable{user: "u1", instance: "i2"}, &fakeRevocable{user: "u1", instance: "i1"},
	})
	assert.Equal(t, []bool{false, true, false}, got)
	clRepo.mu.RLock()
	defer clRepo.mu.RUnlock()
	assert.ElementsMatch(t, []string{"c1", "c2"}, clRepo.streamFlagIDs)
}

// The feature switch applies to the sessions of its kind only.
func TestStreamAccessChecker_SwitchPerKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	instRepo, userRepo, clRepo := NewMockStackInstanceRepository(), NewMockUserRepository(), NewMockClusterRepository()
	require.NoError(t, instRepo.Create(ctx, &models.StackInstance{ID: "i1", OwnerID: "u1", Namespace: "ns", ClusterID: "c1", Status: models.StackStatusRunning}))
	require.NoError(t, userRepo.Create(ctx, &models.User{ID: "u1", Username: "u1", Role: "user"}))
	require.NoError(t, clRepo.Create(ctx, &models.Cluster{ID: "c1"})) // port-forward off on the cluster
	rs := []websocket.Revocable{
		&fakeRevocable{user: "u1", instance: "i1", kind: "port_forward"},
		&fakeRevocable{user: "u1", instance: "i1", kind: "other_kind"},
	}
	tests := []struct {
		name    string
		enabled bool
		want    []bool
	}{
		{"port-forward switch off closes its sessions only", false, []bool{true, false}},
		// The cluster flag is the port-forward flag: it does not apply to another kind.
		{"switch on: the cluster flag closes the port-forward session only", true, []bool{true, false}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := defaultStreamCfg()
			cfg.PortForward.Enabled = tt.enabled
			got := NewStreamAccessChecker(instRepo, userRepo, clRepo, cfg)(ctx, rs)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStreamAccessChecker_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	check := NewStreamAccessChecker(NewMockStackInstanceRepository(), NewMockUserRepository(), NewMockClusterRepository(), defaultStreamCfg())
	got := check(ctx, []websocket.Revocable{&fakeRevocable{user: "u", instance: "i"}})
	assert.Equal(t, []bool{false}, got, "a cancelled run closes nothing")
}

func TestCanModifyInstanceAs(t *testing.T) {
	t.Parallel()
	inst := &models.StackInstance{OwnerID: "o"}
	tests := []struct {
		name, user, role string
		inst             *models.StackInstance
		want             bool
	}{
		{"owner", "o", "user", inst, true},
		{"other user", "x", "user", inst, false},
		{"devops", "x", "devops", inst, true},
		{"admin", "x", "admin", inst, true},
		{"empty user", "", "user", &models.StackInstance{}, false},
		{"nil instance", "o", "admin", nil, false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, canModifyInstanceAs(tt.user, tt.role, tt.inst))
		})
	}
}

// The re-check applies the switch of the kind of each session: the exec
// settings close exec sessions only and the port-forward settings close
// port-forward sessions only.
func TestStreamAccessChecker_KindSwitches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		pfEnabled  bool
		execOn     bool
		cluster    *models.Cluster
		inst       func(*models.StackInstance)
		role       string
		sessUser   string
		wantPF     bool // port_forward session lost
		wantExec   bool // exec session lost
		clusterNil bool
	}{
		{name: "both on", pfEnabled: true, execOn: true, cluster: &models.Cluster{ID: "c1", AllowPortForward: true, AllowExec: true}},
		{name: "exec feature off", pfEnabled: true, execOn: false, cluster: &models.Cluster{ID: "c1", AllowPortForward: true, AllowExec: true}, wantExec: true},
		{name: "port-forward feature off", pfEnabled: false, execOn: true, cluster: &models.Cluster{ID: "c1", AllowPortForward: true, AllowExec: true}, wantPF: true},
		{name: "allow_exec off", pfEnabled: true, execOn: true, cluster: &models.Cluster{ID: "c1", AllowPortForward: true}, wantExec: true},
		{name: "allow_port_forward off", pfEnabled: true, execOn: true, cluster: &models.Cluster{ID: "c1", AllowExec: true}, wantPF: true},
		{name: "cluster gone", pfEnabled: true, execOn: true, clusterNil: true, wantPF: true, wantExec: true},
		{name: "instance stopped", pfEnabled: true, execOn: true, cluster: &models.Cluster{ID: "c1", AllowPortForward: true, AllowExec: true},
			inst: func(i *models.StackInstance) { i.Status = models.StackStatusStopped }, wantPF: true, wantExec: true},
		{name: "role downgraded", pfEnabled: true, execOn: true, cluster: &models.Cluster{ID: "c1", AllowPortForward: true, AllowExec: true},
			role: "user", sessUser: "dev", wantPF: true, wantExec: true},
		{name: "devops keeps access", pfEnabled: true, execOn: true, cluster: &models.Cluster{ID: "c1", AllowPortForward: true, AllowExec: true},
			role: "devops", sessUser: "dev"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			instRepo, userRepo, clRepo := NewMockStackInstanceRepository(), NewMockUserRepository(), NewMockClusterRepository()
			inst := &models.StackInstance{ID: "i1", OwnerID: "owner", Namespace: "ns", ClusterID: "c1", Status: models.StackStatusRunning}
			if tt.inst != nil {
				tt.inst(inst)
			}
			require.NoError(t, instRepo.Create(ctx, inst))
			sess := tt.sessUser
			if sess == "" {
				sess = "owner"
			}
			role := tt.role
			if role == "" {
				role = "user"
			}
			require.NoError(t, userRepo.Create(ctx, &models.User{ID: sess, Username: sess, Role: role}))
			if !tt.clusterNil {
				require.NoError(t, clRepo.Create(ctx, tt.cluster))
			}
			cfg := defaultStreamCfg()
			cfg.PortForward.Enabled = tt.pfEnabled
			cfg.Exec.Enabled = tt.execOn
			check := NewStreamAccessChecker(instRepo, userRepo, clRepo, cfg)
			got := check(ctx, []websocket.Revocable{
				&fakeRevocable{user: sess, instance: "i1", kind: podstream.KindPortForward},
				&fakeRevocable{user: sess, instance: "i1", kind: podstream.KindExec},
			})
			require.Len(t, got, 2)
			assert.Equal(t, tt.wantPF, got[0], "port-forward session")
			assert.Equal(t, tt.wantExec, got[1], "exec session")
		})
	}
}
