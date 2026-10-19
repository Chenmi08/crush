// Package imageresize normalizes and resizes images before they are handed
// to a model, so an oversized file is downscaled or re-encoded instead of
// rejected. It is a pure strategy layer: it depends only on the standard
// image codecs and imaging, never on the TUI, the provider layer, or session
// state.
package imageresize

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"

	"github.com/disintegration/imaging"
	// Register the WebP decoder. imaging decodes through image.Decode, which
	// only knows formats whose packages are linked in; importing x/image/webp
	// makes WebP decodable (and therefore resizable) without a new
	// third-party library. BMP and TIFF are registered transitively by
	// imaging's own imports of x/image/bmp and x/image/tiff.
	_ "golang.org/x/image/webp"
)

// Default ceilings. 2000px matches Anthropic's 2000px-per-side rule for
// many-image requests; the byte ceiling is 4.5MB of base64 payload, which
// keeps headroom below Anthropic's 5MB tier.
const (
	DefaultMaxWidth  = 2000
	DefaultMaxHeight = 2000
	DefaultMaxBytes  = int(4.5 * 1024 * 1024) // 4.5MB of base64 payload
)

// DefaultJpegQualityLadder is the default JPEG quality ladder stepped down
// through when an image is still too large after the initial encode. The
// leading 85 is a deliberate one-off bump: screenshots are often better
// served by 85 than 80, and the ladder stops as soon as an encoding fits,
// so a rung the image does not need is never applied.
var DefaultJpegQualityLadder = []int{80, 85, 70, 55, 40}

// Limits configures the normalize-and-resize strategy.
type Limits struct {
	// MaxWidth and MaxHeight bound the encoded image's pixel dimensions.
	MaxWidth  int
	MaxHeight int
	// MaxBytes bounds the base64-encoded payload sent to the model.
	MaxBytes int
	// JpegQuality is the JPEG quality ladder stepped down through when an
	// image is still too large. When empty, DefaultJpegQualityLadder is
	// used.
	JpegQuality []int
}

// Config pairs the on/off switch with the strategy limits.
type Config struct {
	Enabled bool
	Limits  Limits
}

// DefaultConfig returns the baseline strategy: enabled, a 2000px cap, a
// 4.5MB base64 ceiling, and the default JPEG quality ladder.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Limits:  DefaultLimits(),
	}
}

// DefaultLimits returns the default pixel and byte ceilings and the default
// JPEG quality ladder.
func DefaultLimits() Limits {
	return Limits{
		MaxWidth:    DefaultMaxWidth,
		MaxHeight:   DefaultMaxHeight,
		MaxBytes:    DefaultMaxBytes,
		JpegQuality: append([]int(nil), DefaultJpegQualityLadder...),
	}
}

// withDefaults fills any zero limit with its default and copies the quality
// ladder so callers cannot mutate the package-level default.
func (c Config) withDefaults() Config {
	if c.Limits.MaxWidth <= 0 {
		c.Limits.MaxWidth = DefaultMaxWidth
	}
	if c.Limits.MaxHeight <= 0 {
		c.Limits.MaxHeight = DefaultMaxHeight
	}
	if c.Limits.MaxBytes <= 0 {
		c.Limits.MaxBytes = DefaultMaxBytes
	}
	if len(c.Limits.JpegQuality) == 0 {
		c.Limits.JpegQuality = append([]int(nil), DefaultJpegQualityLadder...)
	}
	return c
}

// Result is the outcome of normalization: the bytes to send, the media type
// derived from those bytes, whether the image was resized or re-encoded, and
// the original vs displayed dimensions.
type Result struct {
	Data       []byte
	MimeType   string
	WasResized bool
	OrigWidth  int
	OrigHeight int
	NewWidth   int
	NewHeight  int
}

// Hint returns a coordinate-mapping note describing the resize, or "" when
// the image was passed through unchanged or its dimensions did not change
// (for example a format-only re-encode), since there is then no coordinate
// mapping for the model to reason about. Callers append it to the tool
// response so the model can map pixel coordinates back to the original image.
func (r Result) Hint() string {
	if !r.WasResized || r.OrigWidth == r.NewWidth && r.OrigHeight == r.NewHeight {
		return ""
	}
	scale := float64(0)
	if r.OrigWidth > 0 {
		scale = float64(r.NewWidth) / float64(r.OrigWidth)
	}
	return fmt.Sprintf("[Image resized: %dx%d -> %dx%d (scale %.2f)]",
		r.OrigWidth, r.OrigHeight, r.NewWidth, r.NewHeight, scale)
}

