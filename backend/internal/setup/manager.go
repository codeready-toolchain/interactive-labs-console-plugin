package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ManagerConfig supplies existing cluster clients and setup settings.
type ManagerConfig struct {
	Helm                 HelmManager
	SpaceRequests        *SpaceRequestClient
	Coordinator          *OperationCoordinator
	TierName             string
	HelmTimeout          time.Duration
	ClusterIngressDomain string
}

// Manager owns setup and teardown lifecycle; request authentication and catalog
// resolution remain the caller's responsibility.
type Manager struct {
	config  ManagerConfig
	workers sync.Map // home namespace + catalogRef -> immutable state; never stores user tokens
}

type managerWorkerState struct {
	owner      string // Lease holder identity; never a user token.
	namespace  string
	catalogRef string
	phase      string
	message    string
}

func NewManager(config ManagerConfig) *Manager { return &Manager{config: config} }

// ManagerError is a safe domain failure for the HTTP layer to map to a response.
// Underlying client errors are deliberately not exposed: they can contain tokens.
type ManagerError struct {
	Kind    ManagerErrorKind
	Message string
}

type ManagerErrorKind string

const (
	ManagerConflict    ManagerErrorKind = "conflict"
	ManagerUnavailable ManagerErrorKind = "unavailable"
	ManagerBadGateway  ManagerErrorKind = "bad_gateway"
	ManagerBadRequest  ManagerErrorKind = "bad_request"
	ManagerNotFound    ManagerErrorKind = "not_found"
)

func (e *ManagerError) Error() string { return e.Message }

func managerError(kind ManagerErrorKind, message string) error {
	return &ManagerError{Kind: kind, Message: message}
}

// Status preserves the setup status wire shape without exposing a token.
type Status struct {
	APIVersion    string              `json:"apiVersion"`
	Kind          string              `json:"kind"`
	SpaceRequests SpaceRequestsStatus `json:"spaceRequests"`
	Helm          HelmSetupStatus     `json:"helm"`
	RetrySetup    bool                `json:"retrySetup,omitempty"`
	Attributes    map[string]string   `json:"attributes,omitempty"`
}

type SpaceRequestsStatus struct {
	Status     string                     `json:"status"`
	Namespaces map[string]NamespaceStatus `json:"namespaces"`
}

type NamespaceStatus struct {
	Status string `json:"status"`
	Name   string `json:"name"`
}

type HelmSetupStatus struct {
	Status          string            `json:"status"`
	ReleaseName     string            `json:"releaseName"`
	Message         string            `json:"message,omitempty"`
	GeneratedValues map[string]string `json:"generatedValues,omitempty"`
}

func newStatus(srStatus, helmStatus, releaseName string, namespaces map[string]NamespaceStatus) Status {
	return Status{
		APIVersion: "v1alpha1", Kind: "SetupStatus",
		SpaceRequests: SpaceRequestsStatus{Status: srStatus, Namespaces: namespaces},
		Helm:          HelmSetupStatus{Status: helmStatus, ReleaseName: releaseName},
	}
}

func (m *Manager) available() bool {
	return m != nil && m.config.Helm != nil && m.config.SpaceRequests != nil
}

func subjectHomeSpace(subject string) string { return subject + "-dev" }

// labRelease selects a unique, identity-verified release independently of the
// current chart schema. Cleanup eligibility does not restrict opening a lab.
func (m *Manager) labRelease(ctx context.Context, namespace, token, ref string) (*LabRelease, error) {
	releases, err := m.config.Helm.ListLabReleases(ctx, namespace, token, nil)
	if err != nil {
		return nil, managerError(ManagerBadGateway, "Unable to check Helm setup. Check permissions before retrying.")
	}
	var existing *LabRelease
	for i := range releases {
		r := &releases[i]
		if r.CatalogRef != ref {
			continue
		}
		if existing != nil {
			return nil, managerError(ManagerConflict, "Multiple releases found for this lab. Contact an administrator.")
		}
		if r.Name == "" || r.Namespace != namespace || r.UID == "" {
			return nil, managerError(ManagerConflict, "Unverified existing lab release. Contact an administrator before retrying.")
		}
		copy := *r // Adapters must not change the selected identity during reads.
		existing = &copy
	}
	return existing, nil
}

