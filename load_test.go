package san_ops_pipeline

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadReadsEveryKeyInFileOrder(t *testing.T) {
	t.Parallel()

	p, err := Load("testdata/basic", "pipeline.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p.Name != "basic" || p.File != "pipeline.yaml" {
		t.Errorf("name %q file %q", p.Name, p.File)
	}

	if !reflect.DeepEqual(p.Env, map[string]string{"A": "1", "B": "2"}) {
		t.Errorf("pipeline env %v", p.Env)
	}

	var names []string
	for _, j := range p.Jobs {
		names = append(names, j.Name)
	}

	// The file writes zeta, alpha, mid: the order is the file's, not the alphabet's.
	if !reflect.DeepEqual(names, []string{"zeta", "alpha", "mid"}) {
		t.Fatalf("jobs %v", names)
	}

	zeta := p.Jobs[0]
	if !reflect.DeepEqual(zeta.Env, map[string]string{"J": "job"}) || len(zeta.Steps) != 2 {
		t.Fatalf("zeta %+v", zeta)
	}

	s := zeta.Steps[1]
	if s.Run != "echo one\necho two\n" || s.Dir != "sub" || !reflect.DeepEqual(s.Env, map[string]string{"S": "step"}) {
		t.Errorf("step 2 of zeta %+v", s)
	}

	alpha := p.Jobs[1]
	if !reflect.DeepEqual(alpha.Needs, []string{"zeta"}) || alpha.Uses != "pipeline/checks.yaml" || alpha.Steps != nil {
		t.Fatalf("alpha %+v", alpha)
	}

	if alpha.Import == nil || alpha.Import.Name != "checks" || alpha.Import.File != "pipeline/checks.yaml" {
		t.Fatalf("alpha's import %+v", alpha.Import)
	}

	if len(alpha.Import.Jobs) != 1 || alpha.Import.Jobs[0].Name != "vet" {
		t.Errorf("checks.yaml jobs %+v", alpha.Import.Jobs)
	}

	if !reflect.DeepEqual(p.Jobs[2].Needs, []string{"zeta", "alpha"}) {
		t.Errorf("mid needs %v", p.Jobs[2].Needs)
	}
}

func TestLoadRefusesEveryUnknownKey(t *testing.T) {
	t.Parallel()

	_, err := Load("testdata", "refused/unknown.yaml")

	// with:, if:, timeouts and continue-on-error are not in this build: refused like any other key.
	wantProblems(t, err, []string{
		`refused/unknown.yaml:2: unknown key "timeout" in the pipeline (allowed: name, env, jobs)`,
		`refused/unknown.yaml:5: unknown key "if" in job "build" (allowed: needs, steps, uses, env)`,
		`refused/unknown.yaml:6: unknown key "with" in job "build"`,
		`refused/unknown.yaml:8: unknown key "runs-on" in job "build"`,
		`refused/unknown.yaml:11: unknown key "timeout-minutes" in step 1 of job "build" (allowed: run, dir, env)`,
		`refused/unknown.yaml:12: unknown key "continue-on-error" in step 1 of job "build"`,
		`refused/unknown.yaml:13: unknown key "shell" in step 1 of job "build"`,
	})
}

func TestLoadRefusesMissingNeedsAndNeedsLoops(t *testing.T) {
	t.Parallel()

	_, err := Load("testdata", "refused/needs.yaml")

	wantProblems(t, err, []string{
		`refused/needs.yaml:6: job "b" needs "ghost", which is not a job in this file`,
		`refused/needs.yaml:9: needs loop: a → c → a (each needs the next)`,
		`refused/needs.yaml:12: needs loop: d → d (each needs the next)`,
	})
}

func TestLoadRefusesAnImportLoopNamingBothFiles(t *testing.T) {
	t.Parallel()

	_, err := Load("testdata", "refused/loop_a.yaml")

	wantProblems(t, err, []string{
		`refused/loop_b.yaml:3: import loop: refused/loop_a.yaml → refused/loop_b.yaml → refused/loop_a.yaml`,
	})
}

func TestLoadReportsAnImportedFilesProblemsInThatFile(t *testing.T) {
	t.Parallel()

	_, err := Load("testdata", "refused/imports_bad.yaml")

	wantProblems(t, err, []string{
		`refused/imports_bad.yaml:5: uses: refused/nope.yaml: cannot read it: no such file`,
		`refused/inner_bad.yaml:2: job "x" has neither steps: nor uses:`,
		`refused/inner_bad.yaml:3: unknown key "stepz" in job "x"`,
	})
}

func TestLoadRefusesBadShapes(t *testing.T) {
	t.Parallel()

	_, err := Load("testdata", "refused/shapes.yaml")

	wantProblems(t, err, []string{
		`refused/shapes.yaml:1: name must be a single value`,
		`refused/shapes.yaml:4: env name "bad-name" in the pipeline`,
		`refused/shapes.yaml:5: env NESTED of the pipeline must be a single value`,
		`refused/shapes.yaml:6: env EMPTY of the pipeline has no value`,
		`refused/shapes.yaml:9: job "both" has both steps: (line 10) and uses:`,
		`refused/shapes.yaml:12: job "neither" has neither steps: nor uses:`,
		`refused/shapes.yaml:14: job name "bad name": use letters`,
		`refused/shapes.yaml:18: job "nosteps" has no steps`,
		`refused/shapes.yaml:20: steps of job "notlist" must be a list of steps`,
		`refused/shapes.yaml:23: step 1 of job "steptype" must be a mapping with run:`,
		`refused/shapes.yaml:24: step 2 of job "steptype" has no run:`,
		`refused/shapes.yaml:25: run of step 3 of job "steptype" is empty`,
		`refused/shapes.yaml:27: needs of job "needsbad" must be a job name or a list of job names`,
	})
}

