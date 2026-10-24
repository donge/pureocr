package pureocr

import (
	"math"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// Recognition constants (Indra OCR's rec_preproc.py and pipeline.py).
const (
	padX = 0.3 // crop padding, in line heights
	padY = 0.2
	// A line read with a mean CTC peak probability under this gets no text: on
	// rules, empty form fields and checkboxes the recognizer invents "-", "I", "1".
	minConf = 0.5
	// The script classifier is trusted above this; below it the Latin recognizer
	// reads the line too and the more confident reading wins.
	routeSure = 0.95
	maxCropW  = 4096
)

type textLine struct {
	box  blockBox
	text string
	conf float64
}

// batchInput is a padded [n,1,h,w] recognizer input.
type batchInput struct {
	data    []float32
	widths  []int64
	n, h, w int
}

func newBatch(n, h, w int) *batchInput {
	return &batchInput{data: make([]float32, n*h*w), widths: make([]int64, n), h: h, w: w}
}

func (b *batchInput) add(ink []float32, width int) {
	off := b.n * b.h * b.w
	for y := 0; y < b.h; y++ {
		copy(b.data[off+y*b.w:off+y*b.w+width], ink[y*width:(y+1)*width])
	}
	b.widths[b.n] = int64(width)
	b.n++
}

// isDarkLine reports light text on a dark ground (banner, table head, slide):
// the 98th percentile sits much further above the median than the 2nd below it.
func isDarkLine(g *grayImg, b blockBox) bool {
	x0, y0 := int(math.Max(0, math.Floor(b.x0))), int(math.Max(0, math.Floor(b.y0)))
	x1, y1 := int(math.Min(float64(g.w-1), math.Ceil(b.x1))), int(math.Min(float64(g.h-1), math.Ceil(b.y1)))
	if x1 < x0 || y1 < y0 {
		return false
	}
	var hist [256]int
	cnt := 0
	for y := y0; y <= y1; y++ {
		row := g.pix[y*g.w:]
		for x := x0; x <= x1; x++ {
			hist[row[x]]++
			cnt++
		}
	}
	if cnt == 0 {
		return false
	}
	pct := func(q float64) float64 {
		pos := q * float64(cnt-1)
		i := int(math.Floor(pos))
		f := pos - float64(i)
		lo := nearestRank(&hist, i+1)
		hi := nearestRank(&hist, min(i+2, cnt))
		return lo + (hi-lo)*f
	}
	lo, med, hi := pct(0.02), pct(0.5), pct(0.98)
	return hi-med > 2*(med-lo)+8
}

func nearestRank(hist *[256]int, rank int) float64 {
	acc := 0
	for v := 0; v < 256; v++ {
		acc += hist[v]
		if acc >= rank {
			return float64(v)
		}
	}
	return 255
}

// cropLine returns the recognizer input for one line: the box padded by padX /
// padY line heights, filled white outside the page, resized to height pixels and
// turned into an ink map. window > 1 reads the page around the line.
func cropLine(g *grayImg, b blockBox, height, window int, dark bool) ([]float32, int) {
	py := (float64(window)*(1+2*padY) - 1) / 2
	hh := height * window
	bh := math.Max(1, b.y1-b.y0)
	X0, X1 := b.x0-padX*bh, b.x1+padX*bh
	Y0, Y1 := b.y0-py*bh, b.y1+py*bh
	ix0, iy0 := int(math.Floor(X0)), int(math.Floor(Y0))
	ix1, iy1 := int(math.Ceil(X1)), int(math.Ceil(Y1))
	cw, ch := max(1, ix1-ix0), max(1, iy1-iy0)
	fill := uint8(255)
	if dark {
		fill = 0
	}
	crop := make([]uint8, cw*ch)
	for i := range crop {
		crop[i] = fill
	}
	sx0, sy0 := max(0, ix0), max(0, iy0)
	sx1, sy1 := min(g.w, ix1), min(g.h, iy1)
	for y := sy0; y < sy1; y++ {
		dst := (y-iy0)*cw + (sx0 - ix0)
		src := y*g.w + sx0
		copy(crop[dst:dst+(sx1-sx0)], g.pix[src:src+(sx1-sx0)])
	}
	outW := int(math.Round(float64(cw) * float64(hh) / float64(ch)))
	outW = min(max(outW, 8), maxCropW)
	res := resize(crop, cw, ch, outW, hh, ch > hh)
	ink := make([]float32, len(res))
	if dark {
		for i, v := range res {
			ink[i] = float32(v) / 255
		}
	} else {
		for i, v := range res {
			ink[i] = 1 - float32(v)/255
		}
	}
	return ink, outW
}

// widthBatches groups lines of similar width so little compute goes to padding.
func widthBatches(widths []int, maxBatch int) [][]int {
	const (
		maxRatio = 1.3
		minWidth = 64
		maxArea  = 64 * 640
	)
	order := make([]int, len(widths))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return widths[order[a]] < widths[order[b]] })
	var batches [][]int
	for _, i := range order {
		if len(batches) > 0 {
			last := batches[len(batches)-1]
			firstW := float64(max(widths[last[0]], minWidth))
			if len(last) < maxBatch &&
				float64(max(widths[i], minWidth)) <= maxRatio*firstW &&
				float64((len(last)+1)*max(widths[i], minWidth)) <= maxArea {
				batches[len(batches)-1] = append(last, i)
				continue
			}
		}
		batches = append(batches, []int{i})
	}
	return batches
}

