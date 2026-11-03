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

const (
	signupCodeTTL = 15 * time.Minute
	// Caps emails per pending sign-up, and new sign-ups per email per hour.
	maxSignupSends = 5
)

func errEmailTaken() *Error {
	return errConflict("email_taken", "an account with this email already exists")
}

func errUsernameTaken() *Error {
	return errConflict("username_taken", "this username is already taken")
}

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

// register holds the sign-up in pending_signups and emails a code; verifyRegistration creates the account.
// The returned verification_id ties the code to this request, so nobody else's sign-up for the
// same email can claim it (and put their own password on the account).
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
	ctx := r.Context()
	var emailTaken, usernameTaken bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE email = $1), EXISTS (SELECT 1 FROM users WHERE username = $2)`,
		email, username).Scan(&emailTaken, &usernameTaken); err != nil {
		return err
	}
	if emailTaken {
		return errEmailTaken()
	}
	if usernameTaken {
		return errUsernameTaken()
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM pending_signups WHERE expires_at < now() AND created_at < now() - interval '1 hour'`); err != nil {
		return err
	}
	var recent int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM pending_signups WHERE email = $1 AND created_at > now() - interval '1 hour'`,
		email).Scan(&recent); err != nil {
		return err
	}
	if recent >= maxSignupSends {
		return errTooMany()
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return err
	}
	token, tokenHash, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	code, err := auth.NewResetCode()
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO pending_signups (token_hash, email, username, password_hash, code_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		tokenHash, email, username, hash, s.tokens.HashSignupCode(tokenHash, code), time.Now().Add(signupCodeTTL)); err != nil {
		return err
	}
	if err := s.sendSignupCode(email, code); err != nil {
		if _, delErr := s.db.Exec(ctx, `DELETE FROM pending_signups WHERE token_hash = $1`, tokenHash); delErr != nil {
			s.log.Error("drop unsent signup", "err", delErr)
		}
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"verification_id": token, "email": email, "expires_in": int(signupCodeTTL.Seconds()),
	})
	return nil
}

// sendSignupCode fails the request when the email cannot go out: unlike a reset, the user is waiting for it right now.
func (s *Server) sendSignupCode(email, code string) error {
	body := fmt.Sprintf("Kode verifikasi Meowshot kamu: %s\n\nKode berlaku 15 menit. Abaikan email ini jika kamu tidak mendaftar.\n", code)
	if err := s.mail.Send(email, "Kode verifikasi Meowshot", body); err != nil {
		s.log.Error("send signup email", "err", err)
		return &Error{http.StatusServiceUnavailable, "email_failed", "the verification email could not be sent"}
	}
	return nil
}

// verifyRegistration creates the account once the emailed code is confirmed.
func (s *Server) verifyRegistration(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		VerificationID string `json:"verification_id"`
		Code           string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	invalid := &Error{http.StatusBadRequest, "invalid_code", "the verification code is invalid or expired"}
	tokenHash := auth.HashOpaqueToken(in.VerificationID)
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var pendingID int64
	var email, username, passwordHash string
	var codeHash []byte
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id, email, username, password_hash, code_hash, attempts FROM pending_signups
		WHERE token_hash = $1 AND expires_at > now() FOR UPDATE`, tokenHash).
		Scan(&pendingID, &email, &username, &passwordHash, &codeHash, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return invalid
	}
	if err != nil {
		return err
	}
	if attempts >= 5 || subtle.ConstantTimeCompare(codeHash, s.tokens.HashSignupCode(tokenHash, strings.TrimSpace(in.Code))) != 1 {
		if _, err := tx.Exec(ctx, `UPDATE pending_signups SET attempts = attempts + 1 WHERE id = $1`, pendingID); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return invalid
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO users (username, email, email_verified, password_hash) VALUES ($1, $2, TRUE, $3) RETURNING id`,
		username, email, passwordHash).Scan(&id)
	switch {
	case isUniqueViolation(err, "users_email_key"):
		return errEmailTaken()
	case isUniqueViolation(err, "users_username_key"):
		return errUsernameTaken()
	case err != nil:
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM pending_signups WHERE email = $1`, email); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	resp, err := s.issueTokens(ctx, id, true)
	if err != nil {
		return err
	}
	return created(w, resp)
}

// resendRegistrationCode replaces the code, so only the newest email works.
func (s *Server) resendRegistrationCode(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		VerificationID string `json:"verification_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	tokenHash := auth.HashOpaqueToken(in.VerificationID)
	code, err := auth.NewResetCode()
	if err != nil {
		return err
	}
	ctx := r.Context()
	var email string
	err = s.db.QueryRow(ctx, `UPDATE pending_signups
		SET code_hash = $2, attempts = 0, sends = sends + 1, last_sent_at = now(), expires_at = $3
		WHERE token_hash = $1 AND sends < $4 AND last_sent_at < now() - interval '1 minute' RETURNING email`,
		tokenHash, s.tokens.HashSignupCode(tokenHash, code), time.Now().Add(signupCodeTTL), maxSignupSends).Scan(&email)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pending_signups WHERE token_hash = $1)`, tokenHash).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errTooMany()
		}
		return &Error{http.StatusBadRequest, "invalid_code", "the verification code is invalid or expired"}
	}
	if err != nil {
		return err
	}
	if err := s.sendSignupCode(email, code); err != nil {
		return err
	}
	return noContent(w)
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
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// Failed attempts are counted per account (email and username share the user ID key), so
	// guessing is capped no matter how many IPs it comes from.
	key := "id:" + itoa(id)
	if id == 0 {
		key = "ident:" + ident
	}
	if s.limitLogin.Exhausted(key) {
		return &Error{http.StatusTooManyRequests, "too_many_attempts", "too many failed logins for this account, try again in a few minutes"}
	}
	if hash == nil {
		auth.BurnPasswordCheck(in.Password)
		s.limitLogin.Allow(key)
		return errUnauthorized("wrong email or password")
	}
	if match, err := auth.VerifyPassword(in.Password, *hash); err != nil || !match {
		s.limitLogin.Allow(key)
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
