package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidToken = errors.New("auth: invalid token")

type Tokens struct {
	secret    []byte
	accessTTL time.Duration
}

func NewTokens(secret []byte, accessTTL time.Duration) *Tokens {
	return &Tokens{secret: secret, accessTTL: accessTTL}
}

func (t *Tokens) AccessTTL() time.Duration { return t.accessTTL }

// IssueAccess returns a short-lived HS256 JWT whose subject is the user ID.
func (t *Tokens) IssueAccess(userID int64) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(userID, 10),
		Issuer:    "meowshot",
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(t.accessTTL)),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
}

// ParseAccess validates an access token and returns its user ID.
func (t *Tokens) ParseAccess(raw string) (int64, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer("meowshot"), jwt.WithExpirationRequired())
	if err != nil {
		return 0, ErrInvalidToken
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil {
		return 0, ErrInvalidToken
	}
	return id, nil
}

// NewOpaqueToken returns a random URL-safe token and the SHA-256 digest to store in the database.
func NewOpaqueToken() (token string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashOpaqueToken(token), nil
}

func HashOpaqueToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// NewResetCode returns a 6-digit numeric code for password reset emails.
func NewResetCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// HashResetCode keys the digest with the server secret so a leaked table cannot be brute-forced offline.
func (t *Tokens) HashResetCode(userID int64, code string) []byte {
	m := hmac.New(sha256.New, t.secret)
	fmt.Fprintf(m, "reset:%d:%s", userID, code)
	return m.Sum(nil)
}
