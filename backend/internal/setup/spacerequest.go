package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	spaceRequestAPIGroup   = "toolchain.dev.openshift.com"
	spaceRequestAPIVersion = "v1alpha1"
	spaceRequestResource   = "spacerequests"
)

var errSpaceRequestTerminating = errors.New("SpaceRequest is terminating")

// SpaceRequestClient performs CRUD operations on SpaceRequest custom resources
// using direct REST calls (no client-go dependency).
type SpaceRequestClient struct {
	HTTPClient *http.Client
	APIHost    string
}

// SpaceRequestCondition represents a single condition on a SpaceRequest.
type SpaceRequestCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// SpaceRequestNamespaceAccess holds a provisioned namespace name and its secret ref.
type SpaceRequestNamespaceAccess struct {
	Name      string `json:"name"`
	SecretRef string `json:"secretRef"`
}

// SpaceRequestResult is the subset of a SpaceRequest we need from the API.
type SpaceRequestResult struct {
	Name              string
	Namespace         string
	UID               string
	DeletionTimestamp string
	Labels            map[string]string
	Annotations       map[string]string
	NamespaceAccess   []SpaceRequestNamespaceAccess
	Conditions        []SpaceRequestCondition
}

// matchesLabBinding checks base identity; callers own lifecycle and pinned UID checks.
func (r *SpaceRequestResult) matchesLabBinding(namespace, name, catalogLabel, bindingKey string) bool {
	return r != nil && r.Name == name && r.Namespace == namespace && r.UID != "" &&
		r.Labels[CatalogRefLabelKey] == catalogLabel && r.Annotations[BindingKeyAnnotation] == bindingKey
}

// IsReady returns true if the SpaceRequest is not terminating and has Ready=True.
func (r *SpaceRequestResult) IsReady() bool {
	if r.DeletionTimestamp != "" {
		return false
	}
	for _, c := range r.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}

// readyNamespace returns a provisioned target only while the request is ready.
func (r *SpaceRequestResult) readyNamespace() string {
	if !r.IsReady() {
		return ""
	}
	return r.TargetNamespace()
}

// TargetNamespace returns the first provisioned namespace name, or empty string.
func (r *SpaceRequestResult) TargetNamespace() string {
	if len(r.NamespaceAccess) > 0 {
		return r.NamespaceAccess[0].Name
	}
	return ""
}

// terminalReasons lists SpaceRequest Ready condition reasons that indicate
// an unrecoverable failure (no point polling further).
var terminalReasons = map[string]bool{
	"UnableToCreateSpace":    true,
	"UnableToCreateSPCOwner": true,
}

// IsFailed returns true if the SpaceRequest has a Ready=False condition with a
// terminal reason. Ready=False with a non-terminal reason (e.g. Provisioning)
// is normal during setup.
func (r *SpaceRequestResult) IsFailed() bool {
	for _, c := range r.Conditions {
		if c.Type == "Ready" && c.Status == "False" && terminalReasons[c.Reason] {
			return true
		}
	}
	return false
}

// ReadyConditionReason returns the reason from the Ready condition, or empty string.
func (r *SpaceRequestResult) ReadyConditionReason() string {
	for _, c := range r.Conditions {
		if c.Type == "Ready" {
			return c.Reason
		}
	}
	return ""
}

func (c *SpaceRequestClient) resourceURL(namespace string) string {
	return fmt.Sprintf("%s/apis/%s/%s/namespaces/%s/%s",
		c.APIHost,
		spaceRequestAPIGroup,
		spaceRequestAPIVersion,
		url.PathEscape(namespace),
		spaceRequestResource,
	)
}

func (c *SpaceRequestClient) namedResourceURL(namespace, name string) string {
	return c.resourceURL(namespace) + "/" + url.PathEscape(name)
}

