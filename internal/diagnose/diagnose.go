// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type checker struct {
	report Report
	refs   map[*yaml.Node]int
	paths  map[*yaml.Node]string
	seen   map[Finding]bool
}

func Diagnose(path string, in io.Reader) (Report, error) {
	b, err := readInput(path, in)
	if err != nil {
		return Report{}, err
	}
	root, refs, err := parse(b)
	c := checker{report: newReport(), refs: refs, seen: make(map[Finding]bool)}
	if err != nil {
		c.add("unsupported_yaml", "unsupported", nil)
		return c.finish(), nil
	}
	c.report.ConfigurationParsed = true
	c.paths = schemaPaths(root)
	c.scan(root)
	c.configuration(root)
	return c.finish(), nil
}

func (c *checker) finish() Report {
	slices.SortFunc(c.report.Findings, func(a, b Finding) int {
		if a.Reference != b.Reference {
			return a.Reference - b.Reference
		}
		return strings.Compare(a.ID, b.ID)
	})
	if c.report.Status == "indeterminate" {
		c.report.NextSteps = []string{"Stop automatic rollout; review unsupported findings and validate with the intended collector. Known blocking findings still require correction."}
	} else if c.report.Status == "unsafe" {
		c.report.NextSteps = []string{"Do not deploy these mappings; remove blocking label or accounting risks, then diagnose again."}
	}
	return c.report
}

func (c *checker) add(id, severity string, n *yaml.Node) {
	if severity == "unsupported" {
		c.report.Status = "indeterminate"
	} else if c.report.Status == "supported_safe" {
		c.report.Status = "unsafe"
	}
	f := Finding{ID: id, Severity: severity, Reference: c.refs[n]}
	if n != nil {
		f.Path, f.Line, f.Column = c.paths[n], n.Line, n.Column
		if knownAttribute(n.Value) {
			f.Attribute = n.Value
		}
	}
	if c.seen[f] {
		return
	}
	if len(c.report.Findings) >= MaxFindings {
		c.report.Status = "indeterminate"
		if len(c.report.Findings) == MaxFindings {
			c.report.Findings = append(c.report.Findings, Finding{ID: "findings_truncated", Severity: "unsupported"})
		}
		return
	}
	c.seen[f] = true
	c.report.Findings = append(c.report.Findings, f)
}

func (c *checker) scan(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "$") {
		c.add("unresolved_interpolation", "unsupported", n)
	}
	if !slices.Contains([]string{"!!map", "!!seq", "!!str", "!!int", "!!float", "!!bool", "!!null"}, n.Tag) {
		c.add("unsupported_mapping", "unsupported", n)
	}
	for _, child := range n.Content {
		c.scan(child)
	}
}

func (c *checker) object(n *yaml.Node, allowed ...string) map[string]*yaml.Node {
	m := make(map[string]*yaml.Node)
	if n == nil {
		return m
	}
	if n.Kind != yaml.MappingNode {
		c.add("unsupported_mapping", "unsupported", n)
		return m
	}
	for i := 0; i < len(n.Content); i += 2 {
		key, value := n.Content[i], n.Content[i+1]
		m[key.Value] = value
		if allowed != nil && !slices.Contains(allowed, key.Value) {
			c.add("unsupported_field", "unsupported", key)
		}
	}
	return m
}

func (c *checker) list(n *yaml.Node, required bool) []*yaml.Node {
	if n == nil && !required {
		return nil
	}
	if n == nil || n.Kind != yaml.SequenceNode {
		c.add("unsupported_mapping", "unsupported", n)
		return nil
	}
	if required && len(n.Content) == 0 {
		c.add("unsupported_mapping", "unsupported", n)
	}
	return n.Content
}

func (c *checker) string(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" || strings.TrimSpace(n.Value) == "" {
		c.add("unsupported_mapping", "unsupported", n)
		return ""
	}
	return n.Value
}

func (c *checker) enum(n *yaml.Node, values ...string) {
	if n != nil && !slices.Contains(values, c.string(n)) {
		c.add("unsupported_mapping", "unsupported", n)
	}
}

func (c *checker) integer(n *yaml.Node, lo, hi int64) {
	if n == nil {
		return
	}
	v, err := strconv.ParseInt(n.Value, 10, 64)
	if n.Tag != "!!int" || err != nil || v < lo || v > hi {
		c.add("unsupported_mapping", "unsupported", n)
	}
}

func (c *checker) duration(n *yaml.Node, lo, hi time.Duration) {
	if n == nil {
		return
	}
	v, err := time.ParseDuration(c.string(n))
	if err != nil || v < lo || v > hi {
		c.add("unsupported_mapping", "unsupported", n)
	}
}

func (c *checker) boolean(n *yaml.Node) {
	if n != nil && (n.Tag != "!!bool" || (n.Value != "true" && n.Value != "false")) {
		c.add("unsupported_mapping", "unsupported", n)
	}
}

func ordered(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	return n.Content
}

func componentType(name string) string {
	kind, _, _ := strings.Cut(name, "/")
	return kind
}