// AppendHint returns content with the coordinate-mapping note appended when
// the image was resized, joining with a newline when content is non-empty.
// It is the single place the hint is formatted into a tool response, so the
// view tool and MCP path share one convention.
func (r Result) AppendHint(content string) string {
	hint := r.Hint()
	if hint == "" {
		return content
	}
	if content == "" {
		return hint
	}
	return content + "\n" + hint
}

// tooLargeError reports an image that could not be brought under the byte
// ceiling by any step of the strategy.
type tooLargeError struct {
	Size int
	Max  int
}

func (e tooLargeError) Error() string {
	return fmt.Sprintf("image is too large to send after resizing (%d bytes base64). Maximum size is %d bytes", e.Size, e.Max)
}

// Normalize returns the bytes and media type that should be sent to the
// model, applying the normalize-and-resize strategy:
//
//   - Pass through unchanged when the image already fits: a provider-supported
//     format at or under the pixel and byte ceilings.
//   - Otherwise fit to the pixel ceiling, then choose the smaller of PNG and
//     JPEG, stepping JPEG quality down the ladder and, if still too large,
//     dimensions down by 0.75x per pass.
//   - Unsupported formats (e.g. BMP) are re-encoded to PNG/JPEG, and the
//     media type is always re-derived from the encoded bytes.
//
// The strategy never returns an image that exceeds the byte ceiling; when it
// cannot bring an image under it, it returns an error rather than failing
// silently. When cfg.Enabled is false the original bytes are returned
// unchanged (subject to the hard byte ceiling), which is the documented
// opt-out.
func Normalize(data []byte, mimeType string, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()

	detected, detectedOK := detectImageMimeType(data)
	sniffed := mimeType
	if detectedOK {
		sniffed = detected
	}
	payloadSize := base64Size(len(data))

	// The opt-out switch sends the original bytes unchanged, still subject
	// to the hard byte ceiling.
	if !cfg.Enabled {
		if payloadSize > cfg.Limits.MaxBytes {
			return Result{}, tooLargeError{Size: payloadSize, Max: cfg.Limits.MaxBytes}
		}
		return Result{Data: data, MimeType: sniffed}, nil
	}

	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		// imaging cannot decode this format (e.g. WebP). If the bytes really
		// sniff as a provider-supported image type and fit the byte ceiling,
		// pass them through; otherwise fail with a clear error rather than
		// send bytes a provider would reject. The pass-through is gated on
		// the sniffed type, not the extension-derived fallback, so bytes
		// that merely claim to be an image are not let through.
		if detectedOK && isProviderImageType(detected) && payloadSize <= cfg.Limits.MaxBytes {
			return Result{Data: data, MimeType: detected}, nil
		}
		return Result{}, fmt.Errorf("unable to decode image (%s): %w", sniffed, err)
	}

	origW, origH := bounds(img)

	// Pass through unchanged when the image already fits.
	if isProviderImageType(sniffed) &&
		origW <= cfg.Limits.MaxWidth && origH <= cfg.Limits.MaxHeight &&
		payloadSize <= cfg.Limits.MaxBytes {
		return Result{
			Data:       data,
			MimeType:   sniffed,
			OrigWidth:  origW,
			OrigHeight: origH,
			NewWidth:   origW,
			NewHeight:  origH,
		}, nil
	}

	// Normalize: fit to the pixel ceiling first, then encode the smaller of
	// PNG/JPEG, stepping JPEG quality down the ladder only while the result
	// exceeds the byte ceiling and, if still too large, dimensions down by
	// 0.75x per pass.
	current := img
	if origW > cfg.Limits.MaxWidth || origH > cfg.Limits.MaxHeight {
		current = imaging.Fit(img, cfg.Limits.MaxWidth, cfg.Limits.MaxHeight, imaging.Lanczos)
	}

	var (
		encoded []byte
		encMime string
	)
	lastSize := payloadSize
	for {
		// First try the initial quality (the first ladder rung), then step
		// down the ladder only as far as needed, so no quality is lost that
		// the byte ceiling does not require.
		encoded, encMime, lastSize = encodeWithinLimit(current, cfg.Limits.JpegQuality, cfg.Limits.MaxBytes)
		if lastSize <= cfg.Limits.MaxBytes {
			newW, newH := bounds(current)
			return Result{
				Data:       encoded,
				MimeType:   sniffMimeType(encoded, encMime),
				WasResized: true,
				OrigWidth:  origW,
				OrigHeight: origH,
				NewWidth:   newW,
				NewHeight:  newH,
			}, nil
		}

		// Step dimensions down by 0.75x, down to 1x1.
		curW, curH := bounds(current)
		newW := int(float64(curW) * 0.75)
		newH := int(float64(curH) * 0.75)
		if newW < 1 {
			newW = 1
		}
		if newH < 1 {
			newH = 1
		}
		if newW >= curW && newH >= curH {
			break // Cannot shrink further.
		}
		current = imaging.Resize(current, newW, newH, imaging.Lanczos)
	}

	return Result{}, tooLargeError{Size: lastSize, Max: cfg.Limits.MaxBytes}
}

