package san_ops_pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunOrderIsNeedsFirstThenFileOrder(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "order")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Failed() {
		t.Fatalf("run %s", res.Status)
	}

	// b needs a, d needs c: a runs before b, c before d, and e — free — waits for its turn in the file.
	got := traceOf(t, filepath.Join(root, "order.txt"))
	if !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e"}) {
		t.Errorf("ran %v", got)
	}

	var names []string
	for _, j := range res.Jobs {
		names = append(names, j.Name)
	}

	if !reflect.DeepEqual(names, []string{"b", "a", "d", "c", "e"}) {
		t.Errorf("results not in file order: %v", names)
	}
}

func TestRunSkipsEveryJobThatNeedsAFailedOne(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "failure")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil {
		t.Fatalf("a failed pipeline is a result, not an error: %v", err)
	}

	if res.Status != Failed || !res.Failed() {
		t.Fatalf("run %s", res.Status)
	}

	build := jobByName(t, res.Jobs, "build")
	if build.Status != Failed || build.Step != 2 || build.Exit != 3 || !strings.Contains(build.Command, "exit 3") {
		t.Errorf("build %+v", build)
	}

	test := jobByName(t, res.Jobs, "test")
	deploy := jobByName(t, res.Jobs, "deploy")

	// test needs build; deploy needs test, so it is skipped through it.
	if test.Status != Skipped || test.Needs != "build" || deploy.Status != Skipped || deploy.Needs != "test" {
		t.Errorf("test %+v deploy %+v", test, deploy)
	}

	trace := strings.Join(traceOf(t, filepath.Join(root, "trace.txt")), " ")
	if strings.Contains(trace, "test") || strings.Contains(trace, "deploy") {
		t.Errorf("a skipped job ran: %s", trace)
	}
}

func TestRunStillRunsTheJobsThatDoNotNeedTheFailedOne(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "failure")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	lint := jobByName(t, res.Jobs, "lint")
	docs := jobByName(t, res.Jobs, "docs")

	if lint.Status != Passed || docs.Status != Passed {
		t.Errorf("lint %s docs %s", lint.Status, docs.Status)
	}

	// build's later step did not run; lint and docs did, after build failed.
	got := traceOf(t, filepath.Join(root, "trace.txt"))
	if !reflect.DeepEqual(got, []string{"build-1", "lint", "docs"}) {
		t.Errorf("ran %v", got)
	}

	log := readText(t, filepath.Join(res.Dir, "build.log"))
	for _, want := range []string{
		"about to fail\n── step 2/3 · exit 3 · ",
		"── step 3/3 · not run (an earlier step failed)",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("build.log lacks %q:\n%s", want, log)
		}
	}
}

func TestRunAnImportedFileAsOneJob(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "imports")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.Failed() {
		t.Fatalf("run %s: %+v", res.Status, res.Jobs)
	}

	// The imported file has its own job "build": no clash with the importing file's "build".
	got := traceOf(t, filepath.Join(root, "trace.txt"))
	if !reflect.DeepEqual(got, []string{"outer-build", "inner-build", "deep-check", "after"}) {
		t.Errorf("ran %v", got)
	}

	lint := jobByName(t, res.Jobs, "lint")
	if lint.Uses != "pipeline/lint.yaml" || lint.Log != "" || len(lint.Jobs) != 2 {
		t.Fatalf("lint %+v", lint)
	}

	vet := jobByName(t, lint.Jobs, "vet")
	if vet.Status != Passed || len(vet.Jobs) != 1 || vet.Jobs[0].Log != "lint/vet/check.log" {
		t.Errorf("vet %+v", vet)
	}

	for _, log := range []string{"build.log", "lint/build.log", "lint/vet/check.log"} {
		if !exists(filepath.Join(res.Dir, filepath.FromSlash(log))) {
			t.Errorf("no %s in the run folder", log)
		}
	}
}

func TestRunAnImportedJobFailsWhenOneOfItsJobsFails(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "importfail")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	lint := jobByName(t, res.Jobs, "lint")
	if lint.Status != Failed {
		t.Fatalf("lint %s", lint.Status)
	}

	// The same rule inside the imported file: vet fails, fmt (needs vet) is skipped, spell runs.
	inner := map[string]Status{}
	for _, j := range lint.Jobs {
		inner[j.Name] = j.Status
	}

	if !reflect.DeepEqual(inner, map[string]Status{"vet": Failed, "fmt": Skipped, "spell": Passed}) {
		t.Errorf("lint's jobs %v", inner)
	}

	after := jobByName(t, res.Jobs, "after")
	other := jobByName(t, res.Jobs, "other")

	if after.Status != Skipped || after.Needs != "lint" || other.Status != Passed || res.Status != Failed {
		t.Errorf("after %+v other %+v run %s", after, other, res.Status)
	}
}

