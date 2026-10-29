package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Friendship status between the viewer and another user, as seen by the viewer.
const (
	relNone     = "none"
	relFriends  = "friends"
	relOutgoing = "request_sent"
	relIncoming = "request_received"
	relSelf     = "self"
)

type relationJSON struct {
	Status    string `json:"status"`
	RequestID *int64 `json:"request_id,omitempty"`
}

func (s *Server) relation(ctx context.Context, viewer, other int64) (relationJSON, error) {
	if viewer == other {
		return relationJSON{Status: relSelf}, nil
	}
	var id, requester int64
	var status string
	err := s.db.QueryRow(ctx, `SELECT id, requester_id, status FROM friendships WHERE user_low = LEAST($1::bigint, $2::bigint)
		AND user_high = GREATEST($1::bigint, $2::bigint)`, viewer, other).Scan(&id, &requester, &status)
	switch {
	case err == pgx.ErrNoRows:
		return relationJSON{Status: relNone}, nil
	case err != nil:
		return relationJSON{}, err
	case status == "ACCEPTED":
		return relationJSON{Status: relFriends}, nil
	case requester == viewer:
		return relationJSON{Status: relOutgoing, RequestID: &id}, nil
	default:
		return relationJSON{Status: relIncoming, RequestID: &id}, nil
	}
}

type profileJSON struct {
	userJSON
	CatCount     int          `json:"cat_count"`
	PostCount    int          `json:"post_count"` // posts this viewer can see
	FriendCount  int          `json:"friend_count"`
	Relationship relationJSON `json:"relationship"`
	JoinedAt     time.Time    `json:"joined_at"`
}

// getProfile: PROF-1 / PUB-5. Blocked or suspended users answer 404.
func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	viewer := userFrom(ctx).ID
	var p profileJSON
	var avatar *string
	err := s.db.QueryRow(ctx, `SELECT u.id, u.username, u.avatar_key, u.bio, u.created_at,
		(SELECT count(*) FROM cats WHERE user_id = u.id),
		(SELECT count(*) FROM posts p WHERE p.user_id = u.id AND `+postVisibleSQL("$1")+`),
		(SELECT count(*) FROM friendships WHERE (user_low = u.id OR user_high = u.id) AND status = 'ACCEPTED')
		FROM users u WHERE u.username = $2 AND `+userVisibleSQL("$1"), viewer, strings.ToLower(chi.URLParam(r, "username"))).
		Scan(&p.ID, &p.Username, &avatar, &p.Bio, &p.JoinedAt, &p.CatCount, &p.PostCount, &p.FriendCount)
	if err == pgx.ErrNoRows {
		return errNotFound("user")
	}
	if err != nil {
		return err
	}
	p.AvatarURL = s.signed(ctx, avatar)
	if p.Relationship, err = s.relation(ctx, viewer, p.ID); err != nil {
		return err
	}
	return ok(w, p)
}

type userWithRelation struct {
	userJSON
	Relationship relationJSON `json:"relationship"`
}

// searchUsers: GET /users/search?q= — username prefix search (FR-1).
func (s *Server) searchUsers(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	viewer := userFrom(ctx).ID
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(r.URL.Query().Get("q"), "@")))
	if q == "" {
		return ok(w, map[string]any{"items": []userWithRelation{}})
	}
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	rows, err := s.db.Query(ctx, `SELECT u.id, u.username, u.avatar_key, u.bio FROM users u
		WHERE u.username LIKE $2 || '%' AND `+userVisibleSQL("$1")+` ORDER BY length(u.username), u.username LIMIT 20`, viewer, q)
	if err != nil {
		return err
	}
	users, err := s.collectUsers(ctx, rows)
	if err != nil {
		return err
	}
	out := make([]userWithRelation, 0, len(users))
	for _, u := range users {
		rel, err := s.relation(ctx, viewer, u.ID)
		if err != nil {
			return err
		}
		out = append(out, userWithRelation{userJSON: u, Relationship: rel})
	}
	return ok(w, map[string]any{"items": out})
}

func (s *Server) collectUsers(ctx context.Context, rows pgx.Rows) ([]userJSON, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (userJSON, error) {
		var u userJSON
		var avatar *string
		err := row.Scan(&u.ID, &u.Username, &avatar, &u.Bio)
		u.AvatarURL = s.signed(ctx, avatar)
		return u, err
	})
}

