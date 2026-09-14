package branding

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngOf(t *testing.T, w, h int, sixteenBit bool) []byte {
	t.Helper()
	var img image.Image
	if sixteenBit {
		m := image.NewNRGBA64(image.Rect(0, 0, w, h))
		m.Set(0, 0, color.NRGBA64{R: 0xffff, A: 0xffff})
		img = m
	} else {
		m := image.NewNRGBA(image.Rect(0, 0, w, h))
		m.Set(0, 0, color.NRGBA{R: 255, A: 255})
		img = m
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestANameIsTrimmedAndBounded(t *testing.T) {
	if got, err := CleanName("  Acme Networks  "); err != nil || got != "Acme Networks" {
		t.Errorf("CleanName trimmed to %q, %v", got, err)
	}
	if got, err := CleanName(""); err != nil || got != "" {
		t.Errorf("an empty name is the default, not an error: %q, %v", got, err)
	}
	if _, err := CleanName(strings.Repeat("a", MaxNameRunes+1)); err == nil {
		t.Error("a name one character over the limit was accepted")
	}
	if _, err := CleanName(strings.Repeat("é", MaxNameRunes)); err != nil {
		t.Errorf("the limit counts characters, not bytes: %v", err)
	}
	if _, err := CleanName("Acme\nNetworks"); err == nil {
		t.Error("a name with a control character was accepted")
	}
}

func TestAFontIsAnIdOrNothing(t *testing.T) {
	for _, ok := range []string{"", "syne", "ibm-plex-sans"} {
		if _, err := CleanFont(ok); err != nil {
			t.Errorf("CleanFont(%q) refused a valid id: %v", ok, err)
		}
	}
	for _, bad := range []string{"Syne", "syne;color:red", "a b", strings.Repeat("a", 33)} {
		if _, err := CleanFont(bad); err == nil {
			t.Errorf("CleanFont(%q) accepted something that is not a font id", bad)
		}
	}
}

func TestAnIconMustBeASquarePNGOrJPEGWithinBounds(t *testing.T) {
	if _, err := Icon(pngOf(t, MinIconPx, MinIconPx, false)); err != nil {
		t.Errorf("the smallest allowed square PNG was refused: %v", err)
	}
	if _, err := Icon(pngOf(t, MaxIconPx, MaxIconPx, false)); err != nil {
		t.Errorf("the largest allowed square PNG was refused: %v", err)
	}
	for name, raw := range map[string][]byte{
		"too small":  pngOf(t, MinIconPx-1, MinIconPx-1, false),
		"too large":  pngOf(t, MaxIconPx+1, MaxIconPx+1, false),
		"not square": pngOf(t, 128, 96, false),
		"empty":      nil,
		"not image":  []byte("<svg xmlns='http://www.w3.org/2000/svg'></svg>"),
		"too heavy":  bytes.Repeat([]byte{0}, MaxIconBytes+1),
	} {
		if _, err := Icon(raw); err == nil {
			t.Errorf("an icon that is %s was accepted", name)
		}
	}

	var g bytes.Buffer
	if err := gif.Encode(&g, image.NewPaletted(image.Rect(0, 0, 64, 64), color.Palette{color.Black}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Icon(g.Bytes()); err == nil {
		t.Error("a GIF was accepted; only PNG and JPEG are drawn everywhere the icon goes")
	}

	var j bytes.Buffer
	if err := jpeg.Encode(&j, image.NewRGBA(image.Rect(0, 0, 128, 128)), nil); err != nil {
		t.Fatal(err)
	}
	out, err := Icon(j.Bytes())
	if err != nil {
		t.Fatalf("a square JPEG was refused: %v", err)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(out)); err != nil || format != "png" {
		t.Errorf("a JPEG upload came back as %q (%v), want a PNG", format, err)
	}
}

func TestAnIconIsStoredAsAnEightBitPNGWithNothingElse(t *testing.T) {
	raw := append(pngOf(t, 64, 64, true), []byte("TRAILING-PAYLOAD")...)
	out, err := Icon(raw)
	if err != nil {
		t.Fatalf("a 16-bit PNG with trailing bytes was refused: %v", err)
	}
	if bytes.Contains(out, []byte("TRAILING-PAYLOAD")) {
		t.Error("bytes after the image survived: the upload was stored rather than re-encoded")
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := img.(*image.NRGBA); !ok {
		t.Errorf("the stored icon decodes as %T, want 8-bit NRGBA: fpdf cannot draw a 16-bit PNG", img)
	}
}

func TestBrandingRoundTripsAndDefaultsToNothing(t *testing.T) {
	dir := t.TempDir()
	if b, err := Load(dir); err != nil || b != (Branding{}) || b.DisplayName() != DefaultName {
		t.Fatalf("a directory with no branding loaded as %+v (%v), want the default app", b, err)
	}
	if _, err := SetText(dir, "Acme", "inter"); err != nil {
		t.Fatal(err)
	}
	icon, err := Icon(pngOf(t, 64, 64, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetIcon(dir, icon); err != nil {
		t.Fatal(err)
	}
	b, err := Load(dir)
	if err != nil || b.Name != "Acme" || b.Font != "inter" || b.IconVersion == 0 {
		t.Fatalf("after saving, loaded %+v (%v)", b, err)
	}
	if !bytes.Equal(ReadIcon(dir), icon) {
		t.Error("the stored icon is not the one saved")
	}
	if b, err = SetText(dir, "", ""); err != nil || b.IconVersion == 0 {
		t.Errorf("clearing the name dropped the icon: %+v (%v)", b, err)
	}
	if b, err = ClearIcon(dir); err != nil || b.IconVersion != 0 || ReadIcon(dir) != nil {
		t.Errorf("after resetting the icon: %+v (%v), icon file still there: %v", b, err, ReadIcon(dir) != nil)
	}
}

func TestAHandEditedFileIsHeldToTheSameRules(t *testing.T) {
	dir := t.TempDir()
	raw := `{"name":"` + strings.Repeat("x", MaxNameRunes+5) + `","font":"x;y","iconVersion":42}`
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "" || b.Font != "" || b.IconVersion != 0 {
		t.Errorf("a hand-edited file loaded as %+v: an over-long name, a bad font and a version "+
			"with no icon file must each fall back to the default", b)
	}
}
