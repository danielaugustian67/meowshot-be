package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"meowshot/internal/media"
)

// readUpload reads one multipart file field, bounded by MAX_UPLOAD_MB.
// Other form fields stay available through r.FormValue afterwards.
func (s *Server) readUpload(w http.ResponseWriter, r *http.Request, field string) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, &Error{http.StatusRequestEntityTooLarge, "file_too_large", fmt.Sprintf("max upload size is %d MB", s.cfg.MaxUploadBytes>>20)}
		}
		return nil, errBadRequest("expected multipart/form-data with a %q file field", field)
	}
	f, hdr, err := r.FormFile(field)
	if err != nil {
		return nil, errBadRequest("missing %q file field", field)
	}
	defer f.Close()
	if hdr.Size > s.cfg.MaxUploadBytes {
		return nil, &Error{http.StatusRequestEntityTooLarge, "file_too_large", fmt.Sprintf("max upload size is %d MB", s.cfg.MaxUploadBytes>>20)}
	}
	return io.ReadAll(io.LimitReader(f, s.cfg.MaxUploadBytes))
}

// withImageSlot runs fn while holding one of the limited image-processing slots.
func (s *Server) withImageSlot(ctx context.Context, fn func() error) error {
	select {
	case s.imageSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.imageSlots }()
	return fn()
}

func objectKey(userID int64, kind string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return fmt.Sprintf("u/%d/%s/%s", userID, kind, hex.EncodeToString(b))
}

type storedPhoto struct {
	key, thumbKey string
	res           *media.Result
}

// storePhoto processes an upload (EXIF stripped, WebP renditions) and writes both objects to B2.
func (s *Server) storePhoto(ctx context.Context, uid int64, data []byte) (*storedPhoto, error) {
	var res *media.Result
	err := s.withImageSlot(ctx, func() (err error) {
		res, err = media.Process(data)
		return err
	})
	if err != nil {
		return nil, mediaError(err)
	}
	base := objectKey(uid, "p")
	sp := &storedPhoto{key: base + ".webp", thumbKey: base + "_t.webp", res: res}
	if err := s.store.Put(ctx, sp.key, res.Display, "image/webp"); err != nil {
		return nil, err
	}
	if err := s.store.Put(ctx, sp.thumbKey, res.Thumb, "image/webp"); err != nil {
		s.deleteObjects([]string{sp.key})
		return nil, err
	}
	return sp, nil
}

func (sp *storedPhoto) keys() []string { return []string{sp.key, sp.thumbKey} }

func (s *Server) collectKeys(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// deleteObjects removes stored files in the background; the DB rows are already gone, so a
// failure only leaves an unreachable object behind (logged for manual cleanup).
func (s *Server) deleteObjects(keys []string) {
	if len(keys) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.store.Delete(ctx, keys...); err != nil {
			s.log.Error("delete objects", "count", len(keys), "err", err)
		}
	}()
}

// locationInput is the location part of an upload/capture form or a JSON body.
type locationInput struct {
	Lat    *float64
	Lng    *float64
	Label  *string
	Source string // "device" or "manual"
	SeenAt *time.Time
}

func parseLocationForm(r *http.Request) (locationInput, error) {
	var in locationInput
	if v := r.FormValue("lat"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return in, errValidation("lat must be a number")
		}
		in.Lat = &f
	}
	if v := r.FormValue("lng"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return in, errValidation("lng must be a number")
		}
		in.Lng = &f
	}
	if v := strings.TrimSpace(r.FormValue("label")); v != "" {
		in.Label = &v
	}
	in.Source = r.FormValue("location_source")
	if v := r.FormValue("seen_at"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return in, errValidation("seen_at must be RFC3339")
		}
		in.SeenAt = &t
	}
	return in, in.validate()
}

func (in *locationInput) validate() error {
	if (in.Lat == nil) != (in.Lng == nil) {
		return errValidation("lat and lng must be given together")
	}
	if in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90 || *in.Lng < -180 || *in.Lng > 180) {
		return errValidation("lat/lng out of range")
	}
	if in.Label != nil && len([]rune(*in.Label)) > 200 {
		return errValidation("label must be at most 200 characters")
	}
	if in.Source != "" && in.Source != "device" && in.Source != "manual" {
		return errValidation("location_source must be 'device' or 'manual'")
	}
	if in.SeenAt != nil && in.SeenAt.After(time.Now().Add(24*time.Hour)) {
		return errValidation("seen_at cannot be in the future")
	}
	return nil
}

// resolveSighting applies the PRD fallback order (LOC-2): EXIF GPS, then the device location sent
// by the app, then a point picked on the map. A map pick (source=manual) always wins because the
// user chose it deliberately.
func resolveSighting(in locationInput, res *media.Result) (lat, lng *float64, source string, seenAt time.Time) {
	seenAt = time.Now()
	if res != nil && res.TakenAt != nil && res.TakenAt.Before(seenAt) {
		seenAt = *res.TakenAt
	}
	if in.SeenAt != nil {
		seenAt = *in.SeenAt
	}
	switch {
	case in.Lat != nil && in.Source == "manual":
		return in.Lat, in.Lng, "manual", seenAt
	case res != nil && res.GPS != nil:
		return &res.GPS.Lat, &res.GPS.Lng, "exif", seenAt
	case in.Lat != nil:
		src := in.Source
		if src == "" {
			src = "device"
		}
		return in.Lat, in.Lng, src, seenAt
	}
	return nil, nil, "manual", seenAt
}
