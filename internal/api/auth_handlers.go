package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"meowshot/internal/auth"
)

var usernameRe = regexp.MustCompile(`^[a-z0-9_.]{3,30}$`)

func normalizeUsername(u string) (string, error) {
	u = strings.ToLower(strings.TrimSpace(u))
	if !usernameRe.MatchString(u) {
		return "", errValidation("username must be 3-30 characters: a-z, 0-9, '_' or '.'")
	}
	return u, nil
}

func normalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || len(e) > 254 {
		return "", errValidation("invalid email address")
	}
	return e, nil
}

func validatePassword(p string) error {
	if n := len([]rune(p)); n < 8 || n > 128 {
		return errValidation("password must be 8-128 characters")
	}
	return nil
}

func (s *Server) checkInvite(code string) error {
	if s.cfg.InviteCode == "" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.cfg.InviteCode)) != 1 {
		return &Error{http.StatusForbidden, "invite_required", "a valid invite code is required to sign up"}
	}
	return nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	IsNewUser    bool   `json:"is_new_user"`
	User         *me    `json:"user"`
}

func (s *Server) issueTokens(ctx context.Context, userID int64, isNew bool) (*tokenResponse, error) {
	access, err := s.tokens.IssueAccess(userID)
	if err != nil {
		return nil, err
	}
	refresh, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, hash, time.Now().Add(s.cfg.RefreshTokenTTL)); err != nil {
		return nil, err
	}
	u, err := s.loadMe(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &tokenResponse{
		AccessToken: access, TokenType: "Bearer", ExpiresIn: int(s.tokens.AccessTTL().Seconds()),
		RefreshToken: refresh, IsNewUser: isNew, User: u,
	}, nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email      string `json:"email"`
		Password   string `json:"password"`
		Username   string `json:"username"`
		InviteCode string `json:"invite_code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return err
	}
	username, err := normalizeUsername(in.Username)
	if err != nil {
		return err
	}
	if err := validatePassword(in.Password); err != nil {
		return err
	}
	if err := s.checkInvite(in.InviteCode); err != nil {
		return err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	var id int64
	err = s.db.QueryRow(r.Context(), `INSERT INTO users (username, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		username, email, hash).Scan(&id)
	switch {
	case isUniqueViolation(err, "users_email_key"):
		return errConflict("email_taken", "an account with this email already exists")
	case isUniqueViolation(err, "users_username_key"):
		return errConflict("username_taken", "this username is already taken")
	case err != nil:
		return err
	}
	resp, err := s.issueTokens(r.Context(), id, true)
	if err != nil {
		return err
	}
	return created(w, resp)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email    string `json:"email"` // email or username
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ident := strings.ToLower(strings.TrimSpace(in.Email))
	var id int64
	var hash *string
	var suspended *time.Time
	err := s.db.QueryRow(r.Context(), `SELECT id, password_hash, suspended_at FROM users WHERE email = $1 OR username = $1`, ident).
		Scan(&id, &hash, &suspended)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && hash == nil) {
		auth.BurnPasswordCheck(in.Password)
		return errUnauthorized("wrong email or password")
	}
	if err != nil {
		return err
	}
	if match, err := auth.VerifyPassword(in.Password, *hash); err != nil || !match {
		return errUnauthorized("wrong email or password")
	}
	if suspended != nil {
		return &Error{http.StatusForbidden, "account_suspended", "this account has been suspended"}
	}
	resp, err := s.issueTokens(r.Context(), id, false)
	if err != nil {
		return err
	}
	return ok(w, resp)
}

