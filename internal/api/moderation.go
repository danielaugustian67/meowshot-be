package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// createReport: MOD-1. Users can only report what they can see.
func (s *Server) createReport(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		TargetType string `json:"target_type"` // POST, COMMENT, USER
		TargetID   int64  `json:"target_id"`
		Reason     string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if n := len([]rune(in.Reason)); n < 1 || n > 1000 {
		return errValidation("reason must be 1-1000 characters")
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID

	var visible bool
	var err error
	switch in.TargetType {
	case "POST":
		visible, err = s.canViewPost(ctx, uid, in.TargetID)
	case "USER":
		visible, err = s.canViewUser(ctx, uid, in.TargetID)
		visible = visible && in.TargetID != uid
	case "COMMENT":
		err = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM comments cm JOIN posts p ON p.id = cm.post_id
			WHERE cm.id = $2 AND `+postVisibleSQL("$1")+` AND `+commentVisibleSQL("$1")+`)`, uid, in.TargetID).Scan(&visible)
	default:
		return errValidation("target_type must be POST, COMMENT or USER")
	}
	if err != nil {
		return err
	}
	if !visible {
		return errNotFound(strings.ToLower(in.TargetType))
	}
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO reports (reporter_id, target_type, target_id, reason) VALUES ($1, $2, $3, $4) RETURNING id`,
		uid, in.TargetType, in.TargetID, in.Reason).Scan(&id); err != nil {
		return err
	}
	return created(w, map[string]any{"id": id, "status": "OPEN"})
}

type reportJSON struct {
	ID         int64      `json:"id"`
	Reporter   *userJSON  `json:"reporter"`
	TargetType string     `json:"target_type"`
	TargetID   int64      `json:"target_id"`
	Reason     string     `json:"reason"`
	Status     string     `json:"status"`
	ResolvedAt *time.Time `json:"resolved_at"`
	CreatedAt  time.Time  `json:"created_at"`
	Target     any        `json:"target"` // snapshot for the moderator; null if already deleted
}

// adminListReports: GET /admin/reports?status=OPEN|RESOLVED|DISMISSED (MOD-4), oldest first.
func (s *Server) adminListReports(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "OPEN"
	}
	pg := readPage(r, 30, 100)
	rows, err := s.db.Query(ctx, `SELECT rp.id, rp.target_type, rp.target_id, rp.reason, rp.status, rp.resolved_at, rp.created_at,
		u.id, u.username, u.avatar_key, u.bio
		FROM reports rp LEFT JOIN users u ON u.id = rp.reporter_id
		WHERE rp.status = $1 AND rp.id > $2 ORDER BY rp.id LIMIT $3`, status, pg.Cursor, pg.Limit+1)
	if err != nil {
		return err
	}
	reports, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*reportJSON, error) {
		var rp reportJSON
		var rid *int64
		var username, avatar, bio *string
		if err := row.Scan(&rp.ID, &rp.TargetType, &rp.TargetID, &rp.Reason, &rp.Status, &rp.ResolvedAt, &rp.CreatedAt,
			&rid, &username, &avatar, &bio); err != nil {
			return nil, err
		}
		if rid != nil {
			u := s.user(ctx, *rid, *username, avatar, *bio)
			rp.Reporter = &u
		}
		return &rp, nil
	})
	if err != nil {
		return err
	}
	for _, rp := range reports {
		if rp.Target, err = s.reportTarget(ctx, rp.TargetType, rp.TargetID); err != nil {
			return err
		}
	}
	return ok(w, paginate(reports, pg.Limit, func(rp *reportJSON) int64 { return rp.ID }))
}

