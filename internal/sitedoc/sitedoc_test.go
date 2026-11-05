package sitedoc

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

// A floor-plan object's colour is drawn into a style, so anything but a hex
// colour is cleared when the plan is saved: a stored value carrying its own
// CSS declarations would run in every browser that opens the plan.
func TestWifiMapKeepsOnlyHexColours(t *testing.T) {
	for in, want := range map[string]string{
		"#38bdf8": "#38bdf8",
		"#abc":    "#abc",
		"":        "",
		"red":     "",
		"#38bdf8;background:url(https://x.test/)": "",
		"red;background:url(https://x.test/)":     "",
		"#12345":                                  "",
		"rgb(1,2,3)":                              "",
	} {
		raw, _ := json.Marshal(map[string]any{"objects": []map[string]any{{"id": "o1", "kind": "building", "colour": in}}})
		got := CleanWifiMap(raw)
		if len(got.Objects) != 1 || got.Objects[0].Colour != want {
			t.Errorf("colour %q was kept as %q, want %q", in, got.Objects[0].Colour, want)
		}
	}
}

// TestABackdropPerFloorAndTilesOnlyUnderTheSite. One backdrop a floor, the first
// one sent; map tiles only under floor 0; an image only by a name that cannot
// leave its directory; and a map sets the scale from its latitude.
func TestABackdropPerFloorAndTilesOnlyUnderTheSite(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"metresPerUnit": 2, "backdrops": []map[string]any{
		{"floor": 0, "kind": "tiles", "provider": "osm", "lat": 55, "lon": 37, "x": 10, "y": 20, "rotation": 400,
			"maxZoom": 30},
		{"floor": 0, "kind": "image", "image": "0123456789abcdef.png", "w": 100, "h": 50},
		{"floor": 2, "kind": "tiles", "provider": "esri", "lat": 55, "lon": 37},
		{"floor": 2, "kind": "image", "image": "../../settings.json", "w": 100, "h": 50},
		{"floor": 3, "kind": "image", "image": "0123456789abcdef.jpg", "w": 0, "h": 50},
		{"floor": 1, "kind": "image", "image": "0123456789abcdef.jpg", "w": 640, "h": 480, "opacity": 0},
		{"floor": 4, "kind": "video", "image": "0123456789abcdef.jpg", "w": 640, "h": 480},
	}})
	got := CleanWifiMap(raw)
	if len(got.Backdrops) != 2 {
		t.Fatalf("kept %+v; want the site's tiles and floor 1's image", got.Backdrops)
	}
	if b := got.Backdrops[0]; b.Floor != 0 || b.Kind != "tiles" || b.Opacity != 1 || b.Image != "" || b.Rotation != 180 || b.MaxZoom != 0 {
		t.Errorf("site backdrop = %+v", b)
	}
	if b := got.Backdrops[1]; b.Floor != 1 || b.Kind != "image" || b.Opacity != 1 {
		t.Errorf("floor 1 backdrop = %+v; an unset opacity draws it opaque", b)
	}
	if mpu := got.MetresPerUnit; mpu < 0.170 || mpu > 0.172 {
		t.Errorf("metres per unit = %v at 55°N, want about 0.171 from the map", mpu)
	}
	if ids := got.ImageIDs(); len(ids) != 1 || !ids["0123456789abcdef.jpg"] {
		t.Errorf("image ids = %v", ids)
	}
}

// TestACustomTileURLIsHeldToHttpsAndItsPlaceholders. Every viewer's browser
// fetches the template and it is drawn into an attribute.
func TestACustomTileURLIsHeldToHttpsAndItsPlaceholders(t *testing.T) {
	for in, ok := range map[string]bool{
		"https://tiles.example.net/{z}/{x}/{y}.png":          true,
		"https://example.net/tile?x={x}&y={y}&z={z}&lang=ru": true,
		"http://tiles.example.net/{z}/{x}/{y}.png":           false,
		"javascript:alert(1)//{z}{x}{y}":                     false,
		"https://example.net/{z}/{x}.png":                    false,
		"https://example.net/{z}/{x}/{y}\" onload=\"x":       false,
		"https://example.net/{z}/{x}/{y}/{s}":                false,
		"https://example.net/{z}/{x}/{y} .png":               false,
	} {
		if got := CleanTileURL(in); (got != "") != ok {
			t.Errorf("CleanTileURL(%q) = %q, want kept %v", in, got, ok)
		}
	}
}

