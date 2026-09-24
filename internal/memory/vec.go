package memory

import (
	"encoding/binary"
	"math"
)

// Embeddings are stored L2-normalised as little-endian float32 bytea, so cosine
// similarity is a plain dot product.

func normalize(v []float32) []float32 {
	var n float64
	for _, x := range v {
		n += float64(x) * float64(x)
	}
	if n == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(n))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	if len(b)%4 != 0 || len(b) == 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}

// Dot returns the dot product; mismatched dimensions score -1 (incomparable).
func Dot(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return -1
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// Normalize and Encode/Decode are exported for other packages (doc index).
func Normalize(v []float32) []float32 { return normalize(v) }
func EncodeVec(v []float32) []byte    { return encodeVec(v) }
func DecodeVec(b []byte) []float32    { return decodeVec(b) }
