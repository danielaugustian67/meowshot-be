package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"meowshot/internal/auth"
	"meowshot/internal/config"
	"meowshot/internal/db"
	"meowshot/internal/geo"
	"meowshot/internal/media/mediatest"
	"meowshot/internal/ratelimit"
)

// Integration tests run against a real Postgres: TEST_DATABASE_URL=postgres://... go test ./...
// The database's public schema is wiped first.

type memStore struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *memStore) Put(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = body
	return nil
}

func (m *memStore) Delete(_ context.Context, keys ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		delete(m.objs, k)
	}
	return nil
}

func (m *memStore) SignedURL(_ context.Context, key string) (string, error) {
	return "https://signed.example/" + key + "?sig=x", nil
}

func (m *memStore) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.objs)
}

type fakeGoogle map[string]*auth.GoogleIdentity

func (f fakeGoogle) Enabled() bool { return true }
func (f fakeGoogle) Verify(_ context.Context, tok string) (*auth.GoogleIdentity, error) {
	if id, ok := f[tok]; ok {
		return id, nil
	}
	return nil, auth.ErrInvalidToken
}

type fakeMail struct {
	mu   sync.Mutex
	last string
}

func (f *fakeMail) Send(_, _, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = body
	return nil
}

type env struct {
	t     *testing.T
	srv   *httptest.Server
	s     *Server
	store *memStore
	mail  *fakeMail
}

func setup(t *testing.T) *env {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		JWTSecret:       []byte("test-jwt-secret-test-jwt-secret-123"),
		LocationSecret:  []byte("test-location-secret-test-location-1"),
		AccessTokenTTL:  time.Minute,
		RefreshTokenTTL: time.Hour,
		MaxUploadBytes:  10 << 20,
	}
	e := &env{t: t, store: &memStore{objs: map[string][]byte{}}, mail: &fakeMail{}}
	e.s = New(Deps{
		Config: cfg, DB: pool, Storage: e.store, Mail: e.mail,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Google: fakeGoogle{
			"g-alice": {Subject: "google-alice", Email: "alice@example.com", EmailVerified: true},
			"g-new":   {Subject: "google-new", Email: "new.person@example.com", EmailVerified: true},
		},
	})
	e.s.limitAuth = ratelimit.New(1000, time.Minute) // the suite logs in far more than 10 times a minute
	e.srv = httptest.NewServer(e.s.Routes())
	t.Cleanup(e.srv.Close)
	return e
}

type resp struct {
	Status int
	Body   map[string]any
	Raw    []byte
}

func (r resp) str(path string) string { v, _ := r.get(path).(string); return v }
func (r resp) num(path string) float64 {
	v, _ := r.get(path).(float64)
	return v
}

// get walks a dotted path like "user.id" or "items.0.id".
func (r resp) get(path string) any {
	var cur any = r.Body
	for _, part := range strings.Split(path, ".") {
		switch c := cur.(type) {
		case map[string]any:
			cur = c[part]
		case []any:
			var i int
			fmt.Sscanf(part, "%d", &i)
			if i >= len(c) {
				return nil
			}
			cur = c[i]
		default:
			return nil
		}
	}
	return cur
}

func (e *env) do(token, method, path string, body any) resp {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	return e.send(token, req)
}

func (e *env) upload(token, path string, fields map[string]string, img []byte) resp {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("photo", "cat.jpg")
	fw.Write(img)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return e.send(token, req)
}

func (e *env) send(token string, req *http.Request) resp {
	e.t.Helper()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw}
	json.Unmarshal(raw, &out.Body)
	return out
}

func (e *env) expect(r resp, status int) resp {
	e.t.Helper()
	if r.Status != status {
		e.t.Fatalf("status = %d, want %d; body: %s", r.Status, status, r.Raw)
	}
	return r
}

func (e *env) register(name string) (token string, id int64) {
	e.t.Helper()
	r := e.expect(e.do("", "POST", "/v1/auth/register", map[string]any{
		"email": name + "@example.com", "password": "password-" + name, "username": name,
	}), 201)
	return r.str("access_token"), int64(r.num("user.id"))
}