// reportTarget loads the reported content regardless of its visibility, for moderators only.
func (s *Server) reportTarget(ctx context.Context, kind string, id int64) (any, error) {
	switch kind {
	case "POST":
		var owner int64
		err := s.db.QueryRow(ctx, `SELECT user_id FROM posts WHERE id = $1`, id).Scan(&owner)
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		// Load as the owner so visibility never hides it from the moderator.
		p, err := s.loadPost(ctx, owner, id)
		if err != nil {
			return nil, err
		}
		p.IsOwner = false
		p.Location = p.SharedLocation
		return p, nil
	case "COMMENT":
		var c commentJSON
		var avatar *string
		err := s.db.QueryRow(ctx, `SELECT cm.id, cm.post_id, cm.body, cm.created_at, u.id, u.username, u.avatar_key, u.bio
			FROM comments cm JOIN users u ON u.id = cm.user_id WHERE cm.id = $1`, id).
			Scan(&c.ID, &c.PostID, &c.Body, &c.CreatedAt, &c.Author.ID, &c.Author.Username, &avatar, &c.Author.Bio)
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		c.Author.AvatarURL = s.signed(ctx, avatar)
		return c, err
	case "USER":
		var u struct {
			userJSON
			Email       string     `json:"email"`
			SuspendedAt *time.Time `json:"suspended_at"`
		}
		var avatar *string
		err := s.db.QueryRow(ctx, `SELECT id, username, avatar_key, bio, email, suspended_at FROM users WHERE id = $1`, id).
			Scan(&u.ID, &u.Username, &avatar, &u.Bio, &u.Email, &u.SuspendedAt)
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		u.AvatarURL = s.signed(ctx, avatar)
		return u, err
	}
	return nil, nil
}

func (s *Server) adminUpdateReport(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "reportID")
	if err != nil {
		return err
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if in.Status != "RESOLVED" && in.Status != "DISMISSED" && in.Status != "OPEN" {
		return errValidation("status must be OPEN, RESOLVED or DISMISSED")
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE reports SET status = $2,
		resolved_by = CASE WHEN $2 = 'OPEN' THEN NULL ELSE $3::bigint END,
		resolved_at = CASE WHEN $2 = 'OPEN' THEN NULL ELSE now() END WHERE id = $1`, id, in.Status, userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("report")
	}
	return noContent(w)
}

// adminDeletePost removes a post; its photos are removed too, since they were reported content.
func (s *Server) adminDeletePost(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "postID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	var keys []string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM photos WHERE post_id = $1 RETURNING key, thumb_key, cat_id`, id)
		if err != nil {
			return err
		}
		var cats []int64
		for rows.Next() {
			var k, t string
			var c int64
			if err := rows.Scan(&k, &t, &c); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, k, t)
			cats = append(cats, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE cats SET cover_photo_id = (SELECT id FROM photos WHERE cat_id = cats.id ORDER BY id LIMIT 1)
			WHERE id = ANY($1) AND cover_photo_id IS NULL`, cats); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM posts WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound("post")
		}
		return s.resolveReportsFor(ctx, tx, "POST", id, userFrom(ctx).ID)
	})
	if err != nil {
		return err
	}
	s.deleteObjects(keys)
	return noContent(w)
}

func (s *Server) adminDeleteComment(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "commentID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	return pgxNoContent(w, pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM comments WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound("comment")
		}
		return s.resolveReportsFor(ctx, tx, "COMMENT", id, userFrom(ctx).ID)
	}))
}

// adminSuspend hides the user's content, blocks sign-in and revokes all sessions.
func (s *Server) adminSuspend(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "userID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	admin := userFrom(ctx).ID
	if id == admin {
		return errValidation("you cannot suspend yourself")
	}
	return pgxNoContent(w, pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE users SET suspended_at = COALESCE(suspended_at, now()) WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound("user")
		}
		if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, id); err != nil {
			return err
		}
		return s.resolveReportsFor(ctx, tx, "USER", id, admin)
	}))
}

func (s *Server) adminUnsuspend(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "userID")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE users SET suspended_at = NULL WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("user")
	}
	return noContent(w)
}

func (s *Server) resolveReportsFor(ctx context.Context, tx pgx.Tx, kind string, id, admin int64) error {
	_, err := tx.Exec(ctx, `UPDATE reports SET status = 'RESOLVED', resolved_by = $3, resolved_at = now()
		WHERE target_type = $1 AND target_id = $2 AND status = 'OPEN'`, kind, id, admin)
	return err
}

func pgxNoContent(w http.ResponseWriter, err error) error {
	if err != nil {
		return err
	}
	return noContent(w)
}
