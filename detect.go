package pureocr

import (
	"math"
	"sort"

	ort "github.com/yalue/onnxruntime_go"
)

// Detector geometry (see Indra OCR's det_post.py: the detector predicts a core
// band per line plus the distance to the line's top and bottom).
const (
	detInputMultiple = 32
	detOutStride     = 2
	detThreshold     = 0.5
	detMinArea       = 2
	coreXShrink      = 0.15
	// A second, finer detection pass when lines come out tiny (dense tables,
	// footnotes on a full-page scan).
	adaptiveMinHeight = 9.0
	adaptiveTarget    = 12.0
	adaptiveMaxSide   = 2048
	// ... and a coarser pass for pages whose text is far too large for the
	// detector (a screenshot of three big words, a poster).
	adaptiveMaxHeight   = 48.0
	adaptiveLargeTarget = 24.0
)

type blockBox struct {
	x0, y0, x1, y1 float64
	score          float64
}

// pageToInput scales the page so its long side is longSide, pads it to a
// multiple of 32 and returns the ink map (1 = ink) and the scale used.
func pageToInput(g *grayImg, longSide int) ([]float32, int, int, float64) {
	scale := float64(longSide) / float64(max(g.w, g.h))
	nw := max(1, int(math.Round(float64(g.w)*scale)))
	nh := max(1, int(math.Round(float64(g.h)*scale)))
	var src []uint8 = g.pix
	if nw != g.w || nh != g.h {
		src = resize(g.pix, g.w, g.h, nw, nh, scale < 1)
	}
	pw := int(math.Ceil(float64(nw)/detInputMultiple)) * detInputMultiple
	ph := int(math.Ceil(float64(nh)/detInputMultiple)) * detInputMultiple
	out := make([]float32, ph*pw)
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			out[y*pw+x] = 1 - float32(src[y*nw+x])/255
		}
	}
	return out, pw, ph, scale
}

func sigmoid(x float32) float32 { return 1 / (1 + float32(math.Exp(float64(-x)))) }

// decodeDet turns the detector output [3,h,w] into line boxes in detector-input
// pixels: a line is a 4-connected region of the core map, its top and bottom
// the mean of the two distance maps over that region.
func decodeDet(out []float32, h, w int) []blockBox {
	n := h * w
	prob := make([]float32, n)
	mask := make([]bool, n)
	for i := 0; i < n; i++ {
		prob[i] = sigmoid(out[i])
		mask[i] = prob[i] > detThreshold
	}
	top := out[n:]
	bot := out[2*n:]

	seen := make([]bool, n)
	stack := make([]int, 0, 256)
	var boxes []blockBox
	for start := 0; start < n; start++ {
		if !mask[start] || seen[start] {
			continue
		}
		var (
			area                  int
			left, right, up, down int
			sumT, sumB, sumS, cnt float64
		)
		left, up = w, h
		seen[start] = true
		stack = append(stack[:0], start)
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := p%w, p/w
			area++
			left = min(left, x)
			right = max(right, x)
			up = min(up, y)
			down = max(down, y)
			ys := (float64(y) + 0.5) * detOutStride
			sumT += ys - float64(top[p])
			sumB += ys + float64(bot[p])
			sumS += float64(prob[p])
			cnt++
			if x > 0 && mask[p-1] && !seen[p-1] {
				seen[p-1] = true
				stack = append(stack, p-1)
			}
			if x < w-1 && mask[p+1] && !seen[p+1] {
				seen[p+1] = true
				stack = append(stack, p+1)
			}
			if y > 0 && mask[p-w] && !seen[p-w] {
				seen[p-w] = true
				stack = append(stack, p-w)
			}
			if y < h-1 && mask[p+w] && !seen[p+w] {
				seen[p+w] = true
				stack = append(stack, p+w)
			}
		}
		if area < detMinArea || cnt == 0 {
			continue
		}
		y0, y1 := sumT/cnt, sumB/cnt
		lh := math.Max(1, y1-y0)
		ex := math.Min(coreXShrink*lh, 0.5*lh)
		boxes = append(boxes, blockBox{
			x0:    float64(left)*detOutStride - ex,
			x1:    float64(right+1)*detOutStride + ex,
			y0:    y0,
			y1:    y1,
			score: sumS / cnt,
		})
	}
	return boxes
}

