package hooks

import (
	"context"
	"log/slog"
	"time"

	"backend/internal/cache"
	"backend/internal/ctxutil"
)

// batchLookupResult is the answer of one lookup call.
type batchLookupResult[V any] struct {
	values map[string]V
	err    error
}

// batchLookup returns the value of each ID in ids: from c, and with one
// lookup call for the uncached IDs. An ID that lookup does not return gets
// the zero value, which is cached too (an unknown ID is not read again
// within the cache TTL).
//
// The wait ends at wait or when ctx ends; the query keeps the values of ctx,
// not its cancel, and runs at most queryTimeout (#656), so a late answer
// still fills the cache. complete is false when the lookup failed, timed out
// or ctx ended: the uncached IDs are then missing from the result. what names
// the lookup in the log lines (for example "cluster name").
func batchLookup[V any](ctx context.Context, c *cache.TTLCache[V], ids []string, wait, queryTimeout time.Duration, what string, lookup func(context.Context, []string) (map[string]V, error)) (values map[string]V, complete bool) {
	out := make(map[string]V, len(ids))
	var missing []string
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		if v, ok := c.Get(id); ok {
			out[id] = v
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return out, true
	}

	done := make(chan batchLookupResult[V], 1)
	queryCtx, cancelQuery := ctxutil.Detach(ctx, queryTimeout)
	go func() {
		defer cancelQuery()
		found, err := lookup(queryCtx, missing)
		if err == nil {
			for _, id := range missing {
				c.Set(id, found[id])
			}
		}
		done <- batchLookupResult[V]{values: found, err: err}
	}()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case res := <-done:
		if res.err != nil {
			slog.Warn("hooks: "+what+" lookup failed", "error", res.err)
			return out, false
		}
		for _, id := range missing {
			out[id] = res.values[id]
		}
		return out, true
	case <-timer.C:
		slog.Warn("hooks: "+what+" lookup timed out", "timeout", wait)
	case <-ctx.Done():
	}
	return out, false
}
