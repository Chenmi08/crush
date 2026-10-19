package imageresize

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"testing"

	"github.com/disintegration/imaging"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/bmp"
)

// Fixture builders create real, decodable images in-test (mirroring the UI
// image package's fixture technique) instead of fake byte strings.

func noisyImage(w, h int) *image.NRGBA {
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
	return img
}

func solidImage(w, h int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func pngBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := imaging.Decode(bytes.NewReader(data))
	require.NoError(t, err)
	return img
}

func TestNormalizePassesThroughSmallProviderImages(t *testing.T) {
	t.Parallel()

	data := pngBytes(t, solidImage(100, 80, color.NRGBA{R: 10, G: 20, B: 30, A: 255}))
	res, err := Normalize(data, "image/png", DefaultConfig())
	require.NoError(t, err)
	require.False(t, res.WasResized)
	require.Equal(t, data, res.Data, "pass-through must return identical bytes")
	require.Equal(t, "image/png", res.MimeType)
	require.Equal(t, 100, res.OrigWidth)
	require.Equal(t, 80, res.OrigHeight)
	require.Empty(t, res.Hint(), "no coordinate hint for a pass-through")
}

func TestNormalizeResizesOversizedPixels(t *testing.T) {
	t.Parallel()

	// Solid color keeps the payload tiny, so only the pixel ceiling is hit.
	data := pngBytes(t, solidImage(4000, 3000, color.NRGBA{R: 10, G: 200, B: 30, A: 255}))
	res, err := Normalize(data, "image/png", DefaultConfig())
	require.NoError(t, err)
	require.True(t, res.WasResized)
	require.Equal(t, 4000, res.OrigWidth)
	require.Equal(t, 3000, res.OrigHeight)
	require.LessOrEqual(t, res.NewWidth, DefaultMaxWidth)
	require.LessOrEqual(t, res.NewHeight, DefaultMaxHeight)
	require.NotEqual(t, data, res.Data)
	require.Contains(t, res.Hint(), "4000x3000")
	require.Contains(t, res.Hint(), "resized")
	img := decode(t, res.Data)
	require.LessOrEqual(t, img.Bounds().Dx(), DefaultMaxWidth)
	require.LessOrEqual(t, img.Bounds().Dy(), DefaultMaxHeight)
}

func TestNormalizeReencodesOversizedBytesOnly(t *testing.T) {
	t.Parallel()

	// Small enough to fit the pixel ceiling but far over the byte ceiling, so
	// the strategy must re-encode (and step dimensions down) to fit.
	data := pngBytes(t, noisyImage(600, 600))
	cfg := DefaultConfig()
	cfg.Limits.MaxBytes = 10 * 1024
	res, err := Normalize(data, "image/png", cfg)
	require.NoError(t, err)
	require.True(t, res.WasResized)
	require.LessOrEqual(t, res.NewWidth, 600)
	require.LessOrEqual(t, res.NewHeight, 600)
	require.Equal(t, 600, res.OrigWidth)
	require.NotEqual(t, data, res.Data)
	require.Contains(t, []string{"image/png", "image/jpeg"}, res.MimeType)
	img := decode(t, res.Data)
	require.Equal(t, res.NewWidth, img.Bounds().Dx())
	require.Equal(t, res.NewHeight, img.Bounds().Dy())
}

func TestNormalizeStepsDownBothOversized(t *testing.T) {
	t.Parallel()

	// Oversized in both pixels and bytes. The initial fit to 2000px is still
	// too large, so the dimension ladder must step below the pixel ceiling.
	data := pngBytes(t, noisyImage(4000, 3000))
	cfg := DefaultConfig()
	cfg.Limits.MaxBytes = 20 * 1024
	res, err := Normalize(data, "image/png", cfg)
	require.NoError(t, err)
	require.True(t, res.WasResized)
	require.Equal(t, 4000, res.OrigWidth)
	require.Equal(t, 3000, res.OrigHeight)
	require.Less(t, res.NewWidth, DefaultMaxWidth, "dimension ladder must step below the pixel ceiling")
	require.GreaterOrEqual(t, res.NewWidth, 1)
	decode(t, res.Data)
}

