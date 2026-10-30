package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"meowshot/internal/geo"
	"meowshot/internal/media"
)

// Cats, their photos and sightings are private to the owner. Other users see a cat through a post
// (posts.go), and friends see the collection's names and covers (listUserCats), never its notes,
// photos or sightings. Every other handler here filters on user_id.

type photoJSON struct {
	ID        int64      `json:"id"`
	CatID     int64      `json:"cat_id"`
	PostID    *int64     `json:"post_id"`
	URL       *string    `json:"url"`
	ThumbURL  *string    `json:"thumb_url"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	Caption   string     `json:"caption"`
	TakenAt   *time.Time `json:"taken_at"`
	CreatedAt time.Time  `json:"created_at"`
}

type sightingJSON struct {
	ID     int64     `json:"id"`
	CatID  int64     `json:"cat_id"`
	Lat    *float64  `json:"lat"`
	Lng    *float64  `json:"lng"`
	Label  *string   `json:"label"`
	Source string    `json:"source"`
	SeenAt time.Time `json:"seen_at"`
}

type coverJSON struct {
	PhotoID  int64   `json:"photo_id"`
	URL      *string `json:"url"`
	ThumbURL *string `json:"thumb_url"`
}

type catJSON struct {
	ID            int64         `json:"id"`
	Name          *string       `json:"name"`
	Notes         *string       `json:"notes"`
	IsFavorite    bool          `json:"is_favorite"`
	Cover         *coverJSON    `json:"cover"`
	PhotoCount    int           `json:"photo_count"`
	SightingCount int           `json:"sighting_count"`
	LastSighting  *sightingJSON `json:"last_sighting"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

const catSelect = `SELECT c.id, c.name, c.notes, c.is_favorite, c.created_at, c.updated_at,
	cp.id, cp.key, cp.thumb_key,
	(SELECT count(*) FROM photos ph WHERE ph.cat_id = c.id),
	(SELECT count(*) FROM sightings sx WHERE sx.cat_id = c.id),
	ls.id, ls.lat, ls.lng, ls.label, ls.source, ls.seen_at
FROM cats c
LEFT JOIN photos cp ON cp.id = c.cover_photo_id
LEFT JOIN LATERAL (SELECT s.id, s.lat, s.lng, s.label, s.source, s.seen_at FROM sightings s
	WHERE s.cat_id = c.id ORDER BY s.seen_at DESC, s.id DESC LIMIT 1) ls ON TRUE `

func (s *Server) scanCat(ctx context.Context, row pgx.Row) (*catJSON, error) {
	var c catJSON
	var coverID *int64
	var coverKey, coverThumb *string
	var sID *int64
	var sLat, sLng *float64
	var sLabel, sSource *string
	var sSeen *time.Time
	if err := row.Scan(&c.ID, &c.Name, &c.Notes, &c.IsFavorite, &c.CreatedAt, &c.UpdatedAt,
		&coverID, &coverKey, &coverThumb, &c.PhotoCount, &c.SightingCount,
		&sID, &sLat, &sLng, &sLabel, &sSource, &sSeen); err != nil {
		return nil, err
	}
	if coverID != nil {
		c.Cover = &coverJSON{PhotoID: *coverID, URL: s.signed(ctx, coverKey), ThumbURL: s.signed(ctx, coverThumb)}
	}
	if sID != nil {
		c.LastSighting = &sightingJSON{ID: *sID, CatID: c.ID, Lat: sLat, Lng: sLng, Label: sLabel, Source: *sSource, SeenAt: *sSeen}
	}
	return &c, nil
}

func (s *Server) loadCat(ctx context.Context, uid, catID int64) (*catJSON, error) {
	c, err := s.scanCat(ctx, s.db.QueryRow(ctx, catSelect+`WHERE c.id = $1 AND c.user_id = $2`, catID, uid))
	if err == pgx.ErrNoRows {
		return nil, errNotFound("cat")
	}
	return c, err
}

func (s *Server) ownsCat(ctx context.Context, uid, catID int64) error {
	var owns bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cats WHERE id = $1 AND user_id = $2)`, catID, uid).Scan(&owns); err != nil {
		return err
	}
	if !owns {
		return errNotFound("cat")
	}
	return nil
}

func (s *Server) photo(ctx context.Context, p photoJSON, key, thumbKey string) photoJSON {
	p.URL = s.signed(ctx, &key)
	p.ThumbURL = s.signed(ctx, &thumbKey)
	return p
}

// listCats: GET /cats?q=&sort=last_seen|found|name&cursor=&limit= (CAT-5, CAT-6, CAT-7).
// Sorting by name or last sighting is not id-ordered, so the cursor here is an offset.
func (s *Server) listCats(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	uid := userFrom(ctx).ID
	pg := readPage(r, 30, 100)
	order := "c.created_at DESC, c.id DESC"
	switch r.URL.Query().Get("sort") {
	case "last_seen":
		order = "ls.seen_at DESC NULLS LAST, c.id DESC"
	case "name":
		order = "lower(c.name) ASC NULLS LAST, c.id DESC"
	}
	q := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(r.URL.Query().Get("q")))
	rows, err := s.db.Query(ctx, catSelect+`WHERE c.user_id = $1 AND ($2 = '' OR c.name ILIKE '%' || $2 || '%')
		ORDER BY c.is_favorite DESC, `+order+` LIMIT $3 OFFSET $4`, uid, q, pg.Limit+1, pg.Cursor)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := list[*catJSON]{Items: []*catJSON{}}
	for rows.Next() {
		c, err := s.scanCat(ctx, rows)
		if err != nil {
			return err
		}
		out.Items = append(out.Items, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(out.Items) > pg.Limit {
		out.Items = out.Items[:pg.Limit]
		next := strconv.FormatInt(pg.Cursor+int64(pg.Limit), 10)
		out.NextCursor = &next
	}
	return ok(w, out)
}

// collectionCatJSON is a cat as a friend sees it in the owner's collection: no notes or sightings.
type collectionCatJSON struct {
	ID         int64      `json:"id"`
	Name       *string    `json:"name"`
	IsFavorite bool       `json:"is_favorite"`
	Cover      *coverJSON `json:"cover"`
	PhotoCount int        `json:"photo_count"`
}

// listUserCats: GET /users/{username}/cats?cursor=&limit= — the collection, for its owner and their
// friends only (403 for everyone else). Blocked or suspended users answer 404, as their profile does.
func (s *Server) listUserCats(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	viewer := userFrom(ctx).ID
	var uid int64
	var allowed bool
	err := s.db.QueryRow(ctx, `SELECT u.id, u.id = $1 OR `+friendsSQL("$1::bigint", "u.id")+`
		FROM users u WHERE u.username = $2 AND `+userVisibleSQL("$1"),
		viewer, strings.ToLower(chi.URLParam(r, "username"))).Scan(&uid, &allowed)
	if err == pgx.ErrNoRows {
		return errNotFound("user")
	}
	if err != nil {
		return err
	}
	if !allowed {
		return errForbidden("only friends can see this collection")
	}
	pg := readPage(r, 30, 100)
	rows, err := s.db.Query(ctx, `SELECT c.id, c.name, c.is_favorite, cp.id, cp.key, cp.thumb_key,
		(SELECT count(*) FROM photos ph WHERE ph.cat_id = c.id)
		FROM cats c LEFT JOIN photos cp ON cp.id = c.cover_photo_id
		WHERE c.user_id = $1 ORDER BY c.is_favorite DESC, c.created_at DESC, c.id DESC LIMIT $2 OFFSET $3`,
		uid, pg.Limit+1, pg.Cursor)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := list[collectionCatJSON]{Items: []collectionCatJSON{}}
	for rows.Next() {
		var c collectionCatJSON
		var coverID *int64
		var coverKey, coverThumb *string
		if err := rows.Scan(&c.ID, &c.Name, &c.IsFavorite, &coverID, &coverKey, &coverThumb, &c.PhotoCount); err != nil {
			return err
		}
		if coverID != nil {
			c.Cover = &coverJSON{PhotoID: *coverID, URL: s.signed(ctx, coverKey), ThumbURL: s.signed(ctx, coverThumb)}
		}
		out.Items = append(out.Items, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(out.Items) > pg.Limit {
		out.Items = out.Items[:pg.Limit]
		next := strconv.FormatInt(pg.Cursor+int64(pg.Limit), 10)
		out.NextCursor = &next
	}
	return ok(w, out)
}

type catInput struct {
	Name       *string `json:"name"`
	Notes      *string `json:"notes"`
	IsFavorite *bool   `json:"is_favorite"`
}

func (in *catInput) clean() error {
	for _, f := range []*string{in.Name, in.Notes} {
		if f != nil {
			*f = strings.TrimSpace(*f)
		}
	}
	if in.Name != nil && len([]rune(*in.Name)) > 60 {
		return errValidation("name must be at most 60 characters")
	}
	if in.Notes != nil && len([]rune(*in.Notes)) > 2000 {
		return errValidation("notes must be at most 2000 characters")
	}
	return nil
}

func (s *Server) createCat(w http.ResponseWriter, r *http.Request) error {
	var in catInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := in.clean(); err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO cats (user_id, name, notes, is_favorite) VALUES ($1, NULLIF($2, ''), NULLIF($3, ''), COALESCE($4, FALSE)) RETURNING id`,
		uid, in.Name, in.Notes, in.IsFavorite).Scan(&id); err != nil {
		return err
	}
	c, err := s.loadCat(ctx, uid, id)
	if err != nil {
		return err
	}
	return created(w, c)
}