// EnsureSpaceRequestForLab never adopts unmarked or foreign SpaceRequests.
func (c *SpaceRequestClient) EnsureSpaceRequestForLab(ctx context.Context, token, namespace, name, tierName, catalogRef, bindingKey string) error {
	label, err := CatalogRefLabel(catalogRef)
	if err != nil || bindingKey == "" {
		return errors.New("invalid SpaceRequest identity")
	}
	existing, err := c.GetSpaceRequest(ctx, token, namespace, name)
	if err != nil {
		return fmt.Errorf("check SpaceRequest: %w", err)
	}
	if existing != nil {
		if !existing.matchesLabBinding(namespace, name, label, bindingKey) {
			return fmt.Errorf("SpaceRequest %s already exists for a different lab", existing.Name)
		}
		if existing.DeletionTimestamp != "" {
			return errSpaceRequestTerminating
		}
		return nil
	}

	body := map[string]any{
		"apiVersion": spaceRequestAPIGroup + "/" + spaceRequestAPIVersion,
		"kind":       "SpaceRequest",
		"metadata": map[string]any{
			"name": name, "namespace": namespace,
			"labels":      map[string]string{CatalogRefLabelKey: label},
			"annotations": map[string]string{BindingKeyAnnotation: bindingKey},
		},
		"spec": map[string]any{"tierName": tierName},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal SpaceRequest: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.resourceURL(namespace), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create SpaceRequest request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("create SpaceRequest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return fmt.Errorf("create SpaceRequest %q in %q: status %d", name, namespace, resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	return nil
}

// ListSpaceRequestsForLab discovers only stamped resources in the authenticated user's home namespace.
func (c *SpaceRequestClient) ListSpaceRequestsForLab(ctx context.Context, token, namespace, catalogRef string) ([]SpaceRequestResult, error) {
	label := ""
	if catalogRef != "" {
		var err error
		label, err = CatalogRefLabel(catalogRef)
		if err != nil {
			return nil, err
		}
	}
	u, err := url.Parse(c.resourceURL(namespace))
	if err != nil {
		return nil, err
	}
	q := u.Query()
	selector := CatalogRefLabelKey
	if label != "" {
		selector += "=" + label
	}
	q.Set("labelSelector", selector)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list SpaceRequests: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list SpaceRequests: status %d", resp.StatusCode)
	}
	var list struct {
		Items    []json.RawMessage `json:"items"`
		Metadata struct {
			Continue string `json:"continue"`
		} `json:"metadata"`
	}
	// Bound inventory memory; reject incomplete results rather than deleting a subset.
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := decoder.Decode(&list); err != nil {
		return nil, fmt.Errorf("decode SpaceRequest list: %w", err)
	}
	if list.Metadata.Continue != "" {
		return nil, errors.New("incomplete SpaceRequest list")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid SpaceRequest list")
	}
	out := make([]SpaceRequestResult, 0, len(list.Items))
	for _, item := range list.Items {
		sr, err := decodeSpaceRequest(bytes.NewReader(item))
		if err != nil {
			return nil, err
		}
		_, identityErr := CatalogRefFromLabel(sr.Labels[CatalogRefLabelKey])
		if identityErr != nil || sr.Name == "" || sr.Namespace != namespace || sr.UID == "" ||
			(label != "" && sr.Labels[CatalogRefLabelKey] != label) || sr.Annotations[BindingKeyAnnotation] == "" {
			return nil, errors.New("SpaceRequest list contains invalid identity")
		}
		out = append(out, *sr)
	}
	return out, nil
}

// DeleteVerifiedSpaceRequest checks current identity and UID before UID-preconditioned deletion.
func (c *SpaceRequestClient) DeleteVerifiedSpaceRequest(ctx context.Context, token, namespace, catalogRef string, target SpaceRequestResult) error {
	label, err := CatalogRefLabel(catalogRef)
	bindingKey := target.Annotations[BindingKeyAnnotation]
	if err != nil || target.Name == "" || bindingKey == "" || !target.matchesLabBinding(namespace, target.Name, label, bindingKey) {
		return errors.New("invalid SpaceRequest target")
	}
	current, err := c.GetSpaceRequest(ctx, token, namespace, target.Name)
	if err != nil {
		return err
	}
	if current == nil {
		return nil // identified target already gone
	}
	if !current.matchesLabBinding(namespace, target.Name, label, bindingKey) || current.UID != target.UID {
		return errors.New("SpaceRequest identity changed")
	}
	payload, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"uid": target.UID},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.namedResourceURL(namespace, target.Name), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("delete SpaceRequest: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted &&
		resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete SpaceRequest: status %d", resp.StatusCode)
	}
	return nil
}

// GetSpaceRequest retrieves a single SpaceRequest by name.
// Returns nil, nil if not found.
func (c *SpaceRequestClient) GetSpaceRequest(ctx context.Context, token, namespace, name string) (*SpaceRequestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.namedResourceURL(namespace, name), nil)
	if err != nil {
		return nil, fmt.Errorf("get SpaceRequest request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get SpaceRequest: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil //nolint:nilnil
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil, fmt.Errorf("get SpaceRequest %q in %q: status %d", name, namespace, resp.StatusCode)
	}

	return decodeSpaceRequest(resp.Body)
}

func decodeSpaceRequest(r io.Reader) (*SpaceRequestResult, error) {
	var raw struct {
		Metadata struct {
			Name              string            `json:"name"`
			Namespace         string            `json:"namespace"`
			UID               string            `json:"uid"`
			DeletionTimestamp string            `json:"deletionTimestamp"`
			Labels            map[string]string `json:"labels"`
			Annotations       map[string]string `json:"annotations"`
		} `json:"metadata"`
		Status struct {
			NamespaceAccess []SpaceRequestNamespaceAccess `json:"namespaceAccess"`
			Conditions      []SpaceRequestCondition       `json:"conditions"`
		} `json:"status"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode SpaceRequest: %w", err)
	}
	return &SpaceRequestResult{
		Name:              raw.Metadata.Name,
		Namespace:         raw.Metadata.Namespace,
		UID:               raw.Metadata.UID,
		DeletionTimestamp: raw.Metadata.DeletionTimestamp,
		Labels:            raw.Metadata.Labels,
		Annotations:       raw.Metadata.Annotations,
		NamespaceAccess:   raw.Status.NamespaceAccess,
		Conditions:        raw.Status.Conditions,
	}, nil
}