// Not parallel: t.Setenv.
func TestRunLayersTheEnvironment(t *testing.T) {
	t.Setenv("LEVEL", "caller")
	t.Setenv("FROM_CALLER", "caller")

	root := copyRoot(t, "env")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil || res.Failed() {
		t.Fatalf("Run: %v %+v", err, res)
	}

	want := map[string]string{
		// caller < pipeline < job < step
		"plain.txt":  "step p j s caller",
		"plain2.txt": "job",
		// the importing file's pipeline and job env reach the imported file; its own env wins
		"inner.txt":  "importing-job p inner-pipeline ip",
		"inner2.txt": "inner-job",
	}

	for file, w := range want {
		got := strings.TrimSpace(readText(t, filepath.Join(root, file)))
		if got != w {
			t.Errorf("%s: got %q, want %q", file, got, w)
		}
	}
}

func TestRunReadsEveryDirFromTheRoot(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "dirs")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, f := range []string{"at-root.txt", "sub/here.txt", "inner-at-root.txt", "sub/inner-here.txt"} {
		if !exists(filepath.Join(root, filepath.FromSlash(f))) {
			t.Errorf("no %s: a step did not start where it should", f)
		}
	}

	missing := jobByName(t, res.Jobs, "missing")
	if missing.Status != Failed || missing.Exit != -1 || !strings.Contains(missing.Reason, "nope") {
		t.Errorf("a step whose dir is missing: %+v", missing)
	}

	if exists(filepath.Join(root, "never.txt")) {
		t.Error("the step with a missing dir ran anyway")
	}
}

func TestRunPassesTheCommandToBashUnchanged(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "quoting")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil || res.Failed() {
		t.Fatalf("Run: %v %+v", err, res)
	}

	got := strings.Split(strings.TrimRight(readText(t, filepath.Join(root, "args.txt")), "\n"), "\n")

	want := []string{`a\b`, `c\d`, `q"q`, `*`, `~`, `$HOME`, `two  spaces`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bash got\n%q\nwant\n%q", got, want)
	}
}

func TestRunWritesItsRunFolder(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "basic")

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil || res.Failed() {
		t.Fatalf("Run: %v %+v", err, res)
	}

	cache := filepath.Join(root, cacheDirName)
	if filepath.Dir(res.Dir) != cache {
		t.Fatalf("run folder %s is not under %s", res.Dir, cache)
	}

	nameRe := regexp.MustCompile(`^\d{8}-\d{6}-basic$`)
	if !nameRe.MatchString(filepath.Base(res.Dir)) {
		t.Errorf("run folder name %s", filepath.Base(res.Dir))
	}

	if res.Started.Format(stampLayout) != filepath.Base(res.Dir)[:15] {
		t.Errorf("stamp %s vs started %s", filepath.Base(res.Dir), res.Started)
	}

	if readText(t, filepath.Join(cache, ".gitignore")) != "*\n" {
		t.Errorf(".gitignore holds %q", readText(t, filepath.Join(cache, ".gitignore")))
	}

	summary := readText(t, filepath.Join(res.Dir, "summary.md"))
	for _, want := range []string{
		"# pipeline basic — PASSED\n",
		"- file: `pipeline.yaml`\n",
		"- ✓ **zeta** · passed · 0m",
		"[zeta.log](zeta.log)\n",
		"- ✓ **alpha** · passed · 0m",
		" · uses `pipeline/checks.yaml`\n",
		"  - ✓ **vet** · passed · 0m",
		"[alpha/vet.log](alpha/vet.log)\n",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary.md lacks %q:\n%s", want, summary)
		}
	}

	log := readText(t, filepath.Join(res.Dir, "zeta.log"))
	for _, want := range []string{
		"── step 1/2 · started ",
		"$ echo zeta\nzeta\n── step 1/2 · exit 0 · 0m",
		" · dir sub\n$ echo one\n$ echo two\none\ntwo\n── step 2/2 · exit 0 · 0m",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("zeta.log lacks %q:\n%s", want, log)
		}
	}
}

