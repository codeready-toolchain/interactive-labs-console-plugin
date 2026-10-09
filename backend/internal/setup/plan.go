package setup

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/bundle"
	"github.com/codeready-toolchain/interactive-labs-console-plugin/backend/internal/chart"
)

var (
	// ErrChartNotFound means the selected chart has no setup values schema.
	ErrChartNotFound = errors.New("lab has no values schema")
	// ErrNamespaceBindingsMissing means the schema cannot provision namespaces.
	ErrNamespaceBindingsMissing = errors.New("values schema declares no namespace bindings")
)

// Plan is the filesystem-prepared input to lab provisioning, without cluster work.
type Plan struct {
	ChartDir    string // absolute selected chart directory
	ReleaseName string // already derived from the authenticated subject and catalog identity
	Bindings    SchemaBindings
}

// Prepare validates lab content and prepares provisioning inputs from an approved
// snapshot. It performs no cluster work; callers must hold the snapshot read lock.
func Prepare(repositoryRoot, labPath, subject, catalogRef string) (*Plan, error) {
	m, _, err := bundle.LoadContentFromSnapshot(repositoryRoot, labPath)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(repositoryRoot)
	if err == nil {
		root, err = filepath.EvalSymlinks(root)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: repository snapshot unavailable", bundle.ErrRepositoryUnavailable)
	}
	chart, err := chart.LoadChart(root, filepath.Join(root, filepath.FromSlash(labPath)), m.Spec.SetupDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", bundle.ErrLabInvalid, err)
	}
	if chart.Schema == nil {
		return nil, ErrChartNotFound
	}
	bindings := ParseSchemaBindings(chart.Schema)
	if len(bindings.Namespaces) == 0 {
		return nil, ErrNamespaceBindingsMissing
	}
	chartPath, err := filepath.Rel(root, chart.Dir)
	if err != nil {
		return nil, fmt.Errorf("%w: chart path", bundle.ErrLabInvalid)
	}
	return &Plan{
		ChartDir:    chart.Dir,
		ReleaseName: HelmReleaseName(subject, catalogRef, filepath.ToSlash(chartPath), bindings.Namespaces),
		Bindings:    *bindings,
	}, nil
}
