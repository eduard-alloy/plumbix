// Package guard reviews changes to pipeline files before a person approves
// them (WU-051, A38). Plumbix runs it itself on every change that touches
// .plumbix/; no repository file defines it or can remove it. It reports what
// each pipeline file now does with secrets, images and commands, and flags
// what looks wrong.
package guard

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/eduard-alloy/plumbix/internal/pipeline"
	"github.com/eduard-alloy/plumbix/internal/validation"
)

// Name is the validator name the guard's risks carry.
const Name = "pipeline-guard"

const (
	changePipeline = ".plumbix/pipeline.yaml"
	triggeredDir   = ".plumbix/pipelines/"
)

// File is one pipeline file a change touches; Before or After is nil when
// the file is absent on that side.
type File struct {
	Path          string
	Before, After []byte
}

// Input is what the guard reviews.
type Input struct {
	Files []File
	// KnownSecrets are the names set in the secrets file; nil skips the check.
	KnownSecrets map[string]bool
	// Protected are the repository's protected branches.
	Protected []string
}

// IsPipelineFile reports whether path is a pipeline file: the change
// pipeline or a triggered pipeline directly under .plumbix/pipelines/.
func IsPipelineFile(p string) bool {
	if p == changePipeline {
		return true
	}
	rest, ok := strings.CutPrefix(p, triggeredDir)
	return ok && !strings.Contains(rest, "/") && path.Ext(rest) == ".yaml"
}

var (
	networkTool = regexp.MustCompile(`\b(curl|wget|nc|ncat|socat|ssh|scp|rsync|telnet)\b|https?://`)
	dumpsEnv    = regexp.MustCompile(`\b(printenv|env)\b|/proc/self/environ`)
)

// Review returns one risk per changed file, summarising it, and one per flag.
// Every risk needs a human.
func Review(in Input) []validation.Risk {
	var out []validation.Risk
	add := func(format string, args ...any) {
		out = append(out, validation.Risk{Area: "pipelines", Message: fmt.Sprintf(format, args...), HumanRequired: true})
	}
	for _, f := range in.Files {
		if !IsPipelineFile(f.Path) || (f.Before != nil && f.After != nil && string(f.Before) == string(f.After)) {
			continue
		}
		if f.After == nil {
			add("%s deleted.", f.Path)
			continue
		}
		after, err := parse(f.Path, f.After)
		if err != nil {
			add("%s does not parse: %v", f.Path, err)
			continue
		}
		var before *pipeline.Pipeline
		if f.Before != nil {
			before, _ = parse(f.Path, f.Before)
		}
		verb := "changed"
		if f.Before == nil {
			verb = "added"
		}
		add("%s %s: %s.", f.Path, verb, strings.Join(summary(after, before), "; "))
		flags(f.Path, after, in, add)
	}
	return out
}

func parse(p string, src []byte) (*pipeline.Pipeline, error) {
	if p == changePipeline {
		return pipeline.Parse(src)
	}
	return pipeline.ParseTriggered(src)
}

