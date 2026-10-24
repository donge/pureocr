package pureocr

import "math"

// process runs the whole pipeline on one page: straighten, turn upright, detect
// lines, read them, put them in reading order and map the boxes back to the
// page as the caller gave it.
func (e *Engine) process(g *grayImg) (Result, error) {
	origW, origH := g.w, g.h
	page := g
	var skew0, skew1 float64
	turn := 0

	// A tilted page breaks line detection long before a quarter turn does, so
	// the tilt is undone first and measured again after a quarter turn.
	if e.deskew {
		skew0 = estimateSkew(page)
		if math.Abs(skew0) >= deskewMin {
			page = page.rotate(skew0)
		} else {
			skew0 = 0
		}
	}
	if e.rotate {
		k, err := e.orientation(page)
		if err != nil {
			return Result{}, err
		}
		if k != 0 {
			page = page.quarterTurn((4 - k) % 4)
			turn = k
			if e.deskew {
				skew1 = estimateSkew(page)
				if math.Abs(skew1) >= deskewMin {
					page = page.rotate(skew1)
				} else {
					skew1 = 0
				}
			}
		}
	}

	boxes, err := e.detect(page)
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
