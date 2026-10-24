package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestSummaryOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		resp    *fantasy.AgentResult
		wantErr error
	}{
		{
			name: "nil result is not a truncation",
			resp: nil,
		},
		{
			name: "stop is complete",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonStop},
			},
		},
		{
			name: "length is truncation",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonLength},
			},
			wantErr: errSummaryTruncated,
		},
		{
			name: "empty finish reason is tolerated",
			resp: &fantasy.AgentResult{Response: fantasy.Response{}},
		},
		{
			name: "unknown finish reason is tolerated",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonUnknown},
			},
		},
		{
			name: "tool calls are not truncation",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonToolCalls},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := summaryOutcome(tt.resp)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, err, errSummaryTruncated)
				require.Contains(t, err.Error(), "truncated")
				return
			}
			require.NoError(t, err)
		})
	}
}
