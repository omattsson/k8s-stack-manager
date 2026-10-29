package handlers

import (
	"context"
	"log/slog"
	"time"

	"backend/internal/config"
	"backend/internal/models"
	"backend/internal/podstream"
	"backend/internal/websocket"
)

// NewStreamAccessChecker returns the access re-check that the hub runs on the
// open stream sessions at each revalidation (Hub.SetRevocableAccessChecker).
// A session loses its access when the feature switch of its kind is off, the
// user is deleted, disabled or no longer the owner and not devops/admin (the
// CURRENT role, same rule as canModifyInstance), the instance is gone or has
// no live namespace (stopped, cleaned, deleting), or its cluster no longer
// allows the feature.
//
// It reads the instances of the open sessions with one FindByIDs call (columns
// id, owner_id, status, namespace, cluster_id), all users with one FindByIDs
// call and the cluster switches with one StreamFlagsByIDs call that selects
// only the id, is_default and allow_port_forward columns of the clusters in
// use (no decryption: a damaged kubeconfig of another cluster cannot disable
// the check). A failed lookup fails open: the affected sessions stay and the
// next run retries; the log line is rate-limited. A run whose context ends
// before every session was checked logs that (rate-limited) and keeps the
// unchecked sessions.
func NewStreamAccessChecker(instanceRepo models.StackInstanceRepository, userRepo models.UserRepository, clusterRepo models.ClusterRepository, cfg config.StreamConfig) websocket.RevocableAccessChecker {
	limit := websocket.NewLogLimiter(time.Minute)
	logFail := func(msg string, args ...any) {
		if limit.Allow() {
			slog.Error(msg, args...)
			return
		}
		slog.Debug(msg, args...)
	}
	return func(ctx context.Context, all []websocket.Revocable) []bool {
		lost := make([]bool, len(all))
		// The switch of the feature of each session.
		var rs []websocket.Revocable
		var idx []int
		for i, r := range all {
			if !streamKindEnabled(cfg, r.Kind()) {
				lost[i] = true
				continue
			}
			rs = append(rs, r)
			idx = append(idx, i)
		}
		if len(rs) == 0 {
			return lost
		}
		ranOut := func(stage string, checked int) bool {
			if ctx.Err() == nil {
				return false
			}
			logFail("Stream revalidation: the run ran out of time; the unchecked sessions stay until the next run",
				"stage", stage, "checked", checked, "sessions", len(rs), "error", ctx.Err())
			return true
		}

		instances := loadStreamInstances(ctx, instanceRepo, rs, logFail)
		if ranOut("instances", 0) {
			return lost
		}
		users := loadStreamUsers(ctx, userRepo, rs, logFail)
		if ranOut("users", 0) {
			return lost
		}
		flags := loadStreamClusterFlags(ctx, clusterRepo, instances, logFail)
		if ranOut("clusters", 0) {
			return lost
		}

		for n, r := range rs {
			if ranOut("sessions", n) {
				return lost
			}
			i := idx[n]
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
			if flags != nil && r.Kind() == podstream.KindPortForward {
				allowed, found := flags[inst.ClusterID]
				if !found || !allowed {
					lost[i] = true
				}
			}
		}
		return lost
	}
}

// streamKindEnabled reports whether the feature switch of the session kind is
// on. A kind that this checker does not know stays (the switch of another
// feature must not close it).
func streamKindEnabled(cfg config.StreamConfig, kind string) bool {
	switch kind {
	case podstream.KindPortForward:
		return cfg.PortForward.Enabled
	default:
		return true
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

// loadStreamInstances reads the distinct instances of the sessions with one
// batch call.
func loadStreamInstances(ctx context.Context, repo models.StackInstanceRepository, rs []websocket.Revocable, logFail func(string, ...any)) streamInstances {
	out := streamInstances{byID: map[string]*models.StackInstance{}}
	seen := map[string]bool{}
	var ids []string
	for _, r := range rs {
		if id := r.InstanceID(); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	found, err := repo.FindByIDs(ctx, ids)
	if err != nil {
		if ctx.Err() == nil {
			logFail("Stream revalidation: instance lookup failed; the sessions stay until the next run", "error", err)
		}
		return out
	}
	for _, id := range ids {
		out.byID[id] = found[id] // nil: the instance is gone
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

// loadStreamClusterFlags returns allow_port_forward of the clusters that the
// known instances use (the default cluster under models.DefaultClusterKey for
// an instance without a cluster ID), read with one projected call. It returns
// nil when the lookup failed, there is no repository or no cluster is needed.
func loadStreamClusterFlags(ctx context.Context, repo models.ClusterRepository, instances streamInstances, logFail func(string, ...any)) map[string]bool {
	if repo == nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	needDefault := false
	for _, inst := range instances.byID {
		if inst == nil || !streamInstanceLive(inst) {
			continue
		}
		switch {
		case inst.ClusterID == models.DefaultClusterKey:
			needDefault = true
		case !seen[inst.ClusterID]:
			seen[inst.ClusterID] = true
			ids = append(ids, inst.ClusterID)
		}
	}
	if len(ids) == 0 && !needDefault {
		return nil
	}
	flags, err := repo.StreamFlagsByIDs(ctx, ids, needDefault)
	if err != nil {
		if ctx.Err() == nil {
			logFail("Stream revalidation: cluster lookup failed; the sessions stay until the next run", "error", err)
		}
		return nil
	}
	return flags
}