func (e *Engine) buildBatch(crops [][]float32, widths, sel []int, height int) *batchInput {
	bw := 0
	for _, j := range sel {
		if widths[j] > bw {
			bw = widths[j]
		}
	}
	bw = (bw + 7) / 8 * 8
	bi := newBatch(len(sel), height, bw)
	for _, j := range sel {
		bi.add(crops[j], widths[j])
	}
	return bi
}

// recognizeLines reads every line box: script routing decides between the Latin
// and the CJK recognizer.
func (e *Engine) recognizeLines(g *grayImg, boxes []blockBox) ([]textLine, error) {
	m := e.models
	n := len(boxes)
	tight := make([][]float32, n)
	tightW := make([]int, n)
	dark := make([]bool, n)

	// Crops are pure pixel work: spread them over a few goroutines.
	parallelFor(n, min(4, max(1, cap(e.sem))), func(i int) {
		dark[i] = isDarkLine(g, boxes[i])
		tight[i], tightW[i] = cropLine(g, boxes[i], m.meta.RecHeight, 1, dark[i])
	})

	// Script routing on the tight crops.
	names := make([]string, n)
	sure := make([]bool, n)
	for i := range names {
		names[i], sure[i] = "base", true
	}
	if m.script != nil && n > 0 {
		for _, part := range widthBatches(tightW, e.batch) {
			bi := e.buildBatch(tight, tightW, part, m.script.height)
			xT, err := ort.NewTensor(ort.NewShape(int64(bi.n), 1, int64(bi.h), int64(bi.w)), bi.data)
			if err != nil {
				return nil, err
			}
			wT, err := ort.NewTensor(ort.NewShape(int64(bi.n)), bi.widths)
			if err != nil {
				xT.Destroy()
				return nil, err
			}
			out := []ort.Value{nil}
			err = m.script.s.Run([]ort.Value{xT, wT}, out)
			xT.Destroy()
			wT.Destroy()
			if err != nil {
				return nil, err
			}
			pt := out[0].(*ort.Tensor[float32])
			p := pt.GetData()
			cols := int(pt.GetShape()[1])
			pt.Destroy()
			for k, j := range part {
				best, pk := 0, p[k*cols]
				for c := 1; c < cols; c++ {
					if p[k*cols+c] > pk {
						best, pk = c, p[k*cols+c]
					}
				}
				names[j] = scriptGroup(best)
				sure[j] = pk > routeSure
			}
		}
	}

	// Split the lines between the two recognizers. A line the classifier is not
	// sure about is read by both; the better confidence wins.
	var baseIdx, cjkIdx []int
	for j := range boxes {
		if names[j] != "cjk" || !sure[j] {
			baseIdx = append(baseIdx, j)
		}
		if names[j] == "cjk" {
			cjkIdx = append(cjkIdx, j)
		}
	}

	// The Latin recognizer reads a 3x tall window; the CJK one the line band.
	tall := make([][]float32, n)
	tallW := make([]int, n)
	parallelFor(len(baseIdx), min(4, max(1, cap(e.sem))), func(k int) {
		j := baseIdx[k]
		tall[j], tallW[j] = cropLine(g, boxes[j], m.meta.RecHeight, m.meta.RecWindow, dark[j])
	})

	texts := make([]string, n)
	conf := make([]float64, n)
	for i := range conf {
		conf[i] = -1
	}
	run := func(s *session, idx []int, crops [][]float32, widths []int) error {
		if len(idx) == 0 {
			return nil
		}
		sel := make([]int, len(idx))
		for i, j := range idx {
			sel[i] = widths[j]
		}
		for _, part := range widthBatches(sel, e.batch) {
			lines := make([]int, len(part))
			for k, p := range part {
				lines[k] = idx[p]
			}
			bi := e.buildBatch(crops, widths, lines, s.height)
			got, confs, err := s.run(bi)
			if err != nil {
				return err
			}
			for k, j := range lines {
				if confs[k] <= conf[j] {
					continue
				}
				conf[j] = confs[k]
				if confs[k] >= minConf {
					texts[j] = got[k]
				} else {
					texts[j] = ""
				}
			}
		}
		return nil
	}
	if err := run(m.rec, baseIdx, tall, tallW); err != nil {
		return nil, err
	}
	if err := run(m.cjk, cjkIdx, tight, tightW); err != nil {
		return nil, err
	}

	out := make([]textLine, 0, n)
	for j, b := range boxes {
		out = append(out, textLine{box: b, text: texts[j], conf: math.Max(0, conf[j])})
	}
	return out, nil
}

func scriptGroup(class int) string {
	switch class {
	case 1:
		return "cjk"
	case 2:
		return "rtl"
	case 3:
		return "world"
	}
	return "base"
}

// parallelFor runs f(0..n-1) on up to workers goroutines.
func parallelFor(n, workers int, f func(int)) {
	if n == 0 {
		return
	}
	if workers <= 1 || n < 8 {
		for i := 0; i < n; i++ {
			f(i)
		}
		return
	}
	if workers > n {
		workers = n
	}
	var (
		next int
		mu   sync.Mutex
		wg   sync.WaitGroup
	)
	wg.Add(workers)
	for k := 0; k < workers; k++ {
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= n {
					return
				}
				f(i)
			}
		}()
	}
	wg.Wait()
}