// summary lists the triggers, secrets, new images and step changes.
func summary(after, before *pipeline.Pipeline) []string {
	var parts []string
	if t := after.On; t != nil {
		var starts []string
		if t.Push != nil {
			s := "on push to " + strings.Join(t.Push.Branches, ", ")
			if len(t.Push.Paths) > 0 {
				s += " when " + strings.Join(t.Push.Paths, ", ") + " change"
			}
			starts = append(starts, s)
		}
		if t.Manual {
			starts = append(starts, "by hand")
		}
		parts = append(parts, "starts "+strings.Join(starts, "; "))
	}
	if after.Off != nil {
		parts = append(parts, "disabled ("+after.Off.Reason+")")
	}
	oldSecrets, oldImages, oldSteps := map[string]bool{}, map[string]bool{}, map[string]pipeline.Step{}
	if before != nil {
		for _, st := range before.Steps {
			oldSteps[st.Name] = st
			oldImages[st.Image] = true
			for _, s := range st.Secrets {
				oldSecrets[s] = true
			}
		}
	}
	var secrets, images, added, changed, envChanged, otherChanged []string
	seenSecret, seenImage := map[string]bool{}, map[string]bool{}
	for _, st := range after.Steps {
		for _, s := range st.Secrets {
			if !seenSecret[s] {
				seenSecret[s] = true
				if oldSecrets[s] {
					secrets = append(secrets, s)
				} else {
					secrets = append(secrets, s+" (new)")
				}
			}
		}
		if st.Image != "" && !seenImage[st.Image] && !oldImages[st.Image] {
			seenImage[st.Image] = true
			images = append(images, st.Image)
		}
		old, existed := oldSteps[st.Name]
		switch {
		case !existed:
			added = append(added, st.Name)
		default:
			// Run is the script built from the commands, so it covers them.
			if old.Run != st.Run || old.Image != st.Image || !slices.Equal(old.Secrets, st.Secrets) {
				changed = append(changed, st.Name)
			}
			if !maps.Equal(old.Env, st.Env) {
				envChanged = append(envChanged, st.Name)
			}
			if !slices.Equal(old.Needs, st.Needs) || !slices.Equal(old.Cache, st.Cache) || !slices.Equal(old.OnlyPaths, st.OnlyPaths) ||
				old.Validator != st.Validator || old.External != st.External || old.Reviewer != st.Reviewer {
				otherChanged = append(otherChanged, st.Name)
			}
		}
		delete(oldSteps, st.Name)
	}
	var removed []string
	for name := range oldSteps {
		removed = append(removed, name)
	}
	sort.Strings(secrets)
	sort.Strings(images)
	sort.Strings(removed)
	if len(secrets) > 0 {
		parts = append(parts, "secrets "+strings.Join(secrets, ", "))
	}
	if len(images) > 0 {
		if before == nil {
			parts = append(parts, "images "+strings.Join(images, ", "))
		} else {
			parts = append(parts, "images "+strings.Join(images, " (new), ")+" (new)")
		}
	}
	if len(added) > 0 {
		parts = append(parts, "steps added: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "steps removed: "+strings.Join(removed, ", "))
	}
	if len(changed) > 0 {
		parts = append(parts, "commands changed: "+strings.Join(changed, ", "))
	}
	if len(envChanged) > 0 {
		parts = append(parts, "env changed: "+strings.Join(envChanged, ", "))
	}
	if len(otherChanged) > 0 {
		parts = append(parts, "needs, caches or conditions changed: "+strings.Join(otherChanged, ", "))
	}
	if len(parts) == 0 {
		parts = append(parts, "no step changes; timeouts, retries, stages or formatting only")
	}
	return parts
}

// flags adds a risk for each thing that looks wrong.
func flags(p string, pl *pipeline.Pipeline, in Input, add func(string, ...any)) {
	named := false
	for _, st := range pl.Steps {
		for _, s := range st.Secrets {
			named = true
			if in.KnownSecrets != nil && !in.KnownSecrets[s] {
				add("secret %s, named by step %s in %s, is not set in the secrets file: a typo, or a name chosen to look like another.", s, st.Name, p)
			}
		}
		if len(st.Secrets) == 0 || !networkTool.MatchString(st.Run) {
			continue
		}
		var used []string
		for _, s := range st.Secrets {
			if regexp.MustCompile(`\$\{?` + regexp.QuoteMeta(s) + `\b`).MatchString(st.Run) {
				used = append(used, s)
			}
		}
		if dumpsEnv.MatchString(st.Run) {
			used = append(used, "(the whole environment)")
		}
		if len(used) > 0 {
			add("step %s uses a network tool with secret %s in %s: check where it sends it.", st.Name, strings.Join(used, ", "), p)
		}
	}
	if !named || pl.On == nil || pl.On.Push == nil {
		return
	}
	var open []string
	for _, b := range pl.On.Push.Branches {
		if !slices.Contains(in.Protected, b) {
			open = append(open, b)
		}
	}
	if len(open) > 0 {
		add("%s names secrets and runs on branches that are not protected (%s): those runs get no secrets (A38).", p, strings.Join(open, ", "))
	}
}
