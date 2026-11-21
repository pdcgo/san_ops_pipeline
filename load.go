package san_ops_pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The keys a pipeline file may use, per level. Anything else is refused — `with:`, `if:`,
// `timeout-minutes:` and `continue-on-error:` too (not in this build, default 6).
const (
	pipelineKeys = "name, env, jobs"
	jobKeys      = "needs, steps, uses, env"
	stepKeys     = "run, dir, env"
)

var (
	// jobNameRe is GitHub Actions' rule for a job id. It also keeps `<job>.log` a safe file name.
	// Build choice (to confirm), as are envNameRe and refusing names that differ only in case.
	jobNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

	// envNameRe is a name bash can read as $NAME.
	envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

	// yamlLineRe pulls the line out of a yaml.v3 syntax error ("yaml: line 3: …").
	yamlLineRe = regexp.MustCompile(`^yaml: line (\d+): (.*)$`)
)

// Load reads the pipeline file at path and every file its jobs import with `uses:`, and checks all
// of it before anything can run: every key known, every job shaped right, every `needs` naming a
// job of the same file, no `needs` loop, no import loop.
//
// root is the folder every path is read from: path itself when it is relative, each `uses:` (an
// imported file's own `uses:` too), each step's `dir:`; the steps start in it and Run writes the run
// folder under it. For revamp it is the repo root. Build choice (to confirm): root moved here from
// the run options of default 1, because the imports are read now, at Load, and must come from the
// same folder the run uses.
//
// A refused file returns a *LoadError listing every problem found, each with its file and line.
func Load(root, path string) (*Pipeline, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("san_ops_pipeline: root %q: %w", root, err)
	}

	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("san_ops_pipeline: root %q: %w", root, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("san_ops_pipeline: root %q is not a folder", root)
	}

	l := &loader{
		root:   absRoot,
		rank:   map[string]int{},
		loaded: map[string]*Pipeline{},
	}

	p := l.file(path, nil, site{})
	if len(l.problems) > 0 {
		sort.SliceStable(l.problems, func(a, b int) bool {
			pa := l.problems[a]
			pb := l.problems[b]
			if l.rank[pa.File] != l.rank[pb.File] {
				return l.rank[pa.File] < l.rank[pb.File]
			}

			return pa.Line < pb.Line
		})

		return nil, &LoadError{Problems: l.problems}
	}

	return p, nil
}

// loader collects every problem of one Load across all the files it reads.
type loader struct {
	root     string
	problems []Problem
	rank     map[string]int       // file name → the order it was first met, to sort problems
	loaded   map[string]*Pipeline // file key → its finished load (nil when it could not be read)
}

// frame is one file on the import chain being loaded.
type frame struct {
	key  string
	name string
}

// site is where a file is imported from — the `uses:` line — for problems about the import itself.
type site struct {
	file string
	line int
}

// kv is one key: value pair of a mapping node.
type kv struct {
	key *yaml.Node
	val *yaml.Node
}

func (l *loader) add(file string, line int, format string, args ...any) {
	l.problems = append(l.problems, Problem{File: file, Line: line, Msg: fmt.Sprintf(format, args...)})
}

// resolve turns a path written in a file (or given to Load) into the file to read, the name shown
// in problems and summaries, and the key that identifies the file in import-loop checks.
func (l *loader) resolve(p string) (abs, name, key string) {
	abs = filepath.FromSlash(p)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(l.root, abs)
	}

	abs = filepath.Clean(abs)
	name = displayPath(l.root, abs)

	key = abs
	if runtime.GOOS == "windows" {
		key = strings.ToLower(abs)
	}

	return abs, name, key
}

// displayPath is abs relative to root with forward slashes, or abs itself when it lies outside root.
func displayPath(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(abs)
	}

	return filepath.ToSlash(rel)
}

