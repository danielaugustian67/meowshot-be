package api

import (
	"fmt"
	"strings"
	"testing"

	"meowshot/internal/media/mediatest"
)

func TestMergeKeepsWhatWasWritten(t *testing.T) {
	e := setup(t)
	alice, _ := e.register("alice")
	img := mediatest.JPEGWithGPS(t, 800, 600, 1, 1)
	newCat := func(body map[string]any) int64 {
		id := int64(e.expect(e.upload(alice, "/v1/captures", map[string]string{}, img), 201).num("cat.id"))
		e.expect(e.do(alice, "PATCH", fmt.Sprintf("/v1/cats/%d", id), body), 200)
		return id
	}
	merge := func(from, into int64) resp {
		return e.expect(e.do(alice, "POST", fmt.Sprintf("/v1/cats/%d/merge", from), map[string]any{"into_cat_id": into}), 200)
	}

	// The album had no name, notes or heart, so it takes them.
	album := newCat(map[string]any{})
	r := merge(newCat(map[string]any{"name": "Oyen", "notes": "takut air", "is_favorite": true}), album)
	if r.str("name") != "Oyen" || r.str("notes") != "takut air" || r.get("is_favorite") != true {
		t.Fatalf("filled in: %s", r.Raw)
	}

	// Its own name stays, and both notes are kept.
	r = merge(newCat(map[string]any{"name": "Belang", "notes": "doyan ikan"}), album)
	if r.str("name") != "Oyen" || r.str("notes") != "takut air\n\ndoyan ikan" || r.num("photo_count") != 3 {
		t.Fatalf("joined: %s", r.Raw)
	}

	// Two long notes still fit the limit.
	long := strings.Repeat("a", 2000)
	other := newCat(map[string]any{"notes": long})
	r = merge(newCat(map[string]any{"notes": long}), other)
	if len(r.str("notes")) != 2000 {
		t.Fatalf("long notes: %d", len(r.str("notes")))
	}

	// Without any notes it stays empty rather than an empty string.
	r = merge(newCat(map[string]any{}), newCat(map[string]any{}))
	if !strings.Contains(string(r.Raw), `"notes":null`) {
		t.Fatalf("no notes: %s", r.Raw)
	}
}
