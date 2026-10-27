package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"go.yaml.in/yaml/v3"
)

// Fingerprint is a SHA-256 of a mapping's parsed content without the skipped
// keys (WU-048). It is taken over canonical JSON, so comments, quoting
// styles and key order do not change it.
func Fingerprint(n *yaml.Node, skip ...string) (string, error) {
	if n == nil || n.Kind != yaml.MappingNode {
		return "", errors.New("not a mapping")
	}
	var v map[string]any
	if err := n.Decode(&v); err != nil {
		return "", err
	}
	for _, k := range skip {
		delete(v, k)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// pipelineSkip are the keys a pipeline's own fingerprint leaves out: the
// steps carry their own, stages follow the steps, and source holds it.
var pipelineSkip = []string{"steps", "stages", "source"}

// stamp marks what an import writes as the import's (WU-048): each step's
// source gets written, the hash of the step without its source, and the
// pipeline gets a top-level source naming the system and file, with the
// hash of its own fields.
func stamp(system, file string, content []byte) ([]byte, error) {
	doc, root, err := parseDoc(content)
	if err != nil {
		return nil, err
	}
	for _, st := range items(get(root, "steps")) {
		src := get(st, "source")
		if src == nil || src.Kind != yaml.MappingNode {
			continue
		}
		h, err := Fingerprint(st, "source")
		if err != nil {
			return nil, err
		}
		set(src, "written", str(h))
	}
	h, err := Fingerprint(root, pipelineSkip...)
	if err != nil {
		return nil, err
	}
	setBefore(root, "source", &yaml.Node{Kind: yaml.MappingNode, Style: yaml.FlowStyle, Content: []*yaml.Node{
		str("system"), str(system), str("file"), str(file), str("written"), str(h),
	}}, "steps")
	return encode(doc)
}

// stepWritten reports whether an imported step is as the import wrote it.
func stepWritten(st *yaml.Node) bool {
	w := scalar(get(get(st, "source"), "written"))
	h, err := Fingerprint(st, "source")
	return w != "" && err == nil && h == w
}

// topWritten reports whether a pipeline's own fields are as written.
func topWritten(root *yaml.Node) bool {
	w := scalar(get(get(root, "source"), "written"))
	h, err := Fingerprint(root, pipelineSkip...)
	return w != "" && err == nil && h == w
}

func parseDoc(content []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errors.New("not a mapping")
	}
	return &doc, doc.Content[0], nil
}

func encode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

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

func str(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }

// set replaces key's value in a mapping, or appends the key.
func set(n *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = val
			return
		}
	}
	n.Content = append(n.Content, str(key), val)
}

// setBefore replaces key's value, or inserts the key before another one
// (appending when that one is missing).
func setBefore(n *yaml.Node, key string, val *yaml.Node, before string) {
	if get(n, key) != nil {
		set(n, key, val)
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == before {
			n.Content = append(n.Content[:i], append([]*yaml.Node{str(key), val}, n.Content[i:]...)...)
			return
		}
	}
	n.Content = append(n.Content, str(key), val)
}
