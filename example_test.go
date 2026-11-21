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

// Run runs a loaded pipeline. It runs bash, so this example only compiles.
func ExamplePipeline_Run() {
	p, err := san_ops_pipeline.Load(".", "pipeline/slice.yaml")
	if err != nil {
		log.Fatal(err) // every problem of the file, with file:line
	}

	// Run catches Ctrl-C itself while it runs; a cancelled ctx stops it the same way.
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