// Start either reports an existing verified deployment or hands the reservation
// to a background worker. bool is true only for an already-ready setup.
func (m *Manager) Start(ctx context.Context, subject, token, ref, chartDir string, bindings SchemaBindings) (Status, bool, error) {
	if !m.available() || m.config.Coordinator == nil {
		return Status{}, false, managerError(ManagerUnavailable, "lab setup unavailable")
	}
	if subject == "" || token == "" {
		return Status{}, false, managerError(ManagerBadRequest, "authentication required")
	}
	label, err := CatalogRefLabel(ref)
	if err != nil {
		return Status{}, false, managerError(ManagerBadRequest, "invalid catalog identity")
	}
	operationCtx, id, finish, err := m.config.Coordinator.acquire(ctx, subject, ref, false)
	if err != nil {
		return Status{}, false, managerError(ManagerConflict, "lab setup or teardown is already running")
	}
	release := func() { _ = finish(false) }
	workerOwnsLock := false
	defer func() {
		if !workerOwnsLock {
			release()
		}
	}()
	preflightCtx, cancelPreflight := context.WithCancel(operationCtx)
	defer cancelPreflight()
	stopRequest := context.AfterFunc(ctx, cancelPreflight)
	defer stopRequest()

	namespace := subjectHomeSpace(subject)
	releaseName := HelmReleaseName(subject, ref, bindings.Namespaces)
	existing, err := m.labRelease(preflightCtx, namespace, token, ref)
	if err != nil {
		return Status{}, false, err
	}
	if existing != nil && existing.Status != "deployed" {
		return Status{}, false, managerError(ManagerConflict, "Existing lab setup must finish or be cleaned up before relaunch.")
	}
	// The reservation ends at DELETE acceptance, but SR finalizers may still run.
	// Inspect all bindings for this lab, including names removed from its schema.
	srs, err := m.config.SpaceRequests.ListSpaceRequestsForLab(preflightCtx, token, namespace, ref)
	if err != nil {
		if existing != nil {
			return Status{}, false, managerError(ManagerConflict, "Existing lab namespace bindings changed. Clean up before relaunch.")
		}
		return Status{}, false, managerError(ManagerBadGateway, "Unable to check existing SpaceRequests. Check permissions before retrying.")
	}
	for _, sr := range srs {
		if sr.DeletionTimestamp != "" {
			return Status{}, false, managerError(ManagerConflict, "Lab namespaces are still terminating. Wait for cleanup before relaunch.")
		}
	}
	if existing != nil {
		if len(srs) != len(bindings.Namespaces) {
			return Status{}, false, managerError(ManagerConflict, "Existing lab namespace bindings changed. Clean up before relaunch.")
		}
		for _, b := range bindings.Namespaces {
			matched := false
			for _, sr := range srs {
				if sr.Annotations[BindingKeyAnnotation] == b.Key && sr.IsReady() && sr.TargetNamespace() != "" {
					matched = true
					break
				}
			}
			if !matched {
				return Status{}, false, managerError(ManagerConflict, "Existing lab namespaces are not ready or bindings changed. Refresh or clean up before relaunch.")
			}
		}
		current, err := m.labRelease(preflightCtx, namespace, token, ref)
		if err != nil {
			return Status{}, false, err
		}
		if current == nil || !current.sameSummary(*existing) {
			return Status{}, false, managerError(ManagerConflict, "Lab release changed. Teardown before reopening.")
		}
		return newStatus("ready", "deployed", existing.Name, nil), true, nil
	}
	status, err := m.config.Helm.ReleaseStatus(preflightCtx, releaseName, namespace, token)
	if err != nil || status != "" {
		return Status{}, false, managerError(ManagerConflict, "Unverified existing lab release. Contact an administrator before retrying.")
	}

	tier := m.config.TierName
	if tier == "" {
		tier = "base1ns"
	}
	uids := make(map[string]string, len(bindings.Namespaces))
	for _, b := range bindings.Namespaces {
		if preflightCtx.Err() != nil {
			return Status{}, false, managerError(ManagerConflict, "Lab operation lost its reservation; reopen to retry.")
		}
		if err := m.config.SpaceRequests.EnsureSpaceRequestForLab(preflightCtx, token, namespace, b.Key, tier, ref, b.Key); err != nil {
			if errors.Is(err, errSpaceRequestTerminating) {
				return Status{}, false, managerError(ManagerConflict, "Lab namespaces are still terminating. Wait for cleanup before relaunch.")
			}
			return Status{}, false, managerError(ManagerBadGateway, "failed to create SpaceRequest")
		}
		sr, err := m.config.SpaceRequests.GetSpaceRequest(preflightCtx, token, namespace, b.Key)
		if err != nil || sr == nil || sr.DeletionTimestamp != "" || sr.Name != b.Key || sr.Namespace != namespace || sr.UID == "" ||
			sr.Labels[CatalogRefLabelKey] != label || sr.Annotations[BindingKeyAnnotation] != b.Key {
			return Status{}, false, managerError(ManagerConflict, "SpaceRequest identity changed; refresh before retrying.")
		}
		uids[b.Key] = sr.UID
	}
	if preflightCtx.Err() != nil {
		return Status{}, false, managerError(ManagerConflict, "Lab operation lost its reservation; reopen to retry.")
	}
	workerOwnsLock = true
	worker := managerWorkerState{owner: id, namespace: namespace, catalogRef: ref, phase: "polling"}
	m.workers.Store(namespace+"\x00"+ref, worker)
	go func() {
		defer release()
		m.runWorker(operationCtx, token, namespace, releaseName, ref, chartDir, bindings, uids, worker)
	}()
	return newStatus("provisioning", "pending", releaseName, nil), false, nil
}

