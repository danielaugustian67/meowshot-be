package api

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// notify records an in-app notification (NOTIF-1). Self-actions are skipped.
func notify(ctx context.Context, tx pgx.Tx, userID, actorID int64, kind string, refID int64) error {
	if userID == actorID {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, type, ref_id) VALUES ($1, $2, $3, $4)`, userID, actorID, kind, refID)
	return err
}

type notificationJSON struct {
	ID        int64      `json:"id"`
	Type      string     `json:"type"` // friend_request, friend_accepted, reaction, comment
	Actor     *userJSON  `json:"actor"`
	RefID     *int64     `json:"ref_id"` // friendship id for friend_*, post id for reaction/comment
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	uid := userFrom(ctx).ID
	pg := readPage(r, 30, 100)
	rows, err := s.db.Query(ctx, `SELECT n.id, n.type, n.ref_id, n.read_at, n.created_at, u.id, u.username, u.avatar_key, u.bio
		FROM notifications n LEFT JOIN users u ON u.id = n.actor_id
		WHERE n.user_id = $1 AND n.id < $2 AND (u.id IS NULL OR u.suspended_at IS NULL)
		ORDER BY n.id DESC LIMIT $3`, uid, pg.cursorArg(), pg.Limit+1)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (notificationJSON, error) {
		var n notificationJSON
		var actorID *int64
		var username, avatar, bio *string
		if err := row.Scan(&n.ID, &n.Type, &n.RefID, &n.ReadAt, &n.CreatedAt, &actorID, &username, &avatar, &bio); err != nil {
			return n, err
		}
		if actorID != nil {
			u := s.user(ctx, *actorID, *username, avatar, *bio)
			n.Actor = &u
		}
		return n, nil
	})
	if err != nil {
		return err
	}
	return ok(w, paginate(items, pg.Limit, func(n notificationJSON) int64 { return n.ID }))
}

func (s *Server) unreadCount(w http.ResponseWriter, r *http.Request) error {
	var n int
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM notifications WHERE user_id = $1 AND read_at IS NULL`,
		userFrom(r.Context()).ID).Scan(&n); err != nil {
		return err
	}
	return ok(w, map[string]int{"unread": n})
}

// markNotificationsRead: {"ids": [..]} marks those, {"all": true} marks everything.
func (s *Server) markNotificationsRead(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	uid := userFrom(r.Context()).ID
	var err error
	if in.All {
		_, err = s.db.Exec(r.Context(), `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND read_at IS NULL`, uid)
	} else {
		_, err = s.db.Exec(r.Context(), `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND id = ANY($2) AND read_at IS NULL`, uid, in.IDs)
	}
	if err != nil {
		return err
	}
	return noContent(w)
}
