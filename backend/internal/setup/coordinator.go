package setup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	ErrOperationBusy    = errors.New("lab setup or teardown is already running")
	ErrTeardownNotFound = errors.New("teardown operation not found")
)

const (
	teardownIDAnnotation     = "interactive-labs.openshift.io/teardown-id"
	teardownStatusAnnotation = "interactive-labs.openshift.io/teardown-status"
)

const (
	leaseRenewInterval         = 20 * time.Second
	leaseDurationSeconds int32 = 200
)

// OperationCoordinator serializes cluster mutations for one user and catalogRef.
// A stale reservation may be reclaimed after 200 seconds without renewal.
// Expiry is best-effort exclusion, not fencing; an old in-flight Helm call can finish after takeover.
type OperationCoordinator struct {
	client    kubernetes.Interface
	namespace string
}

func NewOperationCoordinator(client kubernetes.Interface, namespace string) *OperationCoordinator {
	return &OperationCoordinator{client: client, namespace: namespace}
}

func operationLeaseName(subject, catalogRef string) string {
	sum := sha256.Sum256([]byte(subject + "\x00" + catalogRef))
	label, _ := CatalogRefLabel(hex.EncodeToString(sum[:]))
	return "il-" + label
}

// IsHeld reports reservations without acquiring or releasing them. Unavailable or
// unreadable coordination fails closed, including after a failed release.
func (c *OperationCoordinator) IsHeld(ctx context.Context, subject, catalogRef string) bool {
	_, held := c.currentHolder(ctx, subject, catalogRef)
	return held
}

// currentHolder lets advisory local state distinguish its own reservation from
// a successor on another pod. Unknown ownership still fails closed.
func (c *OperationCoordinator) currentHolder(ctx context.Context, subject, catalogRef string) (string, bool) {
	if c == nil || c.client == nil || subject == "" {
		return "", true
	}
	if _, err := CatalogRefLabel(catalogRef); err != nil {
		return "", true
	}
	lease, err := c.client.CoordinationV1().Leases(c.namespace).Get(ctx, operationLeaseName(subject, catalogRef), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", false
	}
	if err != nil {
		return "", true
	}
	label, _ := CatalogRefLabel(catalogRef)
	if !reservationMatches(lease, subject, catalogRef, label) {
		return "", true
	}
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" || reservationExpired(lease) {
		return "", false
	}
	return *lease.Spec.HolderIdentity, true
}

