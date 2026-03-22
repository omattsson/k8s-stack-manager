package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestUserValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		user    User
		wantErr bool
	}{
		{
			name: "valid user",
			user: User{
				ID:       "uid-1",
				Username: "alice",
				Role:     "user",
			},
			wantErr: false,
		},
		{
			name:    "empty username",
			user:    User{ID: "uid-1"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.user.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStackTemplateValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		tmpl    StackTemplate
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid template",
			tmpl: StackTemplate{
				ID:      "t1",
				Name:    "My Stack",
				OwnerID: "owner-1",
			},
			wantErr: false,
		},
		{
			name:    "missing name",
			tmpl:    StackTemplate{OwnerID: "owner-1"},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name:    "missing owner_id",
			tmpl:    StackTemplate{Name: "My Stack"},
			wantErr: true,
			errMsg:  "owner_id is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.tmpl.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestTemplateChartConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		chart   TemplateChartConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid chart config",
			chart: TemplateChartConfig{
				ID:              "c1",
				StackTemplateID: "t1",
				ChartName:       "backend",
			},
			wantErr: false,
		},
		{
			name:    "missing stack_template_id",
			chart:   TemplateChartConfig{ChartName: "backend"},
			wantErr: true,
			errMsg:  "stack_template_id is required",
		},
		{
			name:    "missing chart_name",
			chart:   TemplateChartConfig{StackTemplateID: "t1"},
			wantErr: true,
			errMsg:  "chart_name is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.chart.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStackDefinitionValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		def     StackDefinition
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid definition",
			def: StackDefinition{
				ID:      "d1",
				Name:    "My Stack",
				OwnerID: "owner-1",
			},
			wantErr: false,
		},
		{
			name:    "missing name",
			def:     StackDefinition{OwnerID: "owner-1"},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name:    "missing owner_id",
			def:     StackDefinition{Name: "My Stack"},
			wantErr: true,
			errMsg:  "owner_id is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.def.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestChartConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		chart   ChartConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid chart config",
			chart: ChartConfig{
				ID:                "c1",
				StackDefinitionID: "d1",
				ChartName:         "frontend",
			},
			wantErr: false,
		},
		{
			name:    "missing stack_definition_id",
			chart:   ChartConfig{ChartName: "frontend"},
			wantErr: true,
			errMsg:  "stack_definition_id is required",
		},
		{
			name:    "missing chart_name",
			chart:   ChartConfig{StackDefinitionID: "d1"},
			wantErr: true,
			errMsg:  "chart_name is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.chart.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStackInstanceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		inst    StackInstance
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid instance",
			inst: StackInstance{
				ID:                "i1",
				StackDefinitionID: "d1",
				Name:              "my-stack",
				OwnerID:           "owner-1",
			},
			wantErr: false,
		},
		{
			name:    "missing stack_definition_id",
			inst:    StackInstance{Name: "my-stack", OwnerID: "owner-1"},
			wantErr: true,
			errMsg:  "stack_definition_id is required",
		},
		{
			name:    "missing name",
			inst:    StackInstance{StackDefinitionID: "d1", OwnerID: "owner-1"},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name:    "missing owner_id",
			inst:    StackInstance{StackDefinitionID: "d1", Name: "my-stack"},
			wantErr: true,
			errMsg:  "owner_id is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.inst.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValueOverrideValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		override ValueOverride
		wantErr  bool
		errMsg   string
	}{
		{
			name: "valid override",
			override: ValueOverride{
				ID:              "v1",
				StackInstanceID: "i1",
				ChartConfigID:   "c1",
			},
			wantErr: false,
		},
		{
			name:     "missing stack_instance_id",
			override: ValueOverride{ChartConfigID: "c1"},
			wantErr:  true,
			errMsg:   "stack_instance_id is required",
		},
		{
			name:     "missing chart_config_id",
			override: ValueOverride{StackInstanceID: "i1"},
			wantErr:  true,
			errMsg:   "chart_config_id is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.override.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestAuditLogValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		log     AuditLog
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid audit log",
			log: AuditLog{
				ID:         "log-1",
				UserID:     "uid-1",
				Action:     "create",
				EntityType: "stack_definition",
				Timestamp:  time.Now(),
			},
			wantErr: false,
		},
		{
			name:    "missing user_id",
			log:     AuditLog{Action: "create", EntityType: "stack_definition"},
			wantErr: true,
			errMsg:  "user_id is required",
		},
		{
			name:    "missing action",
			log:     AuditLog{UserID: "uid-1", EntityType: "stack_definition"},
			wantErr: true,
			errMsg:  "action is required",
		},
		{
			name:    "missing entity_type",
			log:     AuditLog{UserID: "uid-1", Action: "create"},
			wantErr: true,
			errMsg:  "entity_type is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.log.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestClusterValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cluster Cluster
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid cluster with kubeconfig data",
			cluster: Cluster{
				ID:             "c1",
				Name:           "prod-cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "apiVersion: v1...",
			},
		},
		{
			name: "valid cluster with kubeconfig path",
			cluster: Cluster{
				ID:             "c2",
				Name:           "dev-cluster",
				APIServerURL:   "https://dev.k8s.example.com",
				KubeconfigPath: "/etc/kubeconfig/dev",
			},
		},
		{
			name: "valid with explicit health status",
			cluster: Cluster{
				Name:           "cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
				HealthStatus:   ClusterHealthy,
			},
		},
		{
			name: "missing name",
			cluster: Cluster{
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "missing api_server_url",
			cluster: Cluster{
				Name:           "cluster",
				KubeconfigData: "data",
			},
			wantErr: true,
			errMsg:  "api_server_url is required",
		},
		{
			name: "both kubeconfig_data and kubeconfig_path set",
			cluster: Cluster{
				Name:           "cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
				KubeconfigPath: "/path",
			},
			wantErr: true,
			errMsg:  "only one of kubeconfig_data or kubeconfig_path must be set, not both",
		},
		{
			name: "neither kubeconfig_data nor kubeconfig_path",
			cluster: Cluster{
				Name:         "cluster",
				APIServerURL: "https://k8s.example.com",
			},
			wantErr: true,
			errMsg:  "one of kubeconfig_data or kubeconfig_path is required",
		},
		{
			name: "invalid health status",
			cluster: Cluster{
				Name:           "cluster",
				APIServerURL:   "https://k8s.example.com",
				KubeconfigData: "data",
				HealthStatus:   "invalid",
			},
			wantErr: true,
			errMsg:  "invalid health_status: invalid",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cluster.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestChartBranchOverrideValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		override ChartBranchOverride
		wantErr  bool
		errMsg   string
	}{
		{
			name: "valid override",
			override: ChartBranchOverride{
				ID:              "bo-1",
				StackInstanceID: "i1",
				ChartConfigID:   "c1",
				Branch:          "feature/my-branch",
			},
			wantErr: false,
		},
		{
			name: "missing stack_instance_id",
			override: ChartBranchOverride{
				ChartConfigID: "c1",
				Branch:        "main",
			},
			wantErr: true,
			errMsg:  "stack_instance_id is required",
		},
		{
			name: "missing chart_config_id",
			override: ChartBranchOverride{
				StackInstanceID: "i1",
				Branch:          "main",
			},
			wantErr: true,
			errMsg:  "chart_config_id is required",
		},
		{
			name: "missing branch",
			override: ChartBranchOverride{
				StackInstanceID: "i1",
				ChartConfigID:   "c1",
			},
			wantErr: true,
			errMsg:  "branch is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.override.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestUserFavoriteValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fav     UserFavorite
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid favorite",
			fav: UserFavorite{
				ID:         "f1",
				UserID:     "uid-1",
				EntityType: "definition",
				EntityID:   "d1",
			},
			wantErr: false,
		},
		{
			name: "valid favorite with instance type",
			fav: UserFavorite{
				UserID:     "uid-1",
				EntityType: "instance",
				EntityID:   "i1",
			},
			wantErr: false,
		},
		{
			name: "valid favorite with template type",
			fav: UserFavorite{
				UserID:     "uid-1",
				EntityType: "template",
				EntityID:   "t1",
			},
			wantErr: false,
		},
		{
			name:    "missing user_id",
			fav:     UserFavorite{EntityType: "definition", EntityID: "d1"},
			wantErr: true,
			errMsg:  "user_id is required",
		},
		{
			name:    "missing entity_type",
			fav:     UserFavorite{UserID: "uid-1", EntityID: "d1"},
			wantErr: true,
			errMsg:  "entity_type is required",
		},
		{
			name:    "invalid entity_type",
			fav:     UserFavorite{UserID: "uid-1", EntityType: "cluster", EntityID: "c1"},
			wantErr: true,
			errMsg:  "entity_type must be one of: definition, instance, template",
		},
		{
			name:    "missing entity_id",
			fav:     UserFavorite{UserID: "uid-1", EntityType: "definition"},
			wantErr: true,
			errMsg:  "entity_id is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.fav.Validate()
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestStackInstanceValidate_NegativeTTL(t *testing.T) {
	t.Parallel()

	inst := StackInstance{
		StackDefinitionID: "d1",
		Name:              "my-stack",
		OwnerID:           "uid-1",
		TTLMinutes:        -10,
	}
	err := inst.Validate()
	assert.EqualError(t, err, "ttl_minutes must be non-negative")
}

func TestSharedValuesValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sv      SharedValues
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid shared values without YAML",
			sv: SharedValues{
				ID:        "sv-1",
				Name:      "global-defaults",
				ClusterID: "c1",
				Priority:  0,
			},
			wantErr: false,
		},
		{
			name: "valid shared values with YAML",
			sv: SharedValues{
				Name:      "global-defaults",
				ClusterID: "c1",
				Priority:  10,
				Values:    "key: value\nnested:\n  foo: bar",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			sv: SharedValues{
				ClusterID: "c1",
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "missing cluster_id",
			sv: SharedValues{
				Name: "global-defaults",
			},
			wantErr: true,
			errMsg:  "cluster_id is required",
		},
		{
			name: "negative priority",
			sv: SharedValues{
				Name:      "global-defaults",
				ClusterID: "c1",
				Priority:  -1,
			},
			wantErr: true,
			errMsg:  "priority must be non-negative",
		},
		{
			name: "invalid YAML values",
			sv: SharedValues{
				Name:      "global-defaults",
				ClusterID: "c1",
				Values:    ":::invalid yaml{{{",
			},
			wantErr: true,
		},
		{
			name: "empty values string is valid",
			sv: SharedValues{
				Name:      "global-defaults",
				ClusterID: "c1",
				Values:    "",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.sv.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.EqualError(t, err, tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCleanupPolicyValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cp      CleanupPolicy
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid cleanup policy - stop",
			cp: CleanupPolicy{
				ID:        "cp-1",
				Name:      "nightly-stop",
				Action:    "stop",
				Condition: "idle_days:7",
				Schedule:  "0 2 * * *",
				ClusterID: "c1",
			},
			wantErr: false,
		},
		{
			name: "valid cleanup policy - clean",
			cp: CleanupPolicy{
				Name:      "weekly-clean",
				Action:    "clean",
				Condition: "status:stopped,age_days:14",
				Schedule:  "0 3 * * 0",
				ClusterID: "all",
			},
			wantErr: false,
		},
		{
			name: "valid cleanup policy - delete",
			cp: CleanupPolicy{
				Name:      "delete-old",
				Action:    "delete",
				Condition: "ttl_expired",
				Schedule:  "*/30 * * * *",
				ClusterID: "c1",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			cp: CleanupPolicy{
				Action:    "stop",
				Condition: "idle_days:7",
				Schedule:  "0 2 * * *",
				ClusterID: "c1",
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "invalid action",
			cp: CleanupPolicy{
				Name:      "bad-action",
				Action:    "restart",
				Condition: "idle_days:7",
				Schedule:  "0 2 * * *",
				ClusterID: "c1",
			},
			wantErr: true,
			errMsg:  "action must be one of: stop, clean, delete",
		},
		{
			name: "empty action",
			cp: CleanupPolicy{
				Name:      "empty-action",
				Action:    "",
				Condition: "idle_days:7",
				Schedule:  "0 2 * * *",
				ClusterID: "c1",
			},
			wantErr: true,
			errMsg:  "action must be one of: stop, clean, delete",
		},
		{
			name: "missing condition",
			cp: CleanupPolicy{
				Name:      "no-condition",
				Action:    "stop",
				Schedule:  "0 2 * * *",
				ClusterID: "c1",
			},
			wantErr: true,
			errMsg:  "condition is required",
		},
		{
			name: "missing schedule",
			cp: CleanupPolicy{
				Name:      "no-schedule",
				Action:    "stop",
				Condition: "idle_days:7",
				ClusterID: "c1",
			},
			wantErr: true,
			errMsg:  "schedule is required",
		},
		{
			name: "invalid cron schedule",
			cp: CleanupPolicy{
				Name:      "bad-cron",
				Action:    "stop",
				Condition: "idle_days:7",
				Schedule:  "not-a-cron",
				ClusterID: "c1",
			},
			wantErr: true,
		},
		{
			name: "missing cluster_id",
			cp: CleanupPolicy{
				Name:      "no-cluster",
				Action:    "stop",
				Condition: "idle_days:7",
				Schedule:  "0 2 * * *",
			},
			wantErr: true,
			errMsg:  "cluster_id is required",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cp.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.EqualError(t, err, tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