func TestLoadRefusesBrokenFiles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"empty", "", "p.yaml: the file is empty"},
		{"syntax", "jobs:\n  a: [\n", "p.yaml:2: not valid YAML: did not find expected node content"},
		{"two documents", "jobs:\n  a:\n    steps: [{run: x}]\n---\njobs: {}\n", "more than one YAML document"},
		{"a list", "- a\n- b\n", "p.yaml:1: a pipeline file is a mapping with the keys name, env, jobs"},
		{"no jobs", "name: x\n", "p.yaml:1: the pipeline has no jobs:"},
		{"empty jobs", "jobs: {}\n", "p.yaml:1: jobs: has no jobs"},
		{"null jobs", "jobs:\n", "p.yaml:1: jobs: must be a mapping"},
		{"job twice", "jobs:\n  a:\n    steps: [{run: x}]\n  a:\n    steps: [{run: y}]\n", `p.yaml:4: "a" is written twice (first at line 2)`},
		{"case clash", "jobs:\n  build:\n    steps: [{run: x}]\n  Build:\n    steps: [{run: y}]\n", `p.yaml:4: job "Build" differs from job "build" (line 2) only in upper/lower case`},
		{"empty uses", "jobs:\n  a:\n    uses: \"\"\n", `p.yaml:3: uses of job "a" is empty`},
		{"env case clash", "env:\n  Path: a\n  PATH: b\njobs:\n  a:\n    steps: [{run: x}]\n", `p.yaml:3: env PATH of the pipeline differs from Path (line 2)`},
		{"merge key", "jobs:\n  a:\n    <<: {steps: [{run: x}]}\n", `p.yaml:3: unknown key "<<" in job "a"`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()

			err := os.WriteFile(filepath.Join(root, "p.yaml"), []byte(c.yaml), 0o644)
			if err != nil {
				t.Fatal(err)
			}

			_, err = Load(root, "p.yaml")

			var le *LoadError
			if !errors.As(err, &le) {
				t.Fatalf("want a *LoadError, got %v", err)
			}

			if !strings.Contains(le.Error(), c.want) {
				t.Errorf("got %s\nwant it to hold %s", le.Error(), c.want)
			}
		})
	}
}

func TestLoadAcceptsAnAlias(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	yaml := "jobs:\n  a:\n    steps: &s\n      - run: echo a\n  b:\n    steps: *s\n"

	err := os.WriteFile(filepath.Join(root, "p.yaml"), []byte(yaml), 0o644)
	if err != nil {
		t.Fatal(err)
	}

	p, err := Load(root, "p.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p.Jobs[1].Steps[0].Run != "echo a" {
		t.Errorf("b's step %+v", p.Jobs[1].Steps[0])
	}
}

func TestLoadReadsAFileImportedTwiceOnce(t *testing.T) {
	t.Parallel()

	p, err := Load("testdata/diamond", "pipeline.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Two imports of one file are no loop; the file is read once.
	if p.Jobs[0].Import == nil || p.Jobs[0].Import != p.Jobs[1].Import {
		t.Errorf("imports %p %p", p.Jobs[0].Import, p.Jobs[1].Import)
	}
}

func TestLoadRefusesAMissingFileAndABadRoot(t *testing.T) {
	t.Parallel()

	_, err := Load("testdata", "nope.yaml")
	wantProblems(t, err, []string{"nope.yaml: cannot read the pipeline file: no such file"})

	_, err = Load("testdata/no-such-root", "pipeline.yaml")

	var le *LoadError
	if err == nil || errors.As(err, &le) {
		t.Errorf("a missing root: want a plain error, got %v", err)
	}

	_, err = Load("testdata/basic/pipeline.yaml", "pipeline.yaml")
	if err == nil || !strings.Contains(err.Error(), "is not a folder") {
		t.Errorf("a file as root: got %v", err)
	}
}

func TestLoadErrorListsEveryProblemOnItsOwnLine(t *testing.T) {
	t.Parallel()

	_, err := Load("testdata", "refused/needs.yaml")

	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 4 || lines[0] != "3 problems in the pipeline files:" {
		t.Fatalf("%q", err.Error())
	}

	if !strings.HasPrefix(lines[1], "  refused/needs.yaml:6: ") {
		t.Errorf("line 1 %q", lines[1])
	}

	one := &LoadError{Problems: []Problem{{File: "a.yaml", Line: 2, Msg: "bad"}}}
	if one.Error() != "a.yaml:2: bad" {
		t.Errorf("one problem %q", one.Error())
	}
}

func TestLoadNamesAPipelineWithoutNameAfterItsFile(t *testing.T) {
	t.Parallel()

	p, err := Load("testdata/imports", "pipeline/deep.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if p.Name != "deep" {
		t.Errorf("name %q", p.Name)
	}
}