func transparentCheckerboard(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x+y)%2 == 0 {
				img.SetNRGBA(x, y, color.NRGBA{A: 0})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{R: 0, G: 0, B: 0, A: 255})
			}
		}
	}
	return img
}

func hasTransparentPixel(t *testing.T, data []byte) bool {
	t.Helper()
	decoded := decode(t, data)
	b := decoded.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := decoded.At(x, y).RGBA(); a == 0 {
				return true
			}
		}
	}
	return false
}

func TestNormalizeKeepsTransparentPNGAlpha(t *testing.T) {
	t.Parallel()

	// A small transparent PNG fits the ceilings, so it passes through
	// unchanged: it stays PNG and its alpha channel survives byte-for-byte.
	data := pngBytes(t, transparentCheckerboard(100, 100))
	res, err := Normalize(data, "image/png", DefaultConfig())
	require.NoError(t, err)
	require.False(t, res.WasResized)
	require.Equal(t, data, res.Data)
	require.Equal(t, "image/png", res.MimeType)
	require.True(t, hasTransparentPixel(t, res.Data))
}

func TestEncodePicksPNGWhenItWinsForTransparentImages(t *testing.T) {
	t.Parallel()

	// At full resolution (no resampling) a transparent checkerboard is far
	// worse for JPEG than for PNG, so the smaller-of-PNG/JPEG rule picks PNG
	// and the alpha channel survives. After resampling the balance can shift,
	// but that is the documented trade-off of the smaller-of rule.
	img := transparentCheckerboard(200, 200)
	enc, mime := encodeSmallerOf(img, 80)
	require.Equal(t, "image/png", mime)
	require.True(t, hasTransparentPixel(t, enc))
}

func TestEncodeWithinLimitStopsAtFirstFittingRung(t *testing.T) {
	t.Parallel()

	// The strategy must stop at the first rung that fits, not pick the
	// smallest encoding across the whole ladder: if quality 80 already
	// fits, the result is the quality-80 encode even though a lower rung
	// would be smaller. A noisy image makes JPEG the smaller encoding.
	img := noisyImage(60, 40)
	want, _ := encodeSmallerOf(img, 80)
	require.Less(t, len(want), len(encodePNG(img)), "test premise: JPEG must be smaller")
	got, mime, size := encodeWithinLimit(img, DefaultJpegQualityLadder, base64Size(len(want)))
	require.Equal(t, "image/jpeg", mime)
	require.Equal(t, base64Size(len(want)), size)
	require.Equal(t, want, got)
}

func TestHintEmptyWhenDimensionsUnchanged(t *testing.T) {
	t.Parallel()

	// A re-encode that keeps the dimensions (format normalization, or a
	// byte-only fit) produces no coordinate-mapping note.
	res := Result{
		WasResized: true,
		OrigWidth:  100, OrigHeight: 80,
		NewWidth: 100, NewHeight: 80,
	}
	require.Empty(t, res.Hint())
	require.Equal(t, "keep", res.AppendHint("keep"))
}

func TestNormalizeConvertsUnsupportedFormatToPNG(t *testing.T) {
	t.Parallel()

	// BMP is decodable but not a provider-supported media type, so even a
	// small one must be re-encoded instead of passed through. Dimensions
	// are unchanged, so no coordinate hint is emitted.
	data := bmpBytes(t, solidImage(200, 150, color.NRGBA{R: 200, G: 10, B: 10, A: 255}))
	res, err := Normalize(data, "image/bmp", DefaultConfig())
	require.NoError(t, err)
	require.True(t, res.WasResized)
	require.Equal(t, "image/png", res.MimeType)
	require.NotEqual(t, data, res.Data)
	require.Empty(t, res.Hint(), "a format-only re-encode needs no coordinate hint")
	decode(t, res.Data)
}

func TestNormalizeErrorsOnHardCeiling(t *testing.T) {
	t.Parallel()

	data := pngBytes(t, solidImage(8, 8, color.NRGBA{R: 1, G: 2, B: 3, A: 255}))
	cfg := DefaultConfig()
	cfg.Limits.MaxBytes = 1 // Impossibly small: even 1x1 cannot fit.
	_, err := Normalize(data, "image/png", cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "too large")
}

