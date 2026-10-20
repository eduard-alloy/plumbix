// Package azure converts Azure Pipelines YAML into triggered Plumbix
// pipelines (WU-046). Each Azure job becomes one Plumbix step: a job is one
// agent running its steps in order, a Plumbix step is one container, so the
// job's scripts become the step's commands. What Plumbix cannot run yet is
// kept, disabled with a reason.
package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/eduard-alloy/plumbix/internal/importer"
)

// Source is the Azure Pipelines importer.
type Source struct{}

var _ importer.Source = Source{}

// Name implements importer.Source.
func (Source) Name() string { return "azure" }

// Detect implements importer.Source: a YAML file with stages, jobs, steps or
// extends at the top and a trigger, pool, container, resources or schedule beside
// them; templates (under a templates folder, or without any of those) are
// not pipelines of their own.
func (Source) Detect(p string, content []byte) bool {
	if ext := path.Ext(p); (ext != ".yml" && ext != ".yaml") || strings.HasPrefix(p, ".plumbix/") {
		return false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return strings.HasPrefix(path.Base(p), "azure-pipelines")
	}
	root := doc.Content[0]
	body := get(root, "stages") != nil || get(root, "jobs") != nil || get(root, "steps") != nil || get(root, "extends") != nil
	// A template runs only inside another pipeline: it lives under a
	// templates folder, or has nothing that starts or places a run.
	if slices.Contains(strings.Split(path.Dir(p), "/"), "templates") {
		return false
	}
	marker := false
	for _, k := range []string{"trigger", "pr", "schedules", "pool", "resources", "container"} {
		marker = marker || get(root, k) != nil
	}
	return body && marker
}

// reasonOrder decides which reason disables a step when several apply.
var reasonOrder = []string{"deployment", "template", "checkout", "services", "task", "host", "docker", "condition", "secrets", "variables"}

// workspaceVars are Azure's predefined paths of the checked-out sources.
var workspaceVars = map[string]bool{"Build.SourcesDirectory": true, "System.DefaultWorkingDirectory": true, "Pipeline.Workspace": true, "Build.Repository.LocalPath": true}

var (
	macroRe    = regexp.MustCompile(`\$\(([A-Za-z_][A-Za-z0-9_.]*)\)`)
	envNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	dockerRe   = regexp.MustCompile(`\bdocker(\s|-compose\b)`)
	trivialRe  = regexp.MustCompile(`^\s*succeeded\(\)\s*$`)
	scriptKeys = []string{"script", "bash", "sh"}
)

type conv struct {
	file       string
	vars       map[string]string
	groups     []string
	containers map[string]string
	notes      []string
	off        []string
	steps      []outStep
	summaries  []importer.Step
	stages     []stageOut
	taken      map[string]bool // step names handed out so far
	x          *expander
	// varTemplates are variable templates that could not be read; a $(name)
	// may come from them.
	varTemplates []string
}

type stageOut struct {
	name  string
	steps []string
}