const (
	managerPollInterval = 5 * time.Second
	managerPollTimeout  = 30 * time.Minute
)

func (m *Manager) runWorker(ctx context.Context, token, namespace, releaseName, ref, chartDir string, bindings SchemaBindings, uids map[string]string, worker managerWorkerState) {
	store := func(phase, message string) {
		next := worker
		next.phase, next.message = phase, message
		m.workers.CompareAndSwap(namespace+"\x00"+ref, worker, next)
		worker = next
	}
	fail := func(message string) { store("failed", message) }
	pollCtx, cancel := context.WithTimeout(ctx, managerPollTimeout)
	defer cancel()
	var namespaces map[string]string
	for {
		var err error
		namespaces, err = mapVerifiedBindingsToValues(pollCtx, m.config.SpaceRequests, token, namespace, ref, bindings.Namespaces, uids)
		if err == nil {
			break
		}
		select {
		case <-pollCtx.Done():
			fail("timed out waiting for SpaceRequests")
			return
		case <-time.After(managerPollInterval):
		}
	}
	values := make(map[string]string, len(namespaces)+len(bindings.Passwords)+len(bindings.RootURLs))
	for k, v := range namespaces {
		values[k] = v
	}
	if len(bindings.Passwords) > 0 {
		passwords, err := GeneratePasswordValues(bindings.Passwords)
		if err != nil {
			fail("password generation failed")
			return
		}
		for k, v := range passwords {
			values[k] = v
		}
	}
	for _, b := range bindings.RootURLs {
		if m.config.ClusterIngressDomain == "" {
			fail("cluster ingress domain not configured")
			return
		}
		ns := namespaces[NamespaceBindingPath(bindings.Namespaces, b.NamespaceRef)]
		if ns == "" {
			fail("root-url references unknown namespace binding")
			return
		}
		values[b.Path] = fmt.Sprintf("%s-%s.%s", b.RouteName, ns, m.config.ClusterIngressDomain)
	}
	label, err := CatalogRefLabel(ref)
	if err != nil {
		fail("invalid catalog identity")
		return
	}
	timeout := m.config.HelmTimeout
	if timeout == 0 {
		timeout = 300 * time.Second
	}
	store("installing", "")
	installCtx, cancelInstall := context.WithTimeout(ctx, timeout+30*time.Second)
	defer cancelInstall()
	if err := m.config.Helm.Install(installCtx, HelmInstallOpts{
		ChartDir: chartDir, ReleaseName: releaseName, Namespace: namespace, Token: token,
		Values: values, Labels: map[string]string{CatalogRefLabelKey: label}, Timeout: timeout,
	}); err != nil {
		fail("Helm install failed")
		return
	}
	store("done", "")
}

