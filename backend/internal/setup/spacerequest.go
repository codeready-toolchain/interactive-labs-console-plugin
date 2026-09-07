package setup

import (
	"bytes"
	"context"
	"encoding/json"
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
	Name            string
	NamespaceAccess []SpaceRequestNamespaceAccess
	Conditions      []SpaceRequestCondition
}

// IsReady returns true if the SpaceRequest has a Ready=True condition.
func (r *SpaceRequestResult) IsReady() bool {
	for _, c := range r.Conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
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

// EnsureSpaceRequest creates a SpaceRequest if it does not already exist.
// Returns nil if it already exists (idempotent).
func (c *SpaceRequestClient) EnsureSpaceRequest(ctx context.Context, token, namespace, name, tierName string) error {
	existing, err := c.GetSpaceRequest(ctx, token, namespace, name)
	if err == nil && existing != nil {
		return nil
	}

	body := map[string]any{
		"apiVersion": spaceRequestAPIGroup + "/" + spaceRequestAPIVersion,
		"kind":       "SpaceRequest",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"tierName": tierName,
		},
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

	if resp.StatusCode == http.StatusConflict {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create SpaceRequest %q in %q: status %d: %s", name, namespace, resp.StatusCode, body)
	}
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
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
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get SpaceRequest %q in %q: status %d: %s", name, namespace, resp.StatusCode, body)
	}

	return decodeSpaceRequest(resp.Body)
}

func decodeSpaceRequest(r io.Reader) (*SpaceRequestResult, error) {
	var raw struct {
		Metadata struct {
			Name string `json:"name"`
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
		Name:            raw.Metadata.Name,
		NamespaceAccess: raw.Status.NamespaceAccess,
		Conditions:      raw.Status.Conditions,
	}, nil
}
