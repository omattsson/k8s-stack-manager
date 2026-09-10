package telemetry

import (
	"context"
	"log/slog"

	"backend/internal/models"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// activeInstanceStatuses lists the statuses treated as "actively deployed"
// across the backend (see k8s/watcher, cluster/quota_monitor, and
// cluster/secret_refresher). "partial" is included so the KPI does not
// undercount deployments where only some charts succeeded.
var activeInstanceStatuses = []string{
	models.StackStatusRunning,
	models.StackStatusDeploying,
	models.StackStatusStabilizing,
	models.StackStatusPartial,
}

var businessMetrics struct {
	instancesActive metric.Int64ObservableGauge
	instancesTotal  metric.Int64ObservableGauge
	usersTotal      metric.Int64ObservableGauge
	templatesTotal  metric.Int64ObservableGauge
	clustersTotal   metric.Int64ObservableGauge
	clustersHealthy metric.Int64ObservableGauge
}

func StartBusinessMetrics(
	instanceRepo models.StackInstanceRepository,
	userRepo models.UserRepository,
	templateRepo models.StackTemplateRepository,
	clusterRepo models.ClusterRepository,
) error {
	meter := otel.Meter("business")

	var err error
	businessMetrics.instancesActive, err = meter.Int64ObservableGauge(
		"business.instances.active",
		metric.WithDescription("Number of active stack instances."),
	)
	if err != nil {
		return err
	}
	businessMetrics.instancesTotal, err = meter.Int64ObservableGauge(
		"business.instances.total",
		metric.WithDescription("Total number of stack instances."),
	)
	if err != nil {
		return err
	}
	businessMetrics.usersTotal, err = meter.Int64ObservableGauge(
		"business.users.total",
		metric.WithDescription("Total number of registered users."),
	)
	if err != nil {
		return err
	}
	businessMetrics.templatesTotal, err = meter.Int64ObservableGauge(
		"business.templates.total",
		metric.WithDescription("Total number of templates."),
	)
	if err != nil {
		return err
	}
	businessMetrics.clustersTotal, err = meter.Int64ObservableGauge(
		"business.clusters.total",
		metric.WithDescription("Total number of registered clusters."),
	)
	if err != nil {
		return err
	}
	businessMetrics.clustersHealthy, err = meter.Int64ObservableGauge(
		"business.clusters.healthy",
		metric.WithDescription("Number of healthy clusters."),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		// Use aggregate count queries instead of loading every row: instance
		// lists carry heavy TEXT fields and cluster lists decrypt secrets, so
		// List() at scrape interval would grow more expensive as tables grow.
		instancesTotal, countErr := instanceRepo.CountAll()
		if countErr != nil {
			slog.Warn("business metrics: failed to count instances", "error", countErr)
			return nil
		}
		instancesActive := 0
		for _, status := range activeInstanceStatuses {
			n, statusErr := instanceRepo.CountByStatus(status)
			if statusErr != nil {
				slog.Warn("business metrics: failed to count instances by status",
					"status", status, "error", statusErr)
				return nil
			}
			instancesActive += n
		}
		o.ObserveInt64(businessMetrics.instancesActive, int64(instancesActive))
		o.ObserveInt64(businessMetrics.instancesTotal, int64(instancesTotal))

		usersTotal, userErr := userRepo.Count()
		if userErr != nil {
			slog.Warn("business metrics: failed to count users", "error", userErr)
			return nil
		}
		o.ObserveInt64(businessMetrics.usersTotal, usersTotal)

		templatesTotal, templateErr := templateRepo.Count()
		if templateErr != nil {
			slog.Warn("business metrics: failed to count templates", "error", templateErr)
			return nil
		}
		o.ObserveInt64(businessMetrics.templatesTotal, templatesTotal)

		// ClusterRepository has no count method and the healthy count needs
		// per-cluster status, so List() is retained here.
		clusterList, clusterErr := clusterRepo.List()
		if clusterErr != nil {
			slog.Warn("business metrics: failed to list clusters", "error", clusterErr)
			return nil
		}
		o.ObserveInt64(businessMetrics.clustersTotal, int64(len(clusterList)))
		clustersHealthy := 0
		for _, cluster := range clusterList {
			if cluster.HealthStatus == models.ClusterHealthy {
				clustersHealthy++
			}
		}
		o.ObserveInt64(businessMetrics.clustersHealthy, int64(clustersHealthy))
		return nil
	},
		businessMetrics.instancesActive,
		businessMetrics.instancesTotal,
		businessMetrics.usersTotal,
		businessMetrics.templatesTotal,
		businessMetrics.clustersTotal,
		businessMetrics.clustersHealthy,
	)
	if err != nil {
		return err
	}

	slog.Info("Registered business KPI metrics")
	return nil
}
