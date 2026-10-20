package azure

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// errUnsupported marks what Plumbix cannot evaluate at import time; the
// construct holding it stays disabled with reason template.
var errUnsupported = errors.New("not supported")

// scope is what template expressions can see (WU-047): the parameters of
// the template being expanded, the root's inline variables, and loop
// variables of each.
type scope struct {
	params map[string]any
	vars   map[string]string
	locals map[string]any
}

// with returns a copy of s with one more loop variable.
func (s *scope) with(name string, v any) *scope {
	out := &scope{params: s.params, vars: s.vars, locals: map[string]any{}}
	for k, l := range s.locals {
		out.locals[k] = l
	}
	out.locals[name] = v
	return out
}

// pair is one entry of a mapping an each loop walks: x.key and x.value.
type pair struct {
	key   string
	value any
}

// nodeValue turns a YAML node into an expression value: a string, number,
// boolean or null for scalars; the node itself for mappings and lists.
func nodeValue(n *yaml.Node) any {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.AliasNode {
		return nodeValue(n.Alias)
	}
	if n.Kind != yaml.ScalarNode {
		return n
	}
	switch n.ShortTag() {
	case "!!bool":
		return strings.EqualFold(n.Value, "true")
	case "!!int", "!!float":
		if f, err := strconv.ParseFloat(n.Value, 64); err == nil {
			return f
		}
	case "!!null":
		return nil
	}
	return n.Value
}

// valueNode turns an expression value back into a YAML node.
func valueNode(v any) (*yaml.Node, error) {
	if n, ok := v.(*yaml.Node); ok {
		return n, nil
	}
	if p, ok := v.(pair); ok {
		return valueNode(p.value)
	}
	if v == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: ""}, nil
	}
	s, err := toString(v)
	if err != nil {
		return nil, err
	}
	// Booleans become True and False here too, as in an embedded
	// expression and in Azure; a field read as a boolean ignores case.
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!str"}
	if _, ok := v.(float64); ok {
		n.Tag = "!!float"
	}
	return n, nil
}

// toString renders a value as Azure does: booleans as True and False.
func toString(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case pair:
		return toString(x.value)
	}
	return "", errors.New("an object or list cannot be used as text")
}

// truthy is Azure's conversion to boolean.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case float64:
		return x, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// compare orders a and b, converting b to a's type as Azure does; ok is
// false when they cannot be compared.
func compare(a, b any) (int, bool) {
	switch x := a.(type) {
	case nil:
		if b == nil {
			return 0, true
		}
		return -1, true
	case bool:
		y := truthy(b)
		switch {
		case x == y:
			return 0, true
		case !x:
			return -1, true
		}
		return 1, true
	case float64:
		y, ok := toNumber(b)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case string:
		y, err := toString(b)
		if err != nil {
			return 0, false
		}
		return strings.Compare(strings.ToLower(x), strings.ToLower(y)), true
	}
	return 0, false
}

func equal(a, b any) bool {
	c, ok := compare(a, b)
	return ok && c == 0
}

// evalExpr evaluates the inside of a ${{ }} expression.
func evalExpr(src string, s *scope) (any, error) {
	toks, err := tokenize(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, s: s}
	v, err := p.expr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		return nil, fmt.Errorf("unexpected %q in %q", p.toks[p.pos].text, src)
	}
	return v, nil
}

type token struct {
	kind byte // i ident, s string, n number, or the punctuation itself
	text string
}

func tokenize(src string) ([]token, error) {
	var out []token
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case strings.IndexByte("(),.[]", c) >= 0:
			out = append(out, token{kind: c, text: string(c)})
			i++
		case c == '\'':
			var b strings.Builder
			j := i + 1
			for ; j < len(src); j++ {
				if src[j] == '\'' {
					if j+1 < len(src) && src[j+1] == '\'' {
						b.WriteByte('\'')
						j++
						continue
					}
					break
				}
				b.WriteByte(src[j])
			}
			if j >= len(src) {
				return nil, fmt.Errorf("unterminated string in %q", src)
			}
			out = append(out, token{kind: 's', text: b.String()})
			i = j + 1
		case c >= '0' && c <= '9' || c == '-' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9':
			j := i + 1
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
				j++
			}
			out = append(out, token{kind: 'n', text: src[i:j]})
			i = j
		case isIdent(c):
			j := i + 1
			for j < len(src) && (isIdent(src[j]) || src[j] >= '0' && src[j] <= '9' || src[j] == '-') {
				j++
			}
			out = append(out, token{kind: 'i', text: src[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("unexpected %q in %q", c, src)
		}
	}
	return out, nil
}