// Convert implements importer.Source. Templates are inlined and ${{ }}
// expressions evaluated first (WU-047); if the root's own expressions
// cannot be, it is converted as written.
func (Source) Convert(file string, content []byte, look importer.Lookup) (importer.Converted, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return importer.Converted{}, fmt.Errorf("not valid YAML: %v", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return importer.Converted{}, fmt.Errorf("not an Azure pipeline: the top level is not a mapping")
	}
	orig := doc.Content[0]
	c := &conv{file: file, vars: map[string]string{}, containers: map[string]string{}, taken: map[string]bool{}, x: newExpander(look, file, orig)}
	on := c.triggers(orig)
	root := orig
	rs := &scope{params: c.rootParams(get(orig, "parameters")), vars: inlineVars(get(orig, "variables"))}
	if exp, err := c.x.expandRoot(orig, rs); err != nil {
		c.notes = append(c.notes, "template expressions not expanded: "+err.Error())
	} else {
		root = exp
	}
	if res := get(root, "resources"); res != nil {
		for _, ct := range items(get(res, "containers")) {
			if alias, image := scalar(get(ct, "container")), scalar(get(ct, "image")); alias != "" && image != "" {
				c.containers[alias] = image
			}
		}
	}
	c.readVariables(get(root, "variables"), c.vars)

	switch {
	case root == orig && get(root, "extends") != nil:
		tpl := firstOf(get(root, "extends"), "template")
		c.add(outStep{Name: c.unique(importer.Slug(templateName(tpl), "template")),
			Disabled: &outDisabled{Reason: "template", Note: "extends template " + tpl}}, get(root, "extends"), "template", "extends template "+tpl)
	case get(root, "stages") != nil:
		c.convertStages(root)
	case get(root, "jobs") != nil:
		c.convertJobs("", items(get(root, "jobs")), root, nil, nil)
	case get(root, "steps") != nil:
		c.convertJobs("", []*yaml.Node{root}, root, nil, nil)
	}
	if len(c.steps) == 0 {
		return importer.Converted{}, fmt.Errorf("no jobs found")
	}
	c.notes = append(c.notes, c.x.repoNotes()...)
	for _, g := range c.groups {
		c.notes = append(c.notes, fmt.Sprintf("variable group %s: its values are not imported; steps that may use them are disabled (secrets)", g))
	}

	out := outFile{Version: 1, On: on, Steps: c.steps}
	if len(c.off) > 0 {
		out.Disabled = &outDisabled{Reason: "trigger", Note: clip(strings.Join(c.off, "; "))}
	}
	if len(c.stages) > 0 {
		st := &yaml.Node{Kind: yaml.MappingNode}
		for _, s := range c.stages {
			if len(s.steps) == 0 {
				continue
			}
			seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, n := range s.steps {
				seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: n})
			}
			st.Content = append(st.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: s.name}, seq)
		}
		if len(st.Content) > 0 {
			out.Stages = st
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(out); err != nil {
		return importer.Converted{}, err
	}
	body := buf.Bytes()
	header := fmt.Sprintf("# Imported from %s (Azure Pipelines) by Plumbix. Jobs Plumbix cannot run yet\n# are kept, disabled with a reason; the pipeline runs once none is left.\n", file)
	return importer.Converted{Content: append([]byte(header), body...), Steps: c.summaries, Notes: c.notes}, nil
}

// triggers maps trigger, pr, schedules and parameters.
func (c *conv) triggers(root *yaml.Node) outOn {
	on := outOn{Manual: true, Push: &outPush{Branches: []string{"**"}}}
	t := get(root, "trigger")
	switch {
	case t == nil:
	case t.Kind == yaml.ScalarNode && t.Value == "none":
		on.Push = nil
	case t.Kind == yaml.SequenceNode:
		on.Push.Branches = mapBranches(strs(t))
	case t.Kind == yaml.MappingNode:
		br := get(t, "branches")
		if br == nil && get(t, "tags") != nil {
			on.Push = nil
		}
		if br != nil {
			inc := strs(get(br, "include"))
			if br.Kind == yaml.SequenceNode {
				inc = strs(br)
			}
			if len(inc) > 0 {
				on.Push.Branches = mapBranches(inc)
			}
			if ex := strs(get(br, "exclude")); len(ex) > 0 {
				c.notes = append(c.notes, fmt.Sprintf("branch excludes dropped (%s): Plumbix has no excludes, so the pipeline may run more often, never less", strings.Join(ex, ", ")))
			}
		}
		if ps := get(t, "paths"); ps != nil && on.Push != nil {
			if inc := strs(get(ps, "include")); len(inc) > 0 {
				on.Push.Paths = mapPaths(inc)
				c.notes = append(c.notes, "path filters approximated: a folder includes everything under it")
			}
			if ex := strs(get(ps, "exclude")); len(ex) > 0 {
				c.notes = append(c.notes, fmt.Sprintf("path excludes dropped (%s): the pipeline may run more often, never less", strings.Join(ex, ", ")))
			}
		}
		if tags := get(t, "tags"); tags != nil {
			c.off = append(c.off, "tags: "+strings.Join(append(strs(get(tags, "include")), strs(tags)...), ", "))
		}
	}
	if get(root, "pr") != nil {
		c.notes = append(c.notes, "pr trigger not imported: .plumbix/pipeline.yaml judges changes")
	}
	if get(root, "schedules") != nil {
		c.off = append(c.off, "schedules")
	}
	return on
}

