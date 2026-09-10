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
		// Each KPI group is observed independently: use projected aggregate
		// count queries (never List(), which loads heavy TEXT fields and
		// decrypts cluster secrets on every scrape), and let one failing query
		// skip only its own gauge instead of suppressing the others.

		// Instances.
		if total, instErr := instanceRepo.CountAll(); instErr != nil {
			slog.Warn("business metrics: failed to count instances", "error", instErr)
		} else {
			o.ObserveInt64(businessMetrics.instancesTotal, int64(total))
		}
		// Count all active statuses in one query so an instance changing status
		// mid-scrape cannot be double-counted or missed.
		if active, activeErr := instanceRepo.CountByStatuses(activeInstanceStatuses); activeErr != nil {
			slog.Warn("business metrics: failed to count active instances", "error", activeErr)
		} else {
			o.ObserveInt64(businessMetrics.instancesActive, int64(active))
		}

		// Users.
		if users, userErr := userRepo.Count(); userErr != nil {
			slog.Warn("business metrics: failed to count users", "error", userErr)
		} else {
			o.ObserveInt64(businessMetrics.usersTotal, users)
		}

		// Templates.
		if templates, templateErr := templateRepo.Count(); templateErr != nil {
			slog.Warn("business metrics: failed to count templates", "error", templateErr)
		} else {
			o.ObserveInt64(businessMetrics.templatesTotal, templates)
		}

		// Clusters — projected counts, no secret decryption.
		if total, clusterErr := clusterRepo.CountAll(); clusterErr != nil {
			slog.Warn("business metrics: failed to count clusters", "error", clusterErr)
		} else {
			o.ObserveInt64(businessMetrics.clustersTotal, int64(total))
		}
		if healthy, healthyErr := clusterRepo.CountByHealthStatus(models.ClusterHealthy); healthyErr != nil {
			slog.Warn("business metrics: failed to count healthy clusters", "error", healthyErr)
		} else {
			o.ObserveInt64(businessMetrics.clustersHealthy, int64(healthy))
		}
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
