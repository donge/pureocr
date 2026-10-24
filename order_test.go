package pureocr

import "testing"

func line(x0, y0, x1, y1 float64, text string) textLine {
	return textLine{box: blockBox{x0: x0, y0: y0, x1: x1, y1: y1}, text: text}
}

func TestOrderTwoColumns(t *testing.T) {
	// Two columns of three lines each, given in the wrong order.
	ls := []textLine{
		line(250, 10, 440, 30, "r1"),
		line(10, 10, 200, 30, "l1"),
		line(250, 40, 440, 60, "r2"),
		line(10, 40, 200, 60, "l2"),
		line(250, 70, 440, 90, "r3"),
		line(10, 70, 200, 90, "l3"),
	}
	got := orderLines(ls)
	want := []string{"l1", "l2", "l3", "r1", "r2", "r3"}
	for i, l := range got {
		if l.text != want[i] {
			t.Fatalf("order %v, want %v", texts(got), want)
		}
	}
}

func TestOrderHeadingThenColumns(t *testing.T) {
	ls := []textLine{
		line(10, 0, 400, 20, "title"), // spans both columns
		line(250, 50, 440, 70, "r1"),
		line(10, 50, 200, 70, "l1"),
		line(250, 80, 440, 100, "r2"),
		line(10, 80, 200, 100, "l2"),
	}
	got := texts(orderLines(ls))
	want := []string{"title", "l1", "l2", "r1", "r2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}

func texts(ls []textLine) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.text
	}
	return out
}