// file loads one pipeline file. stack is the import chain that led here; from is the `uses:` that
// imports it (zero for the file given to Load).
func (l *loader) file(path string, stack []frame, from site) *Pipeline {
	abs, name, key := l.resolve(path)

	_, seen := l.rank[name]
	if !seen {
		l.rank[name] = len(l.rank)
	}

	for i, f := range stack {
		if f.key != key {
			continue
		}

		chain := make([]string, 0, len(stack)-i+1)
		for _, g := range stack[i:] {
			chain = append(chain, g.name)
		}

		chain = append(chain, name)
		l.add(from.file, from.line, "import loop: %s — a file cannot import itself, directly or through others", strings.Join(chain, " → "))

		return nil
	}

	p, done := l.loaded[key]
	if done {
		return p
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		why := err.Error()
		if errors.Is(err, fs.ErrNotExist) {
			why = "no such file"
		}

		if from.file != "" {
			l.add(from.file, from.line, "uses: %s: cannot read it: %s", name, why)
		} else {
			l.add(name, 0, "cannot read the pipeline file: %s", why)
		}

		l.loaded[key] = nil

		return nil
	}

	next := make([]frame, len(stack), len(stack)+1)
	copy(next, stack)
	next = append(next, frame{key: key, name: name})

	p = l.parse(name, data, next)
	l.loaded[key] = p

	return p
}

// parse reads one file's YAML into a Pipeline, recording every problem.
func (l *loader) parse(name string, data []byte, stack []frame) *Pipeline {
	dec := yaml.NewDecoder(bytes.NewReader(data))

	var doc yaml.Node

	err := dec.Decode(&doc)
	if errors.Is(err, io.EOF) {
		l.add(name, 0, "the file is empty — a pipeline file needs jobs:")

		return nil
	}

	if err != nil {
		line, msg := yamlError(err)
		l.add(name, line, "not valid YAML: %s", msg)

		return nil
	}

	var extra yaml.Node

	err = dec.Decode(&extra)
	if !errors.Is(err, io.EOF) {
		line := extra.Line
		if err != nil {
			line, _ = yamlError(err)
		}

		l.add(name, line, "the file holds more than one YAML document (---); a pipeline file is one document")
	}

	top := &doc
	if top.Kind == yaml.DocumentNode && len(top.Content) > 0 {
		top = top.Content[0]
	}

	top = deref(top)
	if top.Kind != yaml.MappingNode {
		l.add(name, top.Line, "a pipeline file is a mapping with the keys %s", pipelineKeys)

		return nil
	}

	p := &Pipeline{File: name, root: l.root}

	var jobs *kv

	for _, f := range l.pairs(name, top) {
		switch f.key.Value {
		case "name":
			v, ok := l.scalar(name, f, "name")
			if ok {
				p.Name = strings.TrimSpace(v)
			}
		case "env":
			p.Env = l.env(name, f, "the pipeline")
		case "jobs":
			jobs = &f
		default:
			l.unknown(name, f.key, "the pipeline", pipelineKeys)
		}
	}

	if p.Name == "" {
		base := filepath.Base(filepath.FromSlash(name))
		p.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	if jobs == nil {
		l.add(name, top.Line, "the pipeline has no jobs:")

		return p
	}

	l.jobs(p, name, *jobs, stack)

	return p
}

// yamlError splits a yaml.v3 syntax error into its line and message.
func yamlError(err error) (int, string) {
	m := yamlLineRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, strings.TrimPrefix(err.Error(), "yaml: ")
	}

	line, _ := strconv.Atoi(m[1])

	return line, m[2]
}

// deref follows a YAML alias (*name) to the node it points at.
func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}

	return n
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// pairs returns a mapping's key: value pairs in file order, refusing a key written twice (only the
// first is kept) and a key that is not a plain word.
func (l *loader) pairs(file string, m *yaml.Node) []kv {
	out := make([]kv, 0, len(m.Content)/2)
	first := map[string]int{}

	for i := 0; i+1 < len(m.Content); i += 2 {
		k := deref(m.Content[i])
		v := m.Content[i+1]

		if k.Kind != yaml.ScalarNode {
			l.add(file, k.Line, "a key must be a plain word")

			continue
		}

		line, dup := first[k.Value]
		if dup {
			l.add(file, k.Line, "%q is written twice (first at line %d)", k.Value, line)

			continue
		}

		first[k.Value] = k.Line
		out = append(out, kv{key: k, val: v})
	}

	return out
}

func (l *loader) unknown(file string, key *yaml.Node, where, allowed string) {
	l.add(file, key.Line, "unknown key %q in %s (allowed: %s)", key.Value, where, allowed)
}

// scalar reads a single value — a word, a number, a quoted string; not a list, a mapping or nothing.
func (l *loader) scalar(file string, f kv, what string) (string, bool) {
	v := deref(f.val)

	if v.Kind != yaml.ScalarNode {
		l.add(file, v.Line, "%s must be a single value, not a list or a mapping", what)

		return "", false
	}

	if isNull(v) {
		l.add(file, f.key.Line, "%s has no value", what)

		return "", false
	}

	return v.Value, true
}

