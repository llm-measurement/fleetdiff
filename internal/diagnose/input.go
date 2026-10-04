// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/localfile"
	"go.yaml.in/yaml/v3"
)

const MaxInputBytes = 256 << 10
const MaxNodes = 8192
const MaxDepth = 32
const MaxFindings = 128

func readInput(path string, in io.Reader) ([]byte, error) {
	if path == "-" {
		if in == nil {
			return nil, errors.New("configuration input is unavailable")
		}
		b, err := io.ReadAll(io.LimitReader(in, MaxInputBytes+1))
		if err != nil {
			return nil, errors.New("cannot read configuration input")
		}
		if len(b) > MaxInputBytes {
			return nil, errors.New("configuration input exceeds byte limit")
		}
		return b, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("cannot inspect configuration input")
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, errors.New("cannot open configuration directory")
	}
	defer func() { root.Close() }()
	parts := strings.Split(strings.TrimPrefix(abs, string(filepath.Separator)), string(filepath.Separator))
	// Check and pin every directory, not just the final file. Do not allow an
	// intermediate symlink to redirect the confined localfile reader.
	for _, part := range parts[:len(parts)-1] {
		info, err := root.Lstat(part)
		if err != nil || !info.IsDir() {
			return nil, errors.New("configuration directory must not contain symlinks")
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			return nil, errors.New("cannot open configuration directory")
		}
		current, err := next.Stat(".")
		if err != nil || !os.SameFile(info, current) {
			next.Close()
			return nil, errors.New("configuration directory changed while opening")
		}
		root.Close()
		root = next
	}
	return localfile.Read(root, parts[len(parts)-1], MaxInputBytes)
}

func parse(data []byte) (*yaml.Node, map[*yaml.Node]int, error) {
	invalid := errors.New("unsupported configuration syntax or limits")
	// Decode only into the syntax tree, never into values: aliases are not
	// expanded. The byte cap limits input, not parser memory allocation; v3 also
	// limits nesting to 10000. Our smaller node/depth limits apply after parsing.
	loader := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := loader.Decode(&doc); err != nil {
		return nil, nil, invalid
	}
	if err := loader.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, invalid
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, invalid
	}
	refs := make(map[*yaml.Node]int)
	var walk func(*yaml.Node, int) bool
	walk = func(n *yaml.Node, depth int) bool {
		if depth > MaxDepth || len(refs) >= MaxNodes || n.Kind == yaml.AliasNode || n.Anchor != "" {
			return false
		}
		refs[n] = len(refs) + 1
		if n.Kind == yaml.MappingNode {
			seen := make(map[string]bool)
			for i := 0; i < len(n.Content); i += 2 {
				key := n.Content[i]
				if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || key.Value == "<<" || seen[key.Value] {
					return false
				}
				seen[key.Value] = true
			}
		}
		for _, child := range n.Content {
			if !walk(child, depth+1) {
				return false
			}
		}
		return true
	}
	if !walk(doc.Content[0], 1) {
		return nil, nil, invalid
	}
	return doc.Content[0], refs, nil
}
