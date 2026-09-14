// Package branding is what this install calls itself: the name and icon shown
// in the top-left wordmark, the browser tab, the login page, PDF report headers
// and report emails.
//
// ── WHY IT EXISTS ───────────────────────────────────────────────────────────
//
// Issue #131: an operator running several MikroDash instances, open in several
// tabs, could not tell which one they were looking at. An editable wordmark was
// agreed rather than another item in a header that already overflows on a phone.
//
// ── WHY IT IS NOT A SETTING ─────────────────────────────────────────────────
//
// The icon is a file, and a name without its icon is half of one thing. Keeping
// both here, beside `settings.json` rather than inside it, also keeps them out
// of the settings tables and the recordings those are checked against.
//
// The zero value is the default app: no file, no icon, the MikroDash wordmark.
package branding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg" // registers JPEG with image.Decode, for uploads
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultName is what an install is called until an administrator names it.
const DefaultName = "MikroDash"

const (
	fileName = "branding.json"
	iconName = "branding-icon.png"

	// MaxNameRunes bounds the name. It sits in a top bar beside the page title
	// and in a PDF header beside the report title.
	MaxNameRunes = 40
	// MaxIconBytes bounds an upload before anything decodes it.
	MaxIconBytes = 512 << 10
	// MinIconPx and MaxIconPx bound a square icon. It is shown at 24px, so 64 is
	// the smallest that stays sharp on a high-density screen, and 512 is more
	// than any place it is drawn needs.
	MinIconPx = 64
	MaxIconPx = 512
)

// Branding is what branding.json holds.
type Branding struct {
	// Name replaces "MikroDash". Empty means the default.
	Name string `json:"name"`
	// Font is an appearance font id ("syne", "inter", ...). Empty means the
	// wordmark's own font. Checked here by shape only: the browser owns the list
	// and falls back to the default for an id it does not know.
	Font string `json:"font"`
	// IconVersion is when the custom icon was stored, in unix milliseconds, or 0
	// for the default icon. The icon's URL carries it, so a browser cannot keep
	// showing a cached old one.
	IconVersion int64 `json:"iconVersion"`
}

// DisplayName is the name to show: the custom one, or the default.
func (b Branding) DisplayName() string {
	if b.Name == "" {
		return DefaultName
	}
	return b.Name
}

var fontID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// CleanName trims a proposed name and checks it. Empty is valid and means the
// default.
func CleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", errors.New("the name is not valid text")
	}
	if n := utf8.RuneCountInString(s); n > MaxNameRunes {
		return "", fmt.Errorf("the name is %d characters; the most is %d", n, MaxNameRunes)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("the name contains a control character")
		}
	}
	return s, nil
}

// CleanFont checks a font id. Empty is valid and means the wordmark's own font.
func CleanFont(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if !fontID.MatchString(s) {
		return "", errors.New("the font is not one of the app's fonts")
	}
	return s, nil
}

// Icon checks an uploaded icon and returns it as an 8-bit PNG.
//
// ── RE-ENCODED, NEVER STORED AS SENT ────────────────────────────────────────
//
// Decoding and encoding again keeps the pixels and drops everything else a file
// can carry: metadata, trailing bytes, a second format hiding behind the first.
// It also hands the PDF renderer the one format it draws. fpdf refuses a 16-bit
// PNG, which is why the pixels are copied into an 8-bit image first.
//
// The size is read from the header BEFORE the full decode, so an image that
// claims to be huge is refused without being expanded.
func Icon(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("the icon is empty")
	}
	if len(raw) > MaxIconBytes {
		return nil, fmt.Errorf("the icon is larger than %d KB", MaxIconBytes>>10)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, errors.New("the icon must be a PNG or JPEG image")
	}
	if cfg.Width != cfg.Height {
		return nil, fmt.Errorf("the icon must be square; this one is %d × %d px", cfg.Width, cfg.Height)
	}
	if cfg.Width < MinIconPx || cfg.Width > MaxIconPx {
		return nil, fmt.Errorf("the icon must be %d to %d px; this one is %d px", MinIconPx, MaxIconPx, cfg.Width)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errors.New("the icon could not be read")
	}
	flat := image.NewNRGBA(img.Bounds())
	draw.Draw(flat, flat.Bounds(), img, img.Bounds().Min, draw.Src)
	var out bytes.Buffer
	if err := png.Encode(&out, flat); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// mu serialises the read-modify-write of branding.json, so two administrators
// saving at once cannot lose one change.
var mu sync.Mutex

// Load reads a data directory's branding. A missing file is the default app,
// not an error, and a hand-edited file is held to the same rules as a save.
func Load(dir string) (Branding, error) {
	raw, err := os.ReadFile(filepath.Join(dir, fileName))
	if errors.Is(err, os.ErrNotExist) {
		return Branding{}, nil
	}
	if err != nil {
		return Branding{}, err
	}
	var b Branding
	if err := json.Unmarshal(raw, &b); err != nil {
		return Branding{}, fmt.Errorf("branding.json: %w", err)
	}
	if n, err := CleanName(b.Name); err == nil {
		b.Name = n
	} else {
		b.Name = ""
	}
	if f, err := CleanFont(b.Font); err == nil {
		b.Font = f
	} else {
		b.Font = ""
	}
	if b.IconVersion != 0 {
		if _, err := os.Stat(IconPath(dir)); err != nil {
			b.IconVersion = 0
		}
	}
	return b, nil
}

// SetText stores a checked name and font, keeping the icon.
func SetText(dir, name, font string) (Branding, error) {
	mu.Lock()
	defer mu.Unlock()
	b, err := Load(dir)
	if err != nil {
		return b, err
	}
	b.Name, b.Font = name, font
	return b, save(dir, b)
}

// SetIcon stores an icon that has been through Icon, and records its version.
func SetIcon(dir string, pngBytes []byte) (Branding, error) {
	mu.Lock()
	defer mu.Unlock()
	b, err := Load(dir)
	if err != nil {
		return b, err
	}
	if err := writeAtomic(IconPath(dir), pngBytes); err != nil {
		return b, err
	}
	b.IconVersion = time.Now().UnixMilli()
	return b, save(dir, b)
}

// ClearIcon removes the custom icon, going back to the default.
func ClearIcon(dir string) (Branding, error) {
	mu.Lock()
	defer mu.Unlock()
	b, err := Load(dir)
	if err != nil {
		return b, err
	}
	if err := os.Remove(IconPath(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return b, err
	}
	b.IconVersion = 0
	return b, save(dir, b)
}

// IconPath is where the custom icon is kept.
func IconPath(dir string) string { return filepath.Join(dir, iconName) }

// ReadIcon returns the stored custom icon, or nil when the default is in use.
func ReadIcon(dir string) []byte {
	b, err := os.ReadFile(IconPath(dir))
	if err != nil {
		return nil
	}
	return b
}

func save(dir string, b Branding) error {
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, fileName), raw)
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
