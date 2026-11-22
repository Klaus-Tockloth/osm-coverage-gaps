package geo

import (
	"math"
	"testing"
)

func TestLAEAForwardInverse(t *testing.T) {
	proj := NewLAEA(51.1657, 10.4515)

	// Origin projects to (0, 0) and inverse of zero/near-zero returns origin.
	ox, oy := proj.Forward(51.1657, 10.4515)
	if math.Abs(ox) > 1e-9 || math.Abs(oy) > 1e-9 {
		t.Fatalf("Forward(origin) = (%v, %v), want (0, 0)", ox, oy)
	}
	for _, pt := range [][2]float64{{0, 0}, {1e-12, -1e-12}} {
		lat, lon := proj.Inverse(pt[0], pt[1])
		if math.Abs(lat-51.1657) > 1e-9 || math.Abs(lon-10.4515) > 1e-9 {
			t.Fatalf("Inverse(%v,%v) = (%v,%v), want origin", pt[0], pt[1], lat, lon)
		}
	}

	// Round-trip accuracy across hemispheres and latitudes.
	samples := [][2]float64{
		{53.5511, 9.9937},
		{47.3769, 8.5417},
		{0.0, 0.0},
		{-33.8688, 151.2093},
		{64.1466, -21.9426},
		{-22.9068, -43.1729},
	}
	for _, s := range samples {
		x, y := proj.Forward(s[0], s[1])
		lat, lon := proj.Inverse(x, y)
		if math.Abs(lat-s[0]) > 1e-8 || math.Abs(lon-s[1]) > 1e-8 {
			t.Fatalf("round-trip (%v,%v) -> (%v,%v)", s[0], s[1], lat, lon)
		}
	}

	// Antipode and extreme radius must stay finite without NaN/Inf.
	ax, ay := proj.Forward(-51.1657, 10.4515-180.0)
	if math.IsNaN(ax) || math.IsInf(ax, 0) || math.IsNaN(ay) || math.IsInf(ay, 0) {
		t.Fatalf("Forward(antipode) produced non-finite coordinates: (%v, %v)", ax, ay)
	}
	ilat, ilon := proj.Inverse(3*EarthRadiusM, 0)
	if math.IsNaN(ilat) || math.IsInf(ilat, 0) || math.IsNaN(ilon) || math.IsInf(ilon, 0) {
		t.Fatalf("Inverse(extreme rho) produced non-finite coordinates: (%v, %v)", ilat, ilon)
	}
}

func TestLAEAEqualArea(t *testing.T) {
	proj := NewLAEA(51.0, 10.0)
	const (
		lat1, lat2 = 50.0, 52.0
		lon1, lon2 = 9.0, 11.0
		steps      = 100
		deg2rad    = math.Pi / 180.0
	)

	ring := make([]Pt64, 0, 4*steps+1)
	addEdge := func(la0, lo0, la1, lo1 float64) {
		for i := 0; i < steps; i++ {
			f := float64(i) / float64(steps)
			x, y := proj.Forward(la0+f*(la1-la0), lo0+f*(lo1-lo0))
			ring = append(ring, Pt64{X: x, Y: y})
		}
	}
	addEdge(lat1, lon1, lat1, lon2)
	addEdge(lat1, lon2, lat2, lon2)
	addEdge(lat2, lon2, lat2, lon1)
	addEdge(lat2, lon1, lat1, lon1)
	ring = append(ring, ring[0])

	gotM2 := RingAreaM2(ring)
	wantM2 := EarthRadiusM * EarthRadiusM *
		(math.Sin(lat2*deg2rad) - math.Sin(lat1*deg2rad)) *
		((lon2 - lon1) * deg2rad)

	relErr := math.Abs(gotM2-wantM2) / wantM2
	if relErr > 1e-4 {
		t.Fatalf("LAEA equal-area relative error = %g (got %.2f m², want %.2f m²)", relErr, gotM2, wantM2)
	}
}

func TestRingsBBoxAndRound6(t *testing.T) {
	minX, minY, maxX, maxY := RingsBBox([][]Pt64{
		{{X: -10, Y: 5}, {X: 20, Y: -3}, {X: 0, Y: 15}},
		{{X: 30, Y: 1}, {X: -25, Y: -8}},
	})
	if minX != -25 || minY != -8 || maxX != 30 || maxY != 15 {
		t.Fatalf("RingsBBox() = (%v,%v,%v,%v), want (-25,-8,30,15)", minX, minY, maxX, maxY)
	}

	eMinX, eMinY, eMaxX, eMaxY := RingsBBox(nil)
	if !math.IsInf(eMinX, 1) || !math.IsInf(eMinY, 1) || !math.IsInf(eMaxX, -1) || !math.IsInf(eMaxY, -1) {
		t.Fatalf("RingsBBox(nil) = (%v,%v,%v,%v), want +/-Inf", eMinX, eMinY, eMaxX, eMaxY)
	}

	if got := Round6(1.2345674); got != 1.234567 {
		t.Fatalf("Round6(1.2345674) = %v, want 1.234567", got)
	}
	if got := Round6(1.2345675); got != 1.234568 {
		t.Fatalf("Round6(1.2345675) = %v, want 1.234568", got)
	}
	if got := Round6(-9.8765436); got != -9.876544 {
		t.Fatalf("Round6(-9.8765436) = %v, want -9.876544", got)
	}
}

func TestRingAreaM2(t *testing.T) {
	tests := []struct {
		name string
		ring []Pt64
		want float64
	}{
		{
			name: "closed CCW rectangle",
			ring: []Pt64{{0, 0}, {100, 0}, {100, 200}, {0, 200}, {0, 0}},
			want: 20000,
		},
		{
			name: "closed CW rectangle",
			ring: []Pt64{{0, 0}, {0, 200}, {100, 200}, {100, 0}, {0, 0}},
			want: 20000,
		},
		{
			name: "open rectangle",
			ring: []Pt64{{0, 0}, {100, 0}, {100, 200}, {0, 200}},
			want: 20000,
		},
		{
			name: "far from origin",
			ring: []Pt64{{500000, 600000}, {500100, 600000}, {500100, 600200}, {500000, 600200}, {500000, 600000}},
			want: 20000,
		},
		{
			name: "degenerate fewer than 3 vertices",
			ring: []Pt64{{0, 0}, {100, 0}},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RingAreaM2(tc.ring)
			if math.Abs(got-tc.want) > 1e-6 {
				t.Fatalf("RingAreaM2() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNetAreaHa(t *testing.T) {
	outer := [][]Pt64{{{0, 0}, {1000, 0}, {1000, 1000}, {0, 1000}, {0, 0}}}         // 100 ha
	inner := [][]Pt64{{{100, 100}, {600, 100}, {600, 600}, {100, 600}, {100, 100}}} // 25 ha

	if got := NetAreaHa(outer, nil); math.Abs(got-100.0) > 1e-6 {
		t.Fatalf("NetAreaHa(outer, nil) = %v, want 100", got)
	}
	if got := NetAreaHa(outer, inner); math.Abs(got-75.0) > 1e-6 {
		t.Fatalf("NetAreaHa(outer, inner) = %v, want 75", got)
	}
	// Degenerate case where inner exceeds outer falls back to outer area.
	if got := NetAreaHa(inner, outer); math.Abs(got-25.0) > 1e-6 {
		t.Fatalf("NetAreaHa(inner, outer) = %v, want 25 fallback", got)
	}
	if got := NetAreaHa(nil, inner); got != 0 {
		t.Fatalf("NetAreaHa(nil, inner) = %v, want 0", got)
	}
}
