package azure

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/eduard-alloy/plumbix/internal/importer"
)

// Limits on expanding one pipeline (WU-047).
const (
	maxTemplateFiles = 100
	maxTemplateDepth = 20
	maxNodes         = 100000
)

// templateKinds are the lists whose template: items are template references.
var templateKinds = map[string]bool{"stages": true, "jobs": true, "steps": true, "variables": true}

var (
	directiveRe = regexp.MustCompile(`^\$\{\{\s*(if|elseif|else|each|insert)\b\s*(.*?)\s*\}\}$`)
	exprRe      = regexp.MustCompile(`\$\{\{(.*?)\}\}`)
)

// loc is a file in the repository being imported (alias empty) or in a
// repository resource.
type loc struct{ alias, path string }

func (l loc) label() string {
	if l.alias == "" {
		return l.path
	}
	return l.path + "@" + l.alias
}

// expander inlines templates and evaluates ${{ }} expressions (WU-047).
type expander struct {
	look  importer.Lookup
	root  loc
	res   map[string]importer.RepoRef // repository resources by alias
	trees map[string]importer.Tree
	errs  map[string]error
	files int
	nodes int
	// origin is the file each produced mapping was defined in.
	origin map[*yaml.Node]string
	// failed holds, for a template reference kept as is, why it was not
	// inlined.
	failed map[*yaml.Node]string
	// used lists the templates read from each repository resource.
	used map[string][]string
}

func newExpander(look importer.Lookup, file string, root *yaml.Node) *expander {
	x := &expander{look: look, root: loc{path: file}, res: map[string]importer.RepoRef{}, trees: map[string]importer.Tree{}, errs: map[string]error{},
		origin: map[*yaml.Node]string{}, failed: map[*yaml.Node]string{}, used: map[string][]string{}}
	for _, r := range items(get(get(root, "resources"), "repositories")) {
		if alias := scalar(get(r, "repository")); alias != "" {
			x.res[alias] = importer.RepoRef{Kind: scalar(get(r, "type")), Name: scalar(get(r, "name")), Ref: scalar(get(r, "ref"))}
		}
	}
	return x
}

// expandRoot expands the root pipeline; extends is replaced by the body of
// its template.
func (x *expander) expandRoot(root *yaml.Node, s *scope) (*yaml.Node, error) {
	out := &yaml.Node{Kind: yaml.MappingNode, Line: root.Line, Column: root.Column}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		if k.Value == "parameters" {
			continue
		}
		if k.Value == "extends" {
			kind, body := x.extends(v, s)
			out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: kind}, &yaml.Node{Kind: yaml.SequenceNode, Content: body})
			continue
		}
		nv, err := x.value(v, s, x.root, 0, k.Value)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k.Value, Line: k.Line}, nv)
	}
	return out, nil
}

// extends inlines the extended template's stages, jobs or steps.
func (x *expander) extends(n *yaml.Node, s *scope) (string, []*yaml.Node) {
	ref := &yaml.Node{Kind: yaml.MappingNode, Line: n.Line, Column: n.Column, Content: n.Content}
	if n.Kind == yaml.ScalarNode {
		ref.Content = []*yaml.Node{{Kind: yaml.ScalarNode, Value: "template"}, n}
	}
	body, kind := x.reference(ref, s, x.root, 0, "")
	return kind, body
}

func (x *expander) count() error {
	x.nodes++
	if x.nodes > maxNodes {
		return fmt.Errorf("the expanded pipeline has more than %d nodes", maxNodes)
	}
	return nil
}

// value expands one node; parent is the key it sits under.
func (x *expander) value(n *yaml.Node, s *scope, l loc, depth int, parent string) (*yaml.Node, error) {
	if err := x.count(); err != nil {
		return nil, err
	}
	switch n.Kind {
	case yaml.AliasNode:
		return x.value(n.Alias, s, l, depth, parent)
	case yaml.ScalarNode:
		v, err := x.scalar(n, s)
		if err != nil || v.Kind == yaml.ScalarNode {
			return v, err
		}
		return x.value(v, s, l, depth, parent)
	case yaml.SequenceNode:
		list, err := x.list(n.Content, s, l, depth, parent)
		if err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.SequenceNode, Style: n.Style, Line: n.Line, Column: n.Column, Content: list}, nil
	case yaml.MappingNode:
		return x.mapping(n, s, l, depth)
	}
	return n, nil
}

