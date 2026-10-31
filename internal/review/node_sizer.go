package review

import (
	"context"
)

type nodeWireUnit struct {
	anchor, group, rowWeight int
}
type nodeWireGroup struct {
	baseWeight    int
	paths, owners []int
}
type nodeSizer struct {
	units                                 []nodeWireUnit
	groups                                []nodeWireGroup
	paths, owners                         []string
	pathSeen, ownerSeen, groupSeen        []uint64
	pathRank, ownerRank, rows, rowWeights []int
	activeGroups                          []int
	nodePaths, nodeOwners                 []int
	generation                            uint64
}

func sharedNodeIndexes(before, after int) []int {
	if before < 0 {
		if after < 0 {
			return nil
		}
		return []int{after}
	}
	if after < 0 || before == after {
		return []int{before}
	}
	return []int{before, after}
}
func nodeIndexValue(indexes []int, zero bool) any {
	if len(indexes) == 0 {
		return nil
	}
	value := func(index int) int {
		if zero {
			return 0
		}
		return index
	}
	if len(indexes) == 1 {
		return value(indexes[0])
	}
	return []int{value(indexes[0]), value(indexes[1])}
}

// Projection membership contains both nodes in each mutual counterpart pair.
// Store their wire row once and count selected rows without graph copies.
func newNodeSizer(full Graph) *nodeSizer {
	value, _ := newNodeSizerContext(context.Background(), full)
	return value
}
func newNodeSizerContext(ctx context.Context, full Graph) (*nodeSizer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	poll := cpuContextPoll{context: ctx}

	s := &nodeSizer{paths: []string{}, owners: []string{}}
	all := map[string]int{}
	for i, n := range full.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		all[n.ID] = i
		s.paths = append(s.paths, n.Path)
		if n.DeclaringType != nil {
			s.owners = append(s.owners, *n.DeclaringType)
		}
	}
	s.paths = unique(s.paths)
	s.owners = unique(s.owners)
	if err := sortOrdinalContext(ctx, s.paths); err != nil {
		return nil, err
	}
	if err := sortOrdinalContext(ctx, s.owners); err != nil {
		return nil, err
	}
	pathIDs, ownerIDs := map[string]int{}, map[string]int{}
	for i, path := range s.paths {
		if err := poll.check(); err != nil {
			return nil, err
		}
		pathIDs[path] = i
	}
	for i, owner := range s.owners {
		if err := poll.check(); err != nil {
			return nil, err
		}
		ownerIDs[owner] = i
	}
	for _, node := range full.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		s.nodePaths = append(s.nodePaths, pathIDs[node.Path])
		owner := -1
		if node.DeclaringType != nil {
			owner = ownerIDs[*node.DeclaringType]
		}
		s.nodeOwners = append(s.nodeOwners, owner)
	}
	emitted := make([]bool, len(full.Nodes))
	groups := map[string]int{}
	for i, n := range full.Nodes {
		if err := poll.check(); err != nil {
			return nil, err
		}
		if emitted[i] {
			continue
		}
		emitted[i] = true
		var before, after *GraphNode
		if n.Revision == Base {
			before = &full.Nodes[i]
		} else {
			after = &full.Nodes[i]
		}
		if n.CounterpartID != nil {
			if j, ok := all[*n.CounterpartID]; ok && !emitted[j] {
				other := &full.Nodes[j]
				if other.CounterpartID != nil && *other.CounterpartID == n.ID {
					emitted[j] = true
					if other.Revision == Base {
						before = other
					} else {
						after = other
					}
				}
			}
		}
		index := func(node *GraphNode, owner bool) int {
			if node == nil {
				return -1
			}
			if owner {
				if node.DeclaringType == nil {
					return -1
				}
				return ownerIDs[*node.DeclaringType]
			}
			return pathIDs[node.Path]
		}
		change := func(node *GraphNode) int {
			if node == nil {
				return -1
			}
			return node.Change
		}
		paths := sharedNodeIndexes(index(before, false), index(after, false))
		owners := sharedNodeIndexes(index(before, true), index(after, true))
		changes := sharedNodeIndexes(change(before), change(after))
		head := []any{nodeIndexValue(paths, false), nodeIndexValue(owners, false), n.Kind, nodeIndexValue(changes, false)}
		key := canonicalJSON(head)
		group, ok := groups[key]
		if !ok {
			group = len(s.groups)
			groups[key] = group
			head[0] = nodeIndexValue(paths, true)
			head[1] = nodeIndexValue(owners, true)
			s.groups = append(s.groups, nodeWireGroup{jsonWeight(head), paths, owners})
		}
		field := func(node *GraphNode, id bool) any {
			if node == nil {
				return nil
			}
			if id {
				return node.ID
			}
			return node.Signature
		}
		row := []any{[]any{field(before, true), field(after, true)}, shared(field(before, false), field(after, false))}
		rowWeight, err := jsonWeightContext(ctx, row)
		if err != nil {
			return nil, err
		}
		unit := nodeWireUnit{anchor: i, group: group, rowWeight: rowWeight}
		s.units = append(s.units, unit)
	}
	s.pathSeen = make([]uint64, len(s.paths))
	s.ownerSeen = make([]uint64, len(s.owners))
	s.groupSeen = make([]uint64, len(s.groups))
	s.pathRank = make([]int, len(s.paths))
	s.ownerRank = make([]int, len(s.owners))
	s.rows = make([]int, len(s.groups))
	s.rowWeights = make([]int, len(s.groups))
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *nodeSizer) weight(membership []byte) int {
	weight, _ := s.weightContext(context.Background(), membership)
	return weight
}
func (s *nodeSizer) weightContext(ctx context.Context, membership []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	poll := cpuContextPoll{context: ctx}

	s.generation++
	if s.generation == 0 {
		clear(s.pathSeen)
		clear(s.ownerSeen)
		clear(s.groupSeen)
		s.generation = 1
	}
	generation := s.generation
	s.activeGroups = s.activeGroups[:0]
	for _, unit := range s.units {
		if err := poll.check(); err != nil {
			return 0, err
		}
		if membership[unit.anchor/8]&(1<<uint(unit.anchor%8)) == 0 {
			continue
		}
		if s.groupSeen[unit.group] != generation {
			s.groupSeen[unit.group] = generation
			s.rows[unit.group] = 0
			s.rowWeights[unit.group] = 0
			s.activeGroups = append(s.activeGroups, unit.group)
		}
		s.rows[unit.group]++
		s.rowWeights[unit.group] += unit.rowWeight
	}
	pathIDs, ownerIDs := []int{}, []int{}
	for node, path := range s.nodePaths {
		if err := poll.check(); err != nil {
			return 0, err
		}
		if membership[node/8]&(1<<uint(node%8)) == 0 {
			continue
		}
		if s.pathSeen[path] != generation {
			s.pathSeen[path] = generation
			pathIDs = append(pathIDs, path)
		}
		owner := s.nodeOwners[node]
		if owner >= 0 && s.ownerSeen[owner] != generation {
			s.ownerSeen[owner] = generation
			ownerIDs = append(ownerIDs, owner)
		}
	}
	if err := sortOrdinalByContext(ctx, pathIDs, func(id int) string { return s.paths[id] }); err != nil {
		return 0, err
	}
	if err := sortOrdinalByContext(ctx, ownerIDs, func(id int) string { return s.owners[id] }); err != nil {
		return 0, err
	}
	paths, owners := []string{}, []string{}
	for _, id := range pathIDs {
		if err := poll.check(); err != nil {
			return 0, err
		}
		s.pathRank[id] = len(paths)
		paths = append(paths, s.paths[id])
	}
	for _, id := range ownerIDs {
		if err := poll.check(); err != nil {
			return 0, err
		}
		s.ownerRank[id] = len(owners)
		owners = append(owners, s.owners[id])
	}
	prefixes := pathPrefixes(paths)
	wirePaths := []any{}
	for _, path := range paths {
		if err := poll.check(); err != nil {
			return 0, err
		}
		for i, prefix := range prefixes {
			if err := poll.check(); err != nil {
				return 0, err
			}
			if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
				wirePaths = append(wirePaths, []any{i, path[len(prefix):]})
				break
			}
		}
	}
	nodesWeight := 6
	for i, id := range s.activeGroups {
		if err := poll.check(); err != nil {
			return 0, err
		}
		group := s.groups[id]
		headWeight := group.baseWeight
		for _, path := range group.paths {
			if err := poll.check(); err != nil {
				return 0, err
			}
			headWeight += integerWeight(s.pathRank[path]) - 3
		}
		for _, owner := range group.owners {
			if err := poll.check(); err != nil {
				return 0, err
			}
			headWeight += integerWeight(s.ownerRank[owner]) - 3
		}
		if i > 0 {
			nodesWeight += 3
		}
		nodesWeight += headWeight + 9 + s.rowWeights[id] + 3*(s.rows[id]-1)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return jsonWeight(prefixes) + jsonWeight(wirePaths) + jsonWeight(owners) + nodesWeight, nil
}
