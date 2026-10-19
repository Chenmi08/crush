package tools

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

// noisyPNGBytes encodes a real, decodable PNG whose encoded size is large
// enough to exercise the image resize path in tests.
func noisyPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(42))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(rng.Intn(256)),
				G: uint8(rng.Intn(256)),
				B: uint8(rng.Intn(256)),
				A: 255,
			})
		}
	}

	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

// writeNoisyPNG writes a noisy PNG fixture to disk.
func writeNoisyPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, noisyPNGBytes(t, w, h), 0o644))
}