// rootParams returns the run-time parameters' defaults (WU-047): a run of
// the imported pipeline always uses them. One without a default disables
// the pipeline.
func (c *conv) rootParams(decl *yaml.Node) map[string]any {
	out := map[string]any{}
	var none, defaults []string
	for _, p := range items(decl) {
		name, def := scalar(get(p, "name")), get(p, "default")
		if def == nil {
			none = append(none, name)
			continue
		}
		v, err := paramValue(name, strings.ToLower(scalar(get(p, "type"))), def)
		if err != nil {
			none = append(none, name)
			continue
		}
		out[name] = v
		if str, err := toString(v); err == nil {
			defaults = append(defaults, name+"="+str)
		} else {
			defaults = append(defaults, name)
		}
	}
	if len(none) > 0 {
		c.off = append(c.off, "manual parameters without a default: "+strings.Join(none, ", "))
	}
	if len(defaults) > 0 {
		c.notes = append(c.notes, "run-time parameters take their defaults, as every run of the imported pipeline will: "+strings.Join(defaults, ", "))
	}
	return out
}

// inlineVars are the root's inline variables, which ${{ variables.x }}
// can read.
func inlineVars(n *yaml.Node) map[string]string {
	out := map[string]string{}
	if n != nil && n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			out[n.Content[i].Value] = n.Content[i+1].Value
		}
	}
	for _, v := range items(n) {
		if name := scalar(get(v, "name")); name != "" {
			out[name] = scalar(get(v, "value"))
		}
	}
	return out
}

// failure says why a template reference was not inlined, if it was tried.
func (c *conv) failure(n *yaml.Node) string {
	if msg, ok := c.x.failed[n]; ok {
		return " not inlined: " + msg
	}
	return ""
}

func mapBranches(in []string) []string {
	var out []string
	for _, b := range in {
		b = strings.TrimPrefix(b, "refs/heads/")
		if !strings.Contains(b, "**") {
			b = strings.ReplaceAll(b, "*", "**")
		}
		out = append(out, b)
	}
	return out
}

func mapPaths(in []string) []string {
	var out []string
	for _, p := range in {
		p = strings.TrimPrefix(p, "/")
		switch {
		case strings.Contains(p, "**"):
		case strings.Contains(p, "*"):
			p = strings.ReplaceAll(p, "*", "**")
		case path.Ext(p) == "":
			p = strings.TrimSuffix(p, "/") + "/**"
		}
		out = append(out, p)
	}
	return out
}

// readVariables reads a variables mapping or list into vars, noting groups.
func (c *conv) readVariables(n *yaml.Node, vars map[string]string) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			vars[n.Content[i].Value] = n.Content[i+1].Value
		}
		return
	}
	for _, v := range items(n) {
		switch {
		case get(v, "group") != nil:
			if g := scalar(get(v, "group")); !slices.Contains(c.groups, g) {
				c.groups = append(c.groups, g)
			}
		case get(v, "name") != nil:
			vars[scalar(get(v, "name"))] = scalar(get(v, "value"))
		case get(v, "template") != nil:
			tpl := scalar(get(v, "template"))
			if !slices.Contains(c.varTemplates, tpl) {
				c.varTemplates = append(c.varTemplates, tpl)
				c.notes = append(c.notes, "variable template "+tpl+cmpOr(c.failure(v), " not read"))
			}
		}
	}
}

// convertStages converts stages in order: a stage depends on the previous
// one unless dependsOn says otherwise.
func (c *conv) convertStages(root *yaml.Node) {
	byStage := map[string][]string{}
	stageTaken := map[string]bool{}
	prev := ""
	for _, s := range items(get(root, "stages")) {
		if tpl := scalar(get(s, "template")); tpl != "" {
			name := importer.Slug(templateName(tpl), "template")
			note := "stage template " + tpl + c.failure(s)
			c.add(outStep{Name: c.unique(name), Disabled: &outDisabled{Reason: "template", Note: clip(note)}}, s, "template", note)
			continue
		}
		name := scalar(get(s, "stage"))
		var deps []string
		if d := get(s, "dependsOn"); d != nil {
			deps = strs(d)
			if d.Kind == yaml.ScalarNode {
				deps = []string{d.Value}
			}
		} else if prev != "" {
			deps = []string{prev}
		}
		var needs []string
		for _, d := range deps {
			needs = append(needs, byStage[d]...)
		}
		before := len(c.steps)
		slug := importer.Slug(name, "stage")
		for i := 2; stageTaken[slug]; i++ {
			slug = fmt.Sprintf("%s-%d", importer.Slug(name, "stage"), i)
		}
		stageTaken[slug] = true
		c.convertJobs(slug, items(get(s, "jobs")), root, s, needs)
		var names []string
		for _, st := range c.steps[before:] {
			names = append(names, st.Name)
		}
		byStage[name] = names
		c.stages = append(c.stages, stageOut{name: slug, steps: names})
		prev = name
	}
}