// Status reports SpaceRequest readiness and Helm state, including only explicitly
// generated value paths and their corresponding attributes.
func (m *Manager) Status(ctx context.Context, subject, token, ref string, bindings SchemaBindings) (Status, error) {
	if m == nil || m.config.SpaceRequests == nil {
		return Status{}, managerError(ManagerUnavailable, "lab setup unavailable")
	}
	if subject == "" || token == "" {
		return Status{}, managerError(ManagerBadRequest, "authentication required")
	}
	label, err := CatalogRefLabel(ref)
	if err != nil {
		return Status{}, managerError(ManagerBadRequest, "invalid catalog identity")
	}
	namespace := subjectHomeSpace(subject)
	namespaces := make(map[string]NamespaceStatus, len(bindings.Namespaces))
	allReady, anyFailed := true, false
	for _, b := range bindings.Namespaces {
		sr, err := m.config.SpaceRequests.GetSpaceRequest(ctx, token, namespace, b.Key)
		if err != nil {
			return Status{}, managerError(ManagerBadGateway, "Unable to check SpaceRequests. Check permissions before retrying.")
		}
		if sr == nil {
			namespaces[b.Key] = NamespaceStatus{Status: "pending", Name: b.Key}
			allReady = false
			continue
		}
		if sr.Name != b.Key || sr.Namespace != namespace || sr.UID == "" ||
			sr.Labels[CatalogRefLabelKey] != label || sr.Annotations[BindingKeyAnnotation] != b.Key {
			return Status{}, managerError(ManagerConflict, "SpaceRequest identity changed; refresh before retrying.")
		}
		if sr.IsReady() && sr.TargetNamespace() != "" {
			namespaces[b.Key] = NamespaceStatus{Status: "ready", Name: sr.TargetNamespace()}
		} else if sr.IsFailed() {
			namespaces[b.Key] = NamespaceStatus{Status: "failed", Name: b.Key}
			allReady, anyFailed = false, true
		} else {
			namespaces[b.Key] = NamespaceStatus{Status: "pending", Name: b.Key}
			allReady = false
		}
	}
	srStatus := "provisioning"
	if anyFailed {
		srStatus = "failed"
	} else if allReady {
		srStatus = "ready"
	}
	releaseName := HelmReleaseName(subject, ref, bindings.Namespaces)
	result := newStatus(srStatus, "pending", releaseName, namespaces)
	var releaseStatus string
	holder, held := m.config.Coordinator.currentHolder(ctx, subject, ref)
	if m.config.Helm != nil {
		existing, err := m.labRelease(ctx, namespace, token, ref)
		if err != nil {
			return Status{}, err
		}
		if existing != nil {
			result.Helm.ReleaseName = existing.Name
			releaseStatus = existing.Status
			result.Helm.Status = HelmStatusLabel(releaseStatus)
			if result.Helm.Status == "failed" {
				result.Helm.Message = "Lab setup failed. Tear down this lab before launching again. If teardown is unavailable, contact an administrator."
			}
			if allReady && result.Helm.Status == "deployed" && len(bindings.Passwords) > 0 {
				result.Helm.GeneratedValues = make(map[string]string, len(bindings.Passwords))
				for _, b := range bindings.Passwords {
					if value, ok := extractNestedValue(existing.Config, b.Path); ok {
						result.Helm.GeneratedValues[b.Path] = value
					}
				}
			}
		} else if state, ok := m.workers.Load(namespace + "\x00" + ref); ok {
			worker := state.(managerWorkerState)
			switch worker.phase {
			case "installing":
				if held && worker.owner == holder {
					result.Helm.Status = "installing"
				}
			case "failed":
				if !held || worker.owner == holder {
					result.Helm.Status, result.Helm.Message = "failed", worker.message
				}
			}
		}
	}
	// A vanished worker cannot resume without a fresh authenticated POST. A
	// pending release must be reconciled before retry, never installed over.
	if m.config.Helm != nil && m.config.Coordinator != nil && !held {
		if releaseStatus == "" && result.Helm.Status == "pending" && !anyFailed {
			tornDown, err := m.config.Coordinator.HasTeardownReceipt(ctx, subject, ref)
			if err != nil {
				return Status{}, managerError(ManagerUnavailable, "Unable to check lab recovery. Retry status.")
			}
			if tornDown {
				result.Helm.Status, result.Helm.Message = "failed", "Lab was torn down. Close this panel and launch again to recreate it."
			} else {
				result.RetrySetup = true
			}
		} else if releaseStatus != "" && result.Helm.Status != "deployed" && result.Helm.Status != "failed" {
			result.Helm.Status = "failed"
			result.Helm.Message = "Helm installation was interrupted. Refresh setups and tear down this lab before relaunching; unsafe releases need administrator help."
		}
	}
	if allReady && result.Helm.Status == "deployed" {
		result.Attributes = make(map[string]string, len(bindings.Namespaces)+len(bindings.Passwords)+len(bindings.RootURLs))
		for _, b := range bindings.Namespaces {
			result.Attributes[strings.ReplaceAll(b.Path, ".", "-")] = namespaces[b.Key].Name
		}
		for _, b := range bindings.Passwords {
			if value, ok := result.Helm.GeneratedValues[b.Path]; ok {
				result.Attributes[strings.ReplaceAll(b.Path, ".", "-")] = value
			}
		}
		for _, b := range bindings.RootURLs {
			if m.config.ClusterIngressDomain != "" {
				if ns, ok := namespaces[b.NamespaceRef]; ok {
					result.Attributes[strings.ReplaceAll(b.Path, ".", "-")] = fmt.Sprintf("%s-%s.%s", b.RouteName, ns.Name, m.config.ClusterIngressDomain)
				}
			}
		}
	}
	return result, nil
}