func isIdent(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

type parser struct {
	toks []token
	pos  int
	s    *scope
}

func (p *parser) peek() byte {
	if p.pos < len(p.toks) {
		return p.toks[p.pos].kind
	}
	return 0
}

func (p *parser) want(kind byte) error {
	if p.peek() != kind {
		return fmt.Errorf("expected %q", kind)
	}
	p.pos++
	return nil
}

func (p *parser) expr() (any, error) {
	if p.pos >= len(p.toks) {
		return nil, errors.New("expression ends early")
	}
	t := p.toks[p.pos]
	p.pos++
	switch t.kind {
	case 's':
		return t.text, nil
	case 'n':
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %s", t.text)
		}
		return f, nil
	case 'i':
	default:
		return nil, fmt.Errorf("unexpected %q", t.text)
	}
	switch strings.ToLower(t.text) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if p.peek() == '(' {
		p.pos++
		var args []any
		for p.peek() != ')' {
			v, err := p.expr()
			if err != nil {
				return nil, err
			}
			args = append(args, v)
			if p.peek() != ',' {
				break
			}
			p.pos++
		}
		if err := p.want(')'); err != nil {
			return nil, err
		}
		return call(t.text, args)
	}
	return p.ref(t.text)
}

// path reads the .name and ['name'] parts after a reference.
func (p *parser) path() ([]any, error) {
	var out []any
	for {
		switch p.peek() {
		case '.':
			p.pos++
			if p.peek() != 'i' && p.peek() != 'n' {
				return nil, errors.New("expected a name after .")
			}
			out = append(out, p.toks[p.pos].text)
			p.pos++
		case '[':
			p.pos++
			v, err := p.expr()
			if err != nil {
				return nil, err
			}
			if err := p.want(']'); err != nil {
				return nil, err
			}
			out = append(out, v)
		default:
			return out, nil
		}
	}
}

func (p *parser) ref(name string) (any, error) {
	parts, err := p.path()
	if err != nil {
		return nil, err
	}
	var v any
	switch {
	case p.s.locals != nil && hasLocal(p.s.locals, name):
		v = p.s.locals[name]
	case name == "parameters":
		if len(parts) == 0 {
			return nil, errors.New("parameters needs a name")
		}
		key, _ := toString(parts[0])
		pv, ok := p.s.params[key]
		if !ok {
			return nil, fmt.Errorf("parameter %s is not declared", key)
		}
		v, parts = pv, parts[1:]
	case name == "variables":
		var names []string
		for _, part := range parts {
			s, _ := toString(part)
			names = append(names, s)
		}
		key := strings.Join(names, ".")
		if strings.Contains(key, ".") {
			return nil, fmt.Errorf("variable %s is only known when the pipeline runs: %w", key, errUnsupported)
		}
		return p.s.vars[key], nil
	default:
		return nil, fmt.Errorf("%s is %w at import time", name, errUnsupported)
	}
	for _, part := range parts {
		if v, err = property(v, part); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func hasLocal(m map[string]any, k string) bool { _, ok := m[k]; return ok }

// property reads one property or index of a value.
func property(v, key any) (any, error) {
	name, _ := toString(key)
	switch x := v.(type) {
	case pair:
		switch strings.ToLower(name) {
		case "key":
			return x.key, nil
		case "value":
			return x.value, nil
		}
		return property(x.value, key)
	case *yaml.Node:
		switch x.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(x.Content); i += 2 {
				if strings.EqualFold(x.Content[i].Value, name) {
					return nodeValue(x.Content[i+1]), nil
				}
			}
			return nil, nil
		case yaml.SequenceNode:
			if i, ok := key.(float64); ok && i >= 0 && int(i) < len(x.Content) {
				return nodeValue(x.Content[int(i)]), nil
			}
		}
	}
	return nil, fmt.Errorf("cannot read property %s here", name)
}

// elements lists what a value holds: list items, or mapping entries as pairs.
func elements(v any) ([]any, bool) {
	n, ok := v.(*yaml.Node)
	if !ok {
		if p, isPair := v.(pair); isPair {
			return elements(p.value)
		}
		return nil, false
	}
	var out []any
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			out = append(out, nodeValue(c))
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, pair{key: n.Content[i].Value, value: nodeValue(n.Content[i+1])})
		}
	default:
		return nil, false
	}
	return out, true
}

