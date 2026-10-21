package agentconfig

import (
	"bytes"
	"errors"
	"fmt"
	"slices"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func applyCodex(path string, generated []byte) (ApplyResult, error) {
	result := ApplyResult{Path: path}
	existing, _, err := readOptional(path)
	if err != nil {
		return result, err
	}
	data, err := mergeCodexConfiguration(existing, generated)
	if err != nil {
		return result, fmt.Errorf("update %s: %w", path, err)
	}
	return result, writeAtomic(path, data)
}

func mergeCodexConfiguration(existing, generated []byte) ([]byte, error) {
	// Full decoding validates duplicate keys and table semantics as well as
	// syntax. The AST below is used only to preserve unrelated text and comments.
	var original, next map[string]any
	if err := toml.Unmarshal(existing, &original); err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(generated, &next); err != nil {
		return nil, err
	}
	providerID, _ := next["model_provider"].(string)
	providers, _ := next["model_providers"].(map[string]any)
	if providerID == "" || providers[providerID] == nil {
		return nil, errors.New("generated configuration has no model provider")
	}
	if value, ok := original["model_providers"]; ok {
		if _, ok := value.(map[string]any); !ok {
			return nil, errors.New("existing model_providers must be a TOML table")
		}
	}
	root := map[string]any{}
	for _, key := range []string{"model_provider", "model", "model_catalog_json"} {
		if value, ok := next[key]; ok {
			root[key] = value
		}
	}
	managed := func(path []string) bool {
		return len(path) > 0 && (path[0] == "model_provider" || path[0] == "model" || path[0] == "model_catalog_json" ||
			(len(path) > 1 && path[0] == "model_providers" && path[1] == providerID))
	}
	var parser unstable.Parser
	parser.Reset(existing)
	var table []string
	var kept bytes.Buffer
	var inlineOthers map[string]any
	end := 0
	for parser.NextExpression() {
		node := parser.Expression()
		keys := []string{}
		first, last := -1, 0
		for it := node.Key(); it.Next(); {
			key := it.Node()
			keys = append(keys, string(key.Data))
			if first < 0 {
				first = int(key.Raw.Offset)
			}
			last = int(key.Raw.Offset + key.Raw.Length)
		}
		path := keys
		start, stop := int(node.Raw.Offset), int(node.Raw.Offset+node.Raw.Length)
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = keys
			// Table nodes have no Raw range; the key ranges exclude brackets.
			start = first
			stop = last + bytes.IndexByte(existing[last:], ']') + 1
			if node.Kind == unstable.ArrayTable {
				stop++
			}
		case unstable.KeyValue:
			path = append(slices.Clone(table), keys...)
		default:
			continue
		}
		remove := managed(path)
		if node.Kind == unstable.KeyValue && slices.Equal(path, []string{"model_providers"}) {
			// A sealed inline table cannot later be extended by a table header.
			// Re-emit this one table, preserving all its other providers.
			inlineOthers = map[string]any{}
			for key, value := range original["model_providers"].(map[string]any) {
				if key != providerID {
					inlineOthers[key] = value
				}
			}
			remove = true
		}
		if remove {
			start = bytes.LastIndexByte(existing[:start], '\n') + 1
			if newline := bytes.IndexByte(existing[stop:], '\n'); newline >= 0 {
				stop += newline + 1
			} else {
				stop = len(existing)
			}
			kept.Write(existing[end:start])
			end = stop
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	kept.Write(existing[end:])
	head, err := toml.Marshal(root)
	if err != nil {
		return nil, err
	}
	tail, err := toml.Marshal(map[string]any{"model_providers": providers})
	if err != nil {
		return nil, err
	}
	// An existing [model_providers] parent may already be explicit. The
	// managed child can be defined without redefining its empty parent.
	tail = bytes.TrimPrefix(tail, []byte("[model_providers]\n"))
	// Root assignments must precede every table. The provider is a separate
	// final table, so intervening profiles and MCP tables keep their scope.
	data := append(head, '\n')
	data = append(data, bytes.TrimSpace(kept.Bytes())...)
	data = append(data, '\n')
	if len(inlineOthers) > 0 {
		others, err := toml.Marshal(map[string]any{"model_providers": inlineOthers})
		if err != nil {
			return nil, err
		}
		data = append(data, others...)
	}
	data = append(data, tail...)
	var check map[string]any
	if err := toml.Unmarshal(data, &check); err != nil {
		return nil, fmt.Errorf("validate merged TOML: %w", err)
	}
	return data, nil
}