// BlockedReason is a fixed, non-secret cleanup blocker code.
type BlockedReason string

const (
	BlockedCleanup      BlockedReason = "cleanup"
	BlockedOperation    BlockedReason = "operation"
	BlockedHelmState    BlockedReason = "helm-state"
	BlockedHelmHook     BlockedReason = "helm-hook"
	BlockedHelmRetained BlockedReason = "helm-retained"
	BlockedHelmManifest BlockedReason = "helm-manifest"
	BlockedPending      BlockedReason = "pending"
	BlockedUnknown      BlockedReason = "unknown"
)

// TeardownProgress is read from the cluster-visible reservation across pods.
type TeardownProgress struct {
	APIVersion  string `json:"apiVersion"`
	Kind        string `json:"kind"`
	CatalogRef  string `json:"catalogRef"`
	OperationID string `json:"operationId"`
	Status      string `json:"status"`
}

func teardownProgress(ref, id, status string) TeardownProgress {
	return TeardownProgress{APIVersion: "v1alpha1", Kind: "TeardownStatus", CatalogRef: ref, OperationID: id, Status: status}
}

// LabSetup is a catalog-independent inventory item. Status is pending, deployed,
// or failed; Blocked describes cleanup safety independently of opening a deployment.
// Generation confirms exact release and SpaceRequest identities at teardown time.
type LabSetup struct {
	CatalogRef    string        `json:"catalogRef"`
	Generation    string        `json:"generation"`
	Status        string        `json:"status"`
	Release       *LabRelease   `json:"release,omitempty"`
	SpaceRequests []string      `json:"spaceRequests"`
	Blocked       bool          `json:"blocked,omitempty"`
	BlockedReason BlockedReason `json:"blockedReason,omitempty"`
	srs           []SpaceRequestResult
}

