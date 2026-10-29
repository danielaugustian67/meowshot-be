package auth

import (
	"context"
	"errors"
	"slices"

	"github.com/coreos/go-oidc/v3/oidc"
)

const googleJWKS = "https://www.googleapis.com/oauth2/v3/certs"

// GoogleIdentity is the subset of Google ID token claims Meowshot uses.
type GoogleIdentity struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

// GoogleVerifier checks ID tokens produced by Google Sign-In on the mobile app.
// Expo / React Native sign-in yields tokens whose "aud" is one of the configured client IDs
// (web client ID when using @react-native-google-signin, iOS/Android IDs otherwise).
type GoogleVerifier struct {
	verifier  *oidc.IDTokenVerifier
	clientIDs []string
}

func NewGoogleVerifier(ctx context.Context, clientIDs []string) *GoogleVerifier {
	keys := oidc.NewRemoteKeySet(ctx, googleJWKS)
	v := oidc.NewVerifier("https://accounts.google.com", keys, &oidc.Config{
		SkipClientIDCheck: true, // checked against the whole list below
		SkipIssuerCheck:   true, // Google uses two issuer spellings, checked below
	})
	return &GoogleVerifier{verifier: v, clientIDs: clientIDs}
}

func (g *GoogleVerifier) Enabled() bool { return len(g.clientIDs) > 0 }

func (g *GoogleVerifier) Verify(ctx context.Context, rawIDToken string) (*GoogleIdentity, error) {
	if !g.Enabled() {
		return nil, errors.New("google sign-in is not configured")
	}
	tok, err := g.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, ErrInvalidToken
	}
	if tok.Issuer != "https://accounts.google.com" && tok.Issuer != "accounts.google.com" {
		return nil, ErrInvalidToken
	}
	if !slices.ContainsFunc(tok.Audience, func(a string) bool { return slices.Contains(g.clientIDs, a) }) {
		return nil, ErrInvalidToken
	}
	var id GoogleIdentity
	if err := tok.Claims(&id); err != nil {
		return nil, ErrInvalidToken
	}
	if id.Subject == "" || id.Email == "" {
		return nil, ErrInvalidToken
	}
	return &id, nil
}
