package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSummaryAppendix(t *testing.T) {
	t.Parallel()

	t.Run("empty renders nothing", func(t *testing.T) {
		t.Parallel()

		var appendix summaryAppendix
		require.True(t, appendix.empty())
		require.Empty(t, appendix.render())
	})

	t.Run("blank blocks are ignored", func(t *testing.T) {
		t.Parallel()

		var appendix summaryAppendix
		appendix.add("")
		appendix.add("   \n")
		require.True(t, appendix.empty())
	})

	t.Run("one marker introduces every block", func(t *testing.T) {
		t.Parallel()

		var appendix summaryAppendix
		appendix.add("first")
		appendix.add("second")

		block := appendix.render()
		require.True(t, strings.HasPrefix(block, appendixLeadIn))
		require.Equal(t, 1, strings.Count(block, appendixLeadIn))
		require.Contains(t, block, appendixLeadIn+"\n\nfirst\n\nsecond")
	})
}