func (m *Manager) List(ctx context.Context, subject, token string) ([]LabSetup, error) {
	return m.list(ctx, subject, token, "")
}

// reservedRef exempts the target lab from the operation blocker during teardown
// confirmation. Acquisition still rejects live holders before cleanup can start.
func (m *Manager) list(ctx context.Context, subject, token, reservedRef string) ([]LabSetup, error) {
	if !m.available() {
		return nil, managerError(ManagerUnavailable, "lab setup unavailable")
	}
	if subject == "" || token == "" {
		return nil, managerError(ManagerBadRequest, "authentication required")
	}
	namespace := subjectHomeSpace(subject)
	srs, err := m.config.SpaceRequests.ListSpaceRequestsForLab(ctx, token, namespace, "")
	if err != nil {
		return nil, managerError(ManagerBadGateway, "Unable to list lab setups. Check permissions or ask an administrator for help.")
	}
	releases, err := m.config.Helm.ListLabReleases(ctx, namespace, token, srs)
	if err != nil {
		return nil, managerError(ManagerBadGateway, "Unable to list lab setups. Check permissions or ask an administrator for help.")
	}
	byRef := map[string]*LabSetup{}
	get := func(ref string) *LabSetup {
		if byRef[ref] == nil {
			byRef[ref] = &LabSetup{CatalogRef: ref, Status: "failed", SpaceRequests: []string{}}
		}
		return byRef[ref]
	}
	for _, sr := range srs {
		ref, err := CatalogRefFromLabel(sr.Labels[CatalogRefLabelKey])
		if err != nil {
			return nil, managerError(ManagerBadGateway, "Unable to list lab setups. Check permissions or ask an administrator for help.")
		}
		item := get(ref)
		item.srs = append(item.srs, sr)
		item.SpaceRequests = append(item.SpaceRequests, sr.Name)
	}
	for _, rel := range releases {
		if _, err := CatalogRefLabel(rel.CatalogRef); err != nil || rel.Name == "" || rel.Namespace != namespace || rel.UID == "" {
			return nil, managerError(ManagerBadGateway, "Unable to list lab setups. Check permissions or ask an administrator for help.")
		}
		item := get(rel.CatalogRef)
		if item.Release != nil {
			return nil, managerError(ManagerBadGateway, "Unable to list lab setups. Check permissions or ask an administrator for help.")
		}
		copy := rel
		copy.Config = nil // Inventory never retains or exposes Helm values.
		item.Release = &copy
		if rel.Status != "deployed" && rel.Status != "failed" {
			item.BlockedReason = BlockedHelmState
		} else if rel.CleanupBlocked {
			switch rel.BlockedReason {
			case BlockedHelmHook, BlockedHelmRetained, BlockedHelmManifest:
				item.BlockedReason = rel.BlockedReason
			default:
				item.BlockedReason = BlockedUnknown
			}
		}
		item.Blocked = item.BlockedReason != ""
	}
	workers := map[string]managerWorkerState{}
	m.workers.Range(func(_, value any) bool {
		worker := value.(managerWorkerState)
		if worker.namespace == namespace {
			workers[worker.catalogRef] = worker
			if worker.phase != "done" {
				get(worker.catalogRef) // Include failures before any cluster resource exists.
			}
		}
		return true
	})
	out := make([]LabSetup, 0, len(byRef))
	for _, item := range byRef {
		holder, held := m.config.Coordinator.currentHolder(ctx, subject, item.CatalogRef)
		held = item.CatalogRef != reservedRef && held
		if item.Release != nil {
			item.Status = HelmStatusLabel(item.Release.Status)
			if item.Release.Status == "pending-install" && !held && !item.Release.CleanupBlocked {
				// After reservation expiry, verified pending installs may be
				// explicitly torn down; never overlap an active installer.
				item.Status, item.BlockedReason, item.Blocked = "failed", "", false
			} else if item.Status == "installing" {
				item.Status = "pending"
			}
		} else if held {
			item.Status = "pending"
		}
		// Local phases are advisory: only the cluster release and live Lease
		// determine readiness and exclusion after restart or takeover.
		if worker, ok := workers[item.CatalogRef]; ok && item.Release == nil && worker.phase == "failed" && (!held || worker.owner == holder) {
			item.Status = "failed"
		}
		for _, sr := range item.srs {
			if sr.DeletionTimestamp != "" {
				item.Status, item.BlockedReason = "pending", BlockedCleanup
				break
			}
			if sr.IsFailed() {
				item.Status = "failed"
			}
		}
		if held && item.BlockedReason != BlockedCleanup {
			item.BlockedReason = BlockedOperation
		} else if item.BlockedReason == "" && (item.Blocked || item.Status == "pending") {
			item.BlockedReason = BlockedPending
		}
		item.Blocked = item.BlockedReason != ""
		sort.Strings(item.SpaceRequests)
		sort.Slice(item.srs, func(i, j int) bool { return item.srs[i].Name < item.srs[j].Name })
		h := sha256.New()
		for _, sr := range item.srs {
			_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00", sr.Name, sr.UID, sr.Annotations[BindingKeyAnnotation])
		}
		if item.Release != nil {
			rel := item.Release
			_, _ = fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s", rel.Name, rel.UID, rel.Revision, rel.Status)
		}
		item.Generation = fmt.Sprintf("%x", h.Sum(nil))
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CatalogRef < out[j].CatalogRef })
	return out, nil
}

