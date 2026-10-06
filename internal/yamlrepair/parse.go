package yamlrepair

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// maxAliasExpansion bounds the number of nodes reached through aliases while
// converting one document. Values are copied, not shared, so a document
// built from nested aliases ("billion laughs") would otherwise expand
// exponentially. A review answer has no reason to use aliases at all.
const maxAliasExpansion = 10000

var (
	errMultipleDocuments = errors.New("yamlrepair: more than one document")
	errUnsupportedTag    = errors.New("yamlrepair: unsupported tag")
	errBadKey            = errors.New("yamlrepair: mapping key is not a scalar")
	errBadMerge          = errors.New("yamlrepair: merge value is not a mapping")
	errAliasExpansion    = errors.New("yamlrepair: too many alias expansions")
	errUnknownNode       = errors.New("yamlrepair: unknown node kind")
)

// safeTags are the tags PyYAML's safe_load constructs and this package
// supports. Any other explicit tag fails the parse, as it does upstream.
var safeTags = map[string]bool{
	"!!null": true, "!!bool": true, "!!int": true, "!!float": true,
	"!!str": true, "!!seq": true, "!!map": true, "!!binary": true,
	"!!timestamp": true, "!!merge": true,
}

// parse parses one YAML document (upstream's yaml.safe_load). An empty
// stream yields nil. The error never carries document content to a log:
// callers only test it for nil.
func parse(text string) (any, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		// A second document, or an error after the first one.
		return nil, errMultipleDocuments
	}
	c := converter{}
	return c.value(&doc)
}

// converter turns a yaml.Node tree into plain Go values. It exists instead
// of yaml.v3's own decoding into any because that decoding rejects
// duplicate mapping keys, where PyYAML keeps the last value.
type converter struct {
	aliasNodes int
}

func (c *converter) value(n *yaml.Node) (any, error) {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return c.value(n.Content[0])
	case yaml.AliasNode:
		return c.alias(n)
	case yaml.ScalarNode:
		return scalar(n)
	case yaml.SequenceNode:
		if !safeTags[n.ShortTag()] {
			return nil, errUnsupportedTag
		}
		out := make([]any, 0, len(n.Content))
		for _, item := range n.Content {
			v, err := c.value(item)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case yaml.MappingNode:
		return c.mapping(n)
	}
	return nil, errUnknownNode
}

func (c *converter) alias(n *yaml.Node) (any, error) {
	c.aliasNodes += countNodes(n.Alias, maxAliasExpansion+1)
	if c.aliasNodes > maxAliasExpansion {
		return nil, errAliasExpansion
	}
	return c.value(n.Alias)
}

// countNodes counts the nodes under n, following aliases, and stops once the
// count exceeds limit.
func countNodes(n *yaml.Node, limit int) int {
	if n == nil {
		return 0
	}
	count := 1
	if n.Kind == yaml.AliasNode {
		return count + countNodes(n.Alias, limit-count)
	}
	for _, child := range n.Content {
		if count > limit {
			break
		}
		count += countNodes(child, limit-count)
	}
	return count
}

// mapping converts a mapping. Explicit keys are applied in document order,
// so a duplicate key keeps its last value. Merge keys ("<<") contribute
// only keys the mapping does not set itself; with a list of merged
// mappings, an earlier one wins over a later one (as in PyYAML).
func (c *converter) mapping(n *yaml.Node) (any, error) {
	if !safeTags[n.ShortTag()] {
		return nil, errUnsupportedTag
	}
	out := make(map[string]any, len(n.Content)/2)
	var merges []map[string]any
	for i := 0; i+1 < len(n.Content); i += 2 {
		keyNode, valueNode := n.Content[i], n.Content[i+1]
		if keyNode.Kind == yaml.ScalarNode && keyNode.ShortTag() == "!!merge" {
			merged, err := c.mergeSources(valueNode)
			if err != nil {
				return nil, err
			}
			merges = append(merges, merged...)
			continue
		}
		key, err := c.key(keyNode)
		if err != nil {
			return nil, err
		}
		v, err := c.value(valueNode)
		if err != nil {
			return nil, err
		}
		out[key] = v
	}
	for _, m := range merges {
		for k, v := range m {
			if _, set := out[k]; !set {
				out[k] = v
			}
		}
	}
	return out, nil
}

// mergeSources returns the mappings a merge key's value names, in
// precedence order.
func (c *converter) mergeSources(n *yaml.Node) ([]map[string]any, error) {
	target := n
	if n.Kind == yaml.AliasNode {
		target = n.Alias
	}
	switch target.Kind {
	case yaml.MappingNode:
		v, err := c.value(n)
		if err != nil {
			return nil, err
		}
		m, _ := v.(map[string]any)
		return []map[string]any{m}, nil
	case yaml.SequenceNode:
		var out []map[string]any
		for _, item := range target.Content {
			itemTarget := item
			if item.Kind == yaml.AliasNode {
				itemTarget = item.Alias
			}
			if itemTarget.Kind != yaml.MappingNode {
				return nil, errBadMerge
			}
			v, err := c.value(item)
			if err != nil {
				return nil, err
			}
			m, _ := v.(map[string]any)
			out = append(out, m)
		}
		return out, nil
	}
	return nil, errBadMerge
}

// key converts a mapping key to a string the way JSON formats keys.
func (c *converter) key(n *yaml.Node) (string, error) {
	target := n
	if n.Kind == yaml.AliasNode {
		target = n.Alias
	}
	if target.Kind != yaml.ScalarNode {
		return "", errBadKey
	}
	v, err := scalar(target)
	if err != nil {
		return "", err
	}
	switch k := v.(type) {
	case string:
		return k, nil
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(k), nil
	case float64:
		return strconv.FormatFloat(k, 'g', -1, 64), nil
	default:
		return fmt.Sprint(k), nil
	}
}

// scalar resolves one scalar node with yaml.v3's YAML 1.2 rules, except
// that a timestamp stays the string it was written as.
func scalar(n *yaml.Node) (any, error) {
	tag := n.ShortTag()
	if !safeTags[tag] {
		return nil, errUnsupportedTag
	}
	if tag == "!!timestamp" {
		return n.Value, nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}