func (s *Server) getCat(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	c, err := s.loadCat(r.Context(), userFrom(r.Context()).ID, id)
	if err != nil {
		return err
	}
	return ok(w, c)
}

// updateCat: omitted fields are unchanged; an empty string clears name or notes.
func (s *Server) updateCat(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	var in catInput
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := in.clean(); err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	tag, err := s.db.Exec(ctx, `UPDATE cats SET
		name = CASE WHEN $3::text IS NULL THEN name ELSE NULLIF($3, '') END,
		notes = CASE WHEN $4::text IS NULL THEN notes ELSE NULLIF($4, '') END,
		is_favorite = COALESCE($5, is_favorite), updated_at = now()
		WHERE id = $1 AND user_id = $2`, id, uid, in.Name, in.Notes, in.IsFavorite)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("cat")
	}
	c, err := s.loadCat(ctx, uid, id)
	if err != nil {
		return err
	}
	return ok(w, c)
}

// deleteCat removes the cat with its photos, sightings and posts (CAT-4).
func (s *Server) deleteCat(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.ownsCat(ctx, uid, id); err != nil {
		return err
	}
	keys, err := s.collectKeys(ctx, `SELECT key FROM photos WHERE cat_id = $1 UNION ALL SELECT thumb_key FROM photos WHERE cat_id = $1`, id)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM cats WHERE id = $1 AND user_id = $2`, id, uid); err != nil {
		return err
	}
	s.deleteObjects(keys)
	return noContent(w)
}

// mergeCat moves every photo, sighting and post of this cat into another cat and deletes it.
// Used after quick capture (QC-2: "add to existing cat") and to fix duplicates.
func (s *Server) mergeCat(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	var in struct {
		IntoCatID int64 `json:"into_cat_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.IntoCatID == id {
		return errValidation("cannot merge a cat into itself")
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.ownsCat(ctx, uid, id); err != nil {
		return err
	}
	if err := s.ownsCat(ctx, uid, in.IntoCatID); err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, q := range []string{
			`UPDATE photos SET cat_id = $2 WHERE cat_id = $1`,
			`UPDATE sightings SET cat_id = $2 WHERE cat_id = $1`,
			`UPDATE posts SET cat_id = $2 WHERE cat_id = $1`,
			`UPDATE cats SET cover_photo_id = COALESCE(cover_photo_id, (SELECT cover_photo_id FROM cats WHERE id = $1)),
				updated_at = now() WHERE id = $2`,
		} {
			if _, err := tx.Exec(ctx, q, id, in.IntoCatID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM cats WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return err
	}
	c, err := s.loadCat(ctx, uid, in.IntoCatID)
	if err != nil {
		return err
	}
	return ok(w, c)
}

func (s *Server) setCover(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	var in struct {
		PhotoID int64 `json:"photo_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	tag, err := s.db.Exec(ctx, `UPDATE cats SET cover_photo_id = $3, updated_at = now() WHERE id = $1 AND user_id = $2
		AND EXISTS (SELECT 1 FROM photos WHERE id = $3 AND cat_id = $1)`, id, uid, in.PhotoID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("cat or photo")
	}
	c, err := s.loadCat(ctx, uid, id)
	if err != nil {
		return err
	}
	return ok(w, c)
}

func (s *Server) listCatPhotos(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.ownsCat(ctx, uid, id); err != nil {
		return err
	}
	pg := readPage(r, 30, 100)
	rows, err := s.db.Query(ctx, `SELECT id, cat_id, post_id, key, thumb_key, width, height, caption, taken_at, created_at
		FROM photos WHERE cat_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, id, pg.cursorArg(), pg.Limit+1)
	if err != nil {
		return err
	}
	photos, err := s.scanPhotos(ctx, rows)
	if err != nil {
		return err
	}
	return ok(w, paginate(photos, pg.Limit, func(p photoJSON) int64 { return p.ID }))
}

func (s *Server) scanPhotos(ctx context.Context, rows pgx.Rows) ([]photoJSON, error) {
	defer rows.Close()
	var out []photoJSON
	for rows.Next() {
		var p photoJSON
		var key, thumb string
		if err := rows.Scan(&p.ID, &p.CatID, &p.PostID, &key, &thumb, &p.Width, &p.Height, &p.Caption, &p.TakenAt, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s.photo(ctx, p, key, thumb))
	}
	return out, rows.Err()
}

type photoUploadResponse struct {
	Photo            photoJSON     `json:"photo"`
	Sighting         *sightingJSON `json:"sighting"`
	DetectedLocation *location     `json:"detected_location"` // EXIF GPS, visible only to the owner
}

// uploadCatPhoto: POST /cats/{id}/photos (multipart: photo, caption, add_sighting, lat, lng, label,
// location_source, seen_at). The first photo of a cat becomes its cover (CAT-2).
func (s *Server) uploadCatPhoto(w http.ResponseWriter, r *http.Request) error {
	catID, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	data, err := s.readUpload(w, r, "photo")
	if err != nil {
		return err
	}
	loc, err := parseLocationForm(r)
	if err != nil {
		return err
	}
	caption := strings.TrimSpace(r.FormValue("caption"))
	if len([]rune(caption)) > 500 {
		return errValidation("caption must be at most 500 characters")
	}
	if err := s.ownsCat(ctx, uid, catID); err != nil {
		return err
	}
	sp, err := s.storePhoto(ctx, uid, data)
	if err != nil {
		return err
	}
	addSighting, _ := strconv.ParseBool(r.FormValue("add_sighting"))

	var resp photoUploadResponse
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		p, err := insertPhoto(ctx, tx, uid, catID, sp, caption)
		if err != nil {
			return err
		}
		resp.Photo = s.photo(ctx, p, sp.key, sp.thumbKey)
		if addSighting {
			resp.Sighting, err = insertSighting(ctx, tx, uid, catID, loc, sp)
		}
		return err
	})
	if err != nil {
		s.deleteObjects(sp.keys())
		return err
	}
	if g := sp.res.GPS; g != nil {
		resp.DetectedLocation = &location{Lat: &g.Lat, Lng: &g.Lng}
	}
	return created(w, resp)
}

