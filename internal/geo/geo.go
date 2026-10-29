// Package geo implements server-side location blurring and distance helpers.
package geo

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strconv"
)

const (
	earthRadiusM   = 6371000.0
	approxMaxM     = 200.0
	approxMinM     = 60.0
	metersPerDegLa = 111320.0
)

// Approximate moves (lat, lng) by a deterministic offset of 60–200 m. The offset is derived from
// HMAC(secret, sightingID), so refreshing or re-posting the same sighting always yields the same
// point and repeated requests cannot be averaged back to the true coordinates.
func Approximate(secret []byte, sightingID int64, lat, lng float64) (float64, float64) {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("approx:" + strconv.FormatInt(sightingID, 10)))
	sum := m.Sum(nil)

	u1 := float64(binary.BigEndian.Uint64(sum[0:8])) / math.MaxUint64
	u2 := float64(binary.BigEndian.Uint64(sum[8:16])) / math.MaxUint64

	angle := 2 * math.Pi * u1
	// sqrt keeps points uniform over the annulus instead of clustering near the centre.
	dist := math.Sqrt(approxMinM*approxMinM + u2*(approxMaxM*approxMaxM-approxMinM*approxMinM))

	dLat := dist * math.Cos(angle) / metersPerDegLa
	cos := math.Cos(lat * math.Pi / 180)
	if cos < 0.01 {
		cos = 0.01
	}
	dLng := dist * math.Sin(angle) / (metersPerDegLa * cos)

	// Round to ~10 m so the payload does not advertise false precision.
	return round(lat+dLat, 4), round(lng+dLng, 4)
}

// DistanceM returns the great-circle distance in metres.
func DistanceM(lat1, lng1, lat2, lng2 float64) float64 {
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLng := (lng2 - lng1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Min(1, math.Sqrt(a)))
}

// BoundingBox returns a lat/lng box that contains every point within radiusM of the centre.
func BoundingBox(lat, lng, radiusM float64) (minLat, maxLat, minLng, maxLng float64) {
	dLat := radiusM / metersPerDegLa
	cos := math.Max(math.Cos(lat*math.Pi/180), 0.01)
	dLng := radiusM / (metersPerDegLa * cos)
	return lat - dLat, lat + dLat, lng - dLng, lng + dLng
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
