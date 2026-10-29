package handlers

import (
	"context"
	"log/slog"
	"time"

	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/websocket"
)

// NewStreamAccessChecker returns the access re-check that the hub runs on the
// open stream sessions at each revalidation (Hub.SetRevocableAccessChecker).
// A session loses its access when the feature is off, the user is deleted,
// disabled or no longer the owner and not devops/admin (the CURRENT role, same
// rule as canModifyInstance), the instance is gone or has no live namespace
// (stopped, cleaned, deleting), or its cluster no longer allows port-forward.
//
// It reads each distinct instance once, all users with one FindByIDs call and
// the clusters with one List call. A failed lookup fails open: the affected
// sessions stay and the next run retries; the log line is rate-limited.
func NewStreamAccessChecker(instanceRepo models.StackInstanceRepository, userRepo models.UserRepository, clusterRepo models.ClusterRepository, cfg config.StreamConfig) websocket.RevocableAccessChecker {
	limit := websocket.NewLogLimiter(time.Minute)
	logFail := func(msg string, args ...any) {
		if limit.Allow() {
			slog.Error(msg, args...)
			return
		}
		slog.Debug(msg, args...)
	}
	return func(ctx context.Context, rs []websocket.Revocable) []bool {
		lost := make([]bool, len(rs))
		if !cfg.PortForward.Enabled {
			for i := range lost {
				lost[i] = true
			}
			return lost
		}

		instances := loadStreamInstances(ctx, instanceRepo, rs, logFail)
		users := loadStreamUsers(ctx, userRepo, rs, logFail)
		clusters, defaultCluster := loadStreamClusters(ctx, clusterRepo, logFail)

		for i, r := range rs {
			if ctx.Err() != nil {
				return lost
			}
			id := r.Identity()

			if users.ok {
				u := users.byID[id.UserID]
				if u == nil || u.Disabled {
					lost[i] = true
					continue
				}
				if inst, known := instances.byID[r.InstanceID()]; known && inst != nil &&
					!canModifyInstanceAs(u.ID, u.Role, inst) {
					lost[i] = true
					continue
				}
			}

			inst, known := instances.byID[r.InstanceID()]
			if !known {
				continue // lookup failed: fail open
			}
			if inst == nil || !streamInstanceLive(inst) {
				lost[i] = true
				continue
			}
			if clusters != nil {
				cl := clusters[inst.ClusterID]
				if inst.ClusterID == "" {
					cl = defaultCluster
				}
				if cl == nil || !cl.AllowPortForward {
					lost[i] = true
				}
			}
		}
		return lost
	}
}

// streamInstanceLive reports whether the instance can have a live namespace.
func streamInstanceLive(inst *models.StackInstance) bool {
	switch inst.Status {
	case models.StackStatusDraft, models.StackStatusStopped, models.StackStatusStopping, models.StackStatusCleaning:
		return false
	}
	return inst.Namespace != ""
}

type streamInstances struct {
	// byID has an entry (nil: the instance is gone) for each instance that
	// the lookup answered. A missing key means the lookup failed.
	byID map[string]*models.StackInstance
}

func loadStreamInstances(ctx context.Context, repo models.StackInstanceRepository, rs []websocket.Revocable, logFail func(string, ...any)) streamInstances {
	out := streamInstances{byID: map[string]*models.StackInstance{}}
	for _, r := range rs {
		id := r.InstanceID()
		if _, done := out.byID[id]; done || ctx.Err() != nil {
			continue
		}
		inst, err := repo.FindByID(ctx, id)
		switch {
		case err == nil:
			out.byID[id] = inst
		case isNotFoundError(err):
			out.byID[id] = nil
		default:
			if ctx.Err() == nil {
				logFail("Stream revalidation: instance lookup failed; the sessions stay until the next run",
					"instance_id", id, "error", err)
			}
		}
	}
	return out
}

type streamUsers struct {
	byID map[string]*models.User
	ok   bool
}

func loadStreamUsers(ctx context.Context, repo models.UserRepository, rs []websocket.Revocable, logFail func(string, ...any)) streamUsers {
	if repo == nil {
		return streamUsers{}
	}
	seen := map[string]bool{}
	var ids []string
	for _, r := range rs {
		if id := r.Identity().UserID; id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return streamUsers{}
	}
	m, err := repo.FindByIDs(ctx, ids)
	if err != nil {
		if ctx.Err() == nil {
			logFail("Stream revalidation: user lookup failed; the sessions stay until the next run", "error", err)
		}
		return streamUsers{}
	}
	return streamUsers{byID: m, ok: true}
}

// loadStreamClusters returns the clusters by ID and the default cluster. It
// returns a nil map when the lookup failed or there is no repository.
func loadStreamClusters(ctx context.Context, repo models.ClusterRepository, logFail func(string, ...any)) (map[string]*models.Cluster, *models.Cluster) {
	if repo == nil {
		return nil, nil
	}
	list, err := repo.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			logFail("Stream revalidation: cluster lookup failed; the sessions stay until the next run", "error", err)
		}
		return nil, nil
	}
	byID := make(map[string]*models.Cluster, len(list))
	var def *models.Cluster
	for i := range list {
		cl := &list[i]
		byID[cl.ID] = cl
		if cl.IsDefault {
			def = cl
		}
	}
	return byID, def
}
