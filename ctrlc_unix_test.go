//go:build unix

package san_ops_pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Not parallel: the SIGINT reaches every Run in the process. Linux and macOS only — on Windows a
// Ctrl-C cannot be sent to this test alone (GenerateConsoleCtrlEvent reaches the whole console).
func TestCtrlCStopsTheRunLikeACancelledCtx(t *testing.T) {
	root := copyRoot(t, "stop")
	p := mustLoad(t, root)

	type ran struct {
		res *Result
		err error
	}

	done := make(chan ran, 1)

	go func() {
		res, err := p.run(context.Background(), RunOptions{}, runConfig{})
		done <- ran{res, err}
	}()

	// Only once the step runs is Run's handler in place; a SIGINT before it would end the test.
	waitFor(t, 30*time.Second, "the step to start", func() bool {
		return exists(filepath.Join(root, "started.txt"))
	})

	err := syscall.Kill(os.Getpid(), syscall.SIGINT)
	if err != nil {
		t.Fatal(err)
	}

	var r ran

	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after Ctrl-C")
	}

	if !errors.Is(r.err, ErrInterrupted) || !errors.Is(r.err, errCtrlC) {
		t.Errorf("err %v", r.err)
	}

	if r.res == nil || r.res.Status != Interrupted || jobByName(t, r.res.Jobs, "after").Status != NotRun {
		t.Fatalf("result %+v", r.res)
	}

	if stillGrowing(filepath.Join(root, "beat.txt")) {
		t.Error("the step's background child is still running after Ctrl-C")
	}
}