func insertPhoto(ctx context.Context, tx pgx.Tx, uid, catID int64, sp *storedPhoto, caption string) (photoJSON, error) {
	p := photoJSON{CatID: catID, Width: sp.res.Width, Height: sp.res.Height, Caption: caption, TakenAt: sp.res.TakenAt}
	err := tx.QueryRow(ctx, `INSERT INTO photos (user_id, cat_id, key, thumb_key, width, height, caption, taken_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		uid, catID, sp.key, sp.thumbKey, p.Width, p.Height, caption, p.TakenAt).Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		return p, err
	}
	_, err = tx.Exec(ctx, `UPDATE cats SET cover_photo_id = COALESCE(cover_photo_id, $2), updated_at = now() WHERE id = $1`, catID, p.ID)
	return p, err
}

func insertSighting(ctx context.Context, tx pgx.Tx, uid, catID int64, in locationInput, sp *storedPhoto) (*sightingJSON, error) {
	var res *media.Result
	if sp != nil {
		res = sp.res
	}
	lat, lng, source, seenAt := resolveSighting(in, res)
	sg := &sightingJSON{CatID: catID, Lat: lat, Lng: lng, Label: in.Label, Source: source, SeenAt: seenAt}
	err := tx.QueryRow(ctx, `INSERT INTO sightings (cat_id, user_id, lat, lng, label, source, seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, catID, uid, lat, lng, in.Label, source, seenAt).Scan(&sg.ID)
	return sg, err
}

func (s *Server) updatePhoto(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "photoID")
	if err != nil {
		return err
	}
	var in struct {
		Caption string `json:"caption"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	in.Caption = strings.TrimSpace(in.Caption)
	if len([]rune(in.Caption)) > 500 {
		return errValidation("caption must be at most 500 characters")
	}
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `UPDATE photos SET caption = $3 WHERE id = $1 AND user_id = $2
		RETURNING id, cat_id, post_id, key, thumb_key, width, height, caption, taken_at, created_at`, id, userFrom(ctx).ID, in.Caption)
	if err != nil {
		return err
	}
	photos, err := s.scanPhotos(ctx, rows)
	if err != nil {
		return err
	}
	if len(photos) == 0 {
		return errNotFound("photo")
	}
	return ok(w, photos[0])
}

// deletePhoto removes a photo; a new cover is picked if needed, and a post left without photos is deleted.
func (s *Server) deletePhoto(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "photoID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	var keys []string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var catID int64
		var postID *int64
		var key, thumb string
		err := tx.QueryRow(ctx, `DELETE FROM photos WHERE id = $1 AND user_id = $2 RETURNING cat_id, post_id, key, thumb_key`, id, uid).
			Scan(&catID, &postID, &key, &thumb)
		if err == pgx.ErrNoRows {
			return errNotFound("photo")
		}
		if err != nil {
			return err
		}
		keys = []string{key, thumb}
		if _, err := tx.Exec(ctx, `UPDATE cats SET cover_photo_id = (SELECT id FROM photos WHERE cat_id = $1 ORDER BY id LIMIT 1)
			WHERE id = $1 AND cover_photo_id IS NULL`, catID); err != nil {
			return err
		}
		if postID != nil {
			_, err = tx.Exec(ctx, `DELETE FROM posts WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM photos WHERE post_id = $1)`, *postID)
		}
		return err
	})
	if err != nil {
		return err
	}
	s.deleteObjects(keys)
	return noContent(w)
}

func (s *Server) listSightings(w http.ResponseWriter, r *http.Request) error {
	catID, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	if err := s.ownsCat(ctx, userFrom(ctx).ID, catID); err != nil {
		return err
	}
	pg := readPage(r, 50, 200)
	// Ordered by id so the cursor is stable; seen_at is returned for display.
	rows, err := s.db.Query(ctx, `SELECT id, cat_id, lat, lng, label, source, seen_at FROM sightings
		WHERE cat_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, catID, pg.cursorArg(), pg.Limit+1)
	if err != nil {
		return err
	}
	sightings, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (sightingJSON, error) {
		var sg sightingJSON
		err := row.Scan(&sg.ID, &sg.CatID, &sg.Lat, &sg.Lng, &sg.Label, &sg.Source, &sg.SeenAt)
		return sg, err
	})
	if err != nil {
		return err
	}
	return ok(w, paginate(sightings, pg.Limit, func(sg sightingJSON) int64 { return sg.ID }))
}