func (e *Engine) detOnce(ink []float32, pw, ph int) ([]blockBox, error) {
	xT, err := ort.NewTensor(ort.NewShape(1, 1, int64(ph), int64(pw)), ink)
	if err != nil {
		return nil, err
	}
	defer xT.Destroy()
	out := []ort.Value{nil}
	if err := e.models.det.Run([]ort.Value{xT}, out); err != nil {
		return nil, err
	}
	t := out[0].(*ort.Tensor[float32])
	defer t.Destroy()
	shape := t.GetShape()
	return decodeDet(t.GetData(), int(shape[2]), int(shape[3])), nil
}

// detect returns the text-line boxes of a page in page pixels. prev is the
// shared preview (long side <= previewSide) with its scale from g; when the
// detector's input size is at or below the preview size the page is not
// resized a second time.
func (e *Engine) detect(g, prev *grayImg, prevScale float64) ([]blockBox, error) {
	longSide := e.models.meta.DetLongSide
	if longSide <= 0 {
		longSide = 1024
	}
	// Never blow a small image up: a 400 px screenshot of large text becomes
	// 150 px tall glyphs at 1024, far outside the detector's training range, and
	// it fragments the line. Small text is handled by the adaptive pass below.
	longSide = min(longSide, max(g.w, g.h))
	src, srcScale := g, 1.0
	if longSide <= max(prev.w, prev.h) {
		src, srcScale = prev, prevScale
	}
	ink, pw, ph, scale := pageToInput(src, longSide)
	boxes, err := e.detOnce(ink, pw, ph)
	if err != nil {
		return nil, err
	}
	used := scale * srcScale
	// A page whose lines came out under ~9 px gets a finer pass, but only when
	// the first pass did not already shrink the page to the detector's input
	// size: a large page (a screenshot, a 300 dpi scan) was downsized on
	// purpose, and that input is what the detector was trained on. The pages
	// that need the extra pixels are the small ones (a low-resolution scan).
	if len(boxes) >= 8 && max(g.w, g.h) <= longSide {
		hs := make([]float64, len(boxes))
		for i, b := range boxes {
			hs[i] = b.y1 - b.y0
		}
		sort.Float64s(hs)
		medH := hs[len(hs)/2]
		if medH < adaptiveMinHeight {
			ls := int(math.Min(adaptiveMaxSide, float64(longSide)*adaptiveTarget/math.Max(medH, 1)))
			ls = ls / detInputMultiple * detInputMultiple
			if ls > longSide {
				ink2, pw2, ph2, s2 := pageToInput(g, ls)
				if b2, err := e.detOnce(ink2, pw2, ph2); err != nil {
					return nil, err
				} else if len(b2) > 0 {
					boxes, used = b2, s2
				}
			}
		}
	}
	// The mirror case: glyphs far taller than the detector expects fragment the
	// line, so detect again on a smaller render. medH is in page pixels; the
	// detector sees it at `used` scale.
	if len(boxes) >= 1 {
		hs := make([]float64, len(boxes))
		for i, b := range boxes {
			hs[i] = b.y1 - b.y0
		}
		sort.Float64s(hs)
		medH := hs[len(hs)/2] * used
		if medH > adaptiveMaxHeight {
			ls := int(adaptiveLargeTarget * float64(max(g.w, g.h)) / (hs[len(hs)/2]))
			ls = max(96, ls/detInputMultiple*detInputMultiple)
			if float64(ls) < float64(longSide)*0.9 {
				src2, sc2 := g, 1.0
				if ls <= max(prev.w, prev.h) {
					src2, sc2 = prev, prevScale
				}
				ink2, pw2, ph2, s2 := pageToInput(src2, ls)
				s2 *= sc2
				if b2, err := e.detOnce(ink2, pw2, ph2); err == nil && len(b2) > 0 {
					boxes, used = b2, s2
				}
			}
		}
	}
	for i := range boxes {
		b := &boxes[i]
		b.x0 = clamp(b.x0/used, 0, float64(g.w))
		b.x1 = clamp(b.x1/used, 0, float64(g.w))
		b.y0 = clamp(b.y0/used, 0, float64(g.h))
		b.y1 = clamp(b.y1/used, 0, float64(g.h))
	}
	return boxes, nil
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(v, hi)) }
