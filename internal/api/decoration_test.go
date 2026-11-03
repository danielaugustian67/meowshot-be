package api

import (
	"fmt"
	"strings"
	"testing"

	"meowshot/internal/media/mediatest"
)

func TestPhotoDecoration(t *testing.T) {
	e := setup(t)
	alice, _ := e.register("alice")
	bob, _ := e.register("bob")
	img := mediatest.JPEGWithGPS(t, 800, 600, 1, 1)
	const deco = `{"paper":3,"motif":-1,"stickers":[{"id":"paw","x":0.25,"y":0.75,"scale":0.2,"rotation":-30}]}`

	r := e.expect(e.upload(alice, "/v1/captures", map[string]string{"decoration": deco}, img), 201)
	if r.num("photo.decoration.paper") != 3 || r.num("photo.decoration.motif") != -1 || r.str("photo.decoration.stickers.0.id") != "paw" {
		t.Fatalf("capture decoration: %s", r.Raw)
	}
	catID, photoID := int64(r.num("cat.id")), int64(r.num("photo.id"))

	r = e.expect(e.do(alice, "GET", fmt.Sprintf("/v1/cats/%d/photos", catID), nil), 200)
	if r.num("items.0.decoration.stickers.0.rotation") != -30 {
		t.Fatalf("listed decoration: %s", r.Raw)
	}

	// Editing the caption keeps it, and everyone who can see the post sees it.
	r = e.expect(e.do(alice, "PATCH", fmt.Sprintf("/v1/photos/%d", photoID), map[string]any{"caption": "hi"}), 200)
	if r.num("decoration.paper") != 3 {
		t.Fatalf("patched decoration: %s", r.Raw)
	}
	r = e.expect(e.do(alice, "POST", "/v1/posts", map[string]any{
		"cat_id": catID, "photo_ids": []int64{photoID}, "visibility": "PUBLIC",
	}), 201)
	r = e.expect(e.do(bob, "GET", fmt.Sprintf("/v1/posts/%d", int64(r.num("id"))), nil), 200)
	if r.num("photos.0.decoration.stickers.0.x") != 0.25 {
		t.Fatalf("post decoration: %s", r.Raw)
	}

	// A photo without one reports null, and an empty one stores nothing.
	for _, fields := range []map[string]string{{}, {"decoration": "{}"}} {
		r = e.expect(e.upload(alice, "/v1/captures", fields, img), 201)
		if !strings.Contains(string(r.Raw), `"decoration":null`) {
			t.Fatalf("no decoration: %s", r.Raw)
		}
	}

	sticker := `{"id":"paw","x":0.5,"y":0.5,"scale":0.2,"rotation":0}`
	bad := map[string]string{
		"not json":      `nope`,
		"unknown field": `{"text":"hi"}`,
		"paper":         `{"paper":32}`,
		"motif":         `{"motif":-2}`,
		"sticker id":    `{"stickers":[{"id":"<b>","x":0.5,"y":0.5,"scale":0.2}]}`,
		"position":      `{"stickers":[{"id":"paw","x":1.5,"y":0.5,"scale":0.2}]}`,
		"scale":         `{"stickers":[{"id":"paw","x":0.5,"y":0.5,"scale":0}]}`,
		"rotation":      `{"stickers":[{"id":"paw","x":0.5,"y":0.5,"scale":0.2,"rotation":999}]}`,
		"too many":      `{"stickers":[` + strings.TrimSuffix(strings.Repeat(sticker+",", 9), ",") + `]}`,
		"too large":     `{"stickers":[` + strings.Repeat(" ", 2048) + `]}`,
	}
	for name, raw := range bad {
		if res := e.upload(alice, "/v1/captures", map[string]string{"decoration": raw}, img); res.Status != 422 {
			t.Errorf("%s: status = %d, want 422; body: %s", name, res.Status, res.Raw)
		}
	}
}