// TestABackdropImageIsReencodedAndBounded. Only PNG and JPEG are taken, a
// header that claims too many pixels is refused before the decode, and what is
// stored is a fresh encoding.
func TestABackdropImageIsReencodedAndBounded(t *testing.T) {
	var small bytes.Buffer
	_ = png.Encode(&small, image.NewGray(image.Rect(0, 0, 64, 48)))
	tail := append(append([]byte{}, small.Bytes()...), []byte("<script>trailing</script>")...)
	out, ext, w, h, err := BackdropImage(tail)
	if err != nil || ext != "png" || w != 64 || h != 48 || bytes.Contains(out, []byte("<script>")) {
		t.Fatalf("a PNG with trailing bytes gave %q %dx%d %v", ext, w, h, err)
	}
	var jp bytes.Buffer
	_ = jpeg.Encode(&jp, image.NewRGBA(image.Rect(0, 0, 32, 32)), nil)
	if _, ext, _, _, err := BackdropImage(jp.Bytes()); err != nil || ext != "jpg" {
		t.Errorf("a JPEG gave %q %v", ext, err)
	}
	var big bytes.Buffer
	_ = png.Encode(&big, image.NewGray(image.Rect(0, 0, 5000, 4000)))
	if _, _, _, _, err := BackdropImage(big.Bytes()); err == nil {
		t.Error("a 20 megapixel image was taken")
	}
	for _, bad := range [][]byte{nil, []byte("GIF89a"), []byte("<svg/>")} {
		if _, _, _, _, err := BackdropImage(bad); err == nil {
			t.Errorf("%q was taken as an image", bad)
		}
	}
}

// TestABuildingMayBeAnOutlineAndAWallMayNot. An L-shaped building keeps its
// corners and its box is worked out from them; a wall's points are dropped.
func TestABuildingMayBeAnOutlineAndAWallMayNot(t *testing.T) {
	l := []map[string]float64{{"x": 0, "y": 0}, {"x": 100, "y": 0}, {"x": 100, "y": 40},
		{"x": 40, "y": 40}, {"x": 40, "y": 90}, {"x": 0, "y": 90}}
	raw, _ := json.Marshal(map[string]any{"objects": []map[string]any{
		{"id": "b", "kind": "building", "points": l, "x": 999, "w": 1, "floors": 3},
		{"id": "w", "kind": "wall", "points": l, "x": 5, "y": 6, "w": 7, "h": 8},
	}})
	got := CleanWifiMap(raw)
	b, w := got.Objects[0], got.Objects[1]
	if len(b.Points) != 6 || b.X != 0 || b.W != 100 || b.H != 90 || b.Floors != 3 {
		t.Errorf("building = %+v; want its six corners, its box from them, and its storeys", b)
	}
	if len(w.Points) != 0 || w.X != 5 || w.W != 7 {
		t.Errorf("wall = %+v; a wall is a box", w)
	}
}

// TestAPinnedClientIsAMACOnceWithAFloor.
func TestAPinnedClientIsAMACOnceWithAFloor(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"clients": []map[string]any{
		{"mac": "aa:bb:cc:dd:ee:01", "label": "Gate camera", "x": 10, "y": 20, "floor": 0},
		{"mac": "AA:BB:CC:DD:EE:01", "label": "the same, again"},
		{"mac": "not a mac", "label": "x"},
		{"mac": "AA:BB:CC:DD:EE:02", "floor": 99},
	}})
	got := CleanWifiMap(raw).Clients
	if len(got) != 2 || got[0].MAC != "AA:BB:CC:DD:EE:01" || got[0].Label != "Gate camera" || got[0].Floor != 1 {
		t.Fatalf("clients = %+v", got)
	}
	if got[1].Floor != MaxFloors {
		t.Errorf("floor = %d, want it held to %d", got[1].Floor, MaxFloors)
	}
}