func TestRunWritesTheGitignoreOnlyWhenItMakesTheCacheFolder(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "order")

	err := os.Mkdir(filepath.Join(root, cacheDirName), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if exists(filepath.Join(root, cacheDirName, ".gitignore")) {
		t.Error("a .gitignore was written into a cache folder the library did not make")
	}
}

func TestMakeRunDirGivesASameSecondRunASuffix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()

	var got []string

	for range 3 {
		dir, err := makeRunDir(root, "20261121-143005", "slice")
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, filepath.Base(dir))
	}

	want := []string{"20261121-143005-slice", "20261121-143005-slice-2", "20261121-143005-slice-3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestSafeName(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"slice":                  "slice",
		"my pipeline/v2: *fast*": "my-pipeline-v2-fast",
		"..hidden..":             "hidden",
		"a--b__c.d":              "a-b__c.d",
		"日本":                     "pipeline",
		"":                       "pipeline",
		strings.Repeat("x", 80):  strings.Repeat("x", 60),
	}

	for in, want := range cases {
		got := safeName(in)
		if got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunRefusesARootItCannotWriteTo(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "order")

	// A file where the cache folder should be.
	err := os.WriteFile(filepath.Join(root, cacheDirName), []byte("x"), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err == nil || res != nil || errors.Is(err, ErrNoBash) {
		t.Fatalf("want an error and no result, got %v %+v", err, res)
	}

	if exists(filepath.Join(root, "order.txt")) {
		t.Error("a step ran although the run folder could not be made")
	}
}

func TestRunRefusesWithoutBashBeforeAnythingStarts(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "order")
	noBash := func() (string, error) {
		return "", fmt.Errorf("%w: none on this test host", ErrNoBash)
	}

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{findBash: noBash})
	if !errors.Is(err, ErrNoBash) || res != nil {
		t.Fatalf("got %v %+v", err, res)
	}

	if exists(filepath.Join(root, cacheDirName)) {
		t.Error("a run folder was made although nothing could run")
	}

	if !res.Failed() {
		t.Error("a nil result must count as failed")
	}
}

// waitFor polls cond until it holds or the time is up.
func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()

	start := time.Now()
	for !cond() {
		if time.Since(start) > limit {
			t.Fatalf("timed out waiting for %s", what)
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}

	return info.Size()
}

// stillGrowing reports whether a heartbeat file grows over a second — a background child that
// survived.
func stillGrowing(path string) bool {
	before := fileSize(path)
	time.Sleep(1200 * time.Millisecond)

	return fileSize(path) != before
}

func TestRunStopsTheRunningStepWithItsChildrenOnCancel(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "stop")
	p := mustLoad(t, root)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type ran struct {
		res *Result
		err error
	}

	done := make(chan ran, 1)

	go func() {
		res, err := p.run(ctx, RunOptions{}, runConfig{})
		done <- ran{res, err}
	}()

	beat := filepath.Join(root, "beat.txt")
	waitFor(t, 30*time.Second, "the background child's heartbeat", func() bool {
		return fileSize(beat) > 20
	})

	cancel()

	var r ran

	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel: the step was not stopped")
	}

	if !errors.Is(r.err, ErrInterrupted) || !errors.Is(r.err, context.Canceled) {
		t.Errorf("err %v", r.err)
	}

	if r.res == nil || r.res.Status != Interrupted || !r.res.Failed() {
		t.Fatalf("result %+v", r.res)
	}

	long := jobByName(t, r.res.Jobs, "long")
	after := jobByName(t, r.res.Jobs, "after")

	if long.Status != Interrupted || long.Step != 1 || after.Status != NotRun {
		t.Errorf("long %+v after %+v", long, after)
	}

	if exists(filepath.Join(root, "after.txt")) {
		t.Error("a job started after the cancel")
	}

	if stillGrowing(beat) {
		t.Error("the step's background child is still running after Run returned")
	}

	summary := readText(t, filepath.Join(r.res.Dir, "summary.md"))
	if !strings.Contains(summary, "— INTERRUPTED") || !strings.Contains(summary, "while long ran") {
		t.Errorf("summary.md:\n%s", summary)
	}

	if !strings.Contains(readText(t, filepath.Join(r.res.Dir, "long.log")), "── step 1/1 · interrupted after ") {
		t.Error("long.log does not say the step was interrupted")
	}
}