type friendRequestJSON struct {
	ID        int64     `json:"id"`
	User      userJSON  `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

// sendFriendRequest: POST /friends/requests {"username"} (FR-2). If the other user already sent a
// request to the viewer, this accepts it instead of creating a second one.
func (s *Server) sendFriendRequest(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username string `json:"username"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	me := userFrom(ctx).ID
	var target int64
	err := s.db.QueryRow(ctx, `SELECT u.id FROM users u WHERE u.username = $2 AND `+userVisibleSQL("$1"),
		me, strings.ToLower(strings.TrimSpace(in.Username))).Scan(&target)
	if err == pgx.ErrNoRows {
		return errNotFound("user")
	}
	if err != nil {
		return err
	}
	if target == me {
		return errValidation("you cannot befriend yourself")
	}

	var result relationJSON
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var id, requester int64
		var status string
		err := tx.QueryRow(ctx, `SELECT id, requester_id, status FROM friendships WHERE user_low = LEAST($1::bigint, $2::bigint)
			AND user_high = GREATEST($1::bigint, $2::bigint) FOR UPDATE`, me, target).Scan(&id, &requester, &status)
		switch {
		case err == pgx.ErrNoRows:
			if err := tx.QueryRow(ctx, `INSERT INTO friendships (user_low, user_high, requester_id)
				VALUES (LEAST($1::bigint, $2::bigint), GREATEST($1::bigint, $2::bigint), $1) RETURNING id`, me, target).Scan(&id); err != nil {
				return err
			}
			result = relationJSON{Status: relOutgoing, RequestID: &id}
			return notify(ctx, tx, target, me, "friend_request", id)
		case err != nil:
			return err
		case status == "ACCEPTED":
			return errConflict("already_friends", "you are already friends")
		case requester == me:
			return errConflict("already_requested", "friend request already sent")
		default:
			if _, err := tx.Exec(ctx, `UPDATE friendships SET status = 'ACCEPTED', accepted_at = now() WHERE id = $1`, id); err != nil {
				return err
			}
			result = relationJSON{Status: relFriends}
			return notify(ctx, tx, target, me, "friend_accepted", id)
		}
	})
	if isUniqueViolation(err, "") {
		return errConflict("already_requested", "friend request already exists")
	}
	if err != nil {
		return err
	}
	return created(w, result)
}

// listFriendRequests: GET /friends/requests?direction=incoming|outgoing
func (s *Server) listFriendRequests(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	me := userFrom(ctx).ID
	cond := "f.requester_id <> $1"
	if r.URL.Query().Get("direction") == "outgoing" {
		cond = "f.requester_id = $1"
	}
	rows, err := s.db.Query(ctx, `SELECT f.id, f.created_at, u.id, u.username, u.avatar_key, u.bio
		FROM friendships f JOIN users u ON u.id = CASE WHEN f.user_low = $1 THEN f.user_high ELSE f.user_low END
		WHERE (f.user_low = $1 OR f.user_high = $1) AND f.status = 'PENDING' AND `+cond+` AND u.suspended_at IS NULL
		ORDER BY f.id DESC LIMIT 200`, me)
	if err != nil {
		return err
	}
	reqs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (friendRequestJSON, error) {
		var fr friendRequestJSON
		var avatar *string
		err := row.Scan(&fr.ID, &fr.CreatedAt, &fr.User.ID, &fr.User.Username, &avatar, &fr.User.Bio)
		fr.User.AvatarURL = s.signed(ctx, avatar)
		return fr, err
	})
	if err != nil {
		return err
	}
	if reqs == nil {
		reqs = []friendRequestJSON{}
	}
	return ok(w, map[string]any{"items": reqs})
}

func (s *Server) acceptFriendRequest(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "requestID")
	if err != nil {
		return err
	}
	ctx := r.Context()
	me := userFrom(ctx).ID
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var requester int64
		err := tx.QueryRow(ctx, `UPDATE friendships SET status = 'ACCEPTED', accepted_at = now()
			WHERE id = $1 AND status = 'PENDING' AND requester_id <> $2 AND $2 IN (user_low, user_high)
			RETURNING requester_id`, id, me).Scan(&requester)
		if err == pgx.ErrNoRows {
			return errNotFound("friend request")
		}
		if err != nil {
			return err
		}
		return notify(ctx, tx, requester, me, "friend_accepted", id)
	})
	if err != nil {
		return err
	}
	return ok(w, relationJSON{Status: relFriends})
}

