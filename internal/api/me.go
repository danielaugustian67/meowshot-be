package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"meowshot/internal/auth"
	"meowshot/internal/media"
)

type userJSON struct {
	ID        int64   `json:"id"`
	Username  string  `json:"username"`
	AvatarURL *string `json:"avatar_url"`
	Bio       string  `json:"bio"`
}

type me struct {
	userJSON
	Email        string    `json:"email"`
	Role         string    `json:"role"`
	HasPassword  bool      `json:"has_password"`
	GoogleLinked bool      `json:"google_linked"`
	CatCount     int       `json:"cat_count"`
	CreatedAt    time.Time `json:"created_at"`
}

// signed returns a short-lived URL for an object key, or nil when key is empty.
func (s *Server) signed(ctx context.Context, key *string) *string {
	if key == nil || *key == "" {
		return nil
	}
	u, err := s.store.SignedURL(ctx, *key)
	if err != nil {
		s.log.Error("sign url", "key", *key, "err", err)
		return nil
	}
	return &u
}

func (s *Server) user(ctx context.Context, id int64, username string, avatarKey *string, bio string) userJSON {
	return userJSON{ID: id, Username: username, AvatarURL: s.signed(ctx, avatarKey), Bio: bio}
}

func (s *Server) loadMe(ctx context.Context, id int64) (*me, error) {
	var m me
	var avatar *string
	err := s.db.QueryRow(ctx, `SELECT id, username, avatar_key, bio, email, role, password_hash IS NOT NULL,
		google_sub IS NOT NULL, (SELECT count(*) FROM cats WHERE user_id = users.id), created_at FROM users WHERE id = $1`, id).
		Scan(&m.ID, &m.Username, &avatar, &m.Bio, &m.Email, &m.Role, &m.HasPassword, &m.GoogleLinked, &m.CatCount, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	m.AvatarURL = s.signed(ctx, avatar)
	return &m, nil
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) error {
	m, err := s.loadMe(r.Context(), userFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	return ok(w, m)
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Username *string `json:"username"`
		Bio      *string `json:"bio"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	uid := userFrom(r.Context()).ID
	if in.Username != nil {
		u, err := normalizeUsername(*in.Username)
		if err != nil {
			return err
		}
		in.Username = &u
	}
	if in.Bio != nil {
		b := strings.TrimSpace(*in.Bio)
		if len([]rune(b)) > 300 {
			return errValidation("bio must be at most 300 characters")
		}
		in.Bio = &b
	}
	_, err := s.db.Exec(r.Context(), `UPDATE users SET username = COALESCE($2, username), bio = COALESCE($3, bio), updated_at = now() WHERE id = $1`,
		uid, in.Username, in.Bio)
	if isUniqueViolation(err, "users_username_key") {
		return errConflict("username_taken", "this username is already taken")
	}
	if err != nil {
		return err
	}
	return s.getMe(w, r)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := validatePassword(in.NewPassword); err != nil {
		return err
	}
	uid := userFrom(r.Context()).ID
	// Google-only accounts have no password yet and may set one without a current password.
	if err := s.confirmPassword(r.Context(), uid, in.CurrentPassword); err != nil {
		return err
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, uid, hash); err != nil {
		return err
	}
	return noContent(w)
}

// confirmPassword checks the password of accounts that have one; it passes for password-less accounts.
func (s *Server) confirmPassword(ctx context.Context, uid int64, password string) error {
	var hash *string
	if err := s.db.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, uid).Scan(&hash); err != nil {
		return err
	}
	if hash == nil {
		return nil
	}
	if match, err := auth.VerifyPassword(password, *hash); err != nil || !match {
		return &Error{http.StatusForbidden, "wrong_password", "current password is incorrect"}
	}
	return nil
}

// deleteMe removes the account and everything it owns (AUTH-4). Rows cascade in Postgres;
// stored objects are collected first and removed after the commit.
func (s *Server) deleteMe(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Password string `json:"password"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			return err
		}
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	if err := s.confirmPassword(ctx, uid, in.Password); err != nil {
		return err
	}
	keys, err := s.collectKeys(ctx, `SELECT key FROM photos WHERE user_id = $1 UNION ALL SELECT thumb_key FROM photos WHERE user_id = $1
		UNION ALL SELECT avatar_key FROM users WHERE id = $1 AND avatar_key IS NOT NULL`, uid)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid); err != nil {
		return err
	}
	s.deleteObjects(keys)
	return noContent(w)
}

func (s *Server) putAvatar(w http.ResponseWriter, r *http.Request) error {
	data, err := s.readUpload(w, r, "photo")
	if err != nil {
		return err
	}
	var webp []byte
	err = s.withImageSlot(r.Context(), func() (err error) {
		webp, err = media.ProcessAvatar(data)
		return err
	})
	if err != nil {
		return mediaError(err)
	}
	ctx := r.Context()
	uid := userFrom(ctx).ID
	key := objectKey(uid, "avatar")
	if err := s.store.Put(ctx, key, webp, "image/webp"); err != nil {
		return err
	}
	var old *string
	if err := s.db.QueryRow(ctx, `UPDATE users u SET avatar_key = $2, updated_at = now() FROM users prev
		WHERE u.id = $1 AND prev.id = u.id RETURNING prev.avatar_key`, uid, key).Scan(&old); err != nil {
		s.deleteObjects([]string{key})
		return err
	}
	if old != nil {
		s.deleteObjects([]string{*old})
	}
	return s.getMe(w, r)
}

func (s *Server) deleteAvatar(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var old *string
	err := s.db.QueryRow(ctx, `UPDATE users u SET avatar_key = NULL, updated_at = now() FROM users prev
		WHERE u.id = $1 AND prev.id = u.id RETURNING prev.avatar_key`, userFrom(ctx).ID).Scan(&old)
	if err != nil {
		return err
	}
	if old != nil {
		s.deleteObjects([]string{*old})
	}
	return noContent(w)
}

func mediaError(err error) error {
	if errors.Is(err, media.ErrUnsupported) {
		return &Error{http.StatusUnsupportedMediaType, "unsupported_media", err.Error()}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return errValidation("could not process image: %v", err)
}
