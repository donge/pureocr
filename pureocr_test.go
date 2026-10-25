package pureocr

import (
	"image"
	"image/draw"
	"os"
	"strings"
	"testing"
)

func TestEnglish(t *testing.T) {
	eng, err := New(Options{Threads: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	res, err := eng.OCRFile("testdata/sample_en.png")
	if err != nil {
		t.Fatalf("OCRFile: %v", err)
	}
	text := res.Text()
	for _, want := range []string{"Deploy checklist", "release", "rollout"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	if len(res.Blocks) < 5 {
		t.Errorf("got %d blocks, want at least 5", len(res.Blocks))
	}
	if res.Blocks[0].Rate < 0.5 {
		t.Errorf("first block confidence %.2f too low", res.Blocks[0].Rate)
	}
}

func TestChinese(t *testing.T) {
	eng, err := New(Options{Threads: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	res, err := eng.OCRFile("testdata/sample_zh.png")
	if err != nil {
		t.Fatalf("OCRFile: %v", err)
	}
	text := res.Text()
	if !strings.Contains(text, "人工智能技术") {
		t.Errorf("text lacks the first Chinese line:\n%s", text)
	}
	if len([]rune(text)) < 200 {
		t.Errorf("only %d characters, want 200+:\n%s", len([]rune(text)), text)
	}
}

func TestByteInput(t *testing.T) {
	data, err := os.ReadFile("testdata/sample_en.png")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := New(Options{Threads: 1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()
	res, err := eng.OCRBytes(data)
	if err != nil {
		t.Fatalf("OCRBytes: %v", err)
	}
	if !strings.Contains(res.Text(), "Deploy checklist") {
		t.Errorf("OCRBytes text:\n%s", res.Text())
	}
	if res.Elapsed <= 0 {
		t.Error("Elapsed not set")
	}
}

func TestConcurrent(t *testing.T) {
	eng, err := New(Options{Threads: 1, Concurrency: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()
	data, err := os.ReadFile("testdata/sample_en.png")
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			_, err := eng.OCRBytes(data)
			errs <- err
		}()
	}
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Errorf("concurrent OCR: %v", err)
		}
	}
}

func TestClosed(t *testing.T) {
	eng, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := eng.OCRFile("testdata/sample_en.png"); err != ErrClosed {
		t.Errorf("after Close: %v, want ErrClosed", err)
	}
}

func BenchmarkOCR(b *testing.B) {
	eng, err := New(Options{Threads: 4})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer eng.Close()
	data, err := os.ReadFile("testdata/sample_en.png")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := eng.OCRBytes(data); err != nil {
			b.Fatal(err)
		}
	}
}

func TestOCRRawPixels(t *testing.T) {
	eng, err := New(Options{Threads: 2, NoRotation: true, NoDeskew: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer eng.Close()

	// decode once, then feed the pixels directly (as a PDF renderer would)
	f, err := os.Open("testdata/sample_en.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()

	gray := image.NewGray(b)
	draw.Draw(gray, b, img, b.Min, draw.Src)
	fromGray, err := eng.OCRGray(gray.Pix, b.Dx(), b.Dy(), gray.Stride)
	if err != nil {
		t.Fatalf("OCRGray: %v", err)
	}

	nrgba := image.NewNRGBA(b)
	draw.Draw(nrgba, b, img, b.Min, draw.Src)
	rgb := make([]byte, 0, b.Dx()*b.Dy()*3)
	for y := 0; y < b.Dy(); y++ {
		row := nrgba.Pix[y*nrgba.Stride : y*nrgba.Stride+b.Dx()*4]
		for x := 0; x < b.Dx(); x++ {
			rgb = append(rgb, row[4*x], row[4*x+1], row[4*x+2])
		}
	}
	fromRGB, err := eng.OCRRGB(rgb, b.Dx(), b.Dy(), b.Dx()*3)
	if err != nil {
		t.Fatalf("OCRRGB: %v", err)
	}

	want := "Deploy checklist"
	if !strings.Contains(fromGray.Text(), want) {
		t.Errorf("OCRGray text:\n%s", fromGray.Text())
	}
	if !strings.Contains(fromRGB.Text(), want) {
		t.Errorf("OCRRGB text:\n%s", fromRGB.Text())
	}
	if fromGray.Text() != fromRGB.Text() {
		t.Errorf("gray and rgb disagree:\n%s\n---\n%s", fromGray.Text(), fromRGB.Text())
	}
}
