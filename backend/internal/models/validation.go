package models

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

var (
	ErrEmptyUsername = errors.New("username cannot be empty")
	ErrInvalidPrice  = errors.New("price must be positive")
	ErrEmptyItemName = errors.New("item name cannot be empty")
)

// Validate implements model validation for User.
func (u *User) Validate() error {
	if u.Username == "" {
		return ErrEmptyUsername
	}
	return nil
}

// Validate implements model validation for Item.
func (i *Item) Validate() error {
	if i.Name == "" {
		return ErrEmptyItemName
	}

	if i.Price <= 0 {
		return ErrInvalidPrice
	}

	return nil
}

// Validate implements model validation for StackTemplate.
func (t *StackTemplate) Validate() error {
	if t.Name == "" {
		return errors.New("name is required")
	}
	if t.OwnerID == "" {
		return errors.New("owner_id is required")
	}
	return nil
}

// Validate implements model validation for TemplateChartConfig.
func (c *TemplateChartConfig) Validate() error {
	if c.StackTemplateID == "" {
		return errors.New("stack_template_id is required")
	}
	if c.ChartName == "" {
		return errors.New("chart_name is required")
	}
	if len(c.ChartName) > 53 {
		return errors.New("chart_name must be at most 53 characters")
	}
	if !helmReleaseNameRegex.MatchString(c.ChartName) {
		return errors.New("chart_name must contain only lowercase alphanumeric characters, dashes, dots, or underscores, and must start and end with an alphanumeric character")
	}
	return nil
}

// Validate implements model validation for StackDefinition.
func (d *StackDefinition) Validate() error {
	if d.Name == "" {
		return errors.New("name is required")
	}
	if d.OwnerID == "" {
		return errors.New("owner_id is required")
	}
	return nil
}

// helmReleaseNameRegex matches valid Helm release names: lowercase alphanumeric,
// dashes, dots, and underscores; must start and end with alphanumeric; max 53 chars
// (Helm's limit). This is enforced because ChartName is used as a Helm release name
// and passed as a positional argument to the helm CLI.
var helmReleaseNameRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// Validate implements model validation for ChartConfig.
func (c *ChartConfig) Validate() error {
	if c.StackDefinitionID == "" {
		return errors.New("stack_definition_id is required")
	}
	if c.ChartName == "" {
		return errors.New("chart_name is required")
	}
	if len(c.ChartName) > 53 {
		return errors.New("chart_name must be at most 53 characters")
	}
	if !helmReleaseNameRegex.MatchString(c.ChartName) {
		return errors.New("chart_name must contain only lowercase alphanumeric characters, dashes, dots, or underscores, and must start and end with an alphanumeric character")
	}
	return nil
}

// rfc1123LabelRegex matches valid RFC 1123 label names (used for K8s namespaces).
var rfc1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// MaxInstanceNameLength is the maximum allowed length for a stack instance name.
// This leaves room for the "stack-" prefix and "-owner" suffix in the generated namespace.
const MaxInstanceNameLength = 50

// MaxNamespaceLength is the maximum length for a K8s namespace (RFC 1123).
const MaxNamespaceLength = 63

// Validate implements model validation for StackInstance.
func (si *StackInstance) Validate() error {
	if si.StackDefinitionID == "" {
		return errors.New("stack_definition_id is required")
	}
	if si.Name == "" {
		return errors.New("name is required")
	}
	if len(si.Name) > MaxInstanceNameLength {
		return fmt.Errorf("name must be at most %d characters", MaxInstanceNameLength)
	}
	if si.OwnerID == "" {
		return errors.New("owner_id is required")
	}
	if si.TTLMinutes < 0 {
		return errors.New("ttl_minutes must be non-negative")
	}
	if si.Namespace != "" {
		if len(si.Namespace) > MaxNamespaceLength {
			return fmt.Errorf("namespace must be at most %d characters", MaxNamespaceLength)
		}
		if !rfc1123LabelRegex.MatchString(si.Namespace) {
			return errors.New("namespace must be a valid RFC 1123 label: lowercase alphanumeric and dashes, must not start or end with a dash")
		}
	}
	return nil
}

