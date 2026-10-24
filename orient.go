package pureocr

import (
	"math"
	"runtime"

	ort "github.com/yalue/onnxruntime_go"
)

// Orientation and deskew constants (Indra OCR's pipeline.py).
const (
	orientSide  = 768  // orient.onnx input: page thumbnail, long side, padded square
	orientMin   = 0.95 // a turn is applied only above this probability
	previewSide = 1600 // the tilt search reads a thumbnail of at most this side
	deskewMax   = 45.0 // degrees searched, quarter turns cover the rest
	deskewMin   = 0.3  // smaller tilts are left alone
)

// orientInput builds the [768,768] ink map the orientation model reads.
func orientInput(g *grayImg) []float32 {
	s := float64(orientSide) / float64(max(g.w, g.h))
	small := g
	if s < 1 {
		small = g.resized(max(1, int(math.Round(float64(g.w)*s))),
			max(1, int(math.Round(float64(g.h)*s))), true)
	}
	out := make([]float32, orientSide*orientSide)
	for y := 0; y < small.h && y < orientSide; y++ {
		for x := 0; x < small.w && x < orientSide; x++ {
			out[y*orientSide+x] = 1 - float32(small.pix[y*small.w+x])/255
		}
	}
	return out
}

// orientation returns the quarter turns (counter-clockwise) that make the page
// upright, or 0 when no turn is likely enough.
func (e *Engine) orientation(g *grayImg) (int, error) {
	if e.models.orient == nil {
		return 0, nil
	}
	ink := orientInput(g)
	xT, err := ort.NewTensor(ort.NewShape(1, 1, int64(orientSide), int64(orientSide)), ink)
	if err != nil {
		return 0, err
	}
	defer xT.Destroy()
	out := []ort.Value{nil}
	if err := e.models.orient.Run([]ort.Value{xT}, out); err != nil {
		return 0, err
	}
	t := out[0].(*ort.Tensor[float32])
	defer t.Destroy()
	p := t.GetData()
	best := 0
	for i := 1; i < len(p); i++ {
		if p[i] > p[best] {
			best = i
		}
	}
	if float64(p[best]) <= orientMin {
		return 0, nil
	}
	if best > 3 { // class 4 is a vertical layout, not a quarter turn
		return 0, nil
	}
	return best, nil
}

// estimateSkew returns the degrees (counter-clockwise) that straighten the text
// rows: the angle whose horizontal ink profile changes most sharply row to row.
// The search runs coarse-to-fine; a page that is already level stops after the
// coarse pass, which costs about 3 ms.
func estimateSkew(g *grayImg) float64 {
	fine := inkThumb(g, 600)
	if meanInk(fine) < 0.003*255 {
		return 0 // a blank page has no rows to straighten
	}
	coarse := fine.shrinkTo(320)

	var grid []float64
	for a := -deskewMax; a <= deskewMax+1e-6; a += 3 {
		grid = append(grid, a)
	}
	scores := scoreAngles(coarse, grid)
	bestI := 0
	for i, v := range scores {
		if v > scores[bestI] {
			bestI = i
		}
	}
	best := grid[bestI]
	zero := scoreAt(grid, scores, 0)
	if math.Abs(best) < 1.5 && scores[bestI] <= zero*1.02 {
		return 0 // level enough: no tilt to undo
	}

	// Refine around the coarse winner, then around the refined winner.
	refine := func(center, span, step float64) float64 {
		var angles []float64
		for a := center - span; a <= center+span+1e-6; a += step {
			angles = append(angles, round1(a))
		}
		vals := scoreAngles(fine, angles)
		bi := 0
		for i, v := range vals {
			if v > vals[bi] {
				bi = i
			}
		}
		return angles[bi]
	}
	a1 := refine(best, 3, 0.5)
	return refine(a1, 0.5, 0.1)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func scoreAt(angles, scores []float64, a float64) float64 {
	for i, v := range angles {
		if math.Abs(v-a) < 1e-6 {
			return scores[i]
		}
	}
	return 0
}

// scoreAngles scores every angle, spreading the work over a few goroutines.
func scoreAngles(ink *grayImg, angles []float64) []float64 {
	out := make([]float64, len(angles))
	workers := min(4, max(1, runtime.NumCPU()))
	if workers > len(angles) {
		workers = len(angles)
	}
	parallelFor(len(angles), workers, func(i int) {
		out[i] = rowSharpness(ink, angles[i])
	})
	return out
}

// inkThumb is the page shrunk to at most side pixels, as an ink map (255 = ink).
func inkThumb(g *grayImg, side int) *grayImg {
	small := g.shrinkTo(side)
	out := &grayImg{pix: make([]uint8, len(small.pix)), w: small.w, h: small.h}
	for i, v := range small.pix {
		out.pix[i] = 255 - v
	}
	return out
}

func meanInk(g *grayImg) float64 {
	if len(g.pix) == 0 {
		return 0
	}
	var sum int
	for _, v := range g.pix {
		sum += int(v)
	}
	return float64(sum) / float64(len(g.pix))
}

// rowSharpness scores how level the text rows are at angle a: the sum of squared
// differences of the rotated page's row sums (sharpest when rows are horizontal).
func rowSharpness(ink *grayImg, a float64) float64 {
	r := ink.rotate(a)
	rowSum := make([]float64, r.h)
	for y := 0; y < r.h; y++ {
		var s int
		row := r.pix[y*r.w : (y+1)*r.w]
		for _, v := range row {
			s += int(v)
		}
		rowSum[y] = float64(s)
	}
	var score float64
	for y := 1; y < len(rowSum); y++ {
		d := rowSum[y] - rowSum[y-1]
		score += d * d
	}
	return score
}
