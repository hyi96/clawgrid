package httpapi

import (
	"context"
	"fmt"
	"hash/fnv"
	"sort"
	"time"

	"clawgrid/internal/domain"
)

type routingJobRow struct {
	id               string
	sessionID        string
	sessionTitle     string
	sessionSnippet   string
	lastCancelReason string
	tipAmount        float64
	timeLimitMinutes int
	cycles           int
	enteredAt        *time.Time
	endsAt           *time.Time
}

type availableResponderRow struct {
	ownerType     string
	ownerID       string
	displayName   string
	description   string
	lastSeenAt    time.Time
	pollStartedAt time.Time
}

type poolJobRow struct {
	id               string
	sessionID        string
	sessionTitle     string
	sessionSnippet   string
	lastCancelReason string
	tipAmount        float64
	timeLimitMinutes int
	cycles           int
	createdAt        time.Time
	enteredAt        *time.Time
	endsAt           *time.Time
}

func dispatchBandSize(activeDispatchers, visibleSlots, baseBand int) int {
	if activeDispatchers < 1 {
		activeDispatchers = 1
	}
	size := activeDispatchers * visibleSlots * 2
	if size < baseBand {
		size = baseBand
	}
	if size > maxDispatchBandItems {
		size = maxDispatchBandItems
	}
	return size
}

func dispatchShuffleScore(actor domain.Actor, itemKind, itemID string, bucket int64) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s|%s|%s|%d|%s", actor.OwnerType, actor.OwnerID, itemKind, bucket, itemID)
	return h.Sum64()
}

func dispatchShuffleBucket(now time.Time) int64 {
	return now.UTC().Unix() / dispatchShuffleBucketSeconds
}

func (s *Server) markDispatcherActivity(ctx context.Context, actor domain.Actor) error {
	if actor.IsZero() {
		return nil
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO dispatcher_activity(owner_type, owner_id, last_seen_at)
VALUES ($1, $2, now())
ON CONFLICT (owner_type, owner_id)
DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at`,
		string(actor.OwnerType), actor.OwnerID,
	)
	return err
}

func (s *Server) recentActiveDispatchers(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM dispatcher_activity
WHERE last_seen_at > now() - make_interval(secs => $1::int)`,
		dispatchActivityLookbackSeconds,
	).Scan(&count)
	return count, err
}

func (s *Server) recentActiveResponders(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM responder_availability
WHERE owner_type = 'account'
  AND last_seen_at > now() - make_interval(secs => $1::int)`,
		dispatchActivityLookbackSeconds,
	).Scan(&count)
	return count, err
}

func shuffleRoutingJobsForDispatcher(rows []routingJobRow, actor domain.Actor, now time.Time) {
	bucket := dispatchShuffleBucket(now)
	sort.SliceStable(rows, func(i, j int) bool {
		left := dispatchShuffleScore(actor, "job", rows[i].id, bucket)
		right := dispatchShuffleScore(actor, "job", rows[j].id, bucket)
		if left == right {
			return rows[i].id < rows[j].id
		}
		return left < right
	})
}

func shuffleRespondersForDispatcher(rows []availableResponderRow, actor domain.Actor, now time.Time) {
	bucket := dispatchShuffleBucket(now)
	sort.SliceStable(rows, func(i, j int) bool {
		left := dispatchShuffleScore(actor, "responder", rows[i].ownerType+":"+rows[i].ownerID, bucket)
		right := dispatchShuffleScore(actor, "responder", rows[j].ownerType+":"+rows[j].ownerID, bucket)
		if left == right {
			if rows[i].ownerType == rows[j].ownerType {
				return rows[i].ownerID < rows[j].ownerID
			}
			return rows[i].ownerType < rows[j].ownerType
		}
		return left < right
	})
}

func shufflePoolJobsForResponder(rows []poolJobRow, actor domain.Actor, now time.Time) {
	bucket := dispatchShuffleBucket(now)
	sort.SliceStable(rows, func(i, j int) bool {
		left := dispatchShuffleScore(actor, "pooljob", rows[i].id, bucket)
		right := dispatchShuffleScore(actor, "pooljob", rows[j].id, bucket)
		if left == right {
			return rows[i].id < rows[j].id
		}
		return left < right
	})
}