// BeginTeardown validates the confirmed inventory while holding the reservation,
// then hands cleanup and the in-memory user token to a worker before returning.
func (m *Manager) BeginTeardown(ctx context.Context, subject, token, ref, generation string) (TeardownProgress, error) {
	if err := m.validateTeardown(subject, token, ref, generation); err != nil {
		return TeardownProgress{}, err
	}
	// Reject stale confirmations without replacing an earlier operation receipt.
	if _, err := m.teardownTarget(ctx, subject, token, ref, generation); err != nil {
		return TeardownProgress{}, err
	}
	operationCtx, id, finish, err := m.config.Coordinator.AcquireTeardown(ctx, subject, ref)
	if err != nil {
		return TeardownProgress{}, managerError(ManagerConflict, "Lab setup or teardown is running or needs administrator recovery.")
	}
	target, err := m.teardownTarget(operationCtx, subject, token, ref, generation)
	if err != nil {
		_ = finish(false)
		return TeardownProgress{}, err
	}
	key := subjectHomeSpace(subject) + "\x00" + ref
	worker := managerWorkerState{owner: id, namespace: subjectHomeSpace(subject), catalogRef: ref, phase: "tearing-down"}
	m.workers.Store(key, worker)
	go func() {
		err := m.teardownReserved(operationCtx, subject, token, ref, target)
		if finish(err == nil) != nil {
			// The Lease remains held on uncertainty; never report success.
			failed := worker
			failed.phase = "failed"
			m.workers.CompareAndSwap(key, worker, failed)
			return
		}
		m.workers.CompareAndDelete(key, worker)
	}()
	return teardownProgress(ref, id, "running"), nil
}

