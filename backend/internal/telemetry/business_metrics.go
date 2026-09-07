package telemetry

import (
	"context"
	"log/slog"

	"backend/internal/models"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

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
		instanceList, listErr := instanceRepo.List()
		if listErr != nil {
			slog.Warn("business metrics: failed to list instances", "error", listErr)
			return nil
		}
		instancesActive := 0
		for _, inst := range instanceList {
			switch inst.Status {
			case models.StackStatusRunning, models.StackStatusDeploying, models.StackStatusStabilizing:
				instancesActive++
			}
		}
		o.ObserveInt64(businessMetrics.instancesActive, int64(instancesActive))
		o.ObserveInt64(businessMetrics.instancesTotal, int64(len(instanceList)))

		userList, userErr := userRepo.List()
		if userErr != nil {
			slog.Warn("business metrics: failed to list users", "error", userErr)
			return nil
		}
		o.ObserveInt64(businessMetrics.usersTotal, int64(len(userList)))

		templateList, templateErr := templateRepo.List()
		if templateErr != nil {
			slog.Warn("business metrics: failed to list templates", "error", templateErr)
			return nil
		}
		o.ObserveInt64(businessMetrics.templatesTotal, int64(len(templateList)))

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