// convertJobs converts one stage's jobs; needs are the steps of the stages
// it depends on.
func (c *conv) convertJobs(stage string, jobs []*yaml.Node, root, stageNode *yaml.Node, needs []string) {
	prefix := ""
	if stage != "" {
		prefix = stage + "-"
	}
	// First pass: each job's step names, by position, so jobs with the same
	// key keep their own; byKey serves dependsOn, which may name later jobs.
	names := make([][]string, len(jobs))
	byKey := map[string][]string{}
	for i, j := range jobs {
		key := jobKey(j)
		base := c.unique(prefix + importer.Slug(key, "job"))
		names[i] = []string{base}
		if m := get(get(j, "strategy"), "matrix"); m != nil && m.Kind == yaml.MappingNode {
			names[i] = nil
			for k := 0; k+1 < len(m.Content); k += 2 {
				names[i] = append(names[i], c.unique(base+"-"+importer.Slug(m.Content[k].Value, "entry")))
			}
		}
		byKey[key] = append(byKey[key], names[i]...)
	}
	for ji, j := range jobs {
		jobNeeds := slices.Clone(needs)
		if d := get(j, "dependsOn"); d != nil {
			deps := strs(d)
			if d.Kind == yaml.ScalarNode {
				deps = []string{d.Value}
			}
			for _, dep := range deps {
				jobNeeds = append(jobNeeds, byKey[dep]...)
			}
		}
		m := get(get(j, "strategy"), "matrix")
		if m != nil && m.Kind == yaml.MappingNode {
			for i, k := 0, 0; i+1 < len(m.Content); i, k = i+2, k+1 {
				entry := map[string]string{}
				c.readVariables(m.Content[i+1], entry)
				c.job(names[ji][k], j, root, stageNode, jobNeeds, entry)
			}
			continue
		}
		c.job(names[ji][0], j, root, stageNode, jobNeeds, nil)
	}
}

func jobKey(j *yaml.Node) string {
	for _, k := range []string{"job", "deployment"} {
		if v := scalar(get(j, k)); v != "" {
			return v
		}
	}
	if tpl := scalar(get(j, "template")); tpl != "" {
		return templateName(tpl)
	}
	return "build"
}

// templateName is a template reference's file name without folder,
// extension or @alias.
func templateName(tpl string) string {
	p, _, _ := strings.Cut(tpl, "@")
	return strings.TrimSuffix(path.Base(p), path.Ext(p))
}