// Validate implements model validation for ValueOverride.
func (v *ValueOverride) Validate() error {
	if v.StackInstanceID == "" {
		return errors.New("stack_instance_id is required")
	}
	if v.ChartConfigID == "" {
		return errors.New("chart_config_id is required")
	}
	return nil
}

// Validate implements model validation for Cluster.
func (c *Cluster) Validate() error {
	if c.Name == "" {
		return errors.New("name is required")
	}
	if !c.UseInCluster && c.APIServerURL == "" {
		return errors.New("api_server_url is required (unless use_in_cluster is true)")
	}
	hasData := c.KubeconfigData != ""
	hasPath := c.KubeconfigPath != ""
	if hasData && hasPath {
		return errors.New("only one of kubeconfig_data or kubeconfig_path must be set, not both")
	}
	if !c.UseInCluster && !hasData && !hasPath {
		return errors.New("one of kubeconfig_data, kubeconfig_path, or use_in_cluster is required")
	}
	if c.UseInCluster && (hasData || hasPath) {
		return errors.New("kubeconfig_data and kubeconfig_path must not be set when use_in_cluster is true")
	}
	if c.MaxNamespaces < 0 {
		return errors.New("max_namespaces must be non-negative")
	}
	if c.MaxInstancesPerUser < 0 {
		return errors.New("max_instances_per_user must be non-negative")
	}
	switch c.HealthStatus {
	case "", ClusterHealthy, ClusterDegraded, ClusterUnreachable:
		// valid
	default:
		return fmt.Errorf("invalid health_status: %s", c.HealthStatus)
	}
	return nil
}

// Validate implements model validation for AuditLog.
func (a *AuditLog) Validate() error {
	if a.UserID == "" {
		return errors.New("user_id is required")
	}
	if a.Action == "" {
		return errors.New("action is required")
	}
	if a.EntityType == "" {
		return errors.New("entity_type is required")
	}
	return nil
}

// Validate implements model validation for ChartBranchOverride.
func (o *ChartBranchOverride) Validate() error {
	if o.StackInstanceID == "" {
		return errors.New("stack_instance_id is required")
	}
	if o.ChartConfigID == "" {
		return errors.New("chart_config_id is required")
	}
	if o.Branch == "" {
		return errors.New("branch is required")
	}
	return nil
}

// Validate implements model validation for SharedValues.
func (sv *SharedValues) Validate() error {
	if sv.Name == "" {
		return errors.New("name is required")
	}
	if sv.ClusterID == "" {
		return errors.New("cluster_id is required")
	}
	if sv.Priority < 0 {
		return errors.New("priority must be non-negative")
	}
	if sv.Values != "" {
		var parsed map[string]interface{}
		if err := yaml.Unmarshal([]byte(sv.Values), &parsed); err != nil {
			return fmt.Errorf("values must be a valid YAML mapping: %w", err)
		}
	}
	return nil
}

// Valid cleanup policy actions.
var validCleanupActions = map[string]bool{
	"stop":   true,
	"clean":  true,
	"delete": true,
}

// Validate implements model validation for CleanupPolicy.
func (cp *CleanupPolicy) Validate() error {
	if cp.Name == "" {
		return errors.New("name is required")
	}
	if !validCleanupActions[cp.Action] {
		return errors.New("action must be one of: stop, clean, delete")
	}
	if cp.Condition == "" {
		return errors.New("condition is required")
	}
	if cp.Schedule == "" {
		return errors.New("schedule is required")
	}
	if _, err := cron.ParseStandard(cp.Schedule); err != nil {
		return fmt.Errorf("invalid cron schedule: %w", err)
	}
	if cp.ClusterID == "" {
		return errors.New("cluster_id is required")
	}
	return nil
}

// Validate implements model validation for ResourceQuotaConfig.
func (rq *ResourceQuotaConfig) Validate() error {
	if rq.ClusterID == "" {
		return errors.New("cluster_id is required")
	}
	if rq.PodLimit < 0 {
		return errors.New("pod_limit must be non-negative")
	}
	return nil
}

// Validate implements model validation for InstanceQuotaOverride.
func (iqo *InstanceQuotaOverride) Validate() error {
	if iqo.StackInstanceID == "" {
		return errors.New("stack_instance_id is required")
	}
	if iqo.PodLimit != nil && *iqo.PodLimit < 0 {
		return errors.New("pod_limit must be non-negative")
	}
	return nil
}
