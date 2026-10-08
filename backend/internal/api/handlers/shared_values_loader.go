package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"

	"backend/internal/cluster"
	"backend/internal/models"
	"backend/pkg/dberrors"

	"gopkg.in/yaml.v3"
)

// clusterIDResolver resolves an empty cluster ID to the default cluster ID.
// *cluster.Registry implements it; the deployer uses the same call to pick
// the target cluster, so shared values come from the cluster the deploy uses.
type clusterIDResolver interface {
	ResolveClusterID(clusterID string) (string, error)
}

// loadSharedValues returns the cluster shared values that apply to inst, as
// YAML strings ordered by priority ascending (lowest first). The result is
// ready for helm.GenerateParams.SharedValues / helm.ChartValues.SharedValues,
// where shared values are the first (lowest) merge layer:
// shared (by priority) <- chart defaults <- instance overrides <- locked values.
//
// Entries with blank values are skipped. Entries whose YAML is not a mapping
// are skipped with a warning naming the cluster and the shared values entry.
//
// Cluster resolution: inst.ClusterID when set; otherwise the default cluster
// through resolver (the cluster registry) or, without a resolver, through
// clusterRepo.FindDefault. When no default cluster exists, no shared values
// apply and the result is empty.
//
// Error policy: any other lookup failure is returned. Callers fail closed
// (500 or a failed bulk item) because values rendered without the shared
// layer would be wrong for deploy, preview, export and compare alike.
//
// A nil repo (shared values not wired) yields no shared values.
func loadSharedValues(
	repo models.SharedValuesRepository,
	resolver clusterIDResolver,
	clusterRepo models.ClusterRepository,
	inst *models.StackInstance,
) ([]string, error) {
	if repo == nil || inst == nil {
		return nil, nil
	}

	clusterID := inst.ClusterID
	if clusterID == "" {
		switch {
		case resolver != nil:
			resolved, err := resolver.ResolveClusterID("")
			if err != nil {
				if errors.Is(err, cluster.ErrNoDefaultCluster) || errors.Is(err, dberrors.ErrNotFound) {
					return nil, nil
				}
				return nil, fmt.Errorf("resolve default cluster: %w", err)
			}
			clusterID = resolved
		case clusterRepo != nil:
			def, err := clusterRepo.FindDefault()
			if err != nil {
				if errors.Is(err, dberrors.ErrNotFound) {
					return nil, nil
				}
				return nil, fmt.Errorf("find default cluster: %w", err)
			}
			clusterID = def.ID
		}
		if clusterID == "" {
			return nil, nil
		}
	}

	values, err := repo.ListByCluster(clusterID)
	if err != nil {
		return nil, fmt.Errorf("list shared values for cluster %s: %w", clusterID, err)
	}

	// The repository orders already; sort again with the same function so
	// the merge order does not depend on the repository implementation.
	models.SortSharedValues(values)

	out := make([]string, 0, len(values))
	for _, sv := range values {
		if strings.TrimSpace(sv.Values) == "" {
			continue
		}
		// The values generator skips a layer that is not a YAML mapping.
		// Skip it here too, but say so: a silent skip hides a broken layer.
		var parsed map[string]interface{}
		if err := yaml.Unmarshal([]byte(sv.Values), &parsed); err != nil {
			warnInvalidSharedValues(clusterID, &sv, err)
			continue
		}
		out = append(out, sv.Values)
	}
	return out, nil
}

// SharedValuesConfigured reports whether a shared values repository is wired.
// Bootstrap logs a warning at startup when it is not.
func (h *InstanceHandler) SharedValuesConfigured() bool { return h.sharedValuesRepo != nil }

// SharedValuesConfigured reports whether a shared values repository is wired.
// Bootstrap logs a warning at startup when it is not.
func (h *QuickDeployHandler) SharedValuesConfigured() bool { return h.sharedValuesRepo != nil }

// warnedInvalidSharedValues records the shared values entries (ID and update
// time) already reported as invalid, so each version is logged once per process.
var warnedInvalidSharedValues sync.Map

// yamlErrLine extracts the line number from a YAML error message.
var yamlErrLine = regexp.MustCompile(`line (\d+)`)

// warnInvalidSharedValues logs that a shared values entry is skipped because
// its YAML is not a mapping. It logs IDs, the name, the error kind and the
// line only: the YAML error text can quote value content, which may hold
// secrets. Each entry version is logged once per process.
func warnInvalidSharedValues(clusterID string, sv *models.SharedValues, err error) {
	warnInvalidSharedValuesTo(slog.Default(), clusterID, sv, err)
}

// warnInvalidSharedValuesTo is warnInvalidSharedValues with an explicit logger.
func warnInvalidSharedValuesTo(logger *slog.Logger, clusterID string, sv *models.SharedValues, err error) {
	key := fmt.Sprintf("%s@%d", sv.ID, sv.UpdatedAt.UnixNano())
	if _, seen := warnedInvalidSharedValues.LoadOrStore(key, struct{}{}); seen {
		return
	}
	kind := "syntax"
	var typeErr *yaml.TypeError
	if errors.As(err, &typeErr) {
		kind = "not a mapping"
	}
	attrs := []any{
		"cluster_id", clusterID,
		"shared_values_id", sv.ID,
		"shared_values_name", sv.Name,
		"yaml_error_kind", kind,
	}
	if m := yamlErrLine.FindStringSubmatch(err.Error()); m != nil {
		attrs = append(attrs, "yaml_error_line", m[1])
	}
	logger.Warn("skipping shared values with invalid YAML", attrs...)
}
