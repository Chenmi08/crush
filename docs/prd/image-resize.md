# PRD: Automatic Image Resize Before Sending to Model

> Status: draft. Scope: resize images at the point they are handed to a model
> (the `view` tool and MCP tool image results), so an oversized image is
> downscaled/re-encoded instead of rejected. TUI rendering, user-uploaded
> images, and server event replay are out of scope (see
> [Out of Scope](#out-of-scope)).
>
> **Motivation.** The `view` tool currently hard-rejects any image whose
> on-disk byte size exceeds 200KB (`MaxViewSize = 200 * 1024`) before the
> bytes are sent to the model. A 200KB gate is far below every provider's
> documented ceiling (Anthropic direct API 10MB base64 per image, Bedrock
> 5MB, Gemini 20MB inline) yet above what most modern captures weigh in at:
> full-HD screenshots (150KB–1.2MB PNG), high-DPI phone screenshots
> (4–8MB PNG), and phone photos (400KB–2MB JPEG) routinely exceed it. The
> result is that image-based tasks "often exceed the limit" and fail outright
> with no degradation path. The approach here mirrors the pi coding agent's
> image-resize strategy (and its follow-up that applies normalization to all
> tool-produced images).

## Problem Statement

When a user asks the assistant to look at an image, the `view` tool gates on
the image's **on-disk file size**: if it exceeds 200KB the tool returns an
error and the image is never sent to the model. This has three consequences:

- **Frequent false rejections.** Most real screenshots and photos are larger
  than 200KB, so image-based conversations fail even though the image is
  perfectly usable.
- **No degradation path.** There is no resize, re-encode, or quality fallback;
  the only outcome is an error.
- **The gate guards the wrong thing.** 200KB is ~2–5% of the relevant provider
  byte ceilings (it does not protect against provider rejection), while the
  actual cost driver — pixel dimensions converted to tokens — is unconstrained:
  a small-but-high-resolution file can still burn thousands of visual tokens.

A second, related gap: images returned by MCP tools are sent to the model with
no size or dimension guard at all, which can trip Anthropic's 2000px-per-side
cap on many-image requests (>20 images) and 400 the whole request.

## Solution

Send images to the model through an automatic **normalize-and-resize** step
before they leave the tool layer, modeled on the pi agent:

- **Pass through unchanged** when an image already fits: ≤2000×2000 px and
  ≤4.5MB of base64 payload. No quality is lost for images that need none.
- **Downscale** to 2000×2000 px (Lanczos) when dimensions exceed the cap.
- **Choose the smaller encoding** between PNG and JPEG at quality 80.
- **Step down JPEG quality** through `[80, 85, 70, 55, 40]` if still too large.
- **Step down dimensions** by 0.75× per pass (down to 1×1) if still too large.
- **Normalize unsupported formats** (e.g. BMP) to PNG, and re-sniff the MIME
  type from the encoded bytes before sending (some providers validate media
  type against base64 magic bytes).
- **Report the resize** to the model so pixel-coordinate mapping stays
  accurate.
- Replace the 200KB hard rejection with a configurable
  **byte ceiling + pixel ceiling** policy, and cover both the `view` tool and
  MCP tool image results.

## User Stories

1. As a user, I want `view` on an image larger than 200KB to succeed by
   automatically compressing it before sending, so that I can work with
   full-HD screenshots and photos instead of hitting an error.
2. As a user, I want images that already fit the limits (≤2000×2000 px and
   ≤4.5MB base64) to be sent byte-for-byte unchanged, so that no detail is
   lost when none is needed.
3. As a user, I want the resize step to pick the smaller of PNG/JPEG, so that
   quality and size are balanced automatically.
4. As a user, I want JPEG quality stepped down progressively when still too
   large, so that as much of the original content as possible is preserved
   instead of being rejected.
5. As a user, I want dimensions stepped down progressively when still too
   large, so that even very large images can be sent successfully.
6. As a user, I want the model to be told the original size and the displayed
   size after resizing, so that pixel-coordinate instructions remain accurate.
7. As a user, I want an opt-out switch in the config (default on), so that I
   can send original pixel detail when I choose, as long as it stays under the
   hard ceiling.
8. As a user, I want images returned by MCP tools to be normalized the same
   way, so that many-image requests do not trip provider dimension caps.
9. As a user, I want unsupported formats (e.g. BMP) converted to a supported
   one automatically, so that providers do not reject them outright.
10. As a user, I want a clear error when an image exceeds even the new hard
    ceiling, so that I understand why it was not sent rather than seeing a
    silent failure.
11. As a user, I want the image and text limits decoupled, so that relaxing
    the image limit does not widen how much text the `view` tool returns.
12. As a developer, I want the resize strategy contained in one pure function,
    so that it is easy to test, reason about, and keep correct.
13. As a developer, I want the existing "model does not support images" path
    to keep working unchanged, so that behavior on non-vision models is clear.
14. As a user, I want transparent PNGs to keep their alpha (not be force-
    converted to JPEG), so that transparent images remain usable.
15. As a user, I want the GIF handling policy to be explicit and predictable
    (first frame this iteration), so that animated images do not surprise me.

## Implementation Decisions

### Image normalization module (single seam)

Add a pure image-normalization function that owns the entire strategy. It
takes raw image bytes, the media type, and a limits struct, and returns the
normalized bytes, the final media type, and metadata (`wasResized`, original
and displayed dimensions). It must not depend on the TUI, the provider layer,
or any session state.

Reuse the dependencies already in the tree (`imaging` for fit/resize/encode,
`x/image` for EXIF orientation) — no new third-party library is required.

### Default limits and strategy

- `maxWidth` / `maxHeight` = **2000 px** (matches the pi default and
  Anthropic's 2000px-per-side rule for many-image requests).
- `maxBytes` = **4.5MB of base64 payload** (headroom below Anthropic's 5MB
  tier).
- JPEG quality ladder `[80, 85, 70, 55, 40]`; dimension step-down factor
  **0.75×** per pass, down to 1×1.
- Downscale uses Lanczos resampling; EXIF orientation is applied first.

### Pass-through rule

If the decoded image is already ≤2000×2000 px **and** its base64 payload is
≤4.5MB, return the original bytes unchanged with `wasResized=false`. This keeps
the common case zero-cost and lossless.

### Encoding strategy

For each candidate size, encode both PNG and JPEG (quality 80) and keep the
smaller. If the smaller still exceeds `maxBytes`, step JPEG quality down the
ladder; if that is not enough, step dimensions down by 0.75× and retry.

### Output contract

The function returns `(data, mimeType, wasResized, origW×origH, newW×newH)`
so callers can attach a coordinate-mapping hint and tests can assert on the
metadata rather than on internal steps.

### Wiring points

- **`view` tool image path**: replace the 200KB hard rejection with the
  normalize step; send the result through the existing image response path.
- **MCP tool image results**: route image-typed MCP results through the same
  normalize step (mirrors pi's follow-up PR that fixed tool-produced images
  bypassing resize). This closes the "no guard at all" gap.

### Constant split

Decouple the image size ceiling from the text content ceiling. Today a single
`MaxViewSize` constant (200KB) gates both image files and the text section
read back. Introduce two independent limits so relaxing the image policy does
not widen text reads.

### Configuration surface

Add a config switch (e.g. an `options`-style flag, default on) to enable/disable
automatic resize, with the ability to override `maxWidth`, `maxHeight`,
`maxBytes`, and `jpegQuality` per configuration. The default values above are
the baseline.

### Coordinate-mapping hint

When `wasResized` is true, append a note to the tool response describing the
original vs displayed dimensions and the scale factor, so the model can map
pixel coordinates back to the original image (as pi does).

### Format normalization and MIME re-sniffing

Convert unsupported inputs (e.g. BMP) to PNG. After encoding, derive the media
type from the actual bytes rather than trusting the filename or the original
extension, because some providers validate the media type against base64 magic
bytes and reject mismatches with a 400.

### GIF and transparency policies

- **GIF**: this iteration decodes to a static first frame before resizing;
  per-frame animation resize is out of scope.
- **Transparent PNG**: keep the PNG encoding path (the "smaller of PNG/JPEG"
  rule naturally preserves alpha for images where PNG wins).

## Testing Decisions

### What makes a good test here

Test external behavior only: the returned bytes are a valid decodable image,
the media type is correct, dimensions respect the ceiling, pass-through
returns identical bytes, and oversized images produce a response instead of an
error. Do not assert which internal step (e.g. which quality rung) fired.

### Seams

One primary seam: the pure image-normalization function. It is the highest
practical point at which every strategy branch (pass-through, pixel-only
oversize, byte-only oversize, both, quality ladder, dimension ladder,
transparent PNG, format normalization, hard-ceiling error) can be exercised
without the TUI, the provider stack, or a live session.

Two thin behavioral seams at the wiring points: the `view` tool (oversized
image yields an image response, not an error) and the MCP image-result path.
These verify wiring only and do not re-test strategy logic.

### Modules

- The new image-normalization module (strategy tests).
- The `view` tool (behavioral regression for the wiring).
- The MCP tool-result path (behavioral regression for the wiring).
- The config layer (switch parsing and defaults), if a new option is added.

### Prior art in the repository

- Tool-level behavioral tests in the `view` tool test suite (e.g. the existing
  oversized-image and content-limit tests) — updated in place for the new
  behavior.
- The UI image package's tests, which exercise decode/fit with generated image
  fixtures — reuse the same fixture technique to create real in-test images
  instead of fake byte strings.

### Test matrix

| Case | Expected |
|---|---|
| Image ≤2000×2000 and ≤4.5MB base64 | Original bytes, `wasResized=false` |
| Oversized pixels only | Fitted to 2000×2000, resized=true |
| Oversized bytes only | Re-encoded (PNG/JPEG smaller, then quality ladder) |
| Both oversize | Dimension ladder |
| Transparent PNG | Stays PNG, alpha preserved |
| Unsupported format (e.g. BMP) | Converted to PNG |
| Exceeds hard ceiling after all steps | Clear error, no silent failure |
| GIF | Static first frame, resized if needed |
| View tool on >200KB image | Image response (not "too large" error) |
| MCP tool returns an image | Normalized through same function |
| Config switch off | Original bytes sent (subject to hard ceiling) |

## Out of Scope

- TUI image rendering (`fitImage` / kitty graphics) — unchanged.
- User-uploaded/pasted images (chat input path) — can be a follow-up.
- Server event replay / persistence paths.
- Per-model differentiated token optimization — the 2000px default applies to
  all providers.
- Animated GIF resizing — first frame only this iteration.
- Publishing to an issue tracker — saved as a local PRD only (no tracker
  configured).

## Further Notes

- **Why 200KB was wrong.** As a byte gate it sits at ~2–5% of the relevant
  provider ceilings (Anthropic direct API 10MB base64, Bedrock 5MB, Gemini
  20MB inline), so it neither prevents provider rejection nor controls the
  real cost driver, pixel→tokens (Anthropic ≈ pixels/750; 2000×2000 ≈ 5.1k
  visual tokens). Replacing it with a byte ceiling (safety) plus a pixel
  ceiling (cost) addresses both.
- **Reference implementation.** The pi coding agent's image-resize strategy
  (2000px cap, PNG/JPEG smaller-of, quality ladder, 0.75× dimension steps,
  4.5MB base64 cap, coordinate hint) and its follow-up that normalizes all
  tool-produced images.
- **Multi-image requests.** Anthropic applies a 2000px-per-side limit to every
  image in a request once it contains more than 20 image blocks. The 2000px
  default keeps compliant without extra logic.
- **MIME integrity.** Re-sniffing media type from encoded bytes is required,
  not cosmetic: filename/extension mismatches are a real 400 source.
