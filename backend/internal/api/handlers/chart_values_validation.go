package handlers

import (
	"errors"
	"strconv"

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
	prefix := "Invalid YAML in "
	if chartName != "" {
		prefix += "chart " + strconv.Quote(chartName) + " "
	}
	return errors.New(prefix + field + ": " + err.Error())
}