// env reads an `env:` mapping of NAME: value.
func (l *loader) env(file string, f kv, owner string) map[string]string {
	v := deref(f.val)
	if isNull(v) {
		return nil
	}

	if v.Kind != yaml.MappingNode {
		l.add(file, v.Line, "env of %s must be a mapping of NAME: value", owner)

		return nil
	}

	out := map[string]string{}
	folded := map[string]*yaml.Node{}

	for _, e := range l.pairs(file, v) {
		if !envNameRe.MatchString(e.key.Value) {
			l.add(file, e.key.Line, "env name %q in %s: use letters, digits and _, not starting with a digit", e.key.Value, owner)

			continue
		}

		other, clash := folded[strings.ToUpper(e.key.Value)]
		if clash {
			l.add(file, e.key.Line, "env %s of %s differs from %s (line %d) only in upper/lower case — Windows reads them as one name", e.key.Value, owner, other.Value, other.Line)

			continue
		}

		folded[strings.ToUpper(e.key.Value)] = e.key

		val, ok := l.scalar(file, e, fmt.Sprintf("env %s of %s", e.key.Value, owner))
		if ok {
			out[e.key.Value] = val
		}
	}

	return out
}

// jobLines keeps the lines Load needs after the jobs are read, for the `needs` checks.
type jobLines struct {
	name  int   // the job's key
	needs []int // each `needs` entry
}

// jobs reads the `jobs:` mapping and then checks every `needs` of the file.
func (l *loader) jobs(p *Pipeline, file string, f kv, stack []frame) {
	v := deref(f.val)

	if isNull(v) || v.Kind != yaml.MappingNode {
		l.add(file, f.key.Line, "jobs: must be a mapping of job name → job")

		return
	}

	if len(v.Content) == 0 {
		l.add(file, f.key.Line, "jobs: has no jobs")

		return
	}

	lines := map[string]jobLines{}
	folded := map[string]kv{}

	for _, e := range l.pairs(file, v) {
		name := e.key.Value

		if !jobNameRe.MatchString(name) {
			l.add(file, e.key.Line, "job name %q: use letters, digits, - and _, starting with a letter or _", name)
		}

		other, clash := folded[strings.ToLower(name)]
		if clash {
			l.add(file, e.key.Line, "job %q differs from job %q (line %d) only in upper/lower case — their log files would be one file on Windows", name, other.key.Value, other.key.Line)
		} else {
			folded[strings.ToLower(name)] = e
		}

		j, needLines := l.job(file, e, stack)
		p.Jobs = append(p.Jobs, j)
		lines[name] = jobLines{name: e.key.Line, needs: needLines}
	}

	l.checkNeeds(file, p.Jobs, lines)
}

// job reads one job: either steps or uses, never both.
func (l *loader) job(file string, e kv, stack []frame) (*Job, []int) {
	j := &Job{Name: e.key.Value}
	v := deref(e.val)

	if isNull(v) || v.Kind != yaml.MappingNode {
		l.add(file, e.key.Line, "job %q must be a mapping with steps: or uses:", j.Name)

		return j, nil
	}

	where := fmt.Sprintf("job %q", j.Name)

	var (
		steps     *kv
		uses      *kv
		needLines []int
	)

	for _, f := range l.pairs(file, v) {
		switch f.key.Value {
		case "needs":
			j.Needs, needLines = l.needs(file, f, j.Name)
		case "env":
			j.Env = l.env(file, f, where)
		case "steps":
			steps = &f
		case "uses":
			uses = &f
		default:
			l.unknown(file, f.key, where, jobKeys)
		}
	}

	switch {
	case steps != nil && uses != nil:
		l.add(file, uses.key.Line, "job %q has both steps: (line %d) and uses: — a job either runs its steps or imports a file", j.Name, steps.key.Line)
	case steps == nil && uses == nil:
		l.add(file, e.key.Line, "job %q has neither steps: nor uses:", j.Name)
	case steps != nil:
		j.Steps = l.steps(file, *steps, j.Name)
	default:
		path, ok := l.scalar(file, *uses, fmt.Sprintf("uses of job %q", j.Name))
		if !ok {
			break
		}

		path = strings.TrimSpace(path)
		if path == "" {
			l.add(file, uses.key.Line, "uses of job %q is empty", j.Name)

			break
		}

		j.Uses = path
		j.Import = l.file(path, stack, site{file: file, line: uses.key.Line})
	}

	return j, needLines
}

