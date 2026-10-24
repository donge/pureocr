package pureocr

import (
	"image"
	"math"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// grayImg is a single-channel page. Pixels are 0 (black) .. 255 (white).
type grayImg struct {
	pix  []uint8
	w, h int
}

func (g *grayImg) at(x, y int) uint8 { return g.pix[y*g.w+x] }

// toGray converts any image to a grayscale page using OpenCV's luma weights
// (the models were trained on cv2-imread grayscale).
func toGray(img image.Image) *grayImg {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := &grayImg{pix: make([]uint8, w*h), w: w, h: h}
	if g, ok := img.(*image.Gray); ok {
		for y := 0; y < h; y++ {
			copy(out.pix[y*w:(y+1)*w], g.Pix[y*g.Stride:y*g.Stride+w])
		}
		return out
	}
	for y := 0; y < h; y++ {
		row := out.pix[y*w : (y+1)*w]
		for x := 0; x < w; x++ {
			r, gg, bb, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			// 0.299 R + 0.587 G + 0.114 B, in 8-bit fixed point
			row[x] = uint8((19595*(r>>8) + 38470*(gg>>8) + 7471*(bb>>8) + 32768) >> 16)
		}
	}
	return out
}

func (g *grayImg) clone() *grayImg {
	c := &grayImg{pix: make([]uint8, len(g.pix)), w: g.w, h: g.h}
	copy(c.pix, g.pix)
	return c
}

// ---------------------------------------------------------------- resampling

type tap struct {
	idx int
	w   float64
}

// taps builds a separable resampling table, cv2 style: INTER_AREA (area
// averaging) when shrinking, INTER_LINEAR (bilinear, half-pixel centres)
// otherwise.
func taps(src, dst int, area bool) [][]tap {
	out := make([][]tap, dst)
	scale := float64(src) / float64(dst)
	if area && scale > 1 {
		for i := 0; i < dst; i++ {
			lo := float64(i) * scale
			hi := lo + scale
			var row []tap
			for j := int(math.Floor(lo)); j < int(math.Ceil(hi)); j++ {
				s0 := math.Max(lo, float64(j))
				s1 := math.Min(hi, float64(j+1))
				if s1 <= s0 {
					continue
				}
				jj := min(max(j, 0), src-1)
				row = append(row, tap{jj, (s1 - s0) / scale})
			}
			out[i] = row
		}
		return out
	}
	for i := 0; i < dst; i++ {
		c := (float64(i)+0.5)*scale - 0.5
		c = math.Max(c, 0)
		i0 := min(int(math.Floor(c)), src-1)
		i1 := min(i0+1, src-1)
		f := c - float64(i0)
		out[i] = []tap{{i0, 1 - f}, {i1, f}}
	}
	return out
}

// resize resamples a page. area selects cv2.INTER_AREA instead of INTER_LINEAR.
func resize(src []uint8, sw, sh, dw, dh int, area bool) []uint8 {
	if sw == dw && sh == dh {
		out := make([]uint8, sw*sh)
		copy(out, src)
		return out
	}
	tx := taps(sw, dw, area)
	ty := taps(sh, dh, area)
	tmp := make([]float64, dw*sh)
	for y := 0; y < sh; y++ {
		row := src[y*sw : (y+1)*sw]
		for x := 0; x < dw; x++ {
			var v, ws float64
			for _, t := range tx[x] {
				v += float64(row[t.idx]) * t.w
				ws += t.w
			}
			tmp[y*dw+x] = v / ws
		}
	}
	out := make([]uint8, dw*dh)
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var v, ws float64
			for _, t := range ty[y] {
				v += tmp[t.idx*dw+x] * t.w
				ws += t.w
			}
			v /= ws
			out[y*dw+x] = uint8(math.Round(math.Max(0, math.Min(255, v))))
		}
	}
	return out
}

func (g *grayImg) resized(w, h int, area bool) *grayImg {
	if w == g.w && h == g.h {
		return g.clone()
	}
	return &grayImg{pix: resize(g.pix, g.w, g.h, w, h, area), w: w, h: h}
}

// shrinkTo scales the page so its long side is at most side pixels (area
// averaging), keeping the aspect ratio. A page already smaller is returned as is.
func (g *grayImg) shrinkTo(side int) *grayImg {
	s := float64(side) / float64(max(g.w, g.h))
	if s >= 1 {
		return g
	}
	return g.resized(max(1, int(math.Round(float64(g.w)*s))),
		max(1, int(math.Round(float64(g.h)*s))), true)
}

// ---------------------------------------------------------------- rotation

