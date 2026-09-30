package setup

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestOperationCoordinator(t *testing.T) {
	client := fake.NewSimpleClientset()
	labA, labB := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	first := NewOperationCoordinator(client, "plugin-ns")
	second := NewOperationCoordinator(client, "plugin-ns")
	t.Run("same user and catalog cannot overlap across instances", func(t *testing.T) {
		ctx, release, err := first.Acquire(context.Background(), "alice", labA)
		require.NoError(t, err)
		defer release()
		assert.NoError(t, ctx.Err())
		_, _, err = second.Acquire(context.Background(), "alice", labA)
		require.ErrorIs(t, err, ErrOperationBusy)
	})
	t.Run("different labs remain independent", func(t *testing.T) {
		_, releaseFirst, err := first.Acquire(context.Background(), "alice", labA)
		require.NoError(t, err)
		defer releaseFirst()
		_, releaseSecond, err := second.Acquire(context.Background(), "alice", labB)
		require.NoError(t, err)
		releaseSecond()
	})
	t.Run("releases lock after work completes", func(t *testing.T) {
		_, release, err := first.Acquire(context.Background(), "bob", labA)
		require.NoError(t, err)
		release()
		ctx, releaseAgain, err := second.Acquire(context.Background(), "bob", labA)
		require.NoError(t, err)
		assert.NoError(t, ctx.Err())
		releaseAgain()
	})
	t.Run("cancellation prevents acquisition", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := first.Acquire(ctx, "cancelled", labA)
		require.Error(t, err)
	})
	t.Run("restart cannot take over an unfinished operation", func(t *testing.T) {
		_, release, err := first.Acquire(context.Background(), "crashed-user", labA)
		require.NoError(t, err)
		defer release()
		restarted := NewOperationCoordinator(client, "plugin-ns")
		_, _, err = restarted.Acquire(context.Background(), "crashed-user", labA)
		require.ErrorIs(t, err, ErrOperationBusy)
	})
	t.Run("identity is stable for same pair and distinct for another", func(t *testing.T) {
		name := operationLeaseName("alice", labA)
		assert.Equal(t, name, operationLeaseName("alice", labA))
		assert.NotEqual(t, name, operationLeaseName("alice", labB))
		assert.LessOrEqual(t, len(name), 63)
	})
}

// client-go's object tracker does not enforce optimistic concurrency. These
// reactors make stale updates fail like an API server, including heartbeat races.
func coordinatorTestClient(objects ...runtime.Object) *fake.Clientset {
	client := fake.NewSimpleClientset(objects...)
	client.PrependReactor("create", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*coordinationv1.Lease).ResourceVersion = "1"
		return false, nil, nil
	})
	client.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
		lease := action.(ktesting.UpdateAction).GetObject().(*coordinationv1.Lease)
		obj, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), lease.Name)
		if err != nil {
			return true, nil, err
		}
		current := obj.(*coordinationv1.Lease)
		if lease.ResourceVersion == "" || lease.ResourceVersion != current.ResourceVersion {
			return true, nil, apierrors.NewConflict(action.GetResource().GroupResource(), lease.Name, nil)
		}
		next := lease.DeepCopy()
		version, _ := strconv.Atoi(current.ResourceVersion)
		next.ResourceVersion = strconv.Itoa(version + 1)
		err = client.Tracker().Update(action.GetResource(), next, action.GetNamespace())
		return true, next, err
	})
	return client
}

func coordinatorHeldLease(t *testing.T, ref string, renewed *metav1.MicroTime) *coordinationv1.Lease {
	t.Helper()
	label, err := CatalogRefLabel(ref)
	require.NoError(t, err)
	holder := strings.Repeat("e", 32)
	duration := int32(200)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name: operationLeaseName("alice", ref), Namespace: "plugin-ns", ResourceVersion: "1",
			Labels:      map[string]string{CatalogRefLabelKey: label},
			Annotations: map[string]string{CatalogRefLabelKey: ref, "interactive-labs.openshift.io/subject": "alice"},
		},
		Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, RenewTime: renewed, LeaseDurationSeconds: &duration},
	}
}