// job converts one job, or one matrix entry of it, into a step.
func (c *conv) job(name string, j, root, stageNode *yaml.Node, needs []string, entry map[string]string) {
	found := map[string][]string{}
	note := func(reason, detail string) {
		if !slices.Contains(found[reason], detail) {
			found[reason] = append(found[reason], detail)
		}
	}
	st := outStep{Name: name, Needs: needs, Env: map[string]string{}}
	vars := map[string]string{}
	for k, v := range c.vars {
		vars[k] = v
	}
	c.readVariables(get(stageNode, "variables"), vars)
	c.readVariables(get(j, "variables"), vars)
	for k, v := range entry {
		vars[k] = v
	}

	if tpl := scalar(get(j, "template")); tpl != "" {
		note("template", "job template "+tpl+c.failure(j))
	}
	if get(j, "deployment") != nil {
		env := scalar(get(j, "environment"))
		if env == "" {
			env = scalar(get(get(j, "environment"), "name"))
		}
		note("deployment", "deployment job to environment "+env)
	}
	if svc := get(j, "services"); svc != nil {
		var ns []string
		for i := 0; i+1 < len(svc.Content); i += 2 {
			ns = append(ns, svc.Content[i].Value)
		}
		note("services", "service containers "+strings.Join(ns, ", "))
	}
	for _, cond := range []*yaml.Node{get(stageNode, "condition"), get(j, "condition")} {
		if s := scalar(cond); s != "" && !trivialRe.MatchString(s) {
			note("condition", s)
		}
	}

	container := get(j, "container")
	if container == nil && get(j, "steps") != nil && j == root {
		container = get(root, "container")
	}
	switch {
	case container == nil:
	case container.Kind == yaml.ScalarNode:
		st.Image = container.Value
		if img, ok := c.containers[container.Value]; ok {
			st.Image = img
		}
	default:
		st.Image = scalar(get(container, "image"))
	}
	if st.Image == "" && get(j, "template") == nil {
		pool := get(j, "pool")
		if pool == nil {
			pool = get(stageNode, "pool")
		}
		if pool == nil {
			pool = get(root, "pool")
		}
		desc := scalar(pool)
		if desc == "" {
			desc = scalar(get(pool, "vmImage"))
		}
		if desc == "" {
			desc = scalar(get(pool, "name"))
		}
		if desc == "" {
			desc = "the default pool"
		}
		note("host", "ran on the agent's machine ("+desc+") with no container image")
	}

	steps := items(get(j, "steps"))
	if strat := get(j, "strategy"); get(j, "deployment") != nil && strat != nil {
		for i := 0; i+1 < len(strat.Content); i += 2 {
			for _, phase := range []string{"preDeploy", "deploy", "routeTraffic", "postRouteTraffic"} {
				steps = append(steps, items(get(get(strat.Content[i+1], phase), "steps"))...)
			}
		}
	}
	var unresolved []string
	resolve := func(text string, asEnv bool) string {
		return macroRe.ReplaceAllStringFunc(text, func(m string) string {
			v := macroRe.FindStringSubmatch(m)[1]
			switch {
			case workspaceVars[v]:
				return "/workspace"
			case strings.Contains(vars[v], "$["):
				// set by a runtime expression: not known at import
				unresolved = append(unresolved, v)
				return m
			case hasKey(vars, v):
				if asEnv || !envNameRe.MatchString(v) || strings.HasPrefix(v, "PLUMBIX_") {
					if asEnv {
						return vars[v]
					}
					unresolved = append(unresolved, v)
					return m
				}
				st.Env[v] = vars[v]
				return "${" + v + "}"
			case strings.Contains(v, "."), len(c.groups) > 0, len(c.varTemplates) > 0:
				unresolved = append(unresolved, v)
				return m
			}
			if asEnv {
				unresolved = append(unresolved, v)
			}
			return m
		})
	}
	for _, s := range steps {
		if cond := scalar(get(s, "condition")); cond != "" && !trivialRe.MatchString(cond) {
			note("condition", "step condition "+cond)
		}
		switch {
		case firstOf(s, scriptKeys...) != "":
			if dn := scalar(get(s, "displayName")); dn != "" {
				st.Run = append(st.Run, "# "+dn)
			}
			cmd := strings.TrimRight(resolve(firstOf(s, scriptKeys...), false), "\n")
			if wd := scalar(get(s, "workingDirectory")); wd != "" {
				cmd = "(cd " + resolve(wd, true) + " && " + cmd + ")"
			}
			st.Run = append(st.Run, cmd)
			if env := get(s, "env"); env != nil {
				for i := 0; i+1 < len(env.Content); i += 2 {
					st.Env[env.Content[i].Value] = resolve(env.Content[i+1].Value, true)
				}
			}
		case get(s, "pwsh") != nil || get(s, "powershell") != nil:
			note("task", "PowerShell step")
		case get(s, "checkout") != nil:
			if co := scalar(get(s, "checkout")); co != "self" && co != "none" {
				note("checkout", "checks out "+co)
			}
		case get(s, "task") != nil:
			note("task", scalar(get(s, "task")))
		case get(s, "template") != nil:
			note("template", "step template "+scalar(get(s, "template"))+c.failure(s))
		default:
			key := "a step"
			if len(s.Content) > 0 {
				key = s.Content[0].Value
			}
			note("task", key+" step")
		}
	}
	for _, cmd := range st.Run {
		if dockerRe.MatchString(cmd) {
			note("docker", "runs docker itself")
			break
		}
	}
	if raw, _ := yaml.Marshal(j); strings.Contains(string(raw), "${{") {
		note("template", "template expressions ${{ }}")
	} else if strings.Contains(string(raw), "$[") {
		note("variables", "runtime expressions $[ ]")
	}
	sort.Strings(unresolved)
	for _, v := range slices.Compact(unresolved) {
		switch {
		case len(c.groups) > 0 && !strings.Contains(v, "."):
			note("secrets", "$("+v+") may come from variable group "+strings.Join(c.groups, ", "))
		case len(c.varTemplates) > 0 && !strings.Contains(v, "."):
			note("variables", "$("+v+") may come from variable template "+strings.Join(c.varTemplates, ", "))
		default:
			note("variables", "$("+v+")")
		}
	}

	var parts []string
	reason := ""
	for _, r := range reasonOrder {
		if len(found[r]) == 0 {
			continue
		}
		if reason == "" {
			reason = r
		}
		parts = append(parts, r+": "+strings.Join(found[r], ", "))
	}
	if reason == "" && len(st.Run) == 0 {
		st.Run = []string{"true"}
		c.notes = append(c.notes, "job "+name+" has no commands; it runs true")
	}
	if len(st.Env) == 0 {
		st.Env = nil
	}
	summaryNote := strings.Join(parts, "; ")
	if reason != "" {
		st.Disabled = &outDisabled{Reason: reason, Note: clip(summaryNote)}
	}
	c.add(st, j, reason, summaryNote)
}

