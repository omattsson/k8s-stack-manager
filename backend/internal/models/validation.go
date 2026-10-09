package models

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/api/resource"
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

// MsgInstanceNameFormat explains the instance name rule to API clients.
const MsgInstanceNameFormat = "name must be a valid DNS label: lowercase letters a-z, digits 0-9 and '-', starting and ending with a letter or digit"

// ValidateInstanceName checks a new or changed instance name. The name must
// be an RFC 1123 label (lowercase a-z, 0-9 and '-', starting and ending with
// an alphanumeric character) of at most MaxInstanceNameLength characters,
// because charts build host names and labels from {{.InstanceName}}.
//
// StackInstance.Validate does not call this function: instances created
// before the rule existed keep working, and updates that keep their name do
// not fail. Handlers call it on create, clone, quick deploy and rename.
func ValidateInstanceName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > MaxInstanceNameLength {
		return fmt.Errorf("name must be at most %d characters", MaxInstanceNameLength)
	}
	if !rfc1123LabelRegex.MatchString(name) {
		return errors.New(MsgInstanceNameFormat)
	}
	return nil
}

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
	hasRegURL := c.RegistryURL != ""
	hasRegUser := c.RegistryUsername != ""
	hasRegPass := c.RegistryPassword != ""
	if hasRegURL && (!hasRegUser || !hasRegPass) {
		return errors.New("registry_username and registry_password are required when registry_url is set")
	}
	if (hasRegUser || hasRegPass) && !hasRegURL {
		return errors.New("registry_url is required when registry_username or registry_password is set")
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
	return validateQuotaQuantities(rq.CPURequest, rq.CPULimit, rq.MemoryRequest, rq.MemoryLimit, rq.StorageLimit)
}

// Validate implements model validation for InstanceQuotaOverride.
func (iqo *InstanceQuotaOverride) Validate() error {
	if iqo.StackInstanceID == "" {
		return errors.New("stack_instance_id is required")
	}
	if iqo.PodLimit != nil && *iqo.PodLimit < 0 {
		return errors.New("pod_limit must be non-negative")
	}
	return validateQuotaQuantities(iqo.CPURequest, iqo.CPULimit, iqo.MemoryRequest, iqo.MemoryLimit, iqo.StorageLimit)
}

// maxQuotaQuantityLen matches the size:20 column of the quota quantity fields.
const maxQuotaQuantityLen = 20

// validateQuotaQuantities checks the resource quantity fields shared by
// ResourceQuotaConfig and InstanceQuotaOverride. Each non-empty value must
// parse as a Kubernetes quantity (the deployer parses them again when it
// creates the namespace ResourceQuota), must not be negative, and a request
// must not exceed its limit when both are set. Empty values mean "not set".
func validateQuotaQuantities(cpuRequest, cpuLimit, memoryRequest, memoryLimit, storageLimit string) error {
	fields := []struct {
		name  string
		value string
	}{
		{"cpu_request", cpuRequest},
		{"cpu_limit", cpuLimit},
		{"memory_request", memoryRequest},
		{"memory_limit", memoryLimit},
		{"storage_limit", storageLimit},
	}

	parsed := make(map[string]resource.Quantity, len(fields))
	for _, f := range fields {
		if f.value == "" {
			continue
		}
		if len(f.value) > maxQuotaQuantityLen {
			return fmt.Errorf("%s: must be at most %d characters", f.name, maxQuotaQuantityLen)
		}
		q, err := resource.ParseQuantity(f.value)
		if err != nil {
			return fmt.Errorf("%s: invalid quantity (examples: 500m, 2, 512Mi, 10Gi)", f.name)
		}
		if q.Sign() < 0 {
			return fmt.Errorf("%s: must not be negative", f.name)
		}
		parsed[f.name] = q
	}

	pairs := [][2]string{{"cpu_request", "cpu_limit"}, {"memory_request", "memory_limit"}}
	for _, p := range pairs {
		req, hasReq := parsed[p[0]]
		lim, hasLim := parsed[p[1]]
		if hasReq && hasLim && req.Cmp(lim) > 0 {
			return fmt.Errorf("%s must not exceed %s", p[0], p[1])
		}
	}
	return nil
}

// Validate implements model validation for RefreshToken.
func (rt *RefreshToken) Validate() error {
	if rt.ID == "" {
		return errors.New("id is required")
	}
	if rt.UserID == "" {
		return errors.New("user_id is required")
	}
	if rt.TokenHash == "" {
		return errors.New("token_hash is required")
	}
	if rt.ExpiresAt.IsZero() {
		return errors.New("expires_at is required")
	}
	if rt.LastActivity.IsZero() {
		return errors.New("last_activity is required")
	}
	// SHA-256 hex digest is always 64 characters.
	if len(rt.TokenHash) != 64 {
		return errors.New("token_hash must be a 64-character hex string")
	}
	return nil
}