// scalar evaluates the expressions in a scalar. A scalar that is one whole
// expression takes its value, which may be a mapping or a list.
func (x *expander) scalar(n *yaml.Node, s *scope) (*yaml.Node, error) {
	v := n.Value
	out := &yaml.Node{Kind: yaml.ScalarNode, Tag: n.Tag, Style: n.Style, Value: v, Line: n.Line, Column: n.Column}
	if !strings.Contains(v, "${{") {
		return out, nil
	}
	t := strings.TrimSpace(v)
	if strings.HasPrefix(t, "${{") && strings.HasSuffix(t, "}}") && strings.Count(t, "${{") == 1 {
		val, err := evalExpr(t[3:len(t)-2], s)
		if err != nil {
			return nil, err
		}
		vn, err := valueNode(val)
		if err != nil {
			return nil, err
		}
		if vn.Kind == yaml.ScalarNode {
			vn = &yaml.Node{Kind: yaml.ScalarNode, Tag: vn.Tag, Value: vn.Value, Line: n.Line, Column: n.Column}
		}
		return vn, nil
	}
	var firstErr error
	out.Value = exprRe.ReplaceAllStringFunc(v, func(m string) string {
		val, err := evalExpr(m[3:len(m)-2], s)
		if err == nil {
			var str string
			if str, err = toString(val); err == nil {
				return str
			}
		}
		if firstErr == nil {
			firstErr = err
		}
		return m
	})
	out.Tag = "!!str"
	return out, firstErr
}

// directive reads a ${{ if }}, ${{ elseif }}, ${{ else }}, ${{ each }} or
// ${{ insert }} key.
func directive(key string) (kind, arg string, ok bool) {
	m := directiveRe.FindStringSubmatch(strings.TrimSpace(key))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// chain is the state of an if, elseif, else chain, which may span the
// keys of a mapping or the items of a list.
type chain struct{ open, taken bool }

// branches runs directives over the pairs of a mapping, calling take with
// the scope and value of every branch that applies.
func branches(pairs []*yaml.Node, s *scope, ch *chain, take func(*scope, *yaml.Node) error) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		kind, arg, _ := directive(pairs[i].Value)
		v := pairs[i+1]
		switch kind {
		case "if", "elseif":
			if kind == "elseif" && !ch.open {
				return errors.New("${{ elseif }} without ${{ if }}")
			}
			if kind == "if" {
				ch.taken = false
			}
			ch.open = true
			if ch.taken {
				continue
			}
			cond, err := evalExpr(arg, s)
			if err != nil {
				return err
			}
			if truthy(cond) {
				ch.taken = true
				if err := take(s, v); err != nil {
					return err
				}
			}
		case "else":
			if !ch.open {
				return errors.New("${{ else }} without ${{ if }}")
			}
			ch.open = false
			if !ch.taken {
				if err := take(s, v); err != nil {
					return err
				}
			}
		case "each":
			ch.open = false
			name, over, ok := strings.Cut(arg, " in ")
			if !ok {
				return fmt.Errorf("${{ each %s }} needs <name> in <list>", arg)
			}
			val, err := evalExpr(over, s)
			if err != nil {
				return err
			}
			els, ok := elements(val)
			if !ok && val != nil {
				return fmt.Errorf("${{ each }} over %s, which is not a list or an object", strings.TrimSpace(over))
			}
			for _, e := range els {
				if err := take(s.with(strings.TrimSpace(name), e), v); err != nil {
					return err
				}
			}
		case "insert":
			return fmt.Errorf("${{ insert }} is %w", errUnsupported)
		}
	}
	return nil
}

