// Package san_ops_pipeline is our own CI/CD: it runs pipelines written as small YAML files.
//
// A pipeline file uses GitHub Actions' words without being compatible with GitHub Actions:
//
//	name: slice
//	env:
//	  CGO_ENABLED: "0"
//	jobs:
//	  checks:
//	    uses: pipeline/checks.yaml   # this whole file runs as the one job "checks"
//	  go-tests:
//	    needs: [checks]
//	    steps:
//	      - run: go test ./backend/...
//	  stories:
//	    needs: [checks]
//	    steps:
//	      - run: npm run test:stories
//	        dir: frontend
//	        env:
//	          CI: "1"
//
// [Load] reads a file and every file it imports and checks all of it — an unknown key, a job with
// both steps: and uses:, a `needs` to a missing job, a `needs` loop, an import loop — and refuses
// with a [*LoadError] that lists every problem with its file and line. [Pipeline.Run] then runs it:
// one job at a time, in `needs` order and the file's order where `needs` leaves it open, each step
// with `bash -c` (Git for Windows' bash on Windows, never the WSL launcher). A failing step fails
// its job, every job that needs it is skipped, every other job still runs. Each run writes
// `<root>/.san_ops_pipeline_cache/<stamp>-<name>/` — a log per job and summary.md — and prints its
// progress as plain lines to [RunOptions.Out]. A failed pipeline is a [Result] ([Result.Failed]),
// not an error; err is for a run that could not happen or was interrupted ([ErrNoBash],
// [ErrInterrupted]).
//
// The package runs nothing on its own: no scheduler, nothing left running. Every goroutine Run
// starts has ended when it returns, and so has every process a step started. The caller (a `san`
// command, a script, a cron) decides when to run a pipeline.
package san_ops_pipeline