func TestOperationCoordinatorRenewal(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	ctx := context.Background()
	t.Run("expired held lease is reacquired but fresh and invalid holders fail closed", func(t *testing.T) {
		stale := metav1.NewMicroTime(time.Now().Add(-201 * time.Second))
		fresh := metav1.NewMicroTime(time.Now())
		future := metav1.NewMicroTime(time.Now().Add(time.Hour))
		zero := metav1.MicroTime{}
		for _, tc := range []struct {
			name    string
			renewed *metav1.MicroTime
			busy    bool
		}{
			{"expired", &stale, false}, {"fresh", &fresh, true}, {"future", &future, true},
			{"missing renewTime", nil, true}, {"invalid zero renewTime", &zero, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				client := coordinatorTestClient(coordinatorHeldLease(t, ref, tc.renewed))
				coordinator := NewOperationCoordinator(client, "plugin-ns")
				assert.Equal(t, tc.busy, coordinator.IsHeld(ctx, "alice", ref))
				operationCtx, finish, err := coordinator.Acquire(ctx, "alice", ref)
				if tc.busy {
					require.ErrorIs(t, err, ErrOperationBusy)
					return
				}
				require.NoError(t, err)
				defer finish()
				assert.NoError(t, operationCtx.Err())
				current, err := client.CoordinationV1().Leases("plugin-ns").Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
				require.NoError(t, err)
				assert.NotEqual(t, strings.Repeat("e", 32), *current.Spec.HolderIdentity)
				require.NotNil(t, current.Spec.RenewTime)
				assert.WithinDuration(t, time.Now(), current.Spec.RenewTime.Time, time.Second)
				assert.EqualValues(t, 200, *current.Spec.LeaseDurationSeconds)
			})
		}
	})
	t.Run("explicit setup clears old teardown receipt before a new recovery", func(t *testing.T) {
		lease := coordinatorHeldLease(t, ref, nil)
		empty := ""
		lease.Spec.HolderIdentity = &empty
		lease.Annotations[teardownIDAnnotation], lease.Annotations[teardownStatusAnnotation] = strings.Repeat("f", 32), "succeeded"
		client := coordinatorTestClient(lease)
		coordinator := NewOperationCoordinator(client, "plugin-ns")
		_, finish, err := coordinator.Acquire(ctx, "alice", ref)
		require.NoError(t, err)
		defer finish()
		current, err := client.CoordinationV1().Leases("plugin-ns").Get(ctx, lease.Name, metav1.GetOptions{})
		require.NoError(t, err)
		assert.NotContains(t, current.Annotations, teardownIDAnnotation)
		assert.NotContains(t, current.Annotations, teardownStatusAnnotation)
	})
	t.Run("missing renewal fields fail closed without rewriting the Lease", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			check func(*testing.T, *OperationCoordinator, string, string)
		}{
			{"held", func(t *testing.T, c *OperationCoordinator, ref, _ string) {
				assert.True(t, c.IsHeld(ctx, "alice", ref))
			}},
			{"acquire", func(t *testing.T, c *OperationCoordinator, ref, _ string) {
				_, _, err := c.Acquire(ctx, "alice", ref)
				require.ErrorIs(t, err, ErrOperationBusy)
			}},
			{"acquire teardown", func(t *testing.T, c *OperationCoordinator, ref, _ string) {
				_, _, _, err := c.AcquireTeardown(ctx, "alice", ref)
				require.ErrorIs(t, err, ErrOperationBusy)
			}},
			{"current teardown", func(t *testing.T, c *OperationCoordinator, ref, _ string) {
				_, err := c.CurrentTeardownID(ctx, "alice", ref)
				require.ErrorIs(t, err, ErrTeardownNotFound)
			}},
			{"teardown status", func(t *testing.T, c *OperationCoordinator, ref, id string) {
				status, err := c.TeardownStatus(ctx, "alice", ref, id)
				require.NoError(t, err)
				assert.Equal(t, "unknown", status)
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				lease := coordinatorHeldLease(t, ref, nil)
				lease.Spec.LeaseDurationSeconds = nil
				id := *lease.Spec.HolderIdentity
				lease.Annotations[teardownIDAnnotation] = id
				lease.Annotations[teardownStatusAnnotation] = "running"
				client := coordinatorTestClient(lease)
				coordinator := NewOperationCoordinator(client, "plugin-ns")
				tc.check(t, coordinator, ref, id)
				current, err := client.CoordinationV1().Leases("plugin-ns").Get(ctx, lease.Name, metav1.GetOptions{})
				require.NoError(t, err)
				assert.Nil(t, current.Spec.RenewTime)
				assert.Nil(t, current.Spec.LeaseDurationSeconds)
			})
		}
	})
	t.Run("heartbeat renews active work survives request cancellation and stops on finish", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client := coordinatorTestClient()
			coordinator := NewOperationCoordinator(client, "plugin-ns")
			requestCtx, cancel := context.WithCancel(ctx)
			operationCtx, finish, err := coordinator.Acquire(requestCtx, "alice", ref)
			require.NoError(t, err)
			defer finish()
			cancel()
			assert.NoError(t, operationCtx.Err())
			leases := client.CoordinationV1().Leases("plugin-ns")
			initial, err := leases.Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
			require.NoError(t, err)
			time.Sleep(21 * time.Second) // synctest advances virtual time, not wall time.
			synctest.Wait()
			renewed, err := leases.Get(ctx, initial.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.NotEqual(t, initial.Spec.RenewTime, renewed.Spec.RenewTime)
			assert.NoError(t, operationCtx.Err())
			_, _, err = NewOperationCoordinator(client, "plugin-ns").Acquire(ctx, "alice", ref)
			assert.ErrorIs(t, err, ErrOperationBusy)
			finish()
			assert.ErrorIs(t, operationCtx.Err(), context.Canceled)
			assert.False(t, coordinator.IsHeld(ctx, "alice", ref))
			actions := len(client.Actions())
			time.Sleep(time.Minute)
			synctest.Wait()
			assert.Len(t, client.Actions(), actions, "finish must stop heartbeat requests")
		})
	})
	t.Run("holder loss cancels operation and old finish cannot clear successor", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			client := coordinatorTestClient()
			coordinator := NewOperationCoordinator(client, "plugin-ns")
			operationCtx, _, finish, err := coordinator.AcquireTeardown(ctx, "alice", ref)
			require.NoError(t, err)
			defer func() { _ = finish(false) }()
			leases := client.CoordinationV1().Leases("plugin-ns")
			current, err := leases.Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
			require.NoError(t, err)
			successor := strings.Repeat("f", 32)
			current.Spec.HolderIdentity = &successor
			current.Annotations[teardownIDAnnotation] = successor
			_, err = leases.Update(ctx, current, metav1.UpdateOptions{})
			require.NoError(t, err)
			time.Sleep(21 * time.Second)
			synctest.Wait()
			assert.ErrorIs(t, operationCtx.Err(), context.Canceled)
			assert.ErrorIs(t, finish(true), ErrOperationBusy)
			current, err = leases.Get(ctx, current.Name, metav1.GetOptions{})
			require.NoError(t, err)
			assert.Equal(t, successor, *current.Spec.HolderIdentity)
			assert.Equal(t, "running", current.Annotations[teardownStatusAnnotation])
		})
	})
	t.Run("expired teardown reports unknown until a new explicit setup supersedes its receipt", func(t *testing.T) {
		stale := metav1.NewMicroTime(time.Now().Add(-201 * time.Second))
		lease := coordinatorHeldLease(t, ref, &stale)
		oldID := *lease.Spec.HolderIdentity
		lease.Annotations[teardownIDAnnotation], lease.Annotations[teardownStatusAnnotation] = oldID, "running"
		client := coordinatorTestClient(lease)
		coordinator := NewOperationCoordinator(client, "plugin-ns")
		status, err := coordinator.TeardownStatus(ctx, "alice", ref, oldID)
		require.NoError(t, err)
		assert.Equal(t, "unknown", status)
		_, err = coordinator.CurrentTeardownID(ctx, "alice", ref)
		assert.ErrorIs(t, err, ErrTeardownNotFound)
		_, finish, err := coordinator.Acquire(ctx, "alice", ref)
		require.NoError(t, err)
		defer finish()
		_, err = coordinator.TeardownStatus(ctx, "alice", ref, oldID)
		assert.ErrorIs(t, err, ErrTeardownNotFound)
	})
	t.Run("stale foreign identity remains held and cannot be reclaimed", func(t *testing.T) {
		stale := metav1.NewMicroTime(time.Now().Add(-201 * time.Second))
		lease := coordinatorHeldLease(t, ref, &stale)
		lease.Annotations["interactive-labs.openshift.io/subject"] = "bob"
		coordinator := NewOperationCoordinator(coordinatorTestClient(lease), "plugin-ns")
		assert.True(t, coordinator.IsHeld(ctx, "alice", ref))
		_, _, err := coordinator.Acquire(ctx, "alice", ref)
		assert.ErrorIs(t, err, ErrOperationBusy)
	})
	t.Run("stale acquisition resourceVersion conflict preserves renewed holder", func(t *testing.T) {
		stale := metav1.NewMicroTime(time.Now().Add(-201 * time.Second))
		client := coordinatorTestClient(coordinatorHeldLease(t, ref, &stale))
		coordinator := NewOperationCoordinator(client, "plugin-ns")
		client.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
			attempt := action.(ktesting.UpdateAction).GetObject().(*coordinationv1.Lease)
			obj, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), attempt.Name)
			require.NoError(t, err)
			current := obj.(*coordinationv1.Lease)
			current.ResourceVersion = "2"
			fresh := metav1.NewMicroTime(time.Now())
			current.Spec.RenewTime = &fresh
			require.NoError(t, client.Tracker().Update(action.GetResource(), current, action.GetNamespace()))
			return false, nil, nil // The next reactor rejects stale takeover.
		})
		_, _, err := coordinator.Acquire(ctx, "alice", ref)
		assert.ErrorIs(t, err, ErrOperationBusy)
		current, err := client.CoordinationV1().Leases("plugin-ns").Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, "2", current.ResourceVersion)
		assert.Equal(t, strings.Repeat("e", 32), *current.Spec.HolderIdentity)
		assert.True(t, coordinator.IsHeld(ctx, "alice", ref))
	})
	t.Run("release resourceVersion conflict never clears successor", func(t *testing.T) {
		client := coordinatorTestClient()
		coordinator := NewOperationCoordinator(client, "plugin-ns")
		_, _, finish, err := coordinator.AcquireTeardown(ctx, "alice", ref)
		require.NoError(t, err)
		successor := strings.Repeat("f", 32)
		client.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
			attempt := action.(ktesting.UpdateAction).GetObject().(*coordinationv1.Lease)
			if attempt.Spec.HolderIdentity == nil || *attempt.Spec.HolderIdentity != "" {
				return false, nil, nil
			}
			obj, err := client.Tracker().Get(action.GetResource(), action.GetNamespace(), attempt.Name)
			require.NoError(t, err)
			current := obj.(*coordinationv1.Lease)
			current.ResourceVersion = "2"
			current.Spec.HolderIdentity = &successor
			current.Annotations[teardownIDAnnotation] = successor
			require.NoError(t, client.Tracker().Update(action.GetResource(), current, action.GetNamespace()))
			return false, nil, nil // The next reactor rejects the stale release.
		})
		assert.True(t, apierrors.IsConflict(finish(true)))
		current, err := client.CoordinationV1().Leases("plugin-ns").Get(ctx, operationLeaseName("alice", ref), metav1.GetOptions{})
		require.NoError(t, err)
		assert.Equal(t, successor, *current.Spec.HolderIdentity)
		assert.Equal(t, "running", current.Annotations[teardownStatusAnnotation])
	})
}