// needs reads `needs:` — one job name or a list of them (a single name, as GitHub accepts it, is a
// build choice).
func (l *loader) needs(file string, f kv, job string) ([]string, []int) {
	v := deref(f.val)

	if v.Kind == yaml.ScalarNode && !isNull(v) {
		return []string{v.Value}, []int{v.Line}
	}

	if v.Kind != yaml.SequenceNode {
		l.add(file, f.key.Line, "needs of job %q must be a job name or a list of job names", job)

		return nil, nil
	}

	var (
		names []string
		lines []int
	)

	for _, n := range v.Content {
		n = deref(n)
		if n.Kind != yaml.ScalarNode || isNull(n) {
			l.add(file, n.Line, "needs of job %q: each entry must be a job name", job)

			continue
		}

		names = append(names, n.Value)
		lines = append(lines, n.Line)
	}

	return names, lines
}

// steps reads a job's `steps:` list.
func (l *loader) steps(file string, f kv, job string) []*Step {
	v := deref(f.val)

	if isNull(v) || v.Kind != yaml.SequenceNode {
		l.add(file, f.key.Line, "steps of job %q must be a list of steps", job)

		return nil
	}

	if len(v.Content) == 0 {
		l.add(file, f.key.Line, "job %q has no steps", job)

		return nil
	}

	out := make([]*Step, 0, len(v.Content))

	for i, n := range v.Content {
		n = deref(n)
		where := fmt.Sprintf("step %d of job %q", i+1, job)

		if isNull(n) || n.Kind != yaml.MappingNode {
			l.add(file, n.Line, "%s must be a mapping with run:", where)

			continue
		}

		s := &Step{}
		hasRun := false

		for _, g := range l.pairs(file, n) {
			switch g.key.Value {
			case "run":
				hasRun = true

				cmd, ok := l.scalar(file, g, "run of "+where)
				if ok && strings.TrimSpace(cmd) == "" {
					l.add(file, g.key.Line, "run of %s is empty", where)
				}

				s.Run = cmd
			case "dir":
				dir, ok := l.scalar(file, g, "dir of "+where)
				if ok && strings.TrimSpace(dir) == "" {
					l.add(file, g.key.Line, "dir of %s is empty", where)
				}

				s.Dir = strings.TrimSpace(dir)
			case "env":
				s.Env = l.env(file, g, where)
			default:
				l.unknown(file, g.key, where, stepKeys)
			}
		}

		if !hasRun {
			l.add(file, n.Line, "%s has no run:", where)
		}

		out = append(out, s)
	}

	return out
}

// checkNeeds refuses a `needs` naming a job the file does not have, and every `needs` loop, naming
// the jobs in it.
func (l *loader) checkNeeds(file string, jobs []*Job, lines map[string]jobLines) {
	index := map[string]int{}
	for i, j := range jobs {
		index[j.Name] = i
	}

	for _, j := range jobs {
		for k, need := range j.Needs {
			_, ok := index[need]
			if !ok {
				l.add(file, lines[j.Name].needs[k], "job %q needs %q, which is not a job in this file", j.Name, need)
			}
		}
	}

	const (
		fresh = iota
		open
		closed
	)

	state := make([]int, len(jobs))
	reported := map[string]bool{}

	var (
		path  []int
		visit func(i int)
	)

	visit = func(i int) {
		state[i] = open
		path = append(path, i)

		for k, need := range jobs[i].Needs {
			n, ok := index[need]
			if !ok {
				continue
			}

			switch state[n] {
			case fresh:
				visit(n)
			case open:
				at := 0
				for at < len(path) && path[at] != n {
					at++
				}

				names := make([]string, 0, len(path)-at+1)
				for _, x := range path[at:] {
					names = append(names, jobs[x].Name)
				}

				sorted := append([]string(nil), names...)
				sort.Strings(sorted)

				id := strings.Join(sorted, "\x00")
				if !reported[id] {
					reported[id] = true
					names = append(names, jobs[n].Name)
					l.add(file, lines[jobs[i].Name].needs[k], "needs loop: %s (each needs the next) — these jobs can never start", strings.Join(names, " → "))
				}
			}
		}

		path = path[:len(path)-1]
		state[i] = closed
	}

	for i := range jobs {
		if state[i] == fresh {
			visit(i)
		}
	}
}
