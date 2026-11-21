package san_ops_pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/pdcgo/san_ops_pipeline"
)

// Load reads a pipeline file and the files it imports. Every path is read from the root given
// first — for revamp, the repo root.
func ExampleLoad() {
	p, err := san_ops_pipeline.Load("testdata/basic", "pipeline.yaml")
	if err != nil {
		fmt.Println(err)

		return
	}

	fmt.Println("pipeline", p.Name)

	for _, j := range p.Jobs {
		if j.Import != nil {
			fmt.Printf("%s: uses %s (%d job), needs %v\n", j.Name, j.Uses, len(j.Import.Jobs), j.Needs)

			continue
		}

		fmt.Printf("%s: %d steps, needs %v\n", j.Name, len(j.Steps), j.Needs)
	}

	// Output:
	// pipeline basic
	// zeta: 2 steps, needs []
	// alpha: uses pipeline/checks.yaml (1 job), needs [zeta]
	// mid: 1 steps, needs [zeta alpha]
}

// A refused file is a *LoadError: every problem of the file and its imports, each with its file and
// line.
func ExampleLoadError() {
	_, err := san_ops_pipeline.Load("testdata", "refused/needs.yaml")

	var refused *san_ops_pipeline.LoadError
	if errors.As(err, &refused) {
		fmt.Println(refused.Error())
		fmt.Println("first:", refused.Problems[0].File, refused.Problems[0].Line)
	}

	// Output:
	// 3 problems in the pipeline files:
	//   refused/needs.yaml:6: job "b" needs "ghost", which is not a job in this file
	//   refused/needs.yaml:9: needs loop: a → c → a (each needs the next) — these jobs can never start
	//   refused/needs.yaml:12: needs loop: d → d (each needs the next) — these jobs can never start
	// first: refused/needs.yaml 6
}

// Error lists the problems one per line under a count; a single problem is just its line.
func ExampleLoadError_Error() {
	_, err := san_ops_pipeline.Load("testdata", "refused/loop_a.yaml")

	var refused *san_ops_pipeline.LoadError
	if errors.As(err, &refused) {
		fmt.Println(refused.Error())
	}

	_, err = san_ops_pipeline.Load("testdata", "refused/imports_bad.yaml")
	fmt.Println(err)

	// Output:
	// refused/loop_b.yaml:3: import loop: refused/loop_a.yaml → refused/loop_b.yaml → refused/loop_a.yaml — a file cannot import itself, directly or through others
	// 3 problems in the pipeline files:
	//   refused/imports_bad.yaml:5: uses: refused/nope.yaml: cannot read it: no such file
	//   refused/inner_bad.yaml:2: job "x" has neither steps: nor uses:
	//   refused/inner_bad.yaml:3: unknown key "stepz" in job "x" (allowed: needs, steps, uses, env)
}

// String writes a problem as `file:line: msg`, or `file: msg` for the file as a whole.
func ExampleProblem_String() {
	fmt.Println(san_ops_pipeline.Problem{File: "pipeline/slice.yaml", Line: 7, Msg: `unknown key "if" in job "e2e" (allowed: needs, steps, uses, env)`})
	fmt.Println(san_ops_pipeline.Problem{File: "pipeline/empty.yaml", Msg: "the file is empty — a pipeline file needs jobs:"})

	_, err := san_ops_pipeline.Load("testdata", "refused/needs.yaml")

	var refused *san_ops_pipeline.LoadError
	if errors.As(err, &refused) {
		for _, p := range refused.Problems {
			fmt.Println(p.String())
		}
	}

	// Output:
	// pipeline/slice.yaml:7: unknown key "if" in job "e2e" (allowed: needs, steps, uses, env)
	// pipeline/empty.yaml: the file is empty — a pipeline file needs jobs:
	// refused/needs.yaml:6: job "b" needs "ghost", which is not a job in this file
	// refused/needs.yaml:9: needs loop: a → c → a (each needs the next) — these jobs can never start
	// refused/needs.yaml:12: needs loop: d → d (each needs the next) — these jobs can never start
}

// Run runs a loaded pipeline. It runs bash, so this example only compiles.
func ExamplePipeline_Run() {
	p, err := san_ops_pipeline.Load(".", "pipeline/slice.yaml")
	if err != nil {
		log.Fatal(err) // every problem of the file, with file:line
	}

	// os.Stdout on a console or terminal: the live view, redrawn in place. Piped, redirected or in
	// cron: plain lines. Verbose: true streams every line instead. Run catches Ctrl-C itself while
	// it runs; a cancelled ctx stops it the same way.
	res, err := p.Run(context.Background(), san_ops_pipeline.RunOptions{Out: os.Stdout})
	if err != nil {
		log.Fatal(err) // no bash, a run folder that cannot be written, or interrupted
	}

	if res.Failed() {
		os.Exit(1)
	}
}

// Failed tells a red run from a green one; the job results say which step of which job failed and
// where its log is.
func ExampleResult_Failed() {
	p, err := san_ops_pipeline.Load(".", "pipeline/slice.yaml")
	if err != nil {
		log.Fatal(err)
	}

	res, err := p.Run(context.Background(), san_ops_pipeline.RunOptions{})
	if errors.Is(err, san_ops_pipeline.ErrInterrupted) {
		fmt.Println("stopped; what ran is in", res.Dir)

		return
	}

	if err != nil {
		log.Fatal(err)
	}

	if !res.Failed() {
		fmt.Println("green in", res.Took)

		return
	}

	for _, j := range res.Jobs {
		switch j.Status {
		case san_ops_pipeline.Failed:
			fmt.Printf("%s: step %d exited %d, log %s\n", j.Name, j.Step, j.Exit, filepath.Join(res.Dir, j.Log))
		case san_ops_pipeline.Skipped:
			fmt.Printf("%s: skipped, needs %s\n", j.Name, j.Needs)
		}
	}
}