func TestTeardownReservationReceipt(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	client := fake.NewSimpleClientset()
	first := NewOperationCoordinator(client, "plugin-ns")
	second := NewOperationCoordinator(client, "plugin-ns")
	requestCtx, cancel := context.WithCancel(context.Background())
	workerCtx, id, finish, err := first.AcquireTeardown(requestCtx, "alice", ref)
	require.NoError(t, err)
	require.Len(t, id, 32)
	cancel()
	assert.NoError(t, workerCtx.Err(), "worker must survive request cancellation")
	phase, err := second.TeardownStatus(context.Background(), "alice", ref, id)
	require.NoError(t, err)
	assert.Equal(t, "running", phase)
	activeID, err := second.CurrentTeardownID(context.Background(), "alice", ref)
	require.NoError(t, err)
	assert.Equal(t, id, activeID, "lost POST response can rediscover active teardown")
	_, _, err = second.Acquire(context.Background(), "alice", ref)
	require.ErrorIs(t, err, ErrOperationBusy)
	_, err = second.TeardownStatus(context.Background(), "alice", ref, strings.Repeat("f", 32))
	require.ErrorIs(t, err, ErrTeardownNotFound)
	require.NoError(t, finish(true))
	phase, err = second.TeardownStatus(context.Background(), "alice", ref, id)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", phase)
	_, err = second.CurrentTeardownID(context.Background(), "alice", ref)
	require.ErrorIs(t, err, ErrTeardownNotFound, "unidentified status cannot replay stale success")
	_, unlock, err := second.Acquire(context.Background(), "alice", ref)
	require.NoError(t, err)
	unlock()
}

