package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Error is returned by handlers and rendered as {"error": {"code", "message"}}.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func errBadRequest(format string, a ...any) *Error {
	return &Error{http.StatusBadRequest, "bad_request", fmt.Sprintf(format, a...)}
}
func errValidation(format string, a ...any) *Error {
	return &Error{http.StatusUnprocessableEntity, "validation_failed", fmt.Sprintf(format, a...)}
}
func errUnauthorized(msg string) *Error { return &Error{http.StatusUnauthorized, "unauthorized", msg} }
func errForbidden(msg string) *Error    { return &Error{http.StatusForbidden, "forbidden", msg} }
func errNotFound(what string) *Error {
	return &Error{http.StatusNotFound, "not_found", what + " not found"}
}
func errConflict(code, msg string) *Error { return &Error{http.StatusConflict, code, msg} }
func errTooMany() *Error {
	return &Error{http.StatusTooManyRequests, "rate_limited", "too many requests, slow down"}
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

// h adapts an error-returning handler. Unknown errors become 500s and are logged, never echoed.
func (s *Server) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		var apiErr *Error
		if errors.As(err, &apiErr) {
			writeJSON(w, apiErr.Status, map[string]any{"error": apiErr})
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": errNotFound("resource")})
			return
		}
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": &Error{Code: "internal", Message: "internal server error"}})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, v any) error {
	writeJSON(w, http.StatusOK, v)
	return nil
}

func created(w http.ResponseWriter, v any) error {
	writeJSON(w, http.StatusCreated, v)
	return nil
}

func noContent(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errBadRequest("request body is required")
		}
		return errBadRequest("invalid JSON body: %v", err)
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id <= 0 {
		return 0, errNotFound(name)
	}
	return id, nil
}

// page reads ?cursor (an opaque id; items strictly older are returned) and ?limit.
type page struct {
	Cursor int64
	Limit  int
}

func readPage(r *http.Request, def, maxLimit int) page {
	p := page{Limit: def}
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		p.Limit = min(v, maxLimit)
	}
	if v, err := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64); err == nil && v > 0 {
		p.Cursor = v
	}
	return p
}

// cursorArg is the SQL upper bound for "id < $n" when there is no cursor.
func (p page) cursorArg() int64 {
	if p.Cursor == 0 {
		return 1<<63 - 1
	}
	return p.Cursor
}

type list[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// paginate trims a result fetched with LIMIT n+1 and sets the next cursor from idOf.
func paginate[T any](items []T, limit int, idOf func(T) int64) list[T] {
	out := list[T]{Items: items}
	if out.Items == nil {
		out.Items = []T{}
	}
	if len(items) > limit {
		out.Items = items[:limit]
		c := strconv.FormatInt(idOf(out.Items[limit-1]), 10)
		out.NextCursor = &c
	}
	return out
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && (constraint == "" || pgErr.ConstraintName == constraint)
}
