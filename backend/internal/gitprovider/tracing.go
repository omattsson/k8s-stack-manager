package gitprovider

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

var gitProviderMeter = otel.Meter("gitprovider")

var gitProviderMetrics struct {
	branchListTotal metric.Int64Counter
	branchListDur   metric.Float64Histogram
}

func init() {
	initGitProviderMetrics()
}

func initGitProviderMetrics() {
	var err error

	gitProviderMetrics.branchListTotal, err = gitProviderMeter.Int64Counter(
		"gitprovider.branch_list.total",
		metric.WithDescription("Total branch-list requests by provider and outcome."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	gitProviderMetrics.branchListDur, err = gitProviderMeter.Float64Histogram(
		"gitprovider.branch_list.duration",
		metric.WithDescription("Latency of branch-list operations by provider."),
		metric.WithUnit("s"),
	)
	if err != nil {
		otel.Handle(err)
	}
}

func recordBranchListResult(provider, status string, duration time.Duration) {
	if provider == "" || status == "" {
		return
	}
	gitProviderMetrics.branchListTotal.Add(context.Background(), 1,
		metric.WithAttributes(
			attribute.String("provider", provider),
			attribute.String("status", status),
		),
	)
	gitProviderMetrics.branchListDur.Record(context.Background(), duration.Seconds(),
		metric.WithAttributes(
			attribute.String("provider", provider),
		),
	)
}
