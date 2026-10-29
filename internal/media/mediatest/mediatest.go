// Package mediatest builds test images (with EXIF GPS) for media and API tests.
package mediatest

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"testing"
)

// JPEGWithGPS returns a JPEG of w×h carrying an EXIF GPS block for (lat, lng).
func JPEGWithGPS(t testing.TB, w, h int, lat, lng float64) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	plain := buf.Bytes()

	tiff := gpsTIFF(lat, lng)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(2+6+len(tiff)))
	app1 = append(app1, "Exif\x00\x00"...)
	app1 = append(app1, tiff...)

	out := append([]byte{}, plain[:2]...) // SOI
	out = append(out, app1...)
	return append(out, plain[2:]...)
}

func gpsTIFF(lat, lng float64) []byte {
	be := binary.BigEndian
	b := make([]byte, 128)
	copy(b, "MM")
	be.PutUint16(b[2:], 42)
	be.PutUint32(b[4:], 8)
	// IFD0: one entry pointing at the GPS IFD (offset 26).
	be.PutUint16(b[8:], 1)
	entry := func(at int, tag, typ uint16, count, value uint32) {
		be.PutUint16(b[at:], tag)
		be.PutUint16(b[at+2:], typ)
		be.PutUint32(b[at+4:], count)
		be.PutUint32(b[at+8:], value)
	}
	entry(10, 0x8825, 4, 1, 26)
	// GPS IFD at 26: 4 entries, rational data at 80 and 104.
	be.PutUint16(b[26:], 4)
	latRef, lngRef := uint32('N')<<24, uint32('E')<<24
	if lat < 0 {
		latRef = uint32('S') << 24
	}
	if lng < 0 {
		lngRef = uint32('W') << 24
	}
	entry(28, 1, 2, 2, latRef)
	entry(40, 2, 5, 3, 80)
	entry(52, 3, 2, 2, lngRef)
	entry(64, 4, 5, 3, 104)
	dms := func(at int, v float64) {
		v = math.Abs(v)
		d := math.Floor(v)
		m := math.Floor((v - d) * 60)
		sec := (v - d - m/60) * 3600
		for i, r := range [][2]uint32{{uint32(d), 1}, {uint32(m), 1}, {uint32(math.Round(sec * 100)), 100}} {
			be.PutUint32(b[at+i*8:], r[0])
			be.PutUint32(b[at+i*8+4:], r[1])
		}
	}
	dms(80, lat)
	dms(104, lng)
	return b
}