func TestTeardownReservationUncertainOutcome(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	client := fake.NewSimpleClientset()
	coordinator := NewOperationCoordinator(client, "plugin-ns")
	_, id, _, err := coordinator.AcquireTeardown(context.Background(), "alice", ref)
	require.NoError(t, err)
	lease, err := client.CoordinationV1().Leases("plugin-ns").Get(context.Background(), operationLeaseName("alice", ref), metav1.GetOptions{})
	require.NoError(t, err)
	empty := ""
	lease.Spec.HolderIdentity = &empty // Administrator recovered an abandoned reservation.
	_, err = client.CoordinationV1().Leases("plugin-ns").Update(context.Background(), lease, metav1.UpdateOptions{})
	require.NoError(t, err)
	status, err := coordinator.TeardownStatus(context.Background(), "alice", ref, id)
	require.NoError(t, err)
	assert.Equal(t, "unknown", status, "manual recovery cannot be mistaken for completion")
}

func TestTeardownRejectsForeignLeaseIdentity(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	client := fake.NewSimpleClientset(&coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name: operationLeaseName("alice", ref), Namespace: "plugin-ns",
		Annotations: map[string]string{"interactive-labs.openshift.io/subject": "bob", CatalogRefLabelKey: ref},
	}})
	coordinator := NewOperationCoordinator(client, "plugin-ns")
	_, _, _, err := coordinator.AcquireTeardown(context.Background(), "alice", ref)
	require.ErrorIs(t, err, ErrOperationBusy)
}

