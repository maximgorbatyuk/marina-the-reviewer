package review

import (
	"context"
	"slices"
)

type goIndexedChange struct {
	change ChangedLine
	order  int
}

type goChangedLineIndex struct {
	revisions [2][]goIndexedChange
	visited   int
}

func newGoChangedLineIndex(ctx context.Context, changes []ChangedLine) (*goChangedLineIndex, error) {
	index := &goChangedLineIndex{}
	for order, change := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if change.Revision == Base || change.Revision == Head {
			index.revisions[change.Revision] = append(index.revisions[change.Revision], goIndexedChange{change, order})
		}
	}
	for revision := range index.revisions {
		if err := sortStableContext(ctx, index.revisions[revision], func(a, b goIndexedChange) int { return a.change.Line - b.change.Line }); err != nil {
			return nil, err
		}
	}
	return index, nil
}

func (index *goChangedLineIndex) within(before, after *CodeSlice) []ChangedLine {
	selected := []goIndexedChange{}
	for revision, source := range []*CodeSlice{before, after} {
		if source == nil {
			continue
		}
		changes := index.revisions[revision]
		start, _ := slices.BinarySearchFunc(changes, source.StartLine, func(change goIndexedChange, line int) int { return change.change.Line - line })
		for position := start; position < len(changes) && changes[position].change.Line <= source.EndLine; position++ {
			selected = append(selected, changes[position])
			index.visited++
		}
	}
	slices.SortFunc(selected, func(a, b goIndexedChange) int { return a.order - b.order })
	result := make([]ChangedLine, len(selected))
	for position, change := range selected {
		result[position] = change.change
	}
	return result
}

func (file *goSourceFile) indexDeclarationRanges() {
	file.ranges = append([]*goDeclaration{}, file.declarations...)
	slices.SortStableFunc(file.ranges, func(a, b *goDeclaration) int { return a.source.StartLine - b.source.StartLine })
	end := 0
	for _, declaration := range file.ranges {
		end = max(end, declaration.source.EndLine)
		file.rangeEnds = append(file.rangeEnds, end)
	}
}

func (file *goSourceFile) overlappingDeclarations(first, last int) []*goDeclaration {
	end, _ := slices.BinarySearchFunc(file.ranges, last+1, func(declaration *goDeclaration, line int) int { return declaration.source.StartLine - line })
	result := []*goDeclaration{}
	for position := end - 1; position >= 0 && file.rangeEnds[position] >= first; position-- {
		file.rangeVisited++
		declaration := file.ranges[position]
		if declaration.source.EndLine >= first {
			result = append(result, declaration)
		}
	}
	return result
}
