package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Builder builds a exec.Cmd. Each invocation should return a new instance
// so they can be retried independently.
type Builder func(context.Context) *exec.Cmd

// Retry runs the command until it succeeds or the context is cancelled.
// The backoff duration is the time to wait between retries.
func Retry(ctx context.Context, newCmd Builder, backoff time.Duration) error {
	var err error
	for {
		var out []byte
		cmd := newCmd(ctx)
		out, err = cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		err = fmt.Errorf("running command %s: %s [Err %s]", cmd.Args, out, err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", ctx.Err(), err)
		case <-time.After(backoff):
		}
	}
}