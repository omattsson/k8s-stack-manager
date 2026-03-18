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
