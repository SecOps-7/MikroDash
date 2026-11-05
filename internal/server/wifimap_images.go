package server

// The Wi-Fi map's backdrop images:
//
//	POST /api/router-doc/image?routerId=   upload one, answered with its id
//	GET  /api/router-doc/image?routerId=&id=
//
// The plan names an image by id (sitedoc.MapBackdrop.Image), and the file sits
// under the data directory, in wifi-map/<router id>/. Not in the plan itself:
// a floor plan is megabytes, and the plan travels to every viewer on every load.
//
// Both answer to the Wi-Fi Map page's grants on that router, as the plan does.
// An upload no saved plan names is removed by a later save of that router's
// plan, once it is an hour old: an hour leaves a second editor's fresh upload
// alone until their own save.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/safe"
	"mikrodash/internal/sitedoc"
)

const mapImageOrphanAge = time.Hour

func (s *Server) registerMapImages(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/router-doc/image", s.mapImageGet)
	mux.HandleFunc("POST /api/router-doc/image", s.mapImageSave)
}

// mapImageDir is where a router's backdrop images are kept, or "" with no data
// directory. The router id has passed topology.IsValidRouterID by then.
func (s *Server) mapImageDir(routerID string) string {
	if s.store == nil || s.store.Dir == "" {
		return ""
	}
	return filepath.Join(s.store.Dir, "wifi-map", routerID)
}

func (s *Server) mapImageGet(w http.ResponseWriter, r *http.Request) {
	rid, id := r.URL.Query().Get("routerId"), r.URL.Query().Get("id")
	if s.mayUseDoc(w, r, sitedoc.KindWifiMap, rid, "read") == nil {
		return
	}
	dir := s.mapImageDir(rid)
	if dir == "" || !sitedoc.ImageIDRe.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	raw, err := os.ReadFile(filepath.Join(dir, id))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "image/png"
	if filepath.Ext(id) == ".jpg" {
		ct = "image/jpeg"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// An id names one upload for good, so a browser may keep it.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(raw)
}

func (s *Server) mapImageSave(w http.ResponseWriter, r *http.Request) {
	rid := r.URL.Query().Get("routerId")
	sess := s.mayUseDoc(w, r, sitedoc.KindWifiMap, rid, "write")
	if sess == nil {
		return
	}
	dir := s.mapImageDir(rid)
	if dir == "" {
		writeJSONErr(w, http.StatusServiceUnavailable, "data directory unavailable")
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sitedoc.MaxImageBytes))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeJSONErr(w, http.StatusBadRequest, "the image is larger than 12 MB")
		return
	}
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, "the upload did not finish")
		return
	}
	img, ext, iw, ih, err := sitedoc.BackdropImage(raw)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		writeJSON500OK(w)
		return
	}
	id := hex.EncodeToString(b[:]) + "." + ext
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[wifi-map] %v", err)
		writeJSON500OK(w)
		return
	}
	tmp := filepath.Join(dir, id+".tmp")
	if err := os.WriteFile(tmp, img, 0o644); err != nil || os.Rename(tmp, filepath.Join(dir, id)) != nil {
		_ = os.Remove(tmp)
		log.Printf("[wifi-map] could not store an image for %s", rid)
		writeJSON500OK(w)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "sitedoc.image", TargetType: "sitedoc", TargetName: sitedoc.KindWifiMap,
		TargetID: id, RouterID: rid,
	})
	writeJSON(w, map[string]any{"id": id, "w": iw, "h": ih})
}

// pruneMapImages removes a router's backdrop images that the saved plan no
// longer names and that are older than mapImageOrphanAge.
func (s *Server) pruneMapImages(routerID string, plan sitedoc.WifiMap) {
	dir := s.mapImageDir(routerID)
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	keep := plan.ImageIDs()
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || keep[e.Name()] || time.Since(info.ModTime()) < mapImageOrphanAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			log.Printf("[wifi-map] %v", err)
		}
	}
}
