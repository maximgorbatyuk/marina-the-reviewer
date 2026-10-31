package review

import (
	"context"
	"slices"
	"strings"
)

type wireNodeCatalogue struct {
	Prefixes []string
	Paths    []any
	Owners   []string
	Groups   []any
}

func makeWireNodeCatalogue(nodes []GraphNode) wireNodeCatalogue {
	value, _ := makeWireNodeCatalogueContext(context.Background(), nodes)
	return value
}
func makeWireNodeCatalogueContext(ctx context.Context, nodes []GraphNode) (wireNodeCatalogue, error) {
	if err := ctx.Err(); err != nil {
		return wireNodeCatalogue{}, err
	}
	poll := cpuContextPoll{context: ctx}

	paths, owners := []string{}, []string{}
	for _, n := range nodes {
		if err := poll.check(); err != nil {
			return wireNodeCatalogue{}, err
		}
		paths = append(paths, n.Path)
		if n.DeclaringType != nil {
			owners = append(owners, *n.DeclaringType)
		}
	}
	paths = unique(paths)
	owners = unique(owners)
	if err := sortOrdinalContext(ctx, paths); err != nil {
		return wireNodeCatalogue{}, err
	}
	if err := sortOrdinalContext(ctx, owners); err != nil {
		return wireNodeCatalogue{}, err
	}
	prefixes := pathPrefixes(paths)
	wirePaths := []any{}
	for _, p := range paths {
		if err := poll.check(); err != nil {
			return wireNodeCatalogue{}, err
		}
		for i, prefix := range prefixes {
			if err := poll.check(); err != nil {
				return wireNodeCatalogue{}, err
			}
			if strings.HasPrefix(p, prefix) {
				wirePaths = append(wirePaths, []any{i, p[len(prefix):]})
				break
			}
		}
	}
	byID := map[string]GraphNode{}
	for _, n := range nodes {
		if err := poll.check(); err != nil {
			return wireNodeCatalogue{}, err
		}
		byID[n.ID] = n
	}
	emitted := map[string]bool{}
	nodeGroups := []any{}
	groupIndex := map[string]int{}
	for _, n := range nodes {
		if err := poll.check(); err != nil {
			return wireNodeCatalogue{}, err
		}
		if emitted[n.ID] {
			continue
		}
		emitted[n.ID] = true
		var before, after *GraphNode
		node := n
		if n.Revision == Base {
			before = &node
		} else {
			after = &node
		}
		if n.CounterpartID != nil && !emitted[*n.CounterpartID] {
			other, ok := byID[*n.CounterpartID]
			if ok && other.CounterpartID != nil && *other.CounterpartID == n.ID {
				emitted[other.ID] = true
				if other.Revision == Base {
					before = &other
				} else {
					after = &other
				}
			}
		}
		value := func(n *GraphNode, kind string) any {
			if n == nil {
				return nil
			}
			switch kind {
			case "id":
				return n.ID
			case "path":
				return slices.Index(paths, n.Path)
			case "owner":
				if n.DeclaringType == nil {
					return nil
				}
				return slices.Index(owners, *n.DeclaringType)
			case "signature":
				return n.Signature
			default:
				return n.Change
			}
		}
		group := []any{shared(value(before, "path"), value(after, "path")), shared(value(before, "owner"), value(after, "owner")), n.Kind, shared(value(before, "change"), value(after, "change"))}
		key := canonicalJSON(group)
		row := []any{[]any{value(before, "id"), value(after, "id")}, shared(value(before, "signature"), value(after, "signature"))}
		if i, ok := groupIndex[key]; ok {
			prior := nodeGroups[i].([]any)
			prior[4] = append(prior[4].([]any), row)
		} else {
			groupIndex[key] = len(nodeGroups)
			nodeGroups = append(nodeGroups, append(group, []any{row}))
		}
	}
	if err := ctx.Err(); err != nil {
		return wireNodeCatalogue{}, err
	}
	return wireNodeCatalogue{prefixes, wirePaths, owners, nodeGroups}, nil
}
