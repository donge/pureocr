package pureocr

import (
	"image"
	"os"
	"strings"
	"testing"
)

func loadGray(t *testing.T, path string) *grayImg {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return toGray(img)
}

// TestQuarterTurnKeepsText guards the detector thumbnail that process() shares
// with the orientation and tilt steps: it must be turned with the page.
// Otherwise the detector describes the page as it was before the turn while the
// line crops come from the turned page, and a rotated scan loses its text.
func TestQuarterTurnKeepsText(t *testing.T) {
	eng, err := New(Options{Threads: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	cases := []struct {
		file string
		turn int
		want string
	}{
		{"testdata/sample_en.png", 1, "Deploy checklist"}, // 90° counter-clockwise
		{"testdata/sample_zh.png", 2, "人工智能"},             // upside down
	}
	for _, tc := range cases {
		upright := loadGray(t, tc.file)
		want, err := eng.runGray(upright)
		if err != nil {
			t.Fatalf("%s upright: %v", tc.file, err)
		}
		if len(want.Blocks) < 5 {
			t.Fatalf("%s upright: got %d blocks, want at least 5", tc.file, len(want.Blocks))
		}

		got, err := eng.runGray(upright.quarterTurn(tc.turn))
		if err != nil {
			t.Errorf("%s quarterTurn(%d): %v", tc.file, tc.turn, err)
			continue
		}
		if got.Rotation != tc.turn {
			t.Errorf("%s quarterTurn(%d): detected rotation %d, want %d",
				tc.file, tc.turn, got.Rotation, tc.turn)
		}
		if len(got.Blocks)*5 < len(want.Blocks)*4 {
			t.Errorf("%s quarterTurn(%d): got %d blocks, want at least 80%% of %d",
				tc.file, tc.turn, len(got.Blocks), len(want.Blocks))
		}
		if !strings.Contains(got.Text(), tc.want) {
			t.Errorf("%s quarterTurn(%d): text lacks %q:\n%s",
				tc.file, tc.turn, tc.want, got.Text())
		}
	}
}

// TestDeskewKeepsText covers the tilt branch of the same thumbnail. A page
// tilted by more than deskewMin is straightened before detection; the detector
// thumbnail has to be straightened too.
func TestDeskewKeepsText(t *testing.T) {
	eng, err := New(Options{Threads: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	upright := loadGray(t, "testdata/sample_en.png")
	want, err := eng.runGray(upright)
	if err != nil {
		t.Fatalf("upright: %v", err)
	}

	for _, deg := range []float64{5, 10} {
		got, err := eng.runGray(upright.rotate(deg))
		if err != nil {
			t.Errorf("rotate(%.0f): %v", deg, err)
			continue
		}
		if len(got.Blocks)*5 < len(want.Blocks)*4 {
			t.Errorf("rotate(%.0f): got %d blocks, want at least 80%% of %d",
				deg, len(got.Blocks), len(want.Blocks))
		}
		if !strings.Contains(got.Text(), "Deploy checklist") {
			t.Errorf("rotate(%.0f): text lacks %q:\n%s", deg, "Deploy checklist", got.Text())
		}
	}
}
