package pureocr

import (
	"math"
	"sort"
)

// orderLines puts the recognised lines in reading order with a recursive XY cut:
// a column gutter first (multi-column pages), then the widest horizontal gap
// (heads, paragraphs), then rows left to right. It mirrors what a layout model
// would give for the simple cases this package targets.
func orderLines(ls []textLine) []textLine {
	if len(ls) <= 1 {
		return ls
	}
	if cols, ok := splitColumns(ls); ok {
		var out []textLine
		for _, c := range cols {
			out = append(out, orderLines(c)...)
		}
		return out
	}
	if top, bottom, ok := splitRows(ls); ok {
		out := orderLines(top)
		return append(out, orderLines(bottom)...)
	}
	sortRows(ls)
	return ls
}

func medianHeight(ls []textLine) float64 {
	if len(ls) == 0 {
		return 0
	}
	hs := make([]float64, len(ls))
	for i, l := range ls {
		hs[i] = math.Max(1, l.box.y1-l.box.y0)
	}
	sort.Float64s(hs)
	return hs[len(hs)/2]
}

// splitColumns looks for an empty vertical gutter that cuts the lines into two
// groups of at least two lines each. A line spanning the gutter (a title across
// columns) stops the split.
func splitColumns(ls []textLine) ([][]textLine, bool) {
	if len(ls) < 4 {
		return nil, false
	}
	medH := medianHeight(ls)
	byX := make([]textLine, len(ls))
	copy(byX, ls)
	sort.SliceStable(byX, func(i, j int) bool { return byX[i].box.x0 < byX[j].box.x0 })

	maxEnd := byX[0].box.x1
	bestGap, bestSplit := 0.0, 0.0
	for i := 1; i < len(byX); i++ {
		gap := byX[i].box.x0 - maxEnd
		if gap > bestGap && i >= 2 && len(byX)-i >= 2 {
			bestGap, bestSplit = gap, (maxEnd+byX[i].box.x0)/2
		}
		maxEnd = math.Max(maxEnd, byX[i].box.x1)
	}
	if bestGap < math.Max(6, medH) {
		return nil, false
	}
	var left, right []textLine
	for _, l := range ls {
		if (l.box.x0+l.box.x1)/2 < bestSplit {
			left = append(left, l)
		} else {
			right = append(right, l)
		}
	}
	if len(left) < 2 || len(right) < 2 {
		return nil, false
	}
	return [][]textLine{left, right}, true
}

// splitRows cuts between the two blocks with the widest vertical gap.
func splitRows(ls []textLine) ([]textLine, []textLine, bool) {
	if len(ls) < 2 {
		return nil, nil, false
	}
	medH := medianHeight(ls)
	byY := make([]textLine, len(ls))
	copy(byY, ls)
	sort.SliceStable(byY, func(i, j int) bool { return byY[i].box.y0 < byY[j].box.y0 })

	maxEnd := byY[0].box.y1
	bestGap, bestSplit := 0.0, 0.0
	for i := 1; i < len(byY); i++ {
		gap := byY[i].box.y0 - maxEnd
		if gap > bestGap {
			bestGap, bestSplit = gap, (maxEnd+byY[i].box.y0)/2
		}
		maxEnd = math.Max(maxEnd, byY[i].box.y1)
	}
	if bestGap < 0.8*medH {
		return nil, nil, false
	}
	var top, bottom []textLine
	for _, l := range ls {
		if (l.box.y0+l.box.y1)/2 < bestSplit {
			top = append(top, l)
		} else {
			bottom = append(bottom, l)
		}
	}
	if len(top) == 0 || len(bottom) == 0 {
		return nil, nil, false
	}
	return top, bottom, true
}

// sortRows orders rows top to bottom and, inside one row band, left to right.
func sortRows(ls []textLine) {
	sort.SliceStable(ls, func(i, j int) bool {
		hi := math.Max(1, ls[i].box.y1-ls[i].box.y0)
		hj := math.Max(1, ls[j].box.y1-ls[j].box.y0)
		mi := (ls[i].box.y0 + ls[i].box.y1) / 2
		mj := (ls[j].box.y0 + ls[j].box.y1) / 2
		if math.Abs(mi-mj) > 0.5*math.Min(hi, hj) {
			return mi < mj
		}
		return ls[i].box.x0 < ls[j].box.x0
	})
}