func TestRunStopsWhatAStepLeftRunningWhenItEnds(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "leftover")
	started := time.Now()

	res, err := mustRun(t, mustLoad(t, root), RunOptions{}, runConfig{})
	if err != nil || res.Failed() {
		t.Fatalf("Run: %v %+v", err, res)
	}

	took := time.Since(started)
	if took > 20*time.Second {
		t.Errorf("Run took %s: it waited for the background child", took)
	}

	if !exists(filepath.Join(root, "next.txt")) {
		t.Error("the next job did not run")
	}

	if stillGrowing(filepath.Join(root, "beat.txt")) {
		t.Error("the background child a step left behind is still running")
	}
}

// Not parallel: it counts goroutines.
func TestRunEndsEveryGoroutineItStarts(t *testing.T) {
	// os/signal starts one process-wide watcher at the first signal.Notify a program makes, and it
	// never ends; it is the runtime's, not Run's, so it is started here before counting.
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	signal.Stop(c)

	check := func(name string, run func()) {
		before := runtime.NumGoroutine()

		run()

		after := runtime.NumGoroutine()
		if after != before {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Errorf("%s: %d goroutines before Run, %d after:\n%s", name, before, after, buf[:n])
		}
	}

	check("a failing run with ticks and verbose lines", func() {
		root := copyRoot(t, "show")
		_, _ = mustRun(t, mustLoad(t, root), RunOptions{Out: &bytes.Buffer{}, Verbose: true}, runConfig{tickEvery: 20 * time.Millisecond})
	})

	check("an interrupted run", func() {
		root := copyRoot(t, "stop")
		p := mustLoad(t, root)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		go func() {
			start := time.Now()
			for !exists(filepath.Join(root, "started.txt")) && time.Since(start) < 30*time.Second {
				time.Sleep(50 * time.Millisecond)
			}

			cancel()
		}()

		_, err := p.run(ctx, RunOptions{}, runConfig{})
		if !errors.Is(err, ErrInterrupted) {
			t.Errorf("err %v", err)
		}

		// The helper goroutine above is the test's; let it finish before counting.
		time.Sleep(200 * time.Millisecond)
	})
}

func TestPlainLines(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "show")

	var out bytes.Buffer

	res, err := mustRun(t, mustLoad(t, root), RunOptions{Out: &out}, runConfig{})
	if err != nil || res.Status != Failed {
		t.Fatalf("Run: %v %+v", err, res)
	}

	var tail strings.Builder
	for i := 6; i <= 25; i++ {
		fmt.Fprintf(&tail, "    line %d\n", i)
	}

	want := "pipeline show · STAMP-show\n" +
		"→ build  echo built\n" +
		"✓ build  0m00s · build.log\n" +
		"→ test   echo ok\n" +
		"✗ test   0m00s  exit 2 · test.log\n" +
		tail.String() +
		"⏭ e2e    skipped (needs test)\n" +
		"→ lint   uses pipeline/lint.yaml\n" +
		"  → vet  echo vetting\n" +
		"  ✓ vet  0m00s · lint/vet.log\n" +
		"✓ lint   0m00s\n" +
		"FAILED in 0m00s · logs: .san_ops_pipeline_cache/STAMP-show/\n"

	got := normalized(out.String())
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestVerboseStreamsEveryLineWithItsJob(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "show")

	var out bytes.Buffer

	_, err := mustRun(t, mustLoad(t, root), RunOptions{Out: &out, Verbose: true}, runConfig{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"[build] $ echo built\n[build] built\n",
		"[test] line 1\n",
		"[test] line 25\n",
		"[lint/vet] $ echo vetting\n[lint/vet] vetting\n",
		"✗ test   ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("verbose output lacks %q:\n%s", want, got)
		}
	}

	if strings.Contains(got, "    line 25") {
		t.Errorf("verbose output repeats the failing step's last lines:\n%s", got)
	}
}