func TestNormalizeGIFUsesFirstFrame(t *testing.T) {
	t.Parallel()

	palette := color.Palette{color.Black, color.White, color.RGBA{R: 255, A: 255}}
	frames := make([]*image.Paletted, 3)
	delays := make([]int, 3)
	for i := range 3 {
		p := image.NewPaletted(image.Rect(0, 0, 4000, 3000), palette)
		for y := 0; y < 3000; y++ {
			for x := 0; x < 4000; x++ {
				p.Set(x, y, color.Black)
			}
		}
		// Each frame has one white row so the frames differ.
		for x := 0; x < 4000; x++ {
			p.Set(x, i%3000, color.White)
		}
		frames[i] = p
		delays[i] = 10
	}
	var buf bytes.Buffer
	require.NoError(t, gif.EncodeAll(&buf, &gif.GIF{Image: frames, Delay: delays}))
	data := buf.Bytes()

	res, err := Normalize(data, "image/gif", DefaultConfig())
	require.NoError(t, err)
	require.True(t, res.WasResized)
	img := decode(t, res.Data)
	require.LessOrEqual(t, img.Bounds().Dx(), DefaultMaxWidth)
	require.LessOrEqual(t, img.Bounds().Dy(), DefaultMaxHeight)
}

func TestNormalizeDisabledSendsOriginalBytes(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Enabled = false
	data := pngBytes(t, solidImage(3000, 2000, color.NRGBA{R: 5, G: 6, B: 7, A: 255}))
	res, err := Normalize(data, "image/png", cfg)
	require.NoError(t, err)
	require.False(t, res.WasResized)
	require.Equal(t, data, res.Data, "opt-out must send original pixel detail")

	// Still subject to the hard byte ceiling.
	cfg.Limits.MaxBytes = 1
	_, err = Normalize(data, "image/png", cfg)
	require.Error(t, err)
}

func TestNormalizePassesThroughUndecodableProviderFormat(t *testing.T) {
	t.Parallel()

	// WebP is provider-supported but imaging cannot decode it; as long as it
	// fits the byte ceiling it is passed through unchanged.
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	res, err := Normalize(webp, "image/webp", DefaultConfig())
	require.NoError(t, err)
	require.False(t, res.WasResized)
	require.Equal(t, webp, res.Data)
	require.Equal(t, "image/webp", res.MimeType)
}

func TestNormalizeRejectsUndecodableGarbage(t *testing.T) {
	t.Parallel()

	// Bytes that only claim to be an image must not pass through.
	_, err := Normalize([]byte("definitely not an image"), "image/png", DefaultConfig())
	require.Error(t, err)
}

func TestNormalizeSniffsMimeTypeFromBytes(t *testing.T) {
	t.Parallel()

	// JPEG bytes in a .png file are reported as image/jpeg.
	jpegData := jpegBytes(t, solidImage(50, 50, color.NRGBA{R: 1, G: 2, B: 3, A: 255}))
	res, err := Normalize(jpegData, "image/png", DefaultConfig())
	require.NoError(t, err)
	require.Equal(t, "image/jpeg", res.MimeType)
}

func bmpBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, bmp.Encode(&buf, img))
	return buf.Bytes()
}

func jpegBytes(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}))
	return buf.Bytes()
}

func TestNormalizeDecodesWebP(t *testing.T) {
	t.Parallel()

	// WebP is provider-supported and now decodable (the x/image/webp decoder
	// is registered by the package's blank import), so an oversized-in-bytes
	// WebP is re-encoded instead of passed through or rejected.
	data, err := os.ReadFile("testdata/blue-purple-pink.lossy.webp")
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.Limits.MaxBytes = 1024
	res, err := Normalize(data, "image/webp", cfg)
	require.NoError(t, err)
	require.True(t, res.WasResized)
	require.Contains(t, []string{"image/png", "image/jpeg"}, res.MimeType)
	decode(t, res.Data)
}