// add records a step with its provenance and summary.
func (c *conv) add(st outStep, n *yaml.Node, reason, note string) {
	raw, _ := yaml.Marshal(n)
	sum := sha256.Sum256(raw)
	file := c.file
	if o, ok := c.x.origin[n]; ok {
		file = o
	}
	st.Source = outSource{File: file, Line: n.Line, Hash: hex.EncodeToString(sum[:])}
	c.steps = append(c.steps, st)
	c.summaries = append(c.summaries, importer.Step{Name: st.Name, Reason: reason, Note: note, File: file, Line: n.Line})
}

// unique returns name, or name-2, name-3 when taken, and reserves it.
func (c *conv) unique(name string) string {
	out := name
	for i := 2; c.taken[out]; i++ {
		out = fmt.Sprintf("%s-%d", name, i)
	}
	c.taken[out] = true
	return out
}

func clip(s string) string {
	if len(s) <= 500 {
		return s
	}
	return s[:496] + " ..."
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

func get(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

func items(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

func strs(n *yaml.Node) []string {
	var out []string
	for _, it := range items(n) {
		if it.Kind == yaml.ScalarNode {
			out = append(out, it.Value)
		}
	}
	return out
}

func firstOf(n *yaml.Node, keys ...string) string {
	for _, k := range keys {
		if v := scalar(get(n, k)); v != "" {
			return v
		}
	}
	return ""
}

type outFile struct {
	Version  int          `yaml:"version"`
	On       outOn        `yaml:"on"`
	Disabled *outDisabled `yaml:"disabled,omitempty"`
	Steps    []outStep    `yaml:"steps"`
	Stages   *yaml.Node   `yaml:"stages,omitempty"`
}

type outOn struct {
	Push   *outPush `yaml:"push,omitempty"`
	Manual bool     `yaml:"manual"`
}

type outPush struct {
	Branches []string `yaml:"branches,flow"`
	Paths    []string `yaml:"paths,omitempty,flow"`
}

type outDisabled struct {
	Reason string `yaml:"reason"`
	Note   string `yaml:"note,omitempty"`
}

type outSource struct {
	File string `yaml:"file"`
	Line int    `yaml:"line"`
	Hash string `yaml:"hash"`
}

type outStep struct {
	Name     string            `yaml:"name"`
	Image    string            `yaml:"image,omitempty"`
	Run      []string          `yaml:"run,omitempty"`
	Needs    []string          `yaml:"needs,omitempty,flow"`
	Env      map[string]string `yaml:"env,omitempty"`
	Disabled *outDisabled      `yaml:"disabled,omitempty"`
	Source   outSource         `yaml:"source"`
}
