package claudetool

import (
	"errors"
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestBackgroundJobGoneLinux(t *testing.T) {
	g := newGate(t)
	cmd := exec.Command("bash", "-c", g.wait())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	f := g.writer(t)
	st, err := processStartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	job := BackgroundJob{PID: cmd.Process.Pid, StartTime: st}

	t.Run("live", func(t *testing.T) {
		if gone, err := job.Gone(); gone || err != nil {
			t.Fatalf("Gone = %v, %v, want false, nil", gone, err)
		}
	})
	t.Run("reused PID", func(t *testing.T) {
		reused := job
		reused.StartTime++
		if gone, err := reused.Gone(); !gone || err != nil {
			t.Fatalf("Gone = %v, %v, want true, nil", gone, err)
		}
	})

	if _, err := f.WriteString("go\n"); err != nil {
		t.Fatal(err)
	}
	// Observe exit without reaping: /proc still has the matching process.
	exited := make(chan struct{})
	var waitErr error
	go func() {
		defer close(exited)
		var info unix.Siginfo
		for {
			waitErr = unix.Waitid(unix.P_PID, job.PID, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if !errors.Is(waitErr, unix.EINTR) {
				return
			}
		}
	}()
	waitClosed(t, exited, "unreaped child exit")
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	t.Run("exited unreaped", func(t *testing.T) {
		if got, err := processStartTime(job.PID); err != nil || got != st {
			t.Fatalf("start time = %d, %v, want %d, nil", got, err, st)
		}
		if gone, err := job.Gone(); !gone || err != nil {
			t.Fatalf("Gone = %v, %v, want true, nil", gone, err)
		}
	})

	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	t.Run("reaped missing", func(t *testing.T) {
		if gone, err := job.Gone(); !gone || err != nil {
			t.Fatalf("Gone = %v, %v, want true, nil", gone, err)
		}
	})
}

func TestBackgroundJobGoneInvalidPID(t *testing.T) {
	if gone, err := (BackgroundJob{PID: -1}).Gone(); gone || !errors.Is(err, unix.EINVAL) {
		t.Fatalf("Gone = %v, %v, want false, EINVAL", gone, err)
	}
}
