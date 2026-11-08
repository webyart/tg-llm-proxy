package embed

import (
	"math"
	"testing"
)

func TestCosine(t *testing.T) {
	for _, row := range []struct {
		name string
		a    []float32
		b    []float32
		want float64
	}{
		{name: "identical", a: []float32{1, 2, 3}, b: []float32{1, 2, 3}, want: 1},
		{name: "scaled", a: []float32{1, 0}, b: []float32{7, 0}, want: 1},
		{name: "opposite", a: []float32{1, 0}, b: []float32{-1, 0}, want: -1},
		{name: "orthogonal", a: []float32{1, 0}, b: []float32{0, 1}, want: 0},
		{name: "empty", a: nil, b: nil, want: 0},
		{name: "different length", a: []float32{1, 2}, b: []float32{1, 2, 3}, want: 0},
		{name: "zero norm left", a: []float32{0, 0}, b: []float32{1, 1}, want: 0},
		{name: "zero norm right", a: []float32{1, 1}, b: []float32{0, 0}, want: 0},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := Cosine(row.a, row.b)
			if math.Abs(got-row.want) > 1e-6 {
				t.Fatalf("Cosine=%v, want %v", got, row.want)
			}
			if math.IsNaN(got) {
				t.Fatal("Cosine returned NaN")
			}
		})
	}
}

func TestCosineIsSymmetric(t *testing.T) {
	a := []float32{0.2, -1, 3.5, 0}
	b := []float32{1, 1, 0, -2}
	if left, right := Cosine(a, b), Cosine(b, a); math.Abs(left-right) > 1e-9 {
		t.Fatalf("Cosine(a,b)=%v, Cosine(b,a)=%v", left, right)
	}
}
