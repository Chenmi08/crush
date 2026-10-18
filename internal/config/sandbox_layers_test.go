package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMergeSandboxLayers_ProjectTightenIsKept verifies a repository that
// narrows the mode gets what it asked for. The merge used to copy only
// denials, so a project asking for read-only silently ran under the wider user
// mode: the safer configuration was the one dropped.
func TestMergeSandboxLayers_ProjectTightenIsKept(t *testing.T) {
	t.Parallel()
	layers := []sandboxLayer{
		{document: []byte(`{"permissions":{"sandbox":{"mode":"workspace-write"}}}`), project: false},
		{document: []byte(`{"permissions":{"sandbox":{"mode":"read-only","deny_write":["./secrets"]}}}`), project: true},
	}
	merged, err := mergeSandboxLayers(layers)
	require.NoError(t, err)
	require.Equal(t, "read-only", merged.Mode, "a project-mode tightening must not be dropped")
	require.Equal(t, []string{"./secrets"}, merged.DenyWrite)
}

// TestMergeSandboxLayers_ProjectOnlyTightening verifies the same rule when the
// user level sets nothing: the project's mode must not be replaced by the
// empty user request.
func TestMergeSandboxLayers_ProjectOnlyTightening(t *testing.T) {
	t.Parallel()
	layers := []sandboxLayer{
		{document: []byte(`{"permissions":{"sandbox":{"mode":"read-only","deny_write":["./secrets"]}}}`), project: true},
	}
	merged, err := mergeSandboxLayers(layers)
	require.NoError(t, err)
	require.Equal(t, "read-only", merged.Mode)
	require.Equal(t, []string{"./secrets"}, merged.DenyWrite)
}

// TestMergeSandboxLayers_ProjectCannotWiden verifies a widening project layer
// is rejected rather than merged.
func TestMergeSandboxLayers_ProjectCannotWiden(t *testing.T) {
	t.Parallel()
	layers := []sandboxLayer{
		{document: []byte(`{"permissions":{"sandbox":{"mode":"read-only"}}}`), project: false},
		{document: []byte(`{"permissions":{"sandbox":{"mode":"danger-full-access"}}}`), project: true},
	}
	_, err := mergeSandboxLayers(layers)
	require.ErrorIs(t, err, ErrProjectWidensSandbox)
}
