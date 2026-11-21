package san_ops_pipeline

// Pipeline is one loaded pipeline file: every key read, every rule checked, every imported file loaded.
// Load makes it; Run runs it. A Pipeline is never changed by Run, so the same value can run again.
type Pipeline struct {
	// Name is the file's `name:`. When the file has none, it is the file's base name without its
	// extension (`pipeline/slice.yaml` → `slice`).
	Name string

	// File is the file as Load read it: its path relative to the root, with forward slashes (an
	// absolute path when the file lies outside the root).
	File string

	// Env holds the file's top-level `env:` values (nil when it has none).
	Env map[string]string

	// Jobs are the file's jobs in the order the file writes them.
	Jobs []*Job

	root string // absolute; every uses:, dir: and the run folder are read from it
}

// Job is one entry under `jobs:`. It either has Steps, or imports another pipeline file with Uses —
// never both.
type Job struct {
	// Name is the job's key under `jobs:`.
	Name string

	// Needs names the jobs of the same file this job waits for (nil when none).
	Needs []string

	// Env holds the job's `env:` values (nil when it has none).
	Env map[string]string

	// Steps are the job's steps in file order. Nil for a job with Uses.
	Steps []*Step

	// Uses is the `uses:` path as the file writes it. Empty for a job with Steps.
	Uses string

	// Import is the pipeline file Uses names, loaded and checked. That whole file runs as this one
	// job. Nil for a job with Steps.
	Import *Pipeline
}

// Step is one entry under a job's `steps:`: a shell command.
type Step struct {
	// Run is the command, run with `bash -c`. It may hold several lines.
	Run string

	// Dir is the folder the command starts in, read from the root. Empty means the root itself.
	Dir string

	// Env holds the step's `env:` values (nil when it has none).
	Env map[string]string
}
