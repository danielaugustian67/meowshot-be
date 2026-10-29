// Package api implements the Meowshot HTTP API consumed by the React Native (Expo) app.
package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"meowshot/internal/auth"
	"meowshot/internal/config"
	"meowshot/internal/mail"
	"meowshot/internal/ratelimit"
	"meowshot/internal/storage"
)

// GoogleVerifier is satisfied by *auth.GoogleVerifier; tests substitute a fake.
type GoogleVerifier interface {
	Enabled() bool
	Verify(ctx context.Context, idToken string) (*auth.GoogleIdentity, error)
}

type Deps struct {
	Config  *config.Config
	DB      *pgxpool.Pool
	Storage storage.Storage
	Google  GoogleVerifier
	Mail    mail.Sender
	Log     *slog.Logger
}

type Server struct {
	cfg    *config.Config
	db     *pgxpool.Pool
	store  storage.Storage
	google GoogleVerifier
	mail   mail.Sender
	log    *slog.Logger
	tokens *auth.Tokens

	imageSlots chan struct{} // bounds concurrent image decoding (memory heavy)

	limitAuth    *ratelimit.Limiter
	limitFriend  *ratelimit.Limiter
	limitExplore *ratelimit.Limiter
	limitComment *ratelimit.Limiter
	limitReport  *ratelimit.Limiter
	limitUpload  *ratelimit.Limiter
}

func New(d Deps) *Server {
	return &Server{
		cfg:          d.Config,
		db:           d.DB,
		store:        d.Storage,
		google:       d.Google,
		mail:         d.Mail,
		log:          d.Log,
		tokens:       auth.NewTokens(d.Config.JWTSecret, d.Config.AccessTokenTTL),
		imageSlots:   make(chan struct{}, 2),
		limitAuth:    ratelimit.New(10, time.Minute),
		limitFriend:  ratelimit.New(30, time.Hour),
		limitExplore: ratelimit.New(60, time.Minute),
		limitComment: ratelimit.New(20, time.Minute),
		limitReport:  ratelimit.New(10, time.Hour),
		limitUpload:  ratelimit.New(60, time.Minute),
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	if s.cfg.TrustProxy {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.RequestID, s.requestLogger, middleware.Recoverer, cors)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.Ping(r.Context()); err != nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})

	r.Route("/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Use(s.rateLimitIP(s.limitAuth))
			r.Post("/register", s.h(s.register))
			r.Post("/login", s.h(s.login))
			r.Post("/google", s.h(s.loginGoogle))
			r.Post("/refresh", s.h(s.refresh))
			r.Post("/logout", s.h(s.logout))
			r.Post("/password/forgot", s.h(s.forgotPassword))
			r.Post("/password/reset", s.h(s.resetPassword))
		})

		// Everything below requires a logged-in, non-suspended user (PUB-4).
		r.Group(func(r chi.Router) {
			r.Use(s.authenticate)

			r.Get("/me", s.h(s.getMe))
			r.Patch("/me", s.h(s.updateMe))
			r.Delete("/me", s.h(s.deleteMe))
			r.Put("/me/password", s.h(s.changePassword))
			r.With(s.rateLimitUser(s.limitUpload)).Put("/me/avatar", s.h(s.putAvatar))
			r.Delete("/me/avatar", s.h(s.deleteAvatar))

			r.Get("/cats", s.h(s.listCats))
			r.Post("/cats", s.h(s.createCat))
			r.Get("/cats/nearby", s.h(s.nearbyCats))
			r.Get("/cats/{catID}", s.h(s.getCat))
			r.Patch("/cats/{catID}", s.h(s.updateCat))
			r.Delete("/cats/{catID}", s.h(s.deleteCat))
			r.Post("/cats/{catID}/merge", s.h(s.mergeCat))
			r.Put("/cats/{catID}/cover", s.h(s.setCover))
			r.Get("/cats/{catID}/photos", s.h(s.listCatPhotos))
			r.With(s.rateLimitUser(s.limitUpload)).Post("/cats/{catID}/photos", s.h(s.uploadCatPhoto))
			r.Get("/cats/{catID}/sightings", s.h(s.listSightings))
			r.Post("/cats/{catID}/sightings", s.h(s.createSighting))
			r.Patch("/sightings/{sightingID}", s.h(s.updateSighting))
			r.Delete("/sightings/{sightingID}", s.h(s.deleteSighting))
			r.Patch("/photos/{photoID}", s.h(s.updatePhoto))
			r.Delete("/photos/{photoID}", s.h(s.deletePhoto))
			r.With(s.rateLimitUser(s.limitUpload)).Post("/captures", s.h(s.capture))
			r.Get("/map/cats", s.h(s.mapCats))

			r.Get("/users/search", s.h(s.searchUsers))
			r.Get("/users/{username}", s.h(s.getProfile))
			r.Get("/users/{username}/posts", s.h(s.listUserPosts))

			r.Get("/friends", s.h(s.listFriends))
			r.Delete("/friends/{userID}", s.h(s.removeFriend))
			r.Get("/friends/requests", s.h(s.listFriendRequests))
			r.With(s.rateLimitUser(s.limitFriend)).Post("/friends/requests", s.h(s.sendFriendRequest))
			r.Post("/friends/requests/{requestID}/accept", s.h(s.acceptFriendRequest))
			r.Delete("/friends/requests/{requestID}", s.h(s.deleteFriendRequest))
			r.Get("/blocks", s.h(s.listBlocks))
			r.Post("/blocks", s.h(s.blockUser))
			r.Delete("/blocks/{userID}", s.h(s.unblockUser))

			r.Get("/feed", s.h(s.feed))
			r.With(s.rateLimitUser(s.limitExplore)).Get("/explore", s.h(s.explore))
			r.Post("/posts", s.h(s.createPost))
			r.Get("/posts/{postID}", s.h(s.getPost))
			r.Patch("/posts/{postID}", s.h(s.updatePost))
			r.Delete("/posts/{postID}", s.h(s.deletePost))
			r.Put("/posts/{postID}/reaction", s.h(s.putReaction))
			r.Delete("/posts/{postID}/reaction", s.h(s.deleteReaction))
			r.Get("/posts/{postID}/comments", s.h(s.listComments))
			r.With(s.rateLimitUser(s.limitComment)).Post("/posts/{postID}/comments", s.h(s.createComment))
			r.Delete("/comments/{commentID}", s.h(s.deleteComment))

			r.Get("/notifications", s.h(s.listNotifications))
			r.Get("/notifications/unread-count", s.h(s.unreadCount))
			r.Post("/notifications/read", s.h(s.markNotificationsRead))

			r.With(s.rateLimitUser(s.limitReport)).Post("/reports", s.h(s.createReport))

			r.Route("/admin", func(r chi.Router) {
				r.Use(requireAdmin)
				r.Get("/reports", s.h(s.adminListReports))
				r.Post("/reports/{reportID}", s.h(s.adminUpdateReport))
				r.Delete("/posts/{postID}", s.h(s.adminDeletePost))
				r.Delete("/comments/{commentID}", s.h(s.adminDeleteComment))
				r.Post("/users/{userID}/suspend", s.h(s.adminSuspend))
				r.Post("/users/{userID}/unsuspend", s.h(s.adminUnsuspend))
			})
		})
	})
	return r
}