// encodeSmallerOf encodes img as PNG and as JPEG at the given quality and
// returns the smaller encoding with its media type. A transparent image
// keeps its alpha whenever PNG is the smaller encoding.
func encodeSmallerOf(img image.Image, quality int) ([]byte, string) {
	best, bestMime := encodePNG(img), "image/png"
	enc := encodeJPEG(img, quality)
	if len(best) == 0 || (len(enc) > 0 && len(enc) < len(best)) {
		best, bestMime = enc, "image/jpeg"
	}
	return best, bestMime
}

// encodeWithinLimit returns the best encoding of img that fits maxBytes: it
// starts at the first ladder rung (the highest quality) and steps the JPEG
// quality down only while the smaller-of-PNG/JPEG encoding still exceeds
// the limit. When even the last rung is too large it returns that last
// encoding so the caller can step dimensions down instead. The returned
// size is the base64 payload of the returned bytes.
func encodeWithinLimit(img image.Image, qualities []int, maxBytes int) ([]byte, string, int) {
	var (
		data []byte
		mime string
		size int
	)
	for _, q := range qualities {
		if q <= 0 || q > 100 {
			continue
		}
		data, mime = encodeSmallerOf(img, q)
		size = base64Size(len(data))
		if size <= maxBytes {
			break
		}
	}
	if data == nil {
		// The ladder held no usable rung; fall back to PNG so the caller
		// still gets an encoding to step dimensions down from.
		data, mime = encodePNG(img), "image/png"
		size = base64Size(len(data))
	}
	return data, mime, size
}

func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

func encodeJPEG(img image.Image, quality int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// providerImageTypes are the media types every provider accepts.
var providerImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

func isProviderImageType(mime string) bool {
	return providerImageTypes[mime]
}

// detectImageMimeType returns the content-sniffed media type when the bytes
// are recognized as a supported image format, and ok=false otherwise.
func detectImageMimeType(data []byte) (string, bool) {
	sniffed := http.DetectContentType(data)
	// http.DetectContentType may return the MIME with a ";" parameter.
	if i := strings.IndexByte(sniffed, ';'); i >= 0 {
		sniffed = strings.TrimSpace(sniffed[:i])
	}
	switch sniffed {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp":
		return sniffed, true
	}
	return "", false
}

// sniffMimeType returns the content-sniffed media type when it identifies a
// supported image format, otherwise the provided fallback. Providers like
// Anthropic validate the media type against the base64 magic bytes and reject
// mismatches with a 400, so trusting the filename or original extension alone
// is unsafe.
func sniffMimeType(data []byte, fallback string) string {
	if mime, ok := detectImageMimeType(data); ok {
		return mime
	}
	return fallback
}

func base64Size(n int) int {
	return base64.StdEncoding.EncodedLen(n)
}

func bounds(img image.Image) (int, int) {
	b := img.Bounds()
	return b.Dx(), b.Dy()
}
