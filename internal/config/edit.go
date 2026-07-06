package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Editor performs surgical, comment-preserving edits on a YAML config file.
//
// It loads the file into a yaml.Node tree, mutates only the targeted keys, and
// writes the tree back. Hand comments, key order, and any keys norn doesn't
// model all survive — unlike marshalling a Config struct, which would drop
// them. Missing files start as an empty mapping so `Set*` can populate them.
type Editor struct {
	path string
	doc  *yaml.Node
}

// OpenEditor loads path for editing. A non-existent file yields an empty
// document ready to be written to.
func OpenEditor(path string) (*Editor, error) {
	e := &Editor{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			e.doc = emptyDoc()
			return e, nil
		}
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		e.doc = emptyDoc()
		return e, nil
	}
	e.doc = &doc
	return e, nil
}

func emptyDoc() *yaml.Node {
	return &yaml.Node{
		Kind:    yaml.DocumentNode,
		Content: []*yaml.Node{{Kind: yaml.MappingNode}},
	}
}

// root is the top-level mapping node.
func (e *Editor) root() *yaml.Node { return e.doc.Content[0] }

// findKey returns the value node for key within a mapping node, or nil.
func findKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ensureMapping navigates (creating as needed) the mapping at the given key
// path and returns it. An existing non-mapping value at a path segment is
// replaced with a mapping.
func (e *Editor) ensureMapping(keys []string) *yaml.Node {
	node := e.root()
	for _, k := range keys {
		val := findKey(node, k)
		if val == nil {
			keyN := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}
			val = &yaml.Node{Kind: yaml.MappingNode}
			node.Content = append(node.Content, keyN, val)
		}
		if val.Kind != yaml.MappingNode {
			val.Kind = yaml.MappingNode
			val.Content = nil
			val.Tag = ""
			val.Value = ""
		}
		node = val
	}
	return node
}

// setValueNode places (or replaces) the value node for the last key in keys
// under its parent mapping, returning the value node to be filled in.
func (e *Editor) setValueNode(keys []string) *yaml.Node {
	parent := e.ensureMapping(keys[:len(keys)-1])
	last := keys[len(keys)-1]
	if val := findKey(parent, last); val != nil {
		return val
	}
	keyN := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: last}
	val := &yaml.Node{Kind: yaml.ScalarNode}
	parent.Content = append(parent.Content, keyN, val)
	return val
}

// SetString sets the scalar at the key path to a string value.
func (e *Editor) SetString(keys []string, value string) {
	if len(keys) == 0 {
		return
	}
	val := e.setValueNode(keys)
	val.Kind = yaml.ScalarNode
	val.Tag = "!!str"
	val.Value = value
	val.Content = nil
	// Quote strings that would otherwise parse as another type.
	if _, err := strconv.ParseBool(value); err == nil {
		val.Style = yaml.DoubleQuotedStyle
	} else if _, err := strconv.ParseFloat(value, 64); err == nil {
		val.Style = yaml.DoubleQuotedStyle
	} else {
		val.Style = 0
	}
}

// SetBool sets the scalar at the key path to a boolean value.
func (e *Editor) SetBool(keys []string, value bool) {
	if len(keys) == 0 {
		return
	}
	val := e.setValueNode(keys)
	val.Kind = yaml.ScalarNode
	val.Tag = "!!bool"
	val.Style = 0
	val.Value = strconv.FormatBool(value)
	val.Content = nil
}

// SetStringSeq sets the key path to a sequence of string scalars.
func (e *Editor) SetStringSeq(keys []string, values []string) {
	if len(keys) == 0 {
		return
	}
	val := e.setValueNode(keys)
	val.Kind = yaml.SequenceNode
	val.Tag = "!!seq"
	val.Style = 0
	val.Value = ""
	val.Content = val.Content[:0]
	for _, v := range values {
		val.Content = append(val.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
}

// GetString returns the scalar string at the key path and whether it was found.
func (e *Editor) GetString(keys []string) (string, bool) {
	node := e.root()
	for i, k := range keys {
		val := findKey(node, k)
		if val == nil {
			return "", false
		}
		if i == len(keys)-1 {
			if val.Kind == yaml.ScalarNode {
				return val.Value, true
			}
			return "", false
		}
		if val.Kind != yaml.MappingNode {
			return "", false
		}
		node = val
	}
	return "", false
}

// Delete removes the key path if present.
func (e *Editor) Delete(keys []string) {
	if len(keys) == 0 {
		return
	}
	node := e.root()
	for _, k := range keys[:len(keys)-1] {
		val := findKey(node, k)
		if val == nil || val.Kind != yaml.MappingNode {
			return
		}
		node = val
	}
	last := keys[len(keys)-1]
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == last {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

// OpenEditorValue is a one-shot read of a scalar at the key path in path.
// Returns ("", false) if the file or key is absent.
func OpenEditorValue(path string, keys []string) (string, bool) {
	e, err := OpenEditor(path)
	if err != nil {
		return "", false
	}
	return e.GetString(keys)
}

// Save writes the edited tree back to disk atomically (tmp+rename), matching
// the 2-space indent norn's configs use.
func (e *Editor) Save() error {
	if err := os.MkdirAll(filepath.Dir(e.path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(e.doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}