// HasTeardownReceipt distinguishes a completed or interrupted explicit cleanup
// from an abandoned setup, so an old polling tab cannot recreate a torn-down lab.
func (c *OperationCoordinator) HasTeardownReceipt(ctx context.Context, subject, ref string) (bool, error) {
	if c == nil || c.client == nil || subject == "" {
		return false, ErrOperationBusy
	}
	label, err := CatalogRefLabel(ref)
	if err != nil {
		return false, err
	}
	lease, err := c.client.CoordinationV1().Leases(c.namespace).Get(ctx, operationLeaseName(subject, ref), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !reservationMatches(lease, subject, ref, label) {
		return false, ErrOperationBusy
	}
	return lease.Annotations[teardownIDAnnotation] != "" && lease.Annotations[teardownStatusAnnotation] != "", nil
}

func reservationMatches(lease *coordinationv1.Lease, subject, ref, label string) bool {
	return lease.Labels[CatalogRefLabelKey] == label && lease.Annotations[CatalogRefLabelKey] == ref &&
		lease.Annotations["interactive-labs.openshift.io/subject"] == subject
}

func reservationExpired(lease *coordinationv1.Lease) bool {
	return reservationHasHeartbeat(lease) &&
		time.Since(lease.Spec.RenewTime.Time) > time.Duration(leaseDurationSeconds)*time.Second
}

func reservationHasHeartbeat(lease *coordinationv1.Lease) bool {
	return lease.Spec.RenewTime != nil && !lease.Spec.RenewTime.IsZero() &&
		lease.Spec.LeaseDurationSeconds != nil && *lease.Spec.LeaseDurationSeconds == leaseDurationSeconds
}

// Acquire reserves this lab for setup. Returned context is canceled if Lease
// ownership is lost. Call returned function when setup work ends to release it.
func (c *OperationCoordinator) Acquire(ctx context.Context, subject, catalogRef string) (context.Context, func(), error) {
	operationCtx, _, finish, err := c.acquire(ctx, subject, catalogRef, false)
	if err != nil {
		return nil, nil, err
	}
	return operationCtx, func() { _ = finish(false) }, nil
}

// AcquireTeardown reserves this lab for teardown and records a "running" receipt
// under returned operation ID. After cleanup ends, call the returned function
// with true on success or false on failure to record outcome and release reservation.
func (c *OperationCoordinator) AcquireTeardown(ctx context.Context, subject, catalogRef string) (context.Context, string, func(bool) error, error) {
	return c.acquire(ctx, subject, catalogRef, true)
}

func (c *OperationCoordinator) acquire(ctx context.Context, subject, catalogRef string, teardown bool) (context.Context, string, func(bool) error, error) {
	name, holder, err := c.reserveLease(ctx, subject, catalogRef, teardown)
	if err != nil {
		return nil, "", nil, err
	}
	operationCtx, cancel := context.WithCancel(context.Background())
	stopRenewal := make(chan struct{})
	renewalDone := make(chan struct{})
	go c.renewLease(name, holder, stopRenewal, renewalDone, cancel) // #nosec G118 -- Renewal outlives the request; each call has a 10s timeout, finish stops it, and lease loss cancels work.

	var once sync.Once
	var finishErr error
	finish := func(succeeded bool) error {
		once.Do(func() {
			close(stopRenewal)
			<-renewalDone
			cancel()
			cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			finishErr = c.finishLease(cleanupCtx, name, holder, teardown, succeeded)
		})
		return finishErr
	}
	return operationCtx, holder, finish, nil
}

// reserveLease creates or updates only a reservation belonging to this lab.
func (c *OperationCoordinator) reserveLease(ctx context.Context, subject, catalogRef string, teardown bool) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if c == nil || c.client == nil || subject == "" {
		return "", "", errors.New("operation coordinator unavailable")
	}
	label, err := CatalogRefLabel(catalogRef)
	if err != nil {
		return "", "", err
	}
	var nonce [16]byte
	if _, nonceErr := rand.Read(nonce[:]); nonceErr != nil {
		return "", "", fmt.Errorf("create lease identity: %w", nonceErr)
	}
	holder := hex.EncodeToString(nonce[:])
	name := operationLeaseName(subject, catalogRef)
	leases := c.client.CoordinationV1().Leases(c.namespace)
	now := metav1.NewMicroTime(time.Now())
	duration := leaseDurationSeconds
	lease, err := leases.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		annotations := map[string]string{CatalogRefLabelKey: catalogRef, "interactive-labs.openshift.io/subject": subject}
		if teardown {
			annotations[teardownIDAnnotation], annotations[teardownStatusAnnotation] = holder, "running"
		}
		_, err = leases.Create(ctx, &coordinationv1.Lease{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: c.namespace,
				Labels: map[string]string{CatalogRefLabelKey: label}, Annotations: annotations,
			},
			Spec: coordinationv1.LeaseSpec{HolderIdentity: &holder, RenewTime: &now, LeaseDurationSeconds: &duration},
		}, metav1.CreateOptions{})
	} else if err == nil {
		if !reservationMatches(lease, subject, catalogRef, label) {
			return "", "", ErrOperationBusy
		}
		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" && !reservationExpired(lease) {
			return "", "", ErrOperationBusy
		}
		lease.Spec.HolderIdentity = &holder
		lease.Spec.RenewTime, lease.Spec.LeaseDurationSeconds = &now, &duration
		if teardown {
			if lease.Annotations == nil {
				lease.Annotations = map[string]string{}
			}
			lease.Annotations[teardownIDAnnotation], lease.Annotations[teardownStatusAnnotation] = holder, "running"
		} else {
			delete(lease.Annotations, teardownIDAnnotation)
			delete(lease.Annotations, teardownStatusAnnotation)
		}
		_, err = leases.Update(ctx, lease, metav1.UpdateOptions{})
	}
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		return "", "", ErrOperationBusy
	}
	if err != nil {
		return "", "", fmt.Errorf("reserve lab operation: %w", err)
	}
	return name, holder, nil
}

