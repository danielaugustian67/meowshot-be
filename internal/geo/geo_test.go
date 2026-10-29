package geo

import "testing"

func TestApproximateIsDeterministicAndWithinRadius(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	lat, lng := -6.2088, 106.8456
	for id := int64(1); id <= 500; id++ {
		aLat, aLng := Approximate(secret, id, lat, lng)
		bLat, bLng := Approximate(secret, id, lat, lng)
		if aLat != bLat || aLng != bLng {
			t.Fatalf("sighting %d: not deterministic", id)
		}
		d := DistanceM(lat, lng, aLat, aLng)
		// rounding to 4 decimals can add up to ~8 m in either direction
		if d < approxMinM-10 || d > approxMaxM+10 {
			t.Fatalf("sighting %d: offset %.1f m outside [%v,%v]", id, d, approxMinM, approxMaxM)
		}
	}
}

func TestApproximateDependsOnSecret(t *testing.T) {
	a1, a2 := Approximate([]byte("secret-a-secret-a-secret-a-secret"), 7, 1, 1)
	b1, b2 := Approximate([]byte("secret-b-secret-b-secret-b-secret"), 7, 1, 1)
	if a1 == b1 && a2 == b2 {
		t.Fatal("different secrets produced the same point")
	}
}
