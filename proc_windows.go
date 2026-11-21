//go:build windows

package san_ops_pipeline

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// bashCommand builds `bash -c <script>`, started suspended (startTree resumes it once it is in its
// job) and in its own console process group (a Ctrl-C reaches this program, which then stops the
// step itself).
//
// The command line is written by hand. Git for Windows' bash is an MSYS (Cygwin) program, and the
// Cygwin runtime splits its command line by its own rules: inside double quotes `\\` becomes `\`
// and `\"` becomes `"`. Go's own quoting follows the Microsoft rules, which leave `\\` alone, so
// `echo a\\b` reached bash as `echo a\b` (probed on Git 2.53). msysQuote writes the script the way
// Cygwin reads it back unchanged. Git's bin\bash.exe launcher passes the line on as it is.
func bashCommand(bash, script string) *exec.Cmd {
	cmd := exec.Command(bash)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       syscall.EscapeArg(bash) + " -c " + msysQuote(script),
		CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP,
	}

	return cmd
}

// msysQuote wraps s in double quotes for the Cygwin command-line parser: a backslash and a double
// quote get a backslash in front, everything else stays as it is. Quoted, the text is never
// globbed or tilde-expanded by the runtime either — that is left to bash.
func msysQuote(s string) string {
	var b strings.Builder

	b.Grow(len(s) + 2)
	b.WriteByte('"')

	for _, r := range s {
		if r == '\\' || r == '"' {
			b.WriteByte('\\')
		}

		b.WriteRune(r)
	}

	b.WriteByte('"')

	return b.String()
}

// procTree is a started step: its bash and everything that bash starts.
//
// Windows has no process group to signal, so the step runs inside a job object. The process starts
// suspended, joins the job and only then runs, so even its first child is in the job, and every
// process started after that joins it too (the job allows no breakaway). Stopping the step
// terminates the job: bash, `go test` and its test binary, a background `sleep &` whose parent has
// already exited — all of them. KILL_ON_JOB_CLOSE also stops them if this program itself dies.
// When no job can be made, taskkill /T /F stands in; it follows parent links, so it misses an
// orphan whose parent has exited.
type procTree struct {
	cmd *exec.Cmd
	job windows.Handle
}

// startTree starts cmd (built by bashCommand) inside a new job and lets it run.
func startTree(cmd *exec.Cmd) (*procTree, error) {
	job := newKillJob()

	err := cmd.Start()
	if err != nil {
		closeJob(job)

		return nil, err
	}

	t := &procTree{cmd: cmd, job: job}
	pid := uint32(cmd.Process.Pid)

	if t.job != 0 {
		err = assignToJob(t.job, pid)
		if err != nil {
			closeJob(t.job)
			t.job = 0
		}
	}

	err = resumeProcess(pid)
	if err != nil {
		t.kill()
		_ = cmd.Wait()
		t.end()

		return nil, fmt.Errorf("could not resume the started bash: %w", err)
	}

	return t, nil
}

// kill stops the step's whole tree now — terminated, no grace period (build choice, to confirm).
// Safe to call more than once.
func (t *procTree) kill() {
	if t.job != 0 {
		err := windows.TerminateJobObject(t.job, 1)
		if err == nil {
			return
		}
	}

	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(t.cmd.Process.Pid))

	err := kill.Run()
	if err != nil {
		_ = t.cmd.Process.Kill()
	}
}

// end runs after the step's bash has exited: whatever it left running in the background is stopped
// and the job is closed.
func (t *procTree) end() {
	if t.job == 0 {
		return
	}

	_ = windows.TerminateJobObject(t.job, 1)
	closeJob(t.job)
	t.job = 0
}

// newKillJob makes a job object that kills its processes when its last handle closes. It returns 0
// when Windows refuses; the caller then falls back to taskkill.
func newKillJob() windows.Handle {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0
	}

	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE

	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		closeJob(job)

		return 0
	}

	return job
}

func closeJob(job windows.Handle) {
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
}

func assignToJob(job windows.Handle, pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return err
	}

	defer windows.CloseHandle(h)

	return windows.AssignProcessToJobObject(job, h)
}

// resumeProcess resumes every thread of a process started with CREATE_SUSPENDED. Go closes the main
// thread's handle inside Start, so the threads are found again through a Toolhelp snapshot.
func resumeProcess(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}

	defer windows.CloseHandle(snap)

	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	resumed := 0

	err = windows.Thread32First(snap, &entry)
	for err == nil {
		if entry.OwnerProcessID == pid {
			rerr := resumeThread(entry.ThreadID)
			if rerr != nil {
				return rerr
			}

			resumed++
		}

		err = windows.Thread32Next(snap, &entry)
	}

	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}

	if resumed == 0 {
		return fmt.Errorf("no thread of process %d found", pid)
	}

	return nil
}

func resumeThread(id uint32) error {
	h, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, id)
	if err != nil {
		return err
	}

	defer windows.CloseHandle(h)

	_, err = windows.ResumeThread(h)

	return err
}