func TestPlainLinesSayALongStepIsStillRunning(t *testing.T) {
	t.Parallel()

	root := copyRoot(t, "slow")

	var out bytes.Buffer

	_, err := mustRun(t, mustLoad(t, root), RunOptions{Out: &out}, runConfig{tickEvery: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !strings.Contains(out.String(), "● slow  still running 0m\n") {
		t.Errorf("no still-running line:\n%s", out.String())
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	cases := map[time.Duration]string{
		0:                               "0m00s",
		1900 * time.Millisecond:         "0m01s",
		62 * time.Second:                "1m02s",
		13*time.Minute + 58*time.Second: "13m58s",
		75 * time.Minute:                "75m00s",
		-time.Second:                    "0m00s",
	}

	for d, want := range cases {
		got := formatDuration(d)
		if got != want {
			t.Errorf("formatDuration(%s) = %s, want %s", d, got, want)
		}
	}
}

func TestWithEnv(t *testing.T) {
	t.Parallel()

	got := withEnv([]string{"A=1", "B=2", "=C:=C:\\x", "B=old"}, map[string]string{"B": "3", "C": "4"})
	want := []string{"A=1", "B=3", "=C:=C:\\x", "C=4"}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}

	got = withEnv([]string{"Path=x"}, map[string]string{"PATH": "y"})

	want = []string{"Path=x", "PATH=y"}
	if runtime.GOOS == "windows" {
		want = []string{"PATH=y"}
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFindBashOnThisHost(t *testing.T) {
	t.Parallel()

	bash, err := findBash()
	if err != nil {
		t.Fatalf("findBash: %v", err)
	}

	if runtime.GOOS != "windows" {
		return
	}

	if isWSLLauncher(bash) {
		t.Fatalf("picked the WSL launcher %s", bash)
	}

	cmd := exec.Command(bash, "-c", "uname -o")

	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "Msys" {
		t.Errorf("%s is not Git for Windows' bash: %q %v", bash, out, err)
	}
}

func TestFindBashPicksGitBashOnWindows(t *testing.T) {
	t.Parallel()

	g := filepath.Join("fake", "Git")
	sys32 := filepath.Join("fake", "Windows", "System32")
	apps := filepath.Join("fake", "Users", "me", "AppData", "Local", "Microsoft", "WindowsApps")
	msys := filepath.Join("fake", "msys64", "usr", "bin")
	list := func(dirs ...string) string {
		return strings.Join(dirs, string(filepath.ListSeparator))
	}

	cases := []struct {
		name  string
		git   string // "" = no git on PATH
		path  string
		files []string
		want  string // "" = ErrNoBash
	}{
		{"git in cmd", filepath.Join(g, "cmd", "git.exe"), list(sys32, apps),
			[]string{filepath.Join(g, "bin", "bash.exe"), filepath.Join(sys32, "bash.exe")}, filepath.Join(g, "bin", "bash.exe")},
		{"git in mingw64/bin", filepath.Join(g, "mingw64", "bin", "git.exe"), list(sys32),
			[]string{filepath.Join(g, "bin", "bash.exe")}, filepath.Join(g, "bin", "bash.exe")},
		{"git in bin", filepath.Join(g, "bin", "git.exe"), "",
			[]string{filepath.Join(g, "bin", "bash.exe")}, filepath.Join(g, "bin", "bash.exe")},
		{"no git, a bash on PATH past the WSL ones", "", list(sys32, apps, msys),
			[]string{filepath.Join(sys32, "bash.exe"), filepath.Join(apps, "bash.exe"), filepath.Join(msys, "bash.exe")}, filepath.Join(msys, "bash.exe")},
		{"only the WSL launchers", "", list(sys32, apps),
			[]string{filepath.Join(sys32, "bash.exe"), filepath.Join(apps, "bash.exe")}, ""},
		{"git without a bash beside it", filepath.Join(g, "cmd", "git.exe"), list(sys32), nil, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			have := map[string]bool{}
			for _, f := range c.files {
				have[f] = true
			}

			f := bashFinder{
				goos: "windows",
				lookPath: func(file string) (string, error) {
					if file == "git" && c.git != "" {
						return c.git, nil
					}

					return "", exec.ErrNotFound
				},
				pathEnv: c.path,
				isFile:  func(p string) bool { return have[p] },
			}

			got, err := f.find()
			if c.want == "" {
				if !errors.Is(err, ErrNoBash) {
					t.Errorf("want ErrNoBash, got %q %v", got, err)
				}

				return
			}

			if err != nil || got != c.want {
				t.Errorf("got %q %v, want %q", got, err, c.want)
			}
		})
	}
}

func TestFindBashTakesBashFromPathOffWindows(t *testing.T) {
	t.Parallel()

	f := bashFinder{
		goos:     "linux",
		lookPath: func(string) (string, error) { return "/usr/bin/bash", nil },
	}

	got, err := f.find()
	if err != nil || got != "/usr/bin/bash" {
		t.Errorf("got %q %v", got, err)
	}

	f.lookPath = func(string) (string, error) { return "", exec.ErrNotFound }

	_, err = f.find()
	if !errors.Is(err, ErrNoBash) {
		t.Errorf("got %v", err)
	}
}
