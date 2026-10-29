package media

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gen2brain/webp"

	"meowshot/internal/media/mediatest"
)

func TestProcessExtractsGPSAndStripsExif(t *testing.T) {
	src := mediatest.JPEGWithGPS(t, 2400, 1200, -6.2088, 106.8456)
	res, err := Process(src)
	if err != nil {
		t.Fatal(err)
	}
	if res.GPS == nil {
		t.Fatal("GPS not extracted")
	}
	if math.Abs(res.GPS.Lat-(-6.2088)) > 1e-4 || math.Abs(res.GPS.Lng-106.8456) > 1e-4 {
		t.Fatalf("GPS = %+v", res.GPS)
	}
	if res.Width != DisplayEdge || res.Height != DisplayEdge/2 {
		t.Fatalf("display size = %dx%d", res.Width, res.Height)
	}
	for name, out := range map[string][]byte{"display": res.Display, "thumb": res.Thumb} {
		if string(out[0:4]) != "RIFF" || string(out[8:12]) != "WEBP" {
			t.Fatalf("%s is not WebP", name)
		}
		if bytes.Contains(out, []byte("Exif")) || bytes.Contains(out, []byte("EXIF")) {
			t.Fatalf("%s still carries EXIF", name)
		}
	}
	thumb, err := decodeWebPConfig(res.Thumb)
	if err != nil {
		t.Fatal(err)
	}
	if thumb.Width != ThumbEdge {
		t.Fatalf("thumb width = %d", thumb.Width)
	}
}

func TestOrientRotates(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	out := orient(img, 6) // 90° clockwise
	if b := out.Bounds(); b.Dx() != 2 || b.Dy() != 4 {
		t.Fatalf("bounds = %v", b)
	}
	if r, _, _, _ := out.At(1, 0).RGBA(); r>>8 != 255 {
		t.Fatal("top-left pixel should move to top-right")
	}
}

func TestRejectsNonImage(t *testing.T) {
	if _, err := Process([]byte("definitely not an image, just some text")); err == nil {
		t.Fatal("expected error")
	}
}

func decodeWebPConfig(b []byte) (image.Config, error) { return webp.DecodeConfig(bytes.NewReader(b)) }
