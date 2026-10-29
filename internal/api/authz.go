package api

import (
	"context"
	"fmt"
	"strconv"

	"meowshot/internal/geo"
)

// This file is the single source of truth for "who can see what" (PRD §7 access matrix).
// List queries embed the SQL predicates below and single-resource checks run the same SQL,
// so handlers never re-implement visibility by hand.
//
//	Viewer            PUBLIC  FRIENDS  PRIVATE  cats/photos not in a post
//	owner             yes     yes      yes      yes
//	accepted friend   yes     yes      no       no
//	other user        yes     no       no       no
//	blocked (either)  no      no       no       no
//
// Content of suspended users is hidden from everyone but themselves.

// postVisibleSQL is a predicate over alias p for the viewer bound at placeholder v (e.g. "$1").
func postVisibleSQL(v string) string {
	v += "::bigint"
	return fmt.Sprintf(`(p.user_id = %[1]s OR (
		p.visibility <> 'PRIVATE'
		AND NOT EXISTS (SELECT 1 FROM users su WHERE su.id = p.user_id AND su.suspended_at IS NOT NULL)
		AND NOT %[2]s
		AND (p.visibility = 'PUBLIC' OR %[3]s)
	))`, v, blockedSQL(v, "p.user_id"), friendsSQL(v, "p.user_id"))
}

// commentVisibleSQL hides comments written by blocked or suspended users (alias cm).
func commentVisibleSQL(v string) string {
	v += "::bigint"
	return fmt.Sprintf(`(cm.user_id = %[1]s OR (
		NOT EXISTS (SELECT 1 FROM users su WHERE su.id = cm.user_id AND su.suspended_at IS NOT NULL)
		AND NOT %[2]s))`, v, blockedSQL(v, "cm.user_id"))
}

// userVisibleSQL: a user profile is visible unless blocked either way or suspended (alias u).
func userVisibleSQL(v string) string {
	v += "::bigint"
	return fmt.Sprintf(`(u.id = %[1]s OR (u.suspended_at IS NULL AND NOT %[2]s))`, v, blockedSQL(v, "u.id"))
}

func blockedSQL(a, b string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM blocks bl WHERE (bl.blocker_id = %[1]s AND bl.blocked_id = %[2]s)
		OR (bl.blocker_id = %[2]s AND bl.blocked_id = %[1]s))`, a, b)
}

func friendsSQL(a, b string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM friendships fr WHERE fr.user_low = LEAST(%[1]s, %[2]s)
		AND fr.user_high = GREATEST(%[1]s, %[2]s) AND fr.status = 'ACCEPTED')`, a, b)
}

// canViewPost reports whether viewer may see the post. Callers answer 404 (not 403) when it
// returns false so that hidden posts are indistinguishable from missing ones.
func (s *Server) canViewPost(ctx context.Context, viewer, postID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM posts p WHERE p.id = $2 AND `+postVisibleSQL("$1")+`)`,
		viewer, postID).Scan(&ok)
	return ok, err
}

func (s *Server) canViewUser(ctx context.Context, viewer, userID int64) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users u WHERE u.id = $2 AND `+userVisibleSQL("$1")+`)`,
		viewer, userID).Scan(&ok)
	return ok, err
}

func (s *Server) isBlockedEitherWay(ctx context.Context, a, b int64) (bool, error) {
	var blocked bool
	err := s.db.QueryRow(ctx, `SELECT `+blockedSQL("$1::bigint", "$2::bigint"), a, b).Scan(&blocked)
	return blocked, err
}

// location is what a viewer is allowed to know about where a post's cat was seen.
type location struct {
	Lat    *float64 `json:"lat"`
	Lng    *float64 `json:"lng"`
	Label  *string  `json:"label"`
	SeenAt string   `json:"seen_at,omitempty"`
}

// blurLocation applies the post's precision server-side (PRD §7 "Aturan Lokasi"). Owners get the
// true point; everyone else gets EXACT, a deterministic APPROX offset, or label only for HIDDEN.
func (s *Server) blurLocation(isOwner bool, precision string, sightingID int64, lat, lng *float64, label *string, seenAt string) *location {
	loc := &location{Label: label, SeenAt: seenAt}
	if lat == nil || lng == nil {
		return loc
	}
	switch {
	case isOwner || precision == "EXACT":
		loc.Lat, loc.Lng = lat, lng
	case precision == "APPROX":
		aLat, aLng := geo.Approximate(s.cfg.LocationSecret, sightingID, *lat, *lng)
		loc.Lat, loc.Lng = &aLat, &aLng
	}
	return loc
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
