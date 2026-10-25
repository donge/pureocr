package pureocr

import "math"

// previewSide is the longest side of the thumbnail shared by the tilt search,
// the orientation model and the detector. Resizing the full page once for all
// three (instead of once per step) is a large share of the Go-side cost on
// big scans and screenshots.
const previewSide = 1024

// process runs the whole pipeline on one page: straighten, turn upright, detect
// lines, read them, put them in reading order and map the boxes back to the
// page as the caller gave it.
func (e *Engine) process(g *grayImg) (Result, error) {
	origW, origH := g.w, g.h
	prev := g.shrinkTo(previewSide)
	// Pages at or below the preview size are unchanged, so prev == g and the
	// scale is 1: everything below then behaves exactly as before.
	prevScale := float64(max(prev.w, prev.h)) / float64(max(g.w, g.h))

	page := g
	var skew0, skew1 float64
	turn := 0

	// A tilted page breaks line detection long before a quarter turn does, so
	// the tilt is undone first and measured again after a quarter turn.
	if e.deskew {
		skew0 = estimateSkew(prev)
		if math.Abs(skew0) >= deskewMin {
			page = page.rotate(skew0)
		} else {
			skew0 = 0
		}
	}
	if e.rotate {
		k, err := e.orientation(prev)
		if err != nil {
			return Result{}, err
		}
		if k != 0 {
			page = page.quarterTurn((4 - k) % 4)
			turn = k
			if e.deskew {
				skew1 = estimateSkew(page.shrinkTo(previewSide))
				if math.Abs(skew1) >= deskewMin {
					page = page.rotate(skew1)
				} else {
					skew1 = 0
				}
			}
		}
	}

	boxes, err := e.detect(page, prev, prevScale)
	if err != nil {
		return Result{}, err
	}
	lines, err := e.recognizeLines(page, boxes)
	if err != nil {
		return Result{}, err
	}
	ordered := orderLines(lines)

	res := Result{Rotation: turn}
	if turn == 0 {
		res.Skew = skew0
	} else {
		res.Skew = skew1
	}
	res.Blocks = make([]Block, 0, len(ordered))
	for _, l := range ordered {
		if l.text == "" {
			continue
		}
		b := mapBack(l.box, origW, origH, skew0, turn, skew1)
		res.Blocks = append(res.Blocks, Block{
			Text:   l.text,
			Rate:   l.conf,
			Left:   b.x0,
			Top:    b.y0,
			Right:  b.x1,
			Bottom: b.y1,
		})
	}
	return res, nil
}
