//go:build unix

package san_ops_pipeline

import (
	"os/exec"
	"syscall"
)

// bashCommand builds `bash -c <script>` in a process group of its own: the group id stops the step
// and everything it started, and the group is not the terminal's foreground group, so a Ctrl-C
// reaches only this program, which then stops the step itself.
func bashCommand(bash, script string) *exec.Cmd {
	cmd := exec.Command(bash, "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	return cmd
}

// procTree is a started step: its bash and everything that bash starts, one process group.
type procTree struct {
	cmd *exec.Cmd
}

func startTree(cmd *exec.Cmd) (*procTree, error) {
	err := cmd.Start()
	if err != nil {
		return nil, err
	}

	return &procTree{cmd: cmd}, nil
}

// kill stops the step's whole group now, with SIGKILL — no grace period (build choice, to confirm). The NEGATIVE pid is the point: kill(pid) reaches only bash,
// while `bash -c "go test ./..."` has a child doing all the work. Safe to call more than once.
func (t *procTree) kill() {
	err := syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	if err != nil {
		_ = t.cmd.Process.Kill()
	}
}

// end runs after the step's bash has exited: whatever it left running in the background (still in
// the group) is stopped. A group that is already empty answers ESRCH, which is fine.
func (t *procTree) end() {
	_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
}
