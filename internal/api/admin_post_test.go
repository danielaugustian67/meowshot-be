package api

import (
	"context"
	"fmt"
	"testing"

	"meowshot/internal/media/mediatest"
)

func TestAdminDeletesPost(t *testing.T) {
	e := setup(t)
	alice, _ := e.register("alice")
	bob, _ := e.register("bob")
	admin, adminID := e.register("admin")
	if _, err := e.s.db.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE id = $1`, adminID); err != nil {
		t.Fatal(err)
	}

	r := e.expect(e.upload(alice, "/v1/captures", nil, mediatest.JPEGWithGPS(t, 800, 600, 1, 1)), 201)
	catID, photoID := int64(r.num("cat.id")), int64(r.num("photo.id"))
	r = e.expect(e.do(alice, "POST", "/v1/posts", map[string]any{
		"cat_id": catID, "photo_ids": []int64{photoID}, "visibility": "PUBLIC",
	}), 201)
	post := fmt.Sprintf("/v1/posts/%d", int64(r.num("id")))
	adminPost := fmt.Sprintf("/v1/admin/posts/%d", int64(r.num("id")))

	if me := e.expect(e.do(admin, "GET", "/v1/me", nil), 200); me.str("role") != "admin" {
		t.Fatalf("admin role: %s", me.Raw)
	}
	if me := e.expect(e.do(bob, "GET", "/v1/me", nil), 200); me.str("role") != "user" {
		t.Fatalf("user role: %s", me.Raw)
	}

	// Neither a stranger nor the author gets through the admin route, and an admin can't use the owner's.
	e.expect(e.do("", "DELETE", adminPost, nil), 401)
	e.expect(e.do(bob, "DELETE", adminPost, nil), 403)
	e.expect(e.do(alice, "DELETE", adminPost, nil), 403)
	e.expect(e.do(admin, "DELETE", post, nil), 404)
	e.expect(e.do(bob, "GET", post, nil), 200)

	e.expect(e.do(admin, "DELETE", adminPost, nil), 204)
	e.expect(e.do(bob, "GET", post, nil), 404)
	e.expect(e.do(admin, "DELETE", adminPost, nil), 404)

	// The photo goes with it, and the cat no longer has it as a cover.
	r = e.expect(e.do(alice, "GET", fmt.Sprintf("/v1/cats/%d/photos", catID), nil), 200)
	if r.get("items.0") != nil {
		t.Fatalf("photo survived: %s", r.Raw)
	}
	r = e.expect(e.do(alice, "GET", fmt.Sprintf("/v1/cats/%d", catID), nil), 200)
	if r.get("cover") != nil {
		t.Fatalf("cover survived: %s", r.Raw)
	}
}