type ctxKey int

const userKey ctxKey = 1

type currentUser struct {
	ID   int64
	Role string
}

func userFrom(ctx context.Context) currentUser { return ctx.Value(userKey).(currentUser) }

// authenticate validates the Bearer access token and re-checks the account on every request,
// so suspension and deletion take effect immediately instead of when the token expires.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return s.h(func(w http.ResponseWriter, r *http.Request) error {
		raw, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found {
			return errUnauthorized("missing bearer token")
		}
		id, err := s.tokens.ParseAccess(raw)
		if err != nil {
			return errUnauthorized("invalid or expired token")
		}
		var u currentUser
		var suspended *time.Time
		err = s.db.QueryRow(r.Context(), `SELECT id, role, suspended_at FROM users WHERE id = $1`, id).Scan(&u.ID, &u.Role, &suspended)
		if err != nil {
			return errUnauthorized("account not found")
		}
		if suspended != nil {
			return &Error{http.StatusForbidden, "account_suspended", "this account has been suspended"}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
		return nil
	})
}

func requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if userFrom(r.Context()).Role != "admin" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": errForbidden("admin only")})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimitIP(l *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow("ip:" + clientIP(r)) {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": errTooMany()})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (s *Server) rateLimitUser(l *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow("u:" + itoa(userFrom(r.Context()).ID)) {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": errTooMany()})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", ww.Status(),
			"dur_ms", time.Since(start).Milliseconds(), "req_id", middleware.GetReqID(r.Context()))
	})
}

// cors only matters for Expo web / tooling; native apps do not send Origin. Tokens travel in the
// Authorization header, never cookies, so a permissive policy does not enable CSRF.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