// renewLease cancels work if a heartbeat cannot prove continued ownership.
func (c *OperationCoordinator) renewLease(name, holder string, stopRenewal <-chan struct{}, renewalDone chan<- struct{}, cancel context.CancelFunc) {
	leases := c.client.CoordinationV1().Leases(c.namespace)
	defer close(renewalDone)
	ticker := time.NewTicker(leaseRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopRenewal:
			return
		case <-ticker.C:
			renewCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			current, err := leases.Get(renewCtx, name, metav1.GetOptions{})
			if err == nil && current.Spec.HolderIdentity != nil && *current.Spec.HolderIdentity == holder {
				next := metav1.NewMicroTime(time.Now())
				current.Spec.RenewTime = &next
				_, err = leases.Update(renewCtx, current, metav1.UpdateOptions{})
			} else if err == nil {
				err = ErrOperationBusy
			}
			stop()
			if err != nil {
				cancel() // No proof of continued ownership: stop starting mutations.
				return
			}
		}
	}
}

// finishLease clears only the current holder and publishes teardown outcome atomically.
func (c *OperationCoordinator) finishLease(ctx context.Context, name, holder string, teardown, succeeded bool) error {
	leases := c.client.CoordinationV1().Leases(c.namespace)
	current, err := leases.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if current.Spec.HolderIdentity == nil || *current.Spec.HolderIdentity != holder {
		return ErrOperationBusy // Never clear another worker's reservation.
	}
	if teardown {
		status := "failed"
		if succeeded {
			status = "succeeded"
		}
		current.Annotations[teardownStatusAnnotation] = status
	}
	empty := ""
	current.Spec.HolderIdentity = &empty
	_, err = leases.Update(ctx, current, metav1.UpdateOptions{})
	return err
}

// CurrentTeardownID recovers a running operation after a lost POST response.
// Never discover a terminal receipt without its operation ID.
func (c *OperationCoordinator) CurrentTeardownID(ctx context.Context, subject, catalogRef string) (string, error) {
	if c == nil || c.client == nil || subject == "" {
		return "", ErrTeardownNotFound
	}
	if _, err := CatalogRefLabel(catalogRef); err != nil {
		return "", ErrTeardownNotFound
	}
	lease, err := c.client.CoordinationV1().Leases(c.namespace).Get(ctx, operationLeaseName(subject, catalogRef), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", ErrTeardownNotFound
	}
	if err != nil {
		return "", err
	}
	id := lease.Annotations[teardownIDAnnotation]
	if lease.Annotations["interactive-labs.openshift.io/subject"] != subject ||
		lease.Annotations[CatalogRefLabelKey] != catalogRef || lease.Annotations[teardownStatusAnnotation] != "running" ||
		lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != id || len(id) != 32 ||
		!reservationHasHeartbeat(lease) || reservationExpired(lease) {
		return "", ErrTeardownNotFound
	}
	return id, nil
}

// TeardownStatus reads the latest receipt across pods, even after resources vanish.
func (c *OperationCoordinator) TeardownStatus(ctx context.Context, subject, catalogRef, id string) (string, error) {
	if c == nil || c.client == nil || subject == "" || len(id) != 32 {
		return "", ErrTeardownNotFound
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", ErrTeardownNotFound
	}
	if _, err := CatalogRefLabel(catalogRef); err != nil {
		return "", ErrTeardownNotFound
	}
	lease, err := c.client.CoordinationV1().Leases(c.namespace).Get(ctx, operationLeaseName(subject, catalogRef), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", ErrTeardownNotFound
	}
	if err != nil {
		return "", err
	}
	if lease.Annotations["interactive-labs.openshift.io/subject"] != subject ||
		lease.Annotations[CatalogRefLabelKey] != catalogRef || lease.Annotations[teardownIDAnnotation] != id {
		return "", ErrTeardownNotFound
	}
	status := lease.Annotations[teardownStatusAnnotation]
	heldByOperation := lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity == id
	if status == "running" && heldByOperation && reservationHasHeartbeat(lease) && !reservationExpired(lease) {
		return "running", nil // A stale heartbeat is unknown, never success.
	}
	if (status == "succeeded" || status == "failed") && !heldByOperation {
		return status, nil // Terminal receipt was stored with reservation release.
	}
	return "unknown", nil // Stale takeover or incomplete receipt; never infer success.
}
