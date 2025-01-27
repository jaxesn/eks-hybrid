package suite

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

const deferCleanupTimeout = 5 * time.Minute

// TestCleanup is a helper for our use of DeferCleanup in our e2e tests
// to support retrying EC2 instance creation
// The cleanup runs after the test itself, just like the default DeferCleanup
// but it only executes the cleanup if the test was a failure since otherwise
// we want the instance to stay around for future tests
// In the BeforeAll TestCleanup.DeferAll is called to register a defer cleanup
// which runs all registered cleanups from our test methods after all tests
// are complete
type TestCleanup struct {
	cleanups []func(context.Context)
}

func (t *TestCleanup) Register(f func(context.Context)) {
	index := len(t.cleanups)
	t.cleanups = append(t.cleanups, f)
	DeferCleanup(func(ctx context.Context) {
		if CurrentSpecReport().Failed() {
			// remove cleanup if runs due to a test case failure so it does
			// run again in the deferall
			t.cleanups = append(t.cleanups[:index], t.cleanups[index+1:]...)
			f(ctx)
		}
	}, NodeTimeout(deferCleanupTimeout))
}

func (t *TestCleanup) DeferAll() {
	DeferCleanup(func(ctx context.Context) {
		for i := len(t.cleanups) - 1; i >= 0; i-- {
			f := t.cleanups[i]
			f(ctx)
		}
	}, NodeTimeout(deferCleanupTimeout))
}
