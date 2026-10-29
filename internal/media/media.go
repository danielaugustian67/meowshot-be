// Package media turns an uploaded photo into EXIF-free WebP renditions and extracts the
// GPS position and capture time once, before the metadata is thrown away.
package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"time"

	"github.com/gen2brain/heic"
	"github.com/gen2brain/webp"
	"github.com/rwcarlsen/goexif/exif"
	"golang.org/x/image/draw"
)

const (
	maxPixels   = 50_000_000 // decompression-bomb guard
	DisplayEdge = 1600
	ThumbEdge   = 400
	AvatarEdge  = 512
)

var ErrUnsupported = errors.New("unsupported image format (use JPEG, PNG, WebP or HEIC)")

type Result struct {
	Display       []byte
	Thumb         []byte
	Width, Height int // display rendition size
	GPS           *LatLng
	TakenAt       *time.Time
}

type LatLng struct{ Lat, Lng float64 }

type format int

const (
	fmtUnknown format = iota
	fmtJPEG
	fmtPNG
	fmtWEBP
	fmtHEIC
)

func sniff(data []byte) format {
	if len(data) >= 12 && string(data[4:8]) == "ftyp" {
		switch string(data[8:12]) {
		case "heic", "heix", "heim", "heis", "hevc", "hevx", "mif1", "msf1":
			return fmtHEIC
		}
	}
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return fmtJPEG
	case "image/png":
		return fmtPNG
	case "image/webp":
		return fmtWEBP
	}
	return fmtUnknown
}

// Process decodes data, reads location metadata, and returns display and thumbnail WebP files.
// Re-encoding from pixels is what strips EXIF: nothing from the original container is copied.
func Process(data []byte) (*Result, error) {
	img, meta, err := decode(data)
	if err != nil {
		return nil, err
	}
	// Orientation is applied after scaling: rotating the small renditions is far cheaper.
	display := orient(fit(img, DisplayEdge), meta.orientation)
	thumb := orient(fit(img, ThumbEdge), meta.orientation)

	res := &Result{GPS: meta.gps, TakenAt: meta.takenAt}
	b := display.Bounds()
	res.Width, res.Height = b.Dx(), b.Dy()
	if res.Display, err = encode(display, 80); err != nil {
		return nil, err
	}
	if res.Thumb, err = encode(thumb, 70); err != nil {
		return nil, err
	}
	return res, nil
}

// ProcessAvatar returns a single square-cropped WebP.
func ProcessAvatar(data []byte) ([]byte, error) {
	img, meta, err := decode(data)
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	side := min(b.Dx(), b.Dy())
	x0, y0 := b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2
	crop := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Copy(crop, image.Point{}, img, image.Rect(x0, y0, x0+side, y0+side), draw.Src, nil)
	return encode(orient(fit(crop, AvatarEdge), meta.orientation), 80)
}

type metadata struct {
	gps         *LatLng
	takenAt     *time.Time
	orientation int
}

// decode returns the raw pixels; for JPEG, meta.orientation still has to be applied by the caller.
func decode(data []byte) (image.Image, metadata, error) {
	meta := metadata{orientation: 1}
	f := sniff(data)

	var cfg image.Config
	var err error
	switch f {
	case fmtJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case fmtPNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case fmtWEBP:
		cfg, err = webp.DecodeConfig(bytes.NewReader(data))
	case fmtHEIC:
		cfg, err = heic.DecodeConfig(bytes.NewReader(data))
	default:
		return nil, meta, ErrUnsupported
	}
	if err != nil {
		return nil, meta, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, meta, errors.New("image dimensions too large")
	}

	var img image.Image
	switch f {
	case fmtJPEG:
		meta = jpegMeta(data)
		img, err = jpeg.Decode(bytes.NewReader(data))
	case fmtPNG:
		img, err = png.Decode(bytes.NewReader(data))
	case fmtWEBP:
		if x, xerr := webp.DecodeExif(bytes.NewReader(data)); xerr == nil {
			meta = fromFields(x.GPSLatitude, x.GPSLongitude, x.DateTimeOriginal)
		}
		img, err = webp.Decode(bytes.NewReader(data), webp.Options{AutoRotate: true})
	case fmtHEIC:
		if x, xerr := heic.DecodeExif(bytes.NewReader(data)); xerr == nil {
			meta = fromFields(x.GPSLatitude, x.GPSLongitude, x.DateTimeOriginal)
		}
		img, err = heic.Decode(bytes.NewReader(data)) // orientation already applied
	}
	if err != nil {
		return nil, meta, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return img, meta, nil
}

func jpegMeta(data []byte) metadata {
	m := metadata{orientation: 1}
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return m
	}
	if lat, lng, err := x.LatLong(); err == nil && validLatLng(lat, lng) {
		m.gps = &LatLng{Lat: lat, Lng: lng}
	}
	if t, err := x.DateTime(); err == nil && !t.IsZero() {
		m.takenAt = &t
	}
	if tag, err := x.Get(exif.Orientation); err == nil {
		if o, err := tag.Int(0); err == nil {
			m.orientation = o
		}
	}
	return m
}

func fromFields(lat, lng float64, original string) metadata {
	m := metadata{orientation: 1}
	if validLatLng(lat, lng) {
		m.gps = &LatLng{Lat: lat, Lng: lng}
	}
	if t, err := time.ParseInLocation("2006:01:02 15:04:05", original, time.Local); err == nil {
		m.takenAt = &t
	}
	return m
}

func validLatLng(lat, lng float64) bool {
	return lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180 && !(lat == 0 && lng == 0)
}

// fit scales img down so its longer edge is at most edge pixels.
func fit(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= edge && h <= edge {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Copy(dst, image.Point{}, img, b, draw.Src, nil)
		return dst
	}
	if w >= h {
		h = max(1, h*edge/w)
		w = edge
	} else {
		w = max(1, w*edge/h)
		h = edge
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

func encode(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := webp.Encode(&buf, img, webp.Options{Quality: quality, Method: 4}); err != nil {
		return nil, fmt.Errorf("encode webp: %w", err)
	}
	return buf.Bytes(), nil
}

// orient applies an EXIF orientation (1–8) so pixels are stored upright.
func orient(img image.Image, o int) image.Image {
	if o < 2 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
