package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

var validVisibility = map[string]bool{"PRIVATE": true, "FRIENDS": true, "PUBLIC": true}
var validPrecision = map[string]bool{"EXACT": true, "APPROX": true, "HIDDEN": true}

// AllowedReactions is the fixed emoji set for POST-6 (one reaction per user per post).
var AllowedReactions = map[string]bool{"❤️": true, "😻": true, "😹": true, "🐾": true, "👍": true, "😢": true}

type postPhotoJSON struct {
	ID       int64   `json:"id"`
	URL      *string `json:"url"`
	ThumbURL *string `json:"thumb_url"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Caption  string  `json:"caption"`
}

type postJSON struct {
	ID               int64           `json:"id"`
	Author           userJSON        `json:"author"`
	Cat              postCatJSON     `json:"cat"`
	Caption          string          `json:"caption"`
	Visibility       string          `json:"visibility"`
	LocPrecision     string          `json:"loc_precision"`
	Location         *location       `json:"location"`        // what this viewer may see
	SharedLocation   *location       `json:"shared_location"` // owner only: what others see
	Photos           []postPhotoJSON `json:"photos"`
	ReactionCounts   map[string]int  `json:"reaction_counts"`
	MyReaction       *string         `json:"my_reaction"`
	CommentCount     int             `json:"comment_count"`
	CommentsDisabled bool            `json:"comments_disabled"`
	IsOwner          bool            `json:"is_owner"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type postCatJSON struct {
	ID   int64   `json:"id"`
	Name *string `json:"name"`
}

// queryPosts loads posts matching extra (SQL over alias p, with the viewer always bound as $1)
// and only ever returns posts that pass postVisibleSQL.
func (s *Server) queryPosts(ctx context.Context, viewer int64, extra string, args ...any) ([]*postJSON, error) {
	sql := `SELECT p.id, p.user_id, u.username, u.avatar_key, u.bio, p.cat_id, c.name, p.caption, p.visibility,
		p.loc_precision, p.comments_disabled, p.created_at, p.updated_at,
		sg.id, sg.lat, sg.lng, sg.label, sg.seen_at,
		(SELECT count(*) FROM comments cm WHERE cm.post_id = p.id AND ` + commentVisibleSQL("$1") + `),
		(SELECT r.emoji FROM reactions r WHERE r.post_id = p.id AND r.user_id = $1),
		COALESCE((SELECT jsonb_object_agg(x.emoji, x.n) FROM (SELECT emoji, count(*) AS n FROM reactions r
			WHERE r.post_id = p.id GROUP BY emoji) x), '{}'::jsonb)
	FROM posts p
	JOIN users u ON u.id = p.user_id
	JOIN cats c ON c.id = p.cat_id
	LEFT JOIN sightings sg ON sg.id = p.sighting_id
	WHERE ` + postVisibleSQL("$1") + ` AND ` + extra
	rows, err := s.db.Query(ctx, sql, append([]any{viewer}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var posts []*postJSON
	byID := map[int64]*postJSON{}
	for rows.Next() {
		p := &postJSON{Photos: []postPhotoJSON{}}
		var avatar *string
		var sgID *int64
		var lat, lng *float64
		var label *string
		var seenAt *time.Time
		if err := rows.Scan(&p.ID, &p.Author.ID, &p.Author.Username, &avatar, &p.Author.Bio, &p.Cat.ID, &p.Cat.Name,
			&p.Caption, &p.Visibility, &p.LocPrecision, &p.CommentsDisabled, &p.CreatedAt, &p.UpdatedAt,
			&sgID, &lat, &lng, &label, &seenAt, &p.CommentCount, &p.MyReaction, &p.ReactionCounts); err != nil {
			return nil, err
		}
		p.Author.AvatarURL = s.signed(ctx, avatar)
		p.IsOwner = p.Author.ID == viewer
		if sgID != nil {
			seen := seenAt.Format(time.RFC3339)
			p.Location = s.blurLocation(p.IsOwner, p.LocPrecision, *sgID, lat, lng, label, seen)
			if p.IsOwner {
				p.SharedLocation = s.blurLocation(false, p.LocPrecision, *sgID, lat, lng, label, seen)
			}
		}
		posts = append(posts, p)
		byID[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return posts, nil
	}

	ids := make([]int64, 0, len(posts))
	for _, p := range posts {
		ids = append(ids, p.ID)
	}
	prow, err := s.db.Query(ctx, `SELECT post_id, id, key, thumb_key, width, height, caption FROM photos
		WHERE post_id = ANY($1) ORDER BY post_id, post_position, id`, ids)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	for prow.Next() {
		var postID int64
		var ph postPhotoJSON
		var key, thumb string
		if err := prow.Scan(&postID, &ph.ID, &key, &thumb, &ph.Width, &ph.Height, &ph.Caption); err != nil {
			return nil, err
		}
		ph.URL = s.signed(ctx, &key)
		ph.ThumbURL = s.signed(ctx, &thumb)
		byID[postID].Photos = append(byID[postID].Photos, ph)
	}
	return posts, prow.Err()
}

func (s *Server) loadPost(ctx context.Context, viewer, postID int64) (*postJSON, error) {
	posts, err := s.queryPosts(ctx, viewer, `p.id = $2`, postID)
	if err != nil {
		return nil, err
	}
	if len(posts) == 0 {
		return nil, errNotFound("post")
	}
	return posts[0], nil
}

func (s *Server) postList(w http.ResponseWriter, r *http.Request, viewer int64, extra string, args ...any) error {
	pg := readPage(r, 20, 50)
	n := len(args) + 2
	extra += ` AND p.id < $` + itoa(int64(n)) + ` ORDER BY p.id DESC LIMIT $` + itoa(int64(n+1))
	posts, err := s.queryPosts(r.Context(), viewer, extra, append(args, pg.cursorArg(), pg.Limit+1)...)
	if err != nil {
		return err
	}
	return ok(w, paginate(posts, pg.Limit, func(p *postJSON) int64 { return p.ID }))
}

// feed: posts from friends and self, newest first (POST-4). PRIVATE posts of others are already
// excluded by postVisibleSQL; public posts of non-friends belong to /explore instead.
func (s *Server) feed(w http.ResponseWriter, r *http.Request) error {
	viewer := userFrom(r.Context()).ID
	return s.postList(w, r, viewer, `(p.user_id = $1 OR `+friendsSQL("$1::bigint", "p.user_id")+`)`)
}

// explore: public posts from everyone, newest first (PUB-3). Blocks apply both ways (MOD-2).
func (s *Server) explore(w http.ResponseWriter, r *http.Request) error {
	return s.postList(w, r, userFrom(r.Context()).ID, `p.visibility = 'PUBLIC'`)
}

func (s *Server) listUserPosts(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	viewer := userFrom(ctx).ID
	var uid int64
	err := s.db.QueryRow(ctx, `SELECT u.id FROM users u WHERE u.username = $2 AND `+userVisibleSQL("$1"),
		viewer, strings.ToLower(chi.URLParam(r, "username"))).Scan(&uid)
	if err == pgx.ErrNoRows {
		return errNotFound("user")
	}
	if err != nil {
		return err
	}
	return s.postList(w, r, viewer, `p.user_id = $2`, uid)
}

func (s *Server) getPost(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	p, err := s.loadPost(r.Context(), userFrom(r.Context()).ID, id)
	if err != nil {
		return err
	}
	return ok(w, p)
}

// createPost: POST /posts (POST-1..3). Photos must belong to the cat and not be in another post.
// Location precision defaults to EXACT for PUBLIC (PUB-2) and APPROX otherwise.
func (s *Server) createPost(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		CatID        int64   `json:"cat_id"`
		PhotoIDs     []int64 `json:"photo_ids"`
		Caption      string  `json:"caption"`
		Visibility   string  `json:"visibility"`
		LocPrecision string  `json:"loc_precision"`
		SightingID   *int64  `json:"sighting_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Visibility == "" {
		in.Visibility = "FRIENDS"
	}
	if in.LocPrecision == "" {
		in.LocPrecision = "APPROX"
		if in.Visibility == "PUBLIC" {
			in.LocPrecision = "EXACT"
		}
	}
	in.Caption = strings.TrimSpace(in.Caption)
	switch {
	case !validVisibility[in.Visibility]:
		return errValidation("visibility must be PUBLIC, FRIENDS or PRIVATE")
	case !validPrecision[in.LocPrecision]:
		return errValidation("loc_precision must be EXACT, APPROX or HIDDEN")
	case len(in.PhotoIDs) < 1 || len(in.PhotoIDs) > 10:
		return errValidation("a post needs 1 to 10 photos")
	case len([]rune(in.Caption)) > 2200:
		return errValidation("caption must be at most 2200 characters")
	}
	seen := map[int64]bool{}
	for _, id := range in.PhotoIDs {
		if seen[id] {
			return errValidation("photo_ids contains duplicates")
		}
		seen[id] = true
	}

	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.ownsCat(ctx, uid, in.CatID); err != nil {
		return err
	}
	var postID int64
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		sightingID := in.SightingID
		if sightingID != nil {
			var okSighting bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sightings WHERE id = $1 AND cat_id = $2)`, *sightingID, in.CatID).Scan(&okSighting); err != nil {
				return err
			}
			if !okSighting {
				return errValidation("sighting_id does not belong to this cat")
			}
		} else {
			// Default to the cat's latest sighting, preferring one with coordinates, so the post carries a location.
			err := tx.QueryRow(ctx, `SELECT id FROM sightings WHERE cat_id = $1
				ORDER BY (lat IS NOT NULL) DESC, seen_at DESC, id DESC LIMIT 1`, in.CatID).Scan(&sightingID)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO posts (user_id, cat_id, sighting_id, caption, visibility, loc_precision)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, uid, in.CatID, sightingID, in.Caption, in.Visibility, in.LocPrecision).Scan(&postID); err != nil {
			return err
		}
		for i, photoID := range in.PhotoIDs {
			tag, err := tx.Exec(ctx, `UPDATE photos SET post_id = $1, post_position = $2 WHERE id = $3 AND cat_id = $4 AND user_id = $5 AND post_id IS NULL`,
				postID, i, photoID, in.CatID, uid)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errValidation("photo %d does not belong to this cat or is already in another post", photoID)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	p, err := s.loadPost(ctx, uid, postID)
	if err != nil {
		return err
	}
	return created(w, p)
}

// updatePost edits caption, visibility, precision or comment setting at any time (POST-5, MOD-3, PUB-7).
// Photo URLs are short-lived signed URLs, so narrowing visibility takes effect within SIGNED_URL_TTL.
func (s *Server) updatePost(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	var in struct {
		Caption          *string `json:"caption"`
		Visibility       *string `json:"visibility"`
		LocPrecision     *string `json:"loc_precision"`
		CommentsDisabled *bool   `json:"comments_disabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Visibility != nil && !validVisibility[*in.Visibility] {
		return errValidation("visibility must be PUBLIC, FRIENDS or PRIVATE")
	}
	if in.LocPrecision != nil && !validPrecision[*in.LocPrecision] {
		return errValidation("loc_precision must be EXACT, APPROX or HIDDEN")
	}
	if in.Caption != nil {
		c := strings.TrimSpace(*in.Caption)
		if len([]rune(c)) > 2200 {
			return errValidation("caption must be at most 2200 characters")
		}
		in.Caption = &c
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	tag, err := s.db.Exec(ctx, `UPDATE posts SET caption = COALESCE($3, caption), visibility = COALESCE($4, visibility),
		loc_precision = COALESCE($5, loc_precision), comments_disabled = COALESCE($6, comments_disabled), updated_at = now()
		WHERE id = $1 AND user_id = $2`, id, uid, in.Caption, in.Visibility, in.LocPrecision, in.CommentsDisabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("post")
	}
	p, err := s.loadPost(ctx, uid, id)
	if err != nil {
		return err
	}
	return ok(w, p)
}

// deletePost removes the post; its photos stay in the cat's album.
func (s *Server) deletePost(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM posts WHERE id = $1 AND user_id = $2`, id, userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("post")
	}
	return noContent(w)
}

func (s *Server) requireVisiblePost(ctx context.Context, viewer, postID int64) error {
	visible, err := s.canViewPost(ctx, viewer, postID)
	if err != nil {
		return err
	}
	if !visible {
		return errNotFound("post")
	}
	return nil
}

func (s *Server) putReaction(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	var in struct {
		Emoji string `json:"emoji"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if !AllowedReactions[in.Emoji] {
		return errValidation("unsupported reaction emoji")
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.requireVisiblePost(ctx, uid, id); err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var inserted bool
		err := tx.QueryRow(ctx, `INSERT INTO reactions (post_id, user_id, emoji) VALUES ($1, $2, $3)
			ON CONFLICT (post_id, user_id) DO UPDATE SET emoji = EXCLUDED.emoji, created_at = now()
			RETURNING (xmax = 0)`, id, uid, in.Emoji).Scan(&inserted)
		if err != nil || !inserted {
			return err
		}
		var owner int64
		if err := tx.QueryRow(ctx, `SELECT user_id FROM posts WHERE id = $1`, id).Scan(&owner); err != nil {
			return err
		}
		return notify(ctx, tx, owner, uid, "reaction", id)
	})
	if err != nil {
		return err
	}
	p, err := s.loadPost(ctx, uid, id)
	if err != nil {
		return err
	}
	return ok(w, p)
}

func (s *Server) deleteReaction(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if _, err := s.db.Exec(ctx, `DELETE FROM reactions WHERE post_id = $1 AND user_id = $2`, id, uid); err != nil {
		return err
	}
	return noContent(w)
}

type commentJSON struct {
	ID        int64     `json:"id"`
	PostID    int64     `json:"post_id"`
	Author    userJSON  `json:"author"`
	Body      string    `json:"body"`
	CanDelete bool      `json:"can_delete"`
	CreatedAt time.Time `json:"created_at"`
}

// listComments: oldest first; ?cursor returns comments after that id.
func (s *Server) listComments(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.requireVisiblePost(ctx, uid, id); err != nil {
		return err
	}
	pg := readPage(r, 50, 100)
	rows, err := s.db.Query(ctx, `SELECT cm.id, cm.post_id, cm.body, cm.created_at, u.id, u.username, u.avatar_key, u.bio,
		(cm.user_id = $1 OR p.user_id = $1)
		FROM comments cm JOIN users u ON u.id = cm.user_id JOIN posts p ON p.id = cm.post_id
		WHERE cm.post_id = $2 AND cm.id > $3 AND `+commentVisibleSQL("$1")+` ORDER BY cm.id LIMIT $4`,
		uid, id, pg.Cursor, pg.Limit+1)
	if err != nil {
		return err
	}
	comments, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (commentJSON, error) {
		var c commentJSON
		var avatar *string
		err := row.Scan(&c.ID, &c.PostID, &c.Body, &c.CreatedAt, &c.Author.ID, &c.Author.Username, &avatar, &c.Author.Bio, &c.CanDelete)
		c.Author.AvatarURL = s.signed(ctx, avatar)
		return c, err
	})
	if err != nil {
		return err
	}
	return ok(w, paginate(comments, pg.Limit, func(c commentJSON) int64 { return c.ID }))
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	in.Body = strings.TrimSpace(in.Body)
	if n := len([]rune(in.Body)); n < 1 || n > 1000 {
		return errValidation("comment must be 1-1000 characters")
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.requireVisiblePost(ctx, uid, id); err != nil {
		return err
	}
	var c commentJSON
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var owner int64
		var disabled bool
		if err := tx.QueryRow(ctx, `SELECT user_id, comments_disabled FROM posts WHERE id = $1`, id).Scan(&owner, &disabled); err != nil {
			return err
		}
		if disabled && owner != uid {
			return errForbidden("comments are disabled on this post")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO comments (post_id, user_id, body) VALUES ($1, $2, $3) RETURNING id, created_at`,
			id, uid, in.Body).Scan(&c.ID, &c.CreatedAt); err != nil {
			return err
		}
		return notify(ctx, tx, owner, uid, "comment", id)
	})
	if err != nil {
		return err
	}
	me, err := s.loadMe(ctx, uid)
	if err != nil {
		return err
	}
	c.PostID, c.Body, c.Author, c.CanDelete = id, in.Body, me.userJSON, true
	return created(w, c)
}

// deleteComment: the author or the post owner may delete (POST-7).
func (s *Server) deleteComment(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "commentID")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM comments cm USING posts p WHERE cm.id = $1 AND p.id = cm.post_id
		AND (cm.user_id = $2 OR p.user_id = $2)`, id, userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("comment")
	}
	return noContent(w)
}