// loginGoogle exchanges a Google ID token (from Google Sign-In in the Expo app) for Meowshot tokens.
// Accounts are matched by Google subject first, then by verified email.
func (s *Server) loginGoogle(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		IDToken    string `json:"id_token"`
		InviteCode string `json:"invite_code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if !s.google.Enabled() {
		return &Error{http.StatusNotImplemented, "google_disabled", "Google sign-in is not configured on the server"}
	}
	ident, err := s.google.Verify(r.Context(), in.IDToken)
	if err != nil {
		return errUnauthorized("invalid Google ID token")
	}
	if !ident.EmailVerified {
		return errUnauthorized("Google account email is not verified")
	}
	email := strings.ToLower(ident.Email)

	var id int64
	var suspended *time.Time
	isNew := false
	err = s.db.QueryRow(r.Context(), `SELECT id, suspended_at FROM users WHERE google_sub = $1`, ident.Subject).Scan(&id, &suspended)
	if errors.Is(err, pgx.ErrNoRows) {
		var existing int64
		var verified bool
		err = s.db.QueryRow(r.Context(), `SELECT id, email_verified, suspended_at FROM users WHERE email = $1`, email).
			Scan(&existing, &verified, &suspended)
		switch {
		case err == nil:
			// Link Google to the existing email account. If that email was never verified, someone
			// else may have registered it first, so drop the unverified password and its sessions.
			id = existing
			if _, err := s.db.Exec(r.Context(), `UPDATE users SET google_sub = $2, email_verified = TRUE,
				password_hash = CASE WHEN email_verified THEN password_hash ELSE NULL END, updated_at = now() WHERE id = $1`,
				id, ident.Subject); err != nil {
				return err
			}
			if !verified {
				if _, err := s.db.Exec(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, id); err != nil {
					return err
				}
			}
		case errors.Is(err, pgx.ErrNoRows):
			if err := s.checkInvite(in.InviteCode); err != nil {
				return err
			}
			if id, err = s.createGoogleUser(r.Context(), email, ident.Subject); err != nil {
				return err
			}
			isNew = true
		default:
			return err
		}
	} else if err != nil {
		return err
	}
	if suspended != nil {
		return &Error{http.StatusForbidden, "account_suspended", "this account has been suspended"}
	}
	resp, err := s.issueTokens(r.Context(), id, isNew)
	if err != nil {
		return err
	}
	return ok(w, resp)
}

// createGoogleUser picks a username from the email's local part; the app should offer to change it
// when is_new_user is true (AUTH-2).
func (s *Server) createGoogleUser(ctx context.Context, email, sub string) (int64, error) {
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.':
			return r
		}
		return '_'
	}, strings.ToLower(strings.SplitN(email, "@", 2)[0]))
	if len(base) > 24 {
		base = base[:24]
	}
	for len(base) < 3 {
		base += "_"
	}
	for attempt := 0; attempt < 8; attempt++ {
		name := base
		if attempt > 0 {
			n, _ := rand.Int(rand.Reader, big.NewInt(100000))
			name = fmt.Sprintf("%s%05d", base, n.Int64())
		}
		var id int64
		err := s.db.QueryRow(ctx, `INSERT INTO users (username, email, email_verified, google_sub) VALUES ($1, $2, TRUE, $3) RETURNING id`,
			name, email, sub).Scan(&id)
		if isUniqueViolation(err, "users_username_key") {
			continue
		}
		return id, err
	}
	return 0, errors.New("could not allocate a username")
}

// refresh rotates the refresh token. Presenting an already-rotated token is treated as theft and
// revokes every session of that user. Clients must therefore serialise refresh calls.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var tokenID, userID int64
	var expires time.Time
	var revoked, suspended *time.Time
	err = tx.QueryRow(ctx, `SELECT t.id, t.user_id, t.expires_at, t.revoked_at, u.suspended_at
		FROM refresh_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = $1 FOR UPDATE OF t`,
		auth.HashOpaqueToken(in.RefreshToken)).Scan(&tokenID, &userID, &expires, &revoked, &suspended)
	if errors.Is(err, pgx.ErrNoRows) {
		return errUnauthorized("invalid refresh token")
	}
	if err != nil {
		return err
	}
	if revoked != nil {
		if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return errUnauthorized("refresh token reuse detected; please sign in again")
	}
	if time.Now().After(expires) {
		return errUnauthorized("refresh token expired")
	}
	if suspended != nil {
		return &Error{http.StatusForbidden, "account_suspended", "this account has been suspended"}
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE id = $1`, tokenID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	resp, err := s.issueTokens(ctx, userID, false)
	if err != nil {
		return err
	}
	return ok(w, resp)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		auth.HashOpaqueToken(in.RefreshToken)); err != nil {
		return err
	}
	return noContent(w)
}

// forgotPassword always answers 202 so it cannot be used to discover registered emails.
func (s *Server) forgotPassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email string `json:"email"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	var id int64
	var recent int
	err := s.db.QueryRow(r.Context(), `SELECT u.id, (SELECT count(*) FROM password_resets pr
		WHERE pr.user_id = u.id AND pr.created_at > now() - interval '1 hour')
		FROM users u WHERE u.email = $1 AND u.suspended_at IS NULL`, email).Scan(&id, &recent)
	if err == nil && recent < 5 {
		code, err := auth.NewResetCode()
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(r.Context(), `INSERT INTO password_resets (user_id, code_hash, expires_at) VALUES ($1, $2, now() + interval '15 minutes')`,
			id, s.tokens.HashResetCode(id, code)); err != nil {
			return err
		}
		body := fmt.Sprintf("Kode reset password Meowshot kamu: %s\n\nKode berlaku 15 menit. Abaikan email ini jika kamu tidak memintanya.\n", code)
		if err := s.mail.Send(email, "Kode reset password Meowshot", body); err != nil {
			s.log.Error("send reset email", "err", err)
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "if the email exists, a reset code has been sent"})
	return nil
}

func (s *Server) resetPassword(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Email       string `json:"email"`
		Code        string `json:"code"`
		NewPassword string `json:"new_password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	if err := validatePassword(in.NewPassword); err != nil {
		return err
	}
	invalid := &Error{http.StatusBadRequest, "invalid_code", "the reset code is invalid or expired"}
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var userID, resetID int64
	var codeHash []byte
	var attempts int
	err = tx.QueryRow(ctx, `SELECT u.id, pr.id, pr.code_hash, pr.attempts FROM users u
		JOIN password_resets pr ON pr.user_id = u.id
		WHERE u.email = $1 AND pr.used_at IS NULL AND pr.expires_at > now()
		ORDER BY pr.created_at DESC LIMIT 1 FOR UPDATE OF pr`, strings.ToLower(strings.TrimSpace(in.Email))).
		Scan(&userID, &resetID, &codeHash, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid
	}
	if err != nil {
		return err
	}
	if attempts >= 5 || subtle.ConstantTimeCompare(codeHash, s.tokens.HashResetCode(userID, strings.TrimSpace(in.Code))) != 1 {
		if _, err := tx.Exec(ctx, `UPDATE password_resets SET attempts = attempts + 1 WHERE id = $1`, resetID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return invalid
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, email_verified = TRUE, updated_at = now() WHERE id = $1`, userID, hash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE password_resets SET used_at = now() WHERE user_id = $1 AND used_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return noContent(w)
}
