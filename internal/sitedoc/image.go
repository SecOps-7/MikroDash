package sitedoc

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
)

// The bounds on a backdrop image: a floor plan scanned or exported at a few
// thousand pixels a side. The pixel count is read from the header and checked
// BEFORE the decode, so a file that claims to be huge is refused without being
// expanded into memory.
const (
	MaxImageBytes  = 12 << 20
	MaxImagePixels = 16_000_000
)

// BackdropImage checks an uploaded backdrop and returns it re-encoded, with the
// extension it is stored under and its size in pixels.
//
// RE-ENCODED, NEVER STORED AS SENT, as the branding icon is: decoding and
// encoding again keeps the pixels and drops metadata, trailing bytes and a
// second format hiding behind the first. A PNG stays a PNG, because a drawn plan
// is lines a JPEG would smear; a photo stays a JPEG.
func BackdropImage(raw []byte) (out []byte, ext string, w, h int, err error) {
	if len(raw) == 0 {
		return nil, "", 0, 0, errors.New("the image is empty")
	}
	if len(raw) > MaxImageBytes {
		return nil, "", 0, 0, fmt.Errorf("the image is larger than %d MB", MaxImageBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, "", 0, 0, errors.New("the image must be a PNG or JPEG")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width*cfg.Height > MaxImagePixels {
		return nil, "", 0, 0, fmt.Errorf("the image is %d × %d px; the most is %d megapixels",
			cfg.Width, cfg.Height, MaxImagePixels/1_000_000)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", 0, 0, errors.New("the image could not be read")
	}
	var buf bytes.Buffer
	if format == "png" {
		ext, err = "png", png.Encode(&buf, img)
	} else {
		ext, err = "jpg", jpeg.Encode(&buf, img, &jpeg.Options{Quality: 88})
	}
	if err != nil {
		return nil, "", 0, 0, err
	}
	return buf.Bytes(), ext, cfg.Width, cfg.Height, nil
}
