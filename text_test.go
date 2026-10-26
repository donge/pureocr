package pureocr

import "testing"

func TestTidySpaces(t *testing.T) {
	tests := []struct{ in, want string }{
		{"4 5 2 4 2 8 1 9 9 2 0 7 0 9 2 4 1 1", "452428199207092411"},
		{"45242819920709241 X", "45242819920709241X"},
		{"1992 年 7 月 9 日", "1992年7月9日"},
		{"3 元 5 角", "3元5角"},
		{"Deploy checklist", "Deploy checklist"},
		{"Version 1.4.2 is out", "Version 1.4.2 is out"},
		{"2024 年 5 月", "2024年5月"},
		{"第 3 章", "第 3 章"},
	}
	for _, tt := range tests {
		if got := tidySpaces(tt.in); got != tt.want {
			t.Errorf("tidySpaces(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