func strArgs(args []any) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		s, err := toString(a)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func arity(name string, args []any, min, max int) error {
	switch {
	case max < 0 && len(args) < min:
		return fmt.Errorf("%s takes at least %d arguments, got %d", name, min, len(args))
	case max >= 0 && (len(args) < min || len(args) > max):
		return fmt.Errorf("%s takes %d to %d arguments, got %d", name, min, max, len(args))
	}
	return nil
}

// call runs one function; names ignore case.
func call(name string, args []any) (any, error) {
	fn := strings.ToLower(name)
	switch fn {
	case "and", "or":
		if err := arity(name, args, 2, -1); err != nil {
			return nil, err
		}
		for _, a := range args {
			if truthy(a) != (fn == "and") {
				return fn == "or", nil
			}
		}
		return fn == "and", nil
	case "not":
		if err := arity(name, args, 1, 1); err != nil {
			return nil, err
		}
		return !truthy(args[0]), nil
	case "xor":
		if err := arity(name, args, 2, 2); err != nil {
			return nil, err
		}
		return truthy(args[0]) != truthy(args[1]), nil
	case "eq", "ne":
		if err := arity(name, args, 2, 2); err != nil {
			return nil, err
		}
		return equal(args[0], args[1]) == (fn == "eq"), nil
	case "gt", "ge", "lt", "le":
		if err := arity(name, args, 2, 2); err != nil {
			return nil, err
		}
		c, ok := compare(args[0], args[1])
		if !ok {
			return nil, fmt.Errorf("%s cannot compare these values", name)
		}
		return map[string]bool{"gt": c > 0, "ge": c >= 0, "lt": c < 0, "le": c <= 0}[fn], nil
	case "in", "notin":
		if err := arity(name, args, 2, -1); err != nil {
			return nil, err
		}
		found := false
		for _, a := range args[1:] {
			found = found || equal(args[0], a)
		}
		return found == (fn == "in"), nil
	case "containsvalue":
		if err := arity(name, args, 2, 2); err != nil {
			return nil, err
		}
		els, ok := elements(args[0])
		if !ok {
			return nil, errors.New("containsValue needs a list or an object")
		}
		for _, e := range els {
			if p, isPair := e.(pair); isPair {
				e = p.value
			}
			if equal(e, args[1]) {
				return true, nil
			}
		}
		return false, nil
	case "contains", "startswith", "endswith":
		if err := arity(name, args, 2, 2); err != nil {
			return nil, err
		}
		s, err := strArgs(args)
		if err != nil {
			return nil, err
		}
		a, b := strings.ToLower(s[0]), strings.ToLower(s[1])
		return map[string]func(string, string) bool{"contains": strings.Contains, "startswith": strings.HasPrefix, "endswith": strings.HasSuffix}[fn](a, b), nil
	case "coalesce":
		for _, a := range args {
			if s, isStr := a.(string); a != nil && (!isStr || s != "") {
				return a, nil
			}
		}
		return nil, nil
	case "format":
		if err := arity(name, args, 1, -1); err != nil {
			return nil, err
		}
		s, err := strArgs(args)
		if err != nil {
			return nil, err
		}
		out := s[0]
		for i, a := range s[1:] {
			out = strings.ReplaceAll(out, "{"+strconv.Itoa(i)+"}", a)
		}
		return out, nil
	case "join":
		if err := arity(name, args, 2, 2); err != nil {
			return nil, err
		}
		sep, err := toString(args[0])
		if err != nil {
			return nil, err
		}
		els, ok := elements(args[1])
		if !ok {
			return toString(args[1])
		}
		parts, err := strArgs(els)
		if err != nil {
			return nil, err
		}
		return strings.Join(parts, sep), nil
	case "lower", "upper":
		if err := arity(name, args, 1, 1); err != nil {
			return nil, err
		}
		s, err := toString(args[0])
		if err != nil {
			return nil, err
		}
		if fn == "lower" {
			return strings.ToLower(s), nil
		}
		return strings.ToUpper(s), nil
	case "replace":
		if err := arity(name, args, 3, 3); err != nil {
			return nil, err
		}
		s, err := strArgs(args)
		if err != nil {
			return nil, err
		}
		return strings.ReplaceAll(s[0], s[1], s[2]), nil
	case "length":
		if err := arity(name, args, 1, 1); err != nil {
			return nil, err
		}
		if els, ok := elements(args[0]); ok {
			return float64(len(els)), nil
		}
		s, err := toString(args[0])
		if err != nil {
			return nil, err
		}
		return float64(len(s)), nil
	}
	return nil, fmt.Errorf("function %s is %w", name, errUnsupported)
}
