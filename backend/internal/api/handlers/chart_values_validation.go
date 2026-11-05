package handlers

import (
	"fmt"

	"backend/internal/helm"
)

// validateChartValues refuses a chart values layer that the deploy cannot
// parse (#756). field is "default_values" or "locked_values". chartName is
// empty when the request holds one chart. Callers pass only new or changed
// values: a stored invalid value must still save when it is unchanged.
func validateChartValues(field, chartName, value string) error {
	err := helm.ValidateValues(value)
	if err == nil {
		return nil
	}
	if chartName == "" {
		return fmt.Errorf("Invalid YAML in %s: %w", field, err)
	}
	return fmt.Errorf("Invalid YAML in chart %q %s: %w", chartName, field, err)
}
