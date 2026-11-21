package san_ops_pipeline

import (
	"runtime"
	"sort"
	"strings"
)

// withEnv returns base (a list of NAME=value) with the values of add set on top: a name already in
// base gets the new value in its place (any second copy of it is dropped), a new name is added at
// the end (sorted, so runs are repeatable). On Windows names match without regard to case, as
// Windows itself treats them (`Path` is `PATH`).
//
// The layers of a step are applied in this order, the inner one winning: the caller's environment,
// the pipeline's env:, the job's env:, the step's env:. For a job with uses:, the importing file's
// layers are the base the imported file's own layers go on top of (build choice, to confirm: the
// imported file sees the importing pipeline's and job's env, and its own values win).
func withEnv(base []string, add map[string]string) []string {
	if len(add) == 0 {
		return base
	}

	fold := func(name string) string {
		if runtime.GOOS == "windows" {
			return strings.ToUpper(name)
		}

		return name
	}

	written := make(map[string]string, len(add)) // folded name → the name as the file writes it
	for name := range add {
		written[fold(name)] = name
	}

	done := make(map[string]bool, len(add))
	out := make([]string, 0, len(base)+len(add))

	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			// Windows keeps entries such as "=C:=C:\work"; they are passed on untouched.
			out = append(out, entry)

			continue
		}

		f := fold(name)

		w, set := written[f]
		if !set {
			out = append(out, entry)

			continue
		}

		if !done[f] {
			out = append(out, w+"="+add[w])
			done[f] = true
		}
	}

	rest := make([]string, 0, len(add))
	for f, w := range written {
		if !done[f] {
			rest = append(rest, w)
		}
	}

	sort.Strings(rest)

	for _, name := range rest {
		out = append(out, name+"="+add[name])
	}

	return out
}
