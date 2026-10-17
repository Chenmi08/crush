package sandbox

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNotifyDenials_ReceivesRefusals verifies a registered subscriber sees
// every refusal with its count.
func TestNotifyDenials_ReceivesRefusals(t *testing.T) {
	p := testPolicy(t)
	outside := t.TempDir()

	var mu sync.Mutex
	var got []DenialNotice
	NotifyDenials(func(n DenialNotice) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, n)
	})
	t.Cleanup(func() { NotifyDenials(nil) })

	sandboxResetDenialCountForTest()
	require.Error(t, p.CheckWrite(filepath.Join(outside, "x")))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, got, 1)
	require.Equal(t, filepath.Join(outside, "x"), got[0].Path)
	require.Equal(t, int64(1), got[0].Count)
	require.Equal(t, "workspace-write", got[0].Mode)
}

// TestNotifyDenials_UnregisterStopsDelivery verifies shutdown unregisters, so
// a second subscriber in the same process does not inherit the first's.
func TestNotifyDenials_UnregisterStopsDelivery(t *testing.T) {
	p := testPolicy(t)
	outside := t.TempDir()

	called := false
	NotifyDenials(func(DenialNotice) { called = true })
	t.Cleanup(func() { NotifyDenials(nil) })
	NotifyDenials(nil)

	require.NoError(t, resetCountForTest())
	_ = p.CheckWrite(filepath.Join(outside, "x"))
	require.False(t, called, "a nil subscriber unregisters delivery")
}

// TestNotifyDenials_PanicSafe verifies a misbehaving subscriber cannot break
// tool execution: refusal counting and logging still happen.
func TestNotifyDenials_PanicSafe(t *testing.T) {
	p := testPolicy(t)
	outside := t.TempDir()

	NotifyDenials(func(DenialNotice) { panic("subscriber bug") })
	t.Cleanup(func() { NotifyDenials(nil) })

	sandboxResetDenialCountForTest()
	err := p.CheckWrite(filepath.Join(outside, "x"))
	require.Error(t, err, "the refusal must still be produced")
	require.Equal(t, int64(1), DenialCount(), "the refusal must still be counted")
}

func resetCountForTest() error {
	sandboxResetDenialCountForTest()
	return nil
}