func TestTeardownOutcomePublicationFailureRetainsReservation(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	client := fake.NewSimpleClientset()
	coordinator := NewOperationCoordinator(client, "plugin-ns")
	_, id, finish, err := coordinator.AcquireTeardown(context.Background(), "alice", ref)
	require.NoError(t, err)
	client.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
		lease := action.(ktesting.UpdateAction).GetObject().(*coordinationv1.Lease)
		if lease.Annotations[teardownStatusAnnotation] == "succeeded" {
			return true, nil, errors.New("coordination unavailable")
		}
		return false, nil, nil
	})
	require.Error(t, finish(true))
	status, err := coordinator.TeardownStatus(context.Background(), "alice", ref, id)
	require.NoError(t, err)
	assert.Equal(t, "running", status)
	_, _, err = coordinator.Acquire(context.Background(), "alice", ref)
	require.ErrorIs(t, err, ErrOperationBusy)
}

func TestTeardownReservationCrashAndFailure(t *testing.T) {
	ref := strings.Repeat("ab", 32)
	client := fake.NewSimpleClientset()
	first := NewOperationCoordinator(client, "plugin-ns")
	second := NewOperationCoordinator(client, "plugin-ns")
	_, id, _, err := first.AcquireTeardown(context.Background(), "crashed", ref)
	require.NoError(t, err)
	phase, err := second.TeardownStatus(context.Background(), "crashed", ref, id)
	require.NoError(t, err)
	assert.Equal(t, "running", phase, "crashed worker may not be declared successful")
	_, _, err = second.Acquire(context.Background(), "crashed", ref)
	require.ErrorIs(t, err, ErrOperationBusy)
	_, id, finish, err := first.AcquireTeardown(context.Background(), "failed", ref)
	require.NoError(t, err)
	require.NoError(t, finish(false))
	phase, err = second.TeardownStatus(context.Background(), "failed", ref, id)
	require.NoError(t, err)
	assert.Equal(t, "failed", phase)
	_, unlock, err := second.Acquire(context.Background(), "failed", ref)
	require.NoError(t, err)
	unlock()
}