type sightingBody struct {
	Lat    *float64   `json:"lat"`
	Lng    *float64   `json:"lng"`
	Label  *string    `json:"label"`
	Source string     `json:"source"`
	SeenAt *time.Time `json:"seen_at"`
}

func (b sightingBody) input() (locationInput, error) {
	in := locationInput{Lat: b.Lat, Lng: b.Lng, Label: b.Label, Source: b.Source, SeenAt: b.SeenAt}
	if in.Label != nil {
		l := strings.TrimSpace(*in.Label)
		in.Label = &l
		if l == "" {
			in.Label = nil
		}
	}
	return in, in.validate()
}

// createSighting records "ketemu lagi" without a photo (LOC-4).
func (s *Server) createSighting(w http.ResponseWriter, r *http.Request) error {
	catID, err := pathID(r, "catID")
	if err != nil {
		return err
	}
	var body sightingBody
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	in, err := body.input()
	if err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.ownsCat(ctx, uid, catID); err != nil {
		return err
	}
	var sg *sightingJSON
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		sg, err = insertSighting(ctx, tx, uid, catID, in, nil)
		return err
	})
	if err != nil {
		return err
	}
	return created(w, sg)
}

func (s *Server) updateSighting(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "sightingID")
	if err != nil {
		return err
	}
	var body sightingBody
	if err := decodeJSON(r, &body); err != nil {
		return err
	}
	in, err := body.input()
	if err != nil {
		return err
	}
	ctx := r.Context()
	var sg sightingJSON
	err = s.db.QueryRow(ctx, `UPDATE sightings SET
		lat = CASE WHEN $3::float8 IS NULL THEN lat ELSE $3 END,
		lng = CASE WHEN $4::float8 IS NULL THEN lng ELSE $4 END,
		source = CASE WHEN $3::float8 IS NULL THEN source ELSE 'manual' END,
		label = CASE WHEN $5::text IS NULL THEN label ELSE NULLIF($5, '') END,
		seen_at = COALESCE($6, seen_at)
		WHERE id = $1 AND user_id = $2 RETURNING id, cat_id, lat, lng, label, source, seen_at`,
		id, userFrom(ctx).ID, in.Lat, in.Lng, body.Label, in.SeenAt).
		Scan(&sg.ID, &sg.CatID, &sg.Lat, &sg.Lng, &sg.Label, &sg.Source, &sg.SeenAt)
	if err == pgx.ErrNoRows {
		return errNotFound("sighting")
	}
	if err != nil {
		return err
	}
	return ok(w, sg)
}

