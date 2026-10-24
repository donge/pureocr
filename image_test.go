package pureocr

import (
	"math"
	"testing"
)

func TestQuarterTurnMarker(t *testing.T) {
	g := &grayImg{pix: make([]uint8, 40*20), w: 40, h: 20}
	for i := range g.pix {
		g.pix[i] = 255
	}
	g.pix[0] = 0 // marker at the top-left

	// One counter-clockwise quarter turn puts the top-left corner at bottom-left.
	ccw := g.quarterTurn(1)
	if ccw.w != 20 || ccw.h != 40 {
		t.Fatalf("size %dx%d, want 20x40", ccw.w, ccw.h)
	}
	if got := ccw.at(0, 39); got != 0 {
		t.Errorf("marker not at bottom-left: %d", got)
	}
	// Four turns are the identity.
	back := g.quarterTurn(4)
	if back.w != g.w || back.h != g.h || back.pix[0] != 0 {
		t.Error("four quarter turns changed the page")
	}
	// Turn then unturn a point.
	x, y := 37.0, 3.0
	u, v := quarterPoint(x, y, 1, g.w, g.h)
	bx, by := unturnPoint(u, v, 1, g.w, g.h)
	if math.Abs(bx-x) > 1e-9 || math.Abs(by-y) > 1e-9 {
		t.Errorf("round trip: (%v,%v) -> (%v,%v)", x, y, bx, by)
	}
}

func TestMapBackIdentity(t *testing.T) {
	b := blockBox{x0: 10, y0: 20, x1: 110, y1: 40}
	got := mapBack(b, 200, 300, 0, 0, 0)
	if got != b {
		t.Errorf("mapBack without transforms changed the box: %+v", got)
	}
}

func TestMapBackTurn(t *testing.T) {
	// A box on a page turned 90 degrees counter-clockwise maps back to the
	// original page: its x range comes from the original y range.
	b := blockBox{x0: 5, y0: 10, x1: 25, y1: 30}
	got := mapBack(b, 100, 50, 0, 1, 0) // original 100x50, turned 50x100
	if got.y0 < 0 || got.y1 > 50 || got.x0 < 0 || got.x1 > 100 {
		t.Errorf("box outside the page: %+v", got)
	}
}

func TestResizeIdentity(t *testing.T) {
	g := &grayImg{pix: []uint8{0, 64, 128, 255}, w: 2, h: 2}
	got := g.resized(2, 2, true)
	if got.w != 2 || got.h != 2 || got.pix[3] != 255 {
		t.Errorf("resize to the same size changed pixels: %+v", got.pix)
	}
	big := g.resized(4, 4, false)
	if big.w != 4 || big.h != 4 || len(big.pix) != 16 {
		t.Errorf("resize up: %dx%d", big.w, big.h)
	}
}