// mapping expands a mapping: directive keys merge their mapping into it.
func (x *expander) mapping(n *yaml.Node, s *scope, l loc, depth int) (*yaml.Node, error) {
	out := &yaml.Node{Kind: yaml.MappingNode, Style: n.Style, Line: n.Line, Column: n.Column}
	if o, ok := x.origin[n]; ok {
		x.origin[out] = o
	} else {
		x.origin[out] = l.label()
	}
	var ch chain
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if _, _, ok := directive(k.Value); ok {
			err := branches(n.Content[i:i+2], s, &ch, func(bs *scope, bv *yaml.Node) error {
				m, err := x.value(bv, bs, l, depth, "")
				if err != nil {
					return err
				}
				if m.Kind != yaml.MappingNode {
					return errors.New("a ${{ }} directive in a mapping must hold a mapping")
				}
				out.Content = append(out.Content, m.Content...)
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		ch = chain{}
		nk, err := x.scalar(k, s)
		if err != nil {
			return nil, err
		}
		if nk.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("the key %s is not text", k.Value)
		}
		nv, err := x.value(v, s, l, depth, nk.Value)
		if err != nil {
			return nil, err
		}
		out.Content = append(out.Content, nk, nv)
	}
	return out, nil
}

// list expands list items: directive items and items that evaluate to a
// list are spliced, and template references in a stages, jobs, steps or
// variables list are inlined.
func (x *expander) list(in []*yaml.Node, s *scope, l loc, depth int, kind string) ([]*yaml.Node, error) {
	var out []*yaml.Node
	var ch chain
	for _, it := range in {
		if it.Kind == yaml.MappingNode && len(it.Content) > 0 && allDirectives(it) {
			err := branches(it.Content, s, &ch, func(bs *scope, bv *yaml.Node) error {
				body := []*yaml.Node{bv}
				if bv.Kind == yaml.SequenceNode {
					body = bv.Content
				}
				more, err := x.list(body, bs, l, depth, kind)
				out = append(out, more...)
				return err
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		ch = chain{}
		if templateKinds[kind] && it.Kind == yaml.MappingNode && get(it, "template") != nil {
			more, _ := x.reference(it, s, l, depth, kind)
			out = append(out, more...)
			continue
		}
		if it.Kind == yaml.ScalarNode && strings.Contains(it.Value, "${{") {
			v, err := x.scalar(it, s)
			if err != nil {
				return nil, err
			}
			if v.Kind != yaml.ScalarNode {
				body := []*yaml.Node{v}
				if v.Kind == yaml.SequenceNode {
					body = v.Content
				}
				more, err := x.list(body, s, l, depth, kind)
				if err != nil {
					return nil, err
				}
				out = append(out, more...)
				continue
			}
		}
		v, err := x.value(it, s, l, depth, kind)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func allDirectives(n *yaml.Node) bool {
	for i := 0; i < len(n.Content); i += 2 {
		if _, _, ok := directive(n.Content[i].Value); !ok {
			return false
		}
	}
	return true
}

// reference inlines one template reference. A template that cannot be read
// or expanded stays as a reference, with why in failed.
// With kind empty (extends) the template's own stages, jobs or steps are
// used, and the kind found is returned.
func (x *expander) reference(it *yaml.Node, s *scope, l loc, depth int, kind string) ([]*yaml.Node, string) {
	ref := scalar(get(it, "template"))
	if rn, err := x.scalar(get(it, "template"), s); err == nil && rn.Kind == yaml.ScalarNode {
		ref = rn.Value
	}
	fail := func(err error) ([]*yaml.Node, string) {
		ph := &yaml.Node{Kind: yaml.MappingNode, Line: it.Line, Column: it.Column, Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "template"}, {Kind: yaml.ScalarNode, Value: ref}}}
		x.failed[ph] = err.Error()
		x.origin[ph] = l.label()
		return []*yaml.Node{ph}, cmpOr(kind, "jobs")
	}
	if depth >= maxTemplateDepth {
		return fail(fmt.Errorf("templates nested too deep (more than %d levels)", maxTemplateDepth))
	}
	target, err := x.resolve(l, ref)
	if err != nil {
		return fail(err)
	}
	content, err := x.read(target)
	if err != nil {
		return fail(err)
	}
	if x.files++; x.files > maxTemplateFiles {
		return fail(fmt.Errorf("more than %d template files", maxTemplateFiles))
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fail(fmt.Errorf("%s is not a YAML mapping", target.label()))
	}
	tpl := doc.Content[0]
	var given *yaml.Node
	if g := get(it, "parameters"); g != nil {
		if given, err = x.value(g, s, l, depth, "parameters"); err != nil {
			return fail(err)
		}
	}
	params, err := templateParams(get(tpl, "parameters"), given)
	if err != nil {
		return fail(err)
	}
	if kind == "" {
		for _, k := range []string{"stages", "jobs", "steps"} {
			if get(tpl, k) != nil {
				kind = k
				break
			}
		}
	}
	body := get(tpl, kind)
	if body == nil || body.Kind != yaml.SequenceNode {
		return fail(fmt.Errorf("%s has no %s list", target.label(), cmpOr(kind, "stages, jobs or steps")))
	}
	out, err := x.list(body.Content, &scope{params: params, vars: s.vars}, target, depth+1, kind)
	if err != nil {
		return fail(err)
	}
	if target.alias != "" && !slices.Contains(x.used[target.alias], target.path) {
		x.used[target.alias] = append(x.used[target.alias], target.path)
	}
	return out, kind
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// resolve finds the file a reference names: path@alias from that
// repository's root, /path from the root, other paths from the folder of
// the referring file.
func (x *expander) resolve(l loc, ref string) (loc, error) {
	p, alias, explicit := ref, l.alias, false
	if i := strings.LastIndex(ref, "@"); i >= 0 {
		p, alias, explicit = ref[:i], ref[i+1:], true
		if alias == "self" {
			alias = ""
		} else if _, ok := x.res[alias]; !ok {
			return loc{}, fmt.Errorf("repository %s is not declared in resources.repositories", alias)
		}
	}
	full := p
	switch {
	case explicit || strings.HasPrefix(p, "/"):
		full = strings.TrimPrefix(p, "/")
	default:
		full = path.Join(path.Dir(l.path), p)
	}
	full = path.Clean(full)
	if full == "." || full == ".." || strings.HasPrefix(full, "../") || p == "" {
		return loc{}, fmt.Errorf("%s leaves the repository", ref)
	}
	return loc{alias: alias, path: full}, nil
}

// read returns a template file's content.
func (x *expander) read(l loc) ([]byte, error) {
	if l.alias == "" {
		if c, ok := x.look.Files[l.path]; ok {
			return c, nil
		}
		return nil, fmt.Errorf("%s not found in this repository", l.path)
	}
	t, err := x.tree(l.alias)
	if err != nil {
		return nil, err
	}
	if c, ok := t.Files[l.path]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("%s not found in %s", l.path, t.Label)
}

// tree reads a repository resource once per pipeline.
func (x *expander) tree(alias string) (importer.Tree, error) {
	if t, ok := x.trees[alias]; ok {
		return t, x.errs[alias]
	}
	var t importer.Tree
	err := errors.New("templates from other repositories cannot be read here")
	if x.look.Repos != nil {
		r := x.res[alias]
		if t, err = x.look.Repos(r); err != nil {
			err = fmt.Errorf("repository %s (%s): %w", alias, r.Name, err)
		}
	}
	x.trees[alias], x.errs[alias] = t, err
	return t, err
}

// repoNotes names the templates read from other repositories.
func (x *expander) repoNotes() []string {
	aliases := make([]string, 0, len(x.used))
	for a := range x.used {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	var out []string
	for _, a := range aliases {
		out = append(out, fmt.Sprintf("templates inlined from %s (resource %s): %s", x.trees[a].Label, a, strings.Join(x.used[a], ", ")))
	}
	return out
}

// templateParams checks the parameters given to a template against those
// it declares, filling in defaults and converting to the declared types.
func templateParams(decl, given *yaml.Node) (map[string]any, error) {
	type param struct {
		name, typ string
		def       *yaml.Node
		values    []string
	}
	var ps []param
	switch {
	case decl == nil:
	case decl.Kind == yaml.MappingNode:
		for i := 0; i+1 < len(decl.Content); i += 2 {
			ps = append(ps, param{name: decl.Content[i].Value, def: decl.Content[i+1]})
		}
	default:
		for _, d := range items(decl) {
			ps = append(ps, param{name: scalar(get(d, "name")), typ: strings.ToLower(scalar(get(d, "type"))), def: get(d, "default"), values: strs(get(d, "values"))})
		}
	}
	declared := map[string]bool{}
	for _, p := range ps {
		declared[p.name] = true
	}
	if given != nil {
		for i := 0; i+1 < len(given.Content); i += 2 {
			if !declared[given.Content[i].Value] {
				return nil, fmt.Errorf("parameter %s is not declared by the template", given.Content[i].Value)
			}
		}
	}
	out := map[string]any{}
	for _, p := range ps {
		v := get(given, p.name)
		if v == nil {
			v = p.def
		}
		if v == nil {
			return nil, fmt.Errorf("parameter %s is required", p.name)
		}
		val, err := paramValue(p.name, p.typ, v)
		if err != nil {
			return nil, err
		}
		if len(p.values) > 0 {
			str, _ := toString(val)
			if !slices.ContainsFunc(p.values, func(s string) bool { return strings.EqualFold(s, str) }) {
				return nil, fmt.Errorf("parameter %s: %s is not one of %s", p.name, str, strings.Join(p.values, ", "))
			}
		}
		out[p.name] = val
	}
	return out, nil
}

// paramValue converts a parameter to its declared type.
func paramValue(name, typ string, v *yaml.Node) (any, error) {
	switch typ {
	case "boolean":
		if v.Kind == yaml.ScalarNode {
			switch strings.ToLower(v.Value) {
			case "true":
				return true, nil
			case "false":
				return false, nil
			}
		}
		return nil, fmt.Errorf("parameter %s must be true or false", name)
	case "number":
		if n, ok := toNumber(v.Value); ok && v.Kind == yaml.ScalarNode {
			return n, nil
		}
		return nil, fmt.Errorf("parameter %s must be a number", name)
	case "", "string":
		if v.Kind != yaml.ScalarNode {
			if typ == "" {
				return v, nil
			}
			return nil, fmt.Errorf("parameter %s must be text", name)
		}
		return v.Value, nil
	}
	return nodeValue(v), nil
}