// rotate turns the page by degrees counter-clockwise about its centre, keeping
// the size (cv2.warpAffine + BORDER_REPLICATE). It is used by the deskew step.
func (g *grayImg) rotate(degrees float64) *grayImg {
	if math.Abs(degrees) < 1e-3 {
		return g
	}
	a := degrees * math.Pi / 180
	cos, sin := math.Cos(a), math.Sin(a)
	cx, cy := float64(g.w-1)/2, float64(g.h-1)/2
	maxX, maxY := float64(g.w-1), float64(g.h-1)
	out := &grayImg{pix: make([]uint8, g.w*g.h), w: g.w, h: g.h}
	src := g.pix
	for y := 0; y < g.h; y++ {
		dy := float64(y) - cy
		row := out.pix[y*g.w : (y+1)*g.w]
		for x := 0; x < g.w; x++ {
			dx := float64(x) - cx
			sx := cos*dx + sin*dy + cx
			sy := -sin*dx + cos*dy + cy
			if sx < 0 {
				sx = 0
			} else if sx > maxX {
				sx = maxX
			}
			if sy < 0 {
				sy = 0
			} else if sy > maxY {
				sy = maxY
			}
			x0, y0 := int(sx), int(sy)
			x1, y1 := x0+1, y0+1
			if x1 > g.w-1 {
				x1 = g.w - 1
			}
			if y1 > g.h-1 {
				y1 = g.h - 1
			}
			fx, fy := sx-float64(x0), sy-float64(y0)
			p00 := float64(src[y0*g.w+x0])
			p10 := float64(src[y0*g.w+x1])
			p01 := float64(src[y1*g.w+x0])
			p11 := float64(src[y1*g.w+x1])
			top := p00 + (p10-p00)*fx
			bot := p01 + (p11-p01)*fx
			row[x] = uint8(top + (bot-top)*fy + 0.5)
		}
	}
	return out
}

// quarterTurn rotates the page by 90° counter-clockwise × k.
func (g *grayImg) quarterTurn(k int) *grayImg {
	k = ((k % 4) + 4) % 4
	if k == 0 {
		return g
	}
	switch k {
	case 1: // counter-clockwise
		out := &grayImg{pix: make([]uint8, g.w*g.h), w: g.h, h: g.w}
		for y := 0; y < g.h; y++ {
			for x := 0; x < g.w; x++ {
				// dst(x'=y, y'=w-1-x)
				out.pix[(g.w-1-x)*out.w+y] = g.at(x, y)
			}
		}
		return out
	case 2:
		out := &grayImg{pix: make([]uint8, g.w*g.h), w: g.w, h: g.h}
		n := len(g.pix)
		for i := 0; i < n; i++ {
			out.pix[n-1-i] = g.pix[i]
		}
		return out
	default: // 3 == clockwise
		out := &grayImg{pix: make([]uint8, g.w*g.h), w: g.h, h: g.w}
		for y := 0; y < g.h; y++ {
			for x := 0; x < g.w; x++ {
				out.pix[x*out.w+(g.h-1-y)] = g.at(x, y)
			}
		}
		return out
	}
}

// mapBack maps a line box from the processed page back to the page the caller
// gave us: undo the second deskew (if any), undo the quarter turns, then undo
// the first deskew.
func mapBack(b blockBox, origW, origH int, skew0 float64, turn int, skew1 float64) blockBox {
	// Frame sizes: the turned page swaps width and height for odd turns.
	tw, th := origW, origH
	if turn%2 == 1 {
		tw, th = origH, origW
	}
	pts := [4][2]float64{{b.x0, b.y0}, {b.x1, b.y0}, {b.x1, b.y1}, {b.x0, b.y1}}
	if math.Abs(skew1) > 1e-6 {
		for i := range pts {
			pts[i][0], pts[i][1] = rotatePoint(pts[i][0], pts[i][1], -skew1, tw, th)
		}
	}
	if turn%4 != 0 {
		for i := range pts {
			pts[i][0], pts[i][1] = unturnPoint(pts[i][0], pts[i][1], turn, origW, origH)
		}
	}
	if math.Abs(skew0) > 1e-6 {
		for i := range pts {
			pts[i][0], pts[i][1] = rotatePoint(pts[i][0], pts[i][1], -skew0, origW, origH)
		}
	}
	out := blockBox{
		x0: math.Min(math.Min(pts[0][0], pts[1][0]), math.Min(pts[2][0], pts[3][0])),
		x1: math.Max(math.Max(pts[0][0], pts[1][0]), math.Max(pts[2][0], pts[3][0])),
		y0: math.Min(math.Min(pts[0][1], pts[1][1]), math.Min(pts[2][1], pts[3][1])),
		y1: math.Max(math.Max(pts[0][1], pts[1][1]), math.Max(pts[2][1], pts[3][1])),
	}
	out.x0 = clamp(out.x0, 0, float64(origW))
	out.x1 = clamp(out.x1, 0, float64(origW))
	out.y0 = clamp(out.y0, 0, float64(origH))
	out.y1 = clamp(out.y1, 0, float64(origH))
	return out
}

// rotatePoint rotates a point by degrees around the centre of a w x h frame.
func rotatePoint(x, y, degrees float64, w, h int) (float64, float64) {
	a := degrees * math.Pi / 180
	cos, sin := math.Cos(a), math.Sin(a)
	cx, cy := float64(w-1)/2, float64(h-1)/2
	dx, dy := x-cx, y-cy
	return cos*dx - sin*dy + cx, sin*dx + cos*dy + cy
}

// unturnPoint is the inverse of quarterTurn for a point of a w x h page.
func unturnPoint(u, v float64, turn, w, h int) (float64, float64) {
	switch ((turn % 4) + 4) % 4 {
	case 1:
		return float64(w-1) - v, u
	case 2:
		return float64(w-1) - u, float64(h-1) - v
	case 3:
		return v, float64(h-1) - u
	}
	return u, v
}

// quarterPoint applies quarterTurn's point transform to (x,y) on a w x h page.
func quarterPoint(x, y float64, turn, w, h int) (float64, float64) {
	switch ((turn % 4) + 4) % 4 {
	case 1:
		return y, float64(w-1) - x
	case 2:
		return float64(w-1) - x, float64(h-1) - y
	case 3:
		return float64(h-1) - y, x
	}
	return x, y
}
