package httpapi

import (
	"testing"
	"time"

	"clawgrid/internal/domain"
)

func TestDispatchBandSizeScalesAndCaps(t *testing.T) {
	t.Parallel()

	if got := dispatchBandSize(0, maxDispatchRoutingJobs, dispatchJobsBandBase); got != dispatchJobsBandBase {
		t.Fatalf("dispatchBandSize(0, jobs) = %d, want %d", got, dispatchJobsBandBase)
	}
	if got := dispatchBandSize(1, maxDispatchResponders, dispatchRespondersBandBase); got != dispatchRespondersBandBase {
		t.Fatalf("dispatchBandSize(1, responders) = %d, want %d", got, dispatchRespondersBandBase)
	}
	if got := dispatchBandSize(20, maxDispatchRoutingJobs, dispatchJobsBandBase); got != maxDispatchBandItems {
		t.Fatalf("dispatchBandSize(20, jobs) = %d, want %d", got, maxDispatchBandItems)
	}
}

func TestShuffleRoutingJobsForDispatcherIsStablePerActorAndBucket(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	rowsA := []routingJobRow{
		{id: "job_a"},
		{id: "job_b"},
		{id: "job_c"},
		{id: "job_d"},
		{id: "job_e"},
		{id: "job_f"},
		{id: "job_g"},
		{id: "job_h"},
	}
	rowsB := []routingJobRow{
		{id: "job_a"},
		{id: "job_b"},
		{id: "job_c"},
		{id: "job_d"},
		{id: "job_e"},
		{id: "job_f"},
		{id: "job_g"},
		{id: "job_h"},
	}
	rowsC := []routingJobRow{
		{id: "job_a"},
		{id: "job_b"},
		{id: "job_c"},
		{id: "job_d"},
		{id: "job_e"},
		{id: "job_f"},
		{id: "job_g"},
		{id: "job_h"},
	}

	actorA := domain.Actor{OwnerType: domain.OwnerAccount, OwnerID: "acct_a"}
	actorB := domain.Actor{OwnerType: domain.OwnerAccount, OwnerID: "acct_b"}

	shuffleRoutingJobsForDispatcher(rowsA, actorA, now)
	shuffleRoutingJobsForDispatcher(rowsB, actorA, now)
	shuffleRoutingJobsForDispatcher(rowsC, actorB, now)

	gotA := []string{rowsA[0].id, rowsA[1].id, rowsA[2].id, rowsA[3].id, rowsA[4].id, rowsA[5].id, rowsA[6].id, rowsA[7].id}
	gotB := []string{rowsB[0].id, rowsB[1].id, rowsB[2].id, rowsB[3].id, rowsB[4].id, rowsB[5].id, rowsB[6].id, rowsB[7].id}
	gotC := []string{rowsC[0].id, rowsC[1].id, rowsC[2].id, rowsC[3].id, rowsC[4].id, rowsC[5].id, rowsC[6].id, rowsC[7].id}

	for i := range gotA {
		if gotA[i] != gotB[i] {
			t.Fatalf("same actor/bucket order mismatch: %v vs %v", gotA, gotB)
		}
	}
	sameAsOtherActor := true
	for i := range gotA {
		if gotA[i] != gotC[i] {
			sameAsOtherActor = false
			break
		}
	}
	if sameAsOtherActor {
		t.Fatalf("different actors produced same shuffle order: %v", gotA)
	}
}

func TestShufflePoolJobsForResponderIsStablePerActorAndBucket(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_800_000_000, 0).UTC()
	rowsA := []poolJobRow{
		{id: "job_a"},
		{id: "job_b"},
		{id: "job_c"},
		{id: "job_d"},
		{id: "job_e"},
		{id: "job_f"},
		{id: "job_g"},
		{id: "job_h"},
	}
	rowsB := []poolJobRow{
		{id: "job_a"},
		{id: "job_b"},
		{id: "job_c"},
		{id: "job_d"},
		{id: "job_e"},
		{id: "job_f"},
		{id: "job_g"},
		{id: "job_h"},
	}
	rowsC := []poolJobRow{
		{id: "job_a"},
		{id: "job_b"},
		{id: "job_c"},
		{id: "job_d"},
		{id: "job_e"},
		{id: "job_f"},
		{id: "job_g"},
		{id: "job_h"},
	}

	actorA := domain.Actor{OwnerType: domain.OwnerAccount, OwnerID: "acct_a"}
	actorB := domain.Actor{OwnerType: domain.OwnerAccount, OwnerID: "acct_b"}

	shufflePoolJobsForResponder(rowsA, actorA, now)
	shufflePoolJobsForResponder(rowsB, actorA, now)
	shufflePoolJobsForResponder(rowsC, actorB, now)

	gotA := []string{rowsA[0].id, rowsA[1].id, rowsA[2].id, rowsA[3].id, rowsA[4].id, rowsA[5].id, rowsA[6].id, rowsA[7].id}
	gotB := []string{rowsB[0].id, rowsB[1].id, rowsB[2].id, rowsB[3].id, rowsB[4].id, rowsB[5].id, rowsB[6].id, rowsB[7].id}
	gotC := []string{rowsC[0].id, rowsC[1].id, rowsC[2].id, rowsC[3].id, rowsC[4].id, rowsC[5].id, rowsC[6].id, rowsC[7].id}

	for i := range gotA {
		if gotA[i] != gotB[i] {
			t.Fatalf("same actor/bucket order mismatch: %v vs %v", gotA, gotB)
		}
	}
	sameAsOtherActor := true
	for i := range gotA {
		if gotA[i] != gotC[i] {
			sameAsOtherActor = false
			break
		}
	}
	if sameAsOtherActor {
		t.Fatalf("different actors produced same pool shuffle order: %v", gotA)
	}
}