func (s *Server) deleteSighting(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "sightingID")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM sightings WHERE id = $1 AND user_id = $2`, id, userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("sighting")
	}
	return noContent(w)
}

type captureResponse struct {
	Cat         *catJSON      `json:"cat"`
	Photo       photoJSON     `json:"photo"`
	Sighting    *sightingJSON `json:"sighting"`
	Suggestions []nearbyCat   `json:"suggestions"`
}

// capture is the one-tap flow (QC-1/QC-2, LOC-2, LOC-7): store the photo, create a new unnamed cat
// (or attach to cat_id), record a sighting, and suggest nearby existing cats. If the user then picks
// "this is Oyen", the app calls POST /cats/{new}/merge with into_cat_id = Oyen.
func (s *Server) capture(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	uid := userFrom(ctx).ID
	data, err := s.readUpload(w, r, "photo")
	if err != nil {
		return err
	}
	loc, err := parseLocationForm(r)
	if err != nil {
		return err
	}
	in := catInput{}
	if v := r.FormValue("name"); v != "" {
		in.Name = &v
	}
	if v := r.FormValue("notes"); v != "" {
		in.Notes = &v
	}
	if err := in.clean(); err != nil {
		return err
	}
	caption := strings.TrimSpace(r.FormValue("caption"))
	var catID int64
	if v := r.FormValue("cat_id"); v != "" {
		if catID, err = strconv.ParseInt(v, 10, 64); err != nil {
			return errValidation("cat_id must be a number")
		}
		if err := s.ownsCat(ctx, uid, catID); err != nil {
			return err
		}
	}
	sp, err := s.storePhoto(ctx, uid, data)
	if err != nil {
		return err
	}
	isNewCat := catID == 0
	resp := captureResponse{Suggestions: []nearbyCat{}}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if isNewCat {
			if err := tx.QueryRow(ctx, `INSERT INTO cats (user_id, name, notes) VALUES ($1, NULLIF($2, ''), NULLIF($3, '')) RETURNING id`,
				uid, in.Name, in.Notes).Scan(&catID); err != nil {
				return err
			}
		}
		p, err := insertPhoto(ctx, tx, uid, catID, sp, caption)
		if err != nil {
			return err
		}
		resp.Photo = s.photo(ctx, p, sp.key, sp.thumbKey)
		resp.Sighting, err = insertSighting(ctx, tx, uid, catID, loc, sp)
		return err
	})
	if err != nil {
		s.deleteObjects(sp.keys())
		return err
	}
	if resp.Cat, err = s.loadCat(ctx, uid, catID); err != nil {
		return err
	}
	if isNewCat && resp.Sighting.Lat != nil {
		if resp.Suggestions, err = s.nearby(ctx, uid, *resp.Sighting.Lat, *resp.Sighting.Lng, 300, catID, 5); err != nil {
			return err
		}
	}
	return created(w, resp)
}

type nearbyCat struct {
	Cat       *catJSON `json:"cat"`
	DistanceM int      `json:"distance_m"`
}

// nearby finds the user's cats with any sighting within radiusM, closest first.
func (s *Server) nearby(ctx context.Context, uid int64, lat, lng, radiusM float64, excludeCat int64, limit int) ([]nearbyCat, error) {
	minLat, maxLat, minLng, maxLng := geo.BoundingBox(lat, lng, radiusM)
	rows, err := s.db.Query(ctx, `SELECT cat_id, lat, lng FROM sightings WHERE user_id = $1 AND cat_id <> $2
		AND lat BETWEEN $3 AND $4 AND lng BETWEEN $5 AND $6 LIMIT 5000`, uid, excludeCat, minLat, maxLat, minLng, maxLng)
	if err != nil {
		return nil, err
	}
	best := map[int64]float64{}
	for rows.Next() {
		var catID int64
		var sLat, sLng float64
		if err := rows.Scan(&catID, &sLat, &sLng); err != nil {
			rows.Close()
			return nil, err
		}
		d := geo.DistanceM(lat, lng, sLat, sLng)
		if prev, seen := best[catID]; d <= radiusM && (!seen || d < prev) {
			best[catID] = d
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(best))
	for id := range best {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return best[ids[i]] < best[ids[j]] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	out := make([]nearbyCat, 0, len(ids))
	for _, id := range ids {
		c, err := s.loadCat(ctx, uid, id)
		if err != nil {
			return nil, err
		}
		out = append(out, nearbyCat{Cat: c, DistanceM: int(best[id])})
	}
	return out, nil
}

// nearbyCats: GET /cats/nearby?lat=&lng=&radius_m= — "apakah ini si Oyen?" before saving (LOC-7).
func (s *Server) nearbyCats(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(q.Get("lng"), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return errValidation("lat and lng are required")
	}
	radius := 300.0
	if v, err := strconv.ParseFloat(q.Get("radius_m"), 64); err == nil && v > 0 {
		radius = min(v, 5000)
	}
	cats, err := s.nearby(r.Context(), userFrom(r.Context()).ID, lat, lng, radius, 0, 10)
	if err != nil {
		return err
	}
	return ok(w, map[string]any{"items": cats})
}

type mapMarker struct {
	CatID    int64     `json:"cat_id"`
	Name     *string   `json:"name"`
	ThumbURL *string   `json:"thumb_url"`
	Lat      float64   `json:"lat"`
	Lng      float64   `json:"lng"`
	Label    *string   `json:"label"`
	SeenAt   time.Time `json:"seen_at"`
}

// mapCats returns one marker per cat at its latest located sighting (LOC-5); clustering is client-side.
func (s *Server) mapCats(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `SELECT c.id, c.name, cp.thumb_key, s.lat, s.lng, s.label, s.seen_at
		FROM cats c LEFT JOIN photos cp ON cp.id = c.cover_photo_id
		JOIN LATERAL (SELECT lat, lng, label, seen_at FROM sightings WHERE cat_id = c.id AND lat IS NOT NULL
			ORDER BY seen_at DESC, id DESC LIMIT 1) s ON TRUE
		WHERE c.user_id = $1 ORDER BY s.seen_at DESC`, userFrom(ctx).ID)
	if err != nil {
		return err
	}
	markers, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (mapMarker, error) {
		var m mapMarker
		var thumb *string
		err := row.Scan(&m.CatID, &m.Name, &thumb, &m.Lat, &m.Lng, &m.Label, &m.SeenAt)
		m.ThumbURL = s.signed(ctx, thumb)
		return m, err
	})
	if err != nil {
		return err
	}
	if markers == nil {
		markers = []mapMarker{}
	}
	return ok(w, map[string]any{"items": markers})
}