func TestAPIEndToEnd(t *testing.T) {
	e := setup(t)
	alice, aliceID := e.register("alice")
	bob, bobID := e.register("bob")
	carol, carolID := e.register("carol")
	_ = bobID

	t.Run("auth", func(t *testing.T) {
		e.expect(e.do("", "POST", "/v1/auth/register", map[string]any{"email": "ALICE@example.com", "password": "password1", "username": "alice2"}), 409)
		e.expect(e.do("", "POST", "/v1/auth/login", map[string]any{"email": "alice@example.com", "password": "wrong-password"}), 401)
		e.expect(e.do("", "POST", "/v1/auth/login", map[string]any{"email": "alice", "password": "password-alice"}), 200)
		e.expect(e.do("", "GET", "/v1/me", nil), 401)
		e.expect(e.do("not-a-token", "GET", "/v1/me", nil), 401)

		r := e.expect(e.do("", "POST", "/v1/auth/login", map[string]any{"email": "bob@example.com", "password": "password-bob"}), 200)
		old := r.str("refresh_token")
		r2 := e.expect(e.do("", "POST", "/v1/auth/refresh", map[string]any{"refresh_token": old}), 200)
		rotated := r2.str("refresh_token")
		// Reusing the old token is treated as theft and kills the rotated one as well.
		e.expect(e.do("", "POST", "/v1/auth/refresh", map[string]any{"refresh_token": old}), 401)
		e.expect(e.do("", "POST", "/v1/auth/refresh", map[string]any{"refresh_token": rotated}), 401)
	})

	t.Run("password reset", func(t *testing.T) {
		e.expect(e.do("", "POST", "/v1/auth/password/forgot", map[string]any{"email": "nobody@example.com"}), 202)
		e.expect(e.do("", "POST", "/v1/auth/password/forgot", map[string]any{"email": "carol@example.com"}), 202)
		code := regexp.MustCompile(`\d{6}`).FindString(e.mail.last)
		if code == "" {
			t.Fatal("no reset code mailed")
		}
		e.expect(e.do("", "POST", "/v1/auth/password/reset", map[string]any{"email": "carol@example.com", "code": "000000x", "new_password": "brand-new-pass"}), 400)
		e.expect(e.do("", "POST", "/v1/auth/password/reset", map[string]any{"email": "carol@example.com", "code": code, "new_password": "brand-new-pass"}), 204)
		e.expect(e.do("", "POST", "/v1/auth/password/reset", map[string]any{"email": "carol@example.com", "code": code, "new_password": "another-pass"}), 400)
		r := e.expect(e.do("", "POST", "/v1/auth/login", map[string]any{"email": "carol@example.com", "password": "brand-new-pass"}), 200)
		carol = r.str("access_token")
	})

	t.Run("google", func(t *testing.T) {
		r := e.expect(e.do("", "POST", "/v1/auth/google", map[string]any{"id_token": "g-new"}), 200)
		if r.get("is_new_user") != true || r.str("user.username") != "new.person" {
			t.Fatalf("unexpected google signup: %s", r.Raw)
		}
		e.expect(e.do("", "POST", "/v1/auth/google", map[string]any{"id_token": "forged"}), 401)
		// Email registrations are unverified, so linking Google drops the password and its sessions.
		r = e.expect(e.do("", "POST", "/v1/auth/google", map[string]any{"id_token": "g-alice"}), 200)
		if int64(r.num("user.id")) != aliceID || r.get("is_new_user") != false || r.get("user.google_linked") != true || r.get("user.has_password") != false {
			t.Fatalf("expected google to link to alice: %s", r.Raw)
		}
		e.expect(e.do("", "PUT", "/v1/me/password", map[string]any{"new_password": "password-alice"}), 401)
		e.expect(e.do(alice, "PUT", "/v1/me/password", map[string]any{"new_password": "password-alice"}), 204)
	})

	lat, lng := -6.2088, 106.8456
	var catID, postID, sightingID int64
	var photoIDs []int64

	t.Run("capture and cats", func(t *testing.T) {
		img := mediatest.JPEGWithGPS(t, 1200, 900, lat, lng)
		r := e.expect(e.upload(alice, "/v1/captures", map[string]string{"lat": "1", "lng": "1"}, img), 201)
		catID = int64(r.num("cat.id"))
		sightingID = int64(r.num("sighting.id"))
		photoIDs = append(photoIDs, int64(r.num("photo.id")))
		if r.str("sighting.source") != "exif" || r.num("sighting.lat") != lat {
			t.Fatalf("EXIF GPS should win over device location: %s", r.Raw)
		}
		if r.get("cat.cover") == nil || r.get("cat.name") != nil {
			t.Fatalf("new cat should be unnamed with a cover: %s", r.Raw)
		}

		// Second capture ~50 m away suggests the first cat; merging it folds the photo in.
		r = e.expect(e.upload(alice, "/v1/captures", map[string]string{}, mediatest.JPEGWithGPS(t, 800, 600, lat+0.0004, lng)), 201)
		if int64(r.num("suggestions.0.cat.id")) != catID {
			t.Fatalf("expected suggestion for cat %d: %s", catID, r.Raw)
		}
		dupID := int64(r.num("cat.id"))
		photoIDs = append(photoIDs, int64(r.num("photo.id")))
		r = e.expect(e.do(alice, "POST", fmt.Sprintf("/v1/cats/%d/merge", dupID), map[string]any{"into_cat_id": catID}), 200)
		if r.num("photo_count") != 2 || r.num("sighting_count") != 2 {
			t.Fatalf("merge result: %s", r.Raw)
		}
		e.expect(e.do(alice, "GET", fmt.Sprintf("/v1/cats/%d", dupID), nil), 404)

		e.expect(e.do(alice, "PATCH", fmt.Sprintf("/v1/cats/%d", catID), map[string]any{"name": "Oyen"}), 200)
		r = e.expect(e.do(alice, "GET", "/v1/cats?q=oye&sort=last_seen", nil), 200)
		if r.num("items.0.id") != float64(catID) {
			t.Fatalf("search: %s", r.Raw)
		}
		r = e.expect(e.do(alice, "GET", "/v1/map/cats", nil), 200)
		if len(r.get("items").([]any)) != 1 {
			t.Fatalf("map: %s", r.Raw)
		}
		e.expect(e.do(alice, "POST", fmt.Sprintf("/v1/cats/%d/sightings", catID), map[string]any{"label": "Depan Indomaret"}), 201)
		e.expect(e.upload(alice, "/v1/captures", nil, []byte("GIF89a not really")), 415)

		// IDOR: another user cannot reach alice's private cat data.
		e.expect(e.do(bob, "GET", fmt.Sprintf("/v1/cats/%d", catID), nil), 404)
		e.expect(e.do(bob, "GET", fmt.Sprintf("/v1/cats/%d/photos", catID), nil), 404)
		e.expect(e.do(bob, "PATCH", fmt.Sprintf("/v1/photos/%d", photoIDs[0]), map[string]any{"caption": "pwned"}), 404)
		e.expect(e.do(bob, "DELETE", fmt.Sprintf("/v1/cats/%d", catID), nil), 404)
		e.expect(e.upload(bob, fmt.Sprintf("/v1/cats/%d/photos", catID), nil, mediatest.JPEGWithGPS(t, 100, 100, 1, 1)), 404)
	})

	t.Run("post visibility matrix", func(t *testing.T) {
		e.expect(e.do(bob, "POST", "/v1/posts", map[string]any{"cat_id": catID, "photo_ids": photoIDs}), 404)
		r := e.expect(e.do(alice, "POST", "/v1/posts", map[string]any{
			"cat_id": catID, "photo_ids": photoIDs, "caption": "Oyen hari ini", "visibility": "FRIENDS", "loc_precision": "APPROX", "sighting_id": sightingID,
		}), 201)
		postID = int64(r.num("id"))
		if len(r.get("photos").([]any)) != 2 {
			t.Fatalf("post photos: %s", r.Raw)
		}
		// Each photo can only be in one post.
		e.expect(e.do(alice, "POST", "/v1/posts", map[string]any{"cat_id": catID, "photo_ids": photoIDs[:1]}), 422)

		path := fmt.Sprintf("/v1/posts/%d", postID)
		e.expect(e.do(bob, "GET", path, nil), 404) // not friends yet

		r = e.expect(e.do(bob, "POST", "/v1/friends/requests", map[string]any{"username": "alice"}), 201)
		reqID := int64(r.num("request_id"))
		e.expect(e.do(bob, "POST", fmt.Sprintf("/v1/friends/requests/%d/accept", reqID), nil), 404) // only addressee may accept
		e.expect(e.do(alice, "POST", fmt.Sprintf("/v1/friends/requests/%d/accept", reqID), nil), 200)

		r = e.expect(e.do(bob, "GET", path, nil), 200)
		approxLat, approxLng := r.num("location.lat"), r.num("location.lng")
		if approxLat == lat {
			t.Fatal("friend received exact coordinates for an APPROX post")
		}
		if d := geo.DistanceM(lat, lng, approxLat, approxLng); d < 40 || d > 220 {
			t.Fatalf("approx offset %.0f m", d)
		}
		again := e.expect(e.do(bob, "GET", path, nil), 200)
		if again.num("location.lat") != approxLat {
			t.Fatal("APPROX jitter must be deterministic")
		}
		owner := e.expect(e.do(alice, "GET", path, nil), 200)
		if owner.num("location.lat") != lat || owner.num("shared_location.lat") != approxLat {
			t.Fatalf("owner view: %s", owner.Raw)
		}
		if strings.Contains(string(again.Raw), fmt.Sprint(lat)) {
			t.Fatal("exact latitude leaked somewhere in the friend's payload")
		}

		feed := e.expect(e.do(bob, "GET", "/v1/feed", nil), 200)
		if feed.num("items.0.id") != float64(postID) {
			t.Fatalf("feed: %s", feed.Raw)
		}
		e.expect(e.do(carol, "GET", path, nil), 404)
		if items := e.expect(e.do(carol, "GET", "/v1/explore", nil), 200).get("items").([]any); len(items) != 0 {
			t.Fatal("FRIENDS post leaked into explore")
		}

		// PRIVATE hides it from friends too.
		e.expect(e.do(alice, "PATCH", path, map[string]any{"visibility": "PRIVATE"}), 200)
		e.expect(e.do(bob, "GET", path, nil), 404)

		// PUBLIC + HIDDEN: everyone logged in sees it, nobody but alice gets coordinates.
		e.expect(e.do(alice, "PATCH", path, map[string]any{"visibility": "PUBLIC", "loc_precision": "HIDDEN"}), 200)
		r = e.expect(e.do(carol, "GET", path, nil), 200)
		if r.get("location.lat") != nil || r.get("location.lng") != nil {
			t.Fatalf("HIDDEN leaked coordinates: %s", r.Raw)
		}
		ex := e.expect(e.do(carol, "GET", "/v1/explore", nil), 200)
		if ex.num("items.0.id") != float64(postID) {
			t.Fatalf("explore: %s", ex.Raw)
		}
		p := e.expect(e.do(carol, "GET", "/v1/users/alice", nil), 200)
		if p.num("post_count") != 1 || p.str("relationship.status") != "none" {
			t.Fatalf("profile: %s", p.Raw)
		}
	})

	t.Run("interactions and blocking", func(t *testing.T) {
		path := fmt.Sprintf("/v1/posts/%d", postID)
		e.expect(e.do(carol, "PUT", path+"/reaction", map[string]any{"emoji": "💣"}), 422)
		r := e.expect(e.do(carol, "PUT", path+"/reaction", map[string]any{"emoji": "😻"}), 200)
		if r.num("reaction_counts.😻") != 1 || r.str("my_reaction") != "😻" {
			t.Fatalf("reaction: %s", r.Raw)
		}
		c := e.expect(e.do(carol, "POST", path+"/comments", map[string]any{"body": "lucu banget"}), 201)
		commentID := int64(c.num("id"))
		e.expect(e.do(bob, "DELETE", fmt.Sprintf("/v1/comments/%d", commentID), nil), 404)

		n := e.expect(e.do(alice, "GET", "/v1/notifications/unread-count", nil), 200)
		if n.num("unread") != 3 { // friend request + reaction + comment
			t.Fatalf("unread: %s", n.Raw)
		}

		e.expect(e.do(alice, "PATCH", path, map[string]any{"comments_disabled": true}), 200)
		e.expect(e.do(bob, "POST", path+"/comments", map[string]any{"body": "hi"}), 403)
		e.expect(e.do(alice, "PATCH", path, map[string]any{"comments_disabled": false}), 200)

		// Blocking hides content in both directions, including explore and comments.
		e.expect(e.do(alice, "POST", "/v1/blocks", map[string]any{"user_id": carolID}), 204)
		e.expect(e.do(carol, "GET", path, nil), 404)
		if items := e.expect(e.do(carol, "GET", "/v1/explore", nil), 200).get("items").([]any); len(items) != 0 {
			t.Fatal("blocked user still sees post in explore")
		}
		e.expect(e.do(carol, "GET", "/v1/users/alice", nil), 404)
		e.expect(e.do(alice, "GET", "/v1/users/carol", nil), 404)
		cm := e.expect(e.do(alice, "GET", path+"/comments", nil), 200)
		if len(cm.get("items").([]any)) != 0 {
			t.Fatalf("blocked user's comment still visible: %s", cm.Raw)
		}
		e.expect(e.do(carol, "POST", "/v1/friends/requests", map[string]any{"username": "alice"}), 404)
		e.expect(e.do(carol, "POST", "/v1/reports", map[string]any{"target_type": "POST", "target_id": postID, "reason": "spam"}), 404)
		e.expect(e.do(alice, "DELETE", fmt.Sprintf("/v1/blocks/%d", carolID), nil), 204)
		e.expect(e.do(carol, "GET", path, nil), 200)
	})

	t.Run("moderation", func(t *testing.T) {
		e.expect(e.do(carol, "POST", "/v1/reports", map[string]any{"target_type": "USER", "target_id": bobID, "reason": "spam"}), 201)
		e.expect(e.do(bob, "GET", "/v1/admin/reports", nil), 403)
		if _, err := e.s.db.Exec(context.Background(), `UPDATE users SET role = 'admin' WHERE id = $1`, aliceID); err != nil {
			t.Fatal(err)
		}
		r := e.expect(e.do(alice, "GET", "/v1/admin/reports", nil), 200)
		if r.str("items.0.target.username") != "bob" {
			t.Fatalf("reports: %s", r.Raw)
		}
		e.expect(e.do(alice, "POST", fmt.Sprintf("/v1/admin/users/%d/suspend", bobID), nil), 204)
		e.expect(e.do(bob, "GET", "/v1/me", nil), 403)
		e.expect(e.do("", "POST", "/v1/auth/login", map[string]any{"email": "bob", "password": "password-bob"}), 403)
		if items := e.expect(e.do(alice, "GET", "/v1/admin/reports", nil), 200).get("items").([]any); len(items) != 0 {
			t.Fatal("suspending should resolve the open user report")
		}
		e.expect(e.do(alice, "POST", fmt.Sprintf("/v1/admin/users/%d/unsuspend", bobID), nil), 204)
	})

	t.Run("delete account", func(t *testing.T) {
		before := e.store.count()
		if before == 0 {
			t.Fatal("expected stored photos")
		}
		e.expect(e.do(alice, "DELETE", "/v1/me", map[string]any{"password": "wrong"}), 403)
		e.expect(e.do(alice, "DELETE", "/v1/me", map[string]any{"password": "password-alice"}), 204)
		e.expect(e.do(alice, "GET", "/v1/me", nil), 401)
		e.expect(e.do(carol, "GET", fmt.Sprintf("/v1/posts/%d", postID), nil), 404)
		deadline := time.Now().Add(5 * time.Second)
		for e.store.count() != 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if n := e.store.count(); n != 0 {
			t.Fatalf("%d objects left in storage after account deletion", n)
		}
	})
}
