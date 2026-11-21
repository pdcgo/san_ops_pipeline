package san_ops_pipeline

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// copyRoot copies testdata/<name> into a fresh temporary folder and returns it: a root a run may
// write its .san_ops_pipeline_cache/ and its files into.
func copyRoot(t *testing.T, name string) string {
	t.Helper()

	src := filepath.Join("testdata", name)
	dst := t.TempDir()

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}

	return dst
}

// mustLoad loads <root>/pipeline.yaml.
func mustLoad(t *testing.T, root string) *Pipeline {
	t.Helper()

	p, err := Load(root, "pipeline.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	return p
}

// mustRun runs p with a bounded context and returns what Run returned.
func mustRun(t *testing.T, p *Pipeline, opts RunOptions, cfg runConfig) (*Result, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	return p.run(ctx, opts, cfg)
}

func readText(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

// lines of a file written by `echo x >> file` steps.
func traceOf(t *testing.T, path string) []string {
	t.Helper()

	if !exists(path) {
		return nil
	}

	return strings.Fields(readText(t, path))
}

// jobByName finds a job result by name.
func jobByName(t *testing.T, jobs []JobResult, name string) JobResult {
	t.Helper()

	for _, j := range jobs {
		if j.Name == name {
			return j
		}
	}

	t.Fatalf("no job %q in %+v", name, jobs)

	return JobResult{}
}

var (
	durationRe = regexp.MustCompile(`\b\d+m\d\ds\b`)
	stampRe    = regexp.MustCompile(`\d{8}-\d{6}`)
)

// normalized hides what changes from run to run: times and the run folder's stamp.
func normalized(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = durationRe.ReplaceAllString(s, "0m00s")

	return stampRe.ReplaceAllString(s, "STAMP")
}

func wantProblems(t *testing.T, err error, want []string) {
	t.Helper()

	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("want a *LoadError, got %v", err)
	}

	got := make([]string, 0, len(le.Problems))
	for _, p := range le.Problems {
		got = append(got, p.String())
	}

	if len(got) != len(want) {
		t.Fatalf("want %d problems, got %d:\n%s", len(want), len(got), strings.Join(got, "\n"))
	}

	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("problem %d:\n got  %s\n want %s…", i, got[i], want[i])
		}
	}
}