// deleteFriendRequest declines an incoming request or cancels an outgoing one.
func (s *Server) deleteFriendRequest(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "requestID")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM friendships WHERE id = $1 AND status = 'PENDING' AND $2 IN (user_low, user_high)`,
		id, userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("friend request")
	}
	return noContent(w)
}

type friendJSON struct {
	userJSON
	Since time.Time `json:"since"`
	// cursor value for pagination
	FriendshipID int64 `json:"friendship_id"`
}

func (s *Server) listFriends(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	me := userFrom(ctx).ID
	pg := readPage(r, 50, 200)
	rows, err := s.db.Query(ctx, `SELECT f.id, COALESCE(f.accepted_at, f.created_at), u.id, u.username, u.avatar_key, u.bio
		FROM friendships f JOIN users u ON u.id = CASE WHEN f.user_low = $1 THEN f.user_high ELSE f.user_low END
		WHERE (f.user_low = $1 OR f.user_high = $1) AND f.status = 'ACCEPTED' AND u.suspended_at IS NULL AND f.id < $2
		ORDER BY f.id DESC LIMIT $3`, me, pg.cursorArg(), pg.Limit+1)
	if err != nil {
		return err
	}
	friends, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (friendJSON, error) {
		var f friendJSON
		var avatar *string
		err := row.Scan(&f.FriendshipID, &f.Since, &f.ID, &f.Username, &avatar, &f.Bio)
		f.AvatarURL = s.signed(ctx, avatar)
		return f, err
	})
	if err != nil {
		return err
	}
	return ok(w, paginate(friends, pg.Limit, func(f friendJSON) int64 { return f.FriendshipID }))
}

func (s *Server) removeFriend(w http.ResponseWriter, r *http.Request) error {
	other, err := pathID(r, "userID")
	if err != nil {
		return err
	}
	tag, err := s.db.Exec(r.Context(), `DELETE FROM friendships WHERE user_low = LEAST($1::bigint, $2::bigint)
		AND user_high = GREATEST($1::bigint, $2::bigint) AND status = 'ACCEPTED'`, userFrom(r.Context()).ID, other)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errNotFound("friend")
	}
	return noContent(w)
}

// blockUser (FR-5, MOD-2) ends any friendship or pending request; visibility rules in authz.go
// then hide each side's content from the other, including Explore and comments.
func (s *Server) blockUser(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		UserID int64 `json:"user_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	me := userFrom(ctx).ID
	if in.UserID == me {
		return errValidation("you cannot block yourself")
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO blocks (blocker_id, blocked_id) SELECT $1, id FROM users WHERE id = $2
			ON CONFLICT DO NOTHING`, me, in.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, in.UserID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errNotFound("user")
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM friendships WHERE user_low = LEAST($1::bigint, $2::bigint)
			AND user_high = GREATEST($1::bigint, $2::bigint)`, me, in.UserID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM notifications WHERE (user_id = $1 AND actor_id = $2) OR (user_id = $2 AND actor_id = $1)`, me, in.UserID)
		return err
	})
	if err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) unblockUser(w http.ResponseWriter, r *http.Request) error {
	other, err := pathID(r, "userID")
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM blocks WHERE blocker_id = $1 AND blocked_id = $2`, userFrom(r.Context()).ID, other); err != nil {
		return err
	}
	return noContent(w)
}

func (s *Server) listBlocks(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	rows, err := s.db.Query(ctx, `SELECT u.id, u.username, u.avatar_key, u.bio FROM blocks b JOIN users u ON u.id = b.blocked_id
		WHERE b.blocker_id = $1 ORDER BY b.created_at DESC`, userFrom(ctx).ID)
	if err != nil {
		return err
	}
	users, err := s.collectUsers(ctx, rows)
	if err != nil {
		return err
	}
	if users == nil {
		users = []userJSON{}
	}
	return ok(w, map[string]any{"items": users})
}
