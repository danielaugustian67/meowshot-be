package api

import (
	"encoding/json"
	"regexp"
	"strings"
)

const (
	maxDecorationBytes = 2048
	maxStickers        = 8
	// Frame choices are indexes into the app's own lists, with room for it to add more.
	maxFrameChoice = 31
	// Motif -1 is plain paper.
	plainMotif = -1
)

var stickerID = regexp.MustCompile(`^[a-z0-9-]{1,24}$`)

// photoDecoration is what the app draws around and over a photo. A missing paper or motif means the cat's own.
type photoDecoration struct {
	Paper    *int           `json:"paper,omitempty"`
	Motif    *int           `json:"motif,omitempty"`
	Stickers []photoSticker `json:"stickers,omitempty"`
}

// photoSticker sits at (X, Y), its centre as a fraction of the photo. Scale is its size as a fraction of the
// photo's width, and Rotation is in degrees.
type photoSticker struct {
	ID       string  `json:"id"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Scale    float64 `json:"scale"`
	Rotation float64 `json:"rotation"`
}

// parseDecoration reads the optional decoration form field. Anything empty is stored as no decoration at all.
func parseDecoration(raw string) (*photoDecoration, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > maxDecorationBytes {
		return nil, errValidation("decoration is too large")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var d photoDecoration
	if err := dec.Decode(&d); err != nil {
		return nil, errValidation("decoration is not valid")
	}
	if d.Paper != nil && (*d.Paper < 0 || *d.Paper > maxFrameChoice) {
		return nil, errValidation("decoration paper is out of range")
	}
	if d.Motif != nil && (*d.Motif < plainMotif || *d.Motif > maxFrameChoice) {
		return nil, errValidation("decoration motif is out of range")
	}
	if len(d.Stickers) > maxStickers {
		return nil, errValidation("too many stickers")
	}
	for _, st := range d.Stickers {
		if !stickerID.MatchString(st.ID) {
			return nil, errValidation("sticker id is not valid")
		}
		if st.X < 0 || st.X > 1 || st.Y < 0 || st.Y > 1 {
			return nil, errValidation("sticker position is out of range")
		}
		if st.Scale <= 0 || st.Scale > 1 {
			return nil, errValidation("sticker scale is out of range")
		}
		if st.Rotation < -360 || st.Rotation > 360 {
			return nil, errValidation("sticker rotation is out of range")
		}
	}
	if d.Paper == nil && d.Motif == nil && len(d.Stickers) == 0 {
		return nil, nil
	}
	return &d, nil
}
