package embed

import "math"

// Cosine returns the cosine similarity of a and b. A pair of different lengths, an empty vector or a vector of
// zero norm has no direction and returns 0, so a chunk without a comparable vector never contributes a score
// instead of producing NaN.
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for index := range a {
		x, y := float64(a[index]), float64(b[index])
		dot += x * y
		normA += x * x
		normB += y * y
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
