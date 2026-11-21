package san_ops_pipeline

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// bashFinder finds the bash that runs every `run:`. Its fields are the outside world, so a test
// can ask what it would pick on another host.
type bashFinder struct {
	goos     string
	lookPath func(file string) (string, error)
	pathEnv  string // the PATH value
	isFile   func(path string) bool
}

// findBash finds bash on this host.
func findBash() (string, error) {
	f := bashFinder{
		goos:     runtime.GOOS,
		lookPath: exec.LookPath,
		pathEnv:  os.Getenv("PATH"),
		isFile:   isFile,
	}

	return f.find()
}

func isFile(path string) bool {
	info, err := os.Stat(path)

	return err == nil && !info.IsDir()
}

// find picks bash. On Linux and macOS it is the bash on PATH. On Windows it must be Git for
// Windows' bash: PATH there often also holds C:\Windows\System32\bash.exe and
// …\Microsoft\WindowsApps\bash.exe, which are WSL launchers — they run the command inside Linux,
// with Linux paths and Linux tools, so they are never used. Git's bash is found from git itself
// (`<Git>\cmd\git.exe`, `<Git>\bin\git.exe` or `<Git>\mingw64\bin\git.exe` → `<Git>\bin\bash.exe`),
// else from a bash.exe on PATH outside System32 and WindowsApps.
func (f bashFinder) find() (string, error) {
	if f.goos != "windows" {
		bash, err := f.lookPath("bash")
		if err != nil {
			return "", fmt.Errorf("%w on PATH: %v", ErrNoBash, err)
		}

		return bash, nil
	}

	git, err := f.lookPath("git")
	if err == nil {
		dir := filepath.Dir(git)
		for range 3 {
			bash := filepath.Join(dir, "bin", "bash.exe")
			if !isWSLLauncher(bash) && f.isFile(bash) {
				return bash, nil
			}

			dir = filepath.Dir(dir)
		}
	}

	for _, dir := range filepath.SplitList(f.pathEnv) {
		if dir == "" {
			continue
		}

		bash := filepath.Join(dir, "bash.exe")
		if !isWSLLauncher(bash) && f.isFile(bash) {
			return bash, nil
		}
	}

	return "", fmt.Errorf("%w: install Git for Windows (its bin\\bash.exe is found from git on PATH); "+
		"System32\\bash.exe and WindowsApps\\bash.exe are WSL launchers and are not used", ErrNoBash)
}

// isWSLLauncher reports whether path lies in one of the folders Windows keeps its WSL bash in.
func isWSLLauncher(path string) bool {
	p := strings.ToLower(filepath.ToSlash(path))

	return strings.Contains(p, "/windows/system32/") ||
		strings.Contains(p, "/windows/syswow64/") ||
		strings.Contains(p, "/windows/sysnative/") ||
		strings.Contains(p, "/windowsapps/")
}