func (m *Manager) TeardownStatus(ctx context.Context, subject, ref, id string) (TeardownProgress, error) {
	if m == nil || m.config.Coordinator == nil || subject == "" {
		return TeardownProgress{}, managerError(ManagerUnavailable, "lab coordination unavailable")
	}
	var err error
	if id == "" {
		id, err = m.config.Coordinator.CurrentTeardownID(ctx, subject, ref)
	}
	var status string
	if err == nil {
		status, err = m.config.Coordinator.TeardownStatus(ctx, subject, ref, id)
	}
	if errors.Is(err, ErrTeardownNotFound) {
		return TeardownProgress{}, managerError(ManagerNotFound, "Teardown operation not found. Refresh lab setups.")
	}
	if err != nil {
		return TeardownProgress{}, managerError(ManagerUnavailable, "Unable to check teardown status. Retry or contact an administrator.")
	}
	return teardownProgress(ref, id, status), nil
}

func (m *Manager) validateTeardown(subject, token, ref, generation string) error {
	if !m.available() || m.config.Coordinator == nil {
		return managerError(ManagerUnavailable, "lab coordination unavailable")
	}
	if subject == "" || token == "" {
		return managerError(ManagerBadRequest, "authentication required")
	}
	if _, err := CatalogRefLabel(ref); err != nil {
		return managerError(ManagerBadRequest, "invalid catalog identity")
	}
	if generation == "" {
		return managerError(ManagerBadRequest, "teardown requires confirmation of listed setup")
	}
	return nil
}

func (m *Manager) teardownTarget(ctx context.Context, subject, token, ref, generation string) (*LabSetup, error) {
	items, err := m.list(ctx, subject, token, ref)
	if err != nil {
		return nil, managerTeardownFailure()
	}
	for i := range items {
		if items[i].CatalogRef != ref {
			continue
		}
		if items[i].Generation != generation {
			break
		}
		if items[i].Blocked {
			return nil, managerError(ManagerConflict, "Lab release needs administrator recovery before teardown.")
		}
		return &items[i], nil
	}
	return nil, managerError(ManagerConflict, "Lab setup changed. Refresh and confirm again.")
}

func (m *Manager) teardownReserved(ctx context.Context, subject, token, ref string, target *LabSetup) error {
	namespace := subjectHomeSpace(subject)
	currentSRs, err := m.config.SpaceRequests.ListSpaceRequestsForLab(ctx, token, namespace, "")
	if err != nil {
		return managerTeardownFailure()
	}
	byName := make(map[string]SpaceRequestResult, len(currentSRs))
	for _, sr := range currentSRs {
		byName[sr.Name] = sr
	}
	for _, sr := range target.srs {
		current, found := byName[sr.Name]
		if !found || current.Namespace != sr.Namespace || current.UID != sr.UID ||
			current.Labels[CatalogRefLabelKey] != sr.Labels[CatalogRefLabelKey] || current.Annotations[BindingKeyAnnotation] != sr.Annotations[BindingKeyAnnotation] {
			return managerTeardownFailure()
		}
	}
	if target.Release != nil {
		if err := m.config.Helm.UninstallLabRelease(ctx, namespace, token, *target.Release, currentSRs); err != nil {
			return managerTeardownFailure()
		}
	}
	for _, sr := range target.srs {
		if err := m.config.SpaceRequests.DeleteVerifiedSpaceRequest(ctx, token, namespace, ref, sr); err != nil {
			return managerTeardownFailure()
		}
	}
	return nil
}

func managerTeardownFailure() error {
	return managerError(ManagerBadGateway, "Lab cleanup failed. Refresh the setup list before retrying; contact an administrator if the problem persists.")
}
