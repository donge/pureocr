package pureocr

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

// DumpLineCrops writes the recognizer input of every detected line of an image
// as a PNG (8-bit ink, 255 = ink) into dir, and returns how many it wrote.
//
// It exists so a quantized model can be calibrated on the same crops the engine
// sees: run it over a few representative pages, then feed the directory to
// tools/quantize_rec.py. It is not part of the recognition path.
func (e *Engine) DumpLineCrops(path, dir string) (int, error) {
	img, _, err := decodeFile(path)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	if err := e.acquire(); err != nil {
		return 0, err
	}
	defer e.release()

	g := toGray(img)
	boxes, err := e.detect(g, g.shrinkTo(previewSide), 1.0)
	if err != nil {
		return 0, err
	}
	written := 0
	for i, b := range boxes {
		ink, w := cropLine(g, b, e.models.meta.RecHeight, 1, isDarkLine(g, b))
		if w < 8 {
			continue
		}
		// cropLine returns ink in [0,1] row-major; store as 8-bit ink.
		pix := make([]uint8, len(ink))
		for k, v := range ink {
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			pix[k] = uint8(v*255 + 0.5)
		}
		im := image.NewGray(image.Rect(0, 0, w, e.models.meta.RecHeight))
		copy(im.Pix, pix)
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%04d.png", i)))
		if err != nil {
			return written, err
		}
		err = png.Encode(f, im)
		f.Close()
		if err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

var _ = color.Gray{}
