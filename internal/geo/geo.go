package geo

import "math"

// EarthRadiusM is the WGS84 authalic earth radius in meters.
const EarthRadiusM = 6371007.2

// Pt32 stores planar projected coordinates in compact 32-bit floating-point precision.
type Pt32 struct{ X, Y float32 }

// Pt64 stores planar projected coordinates in 64-bit floating-point precision.
type Pt64 struct{ X, Y float64 }

// Cell identifies a discrete pixel coordinate inside a raster grid.
type Cell struct{ X, Y int32 }

// LAEA implements a spherical Lambert Azimuthal Equal-Area projection.
type LAEA struct {
	lat0, lon0       float64
	sinLat0, cosLat0 float64
}

/*
NewLAEA initializes a spherical Lambert Azimuthal Equal-Area projection centered at the given latitude and longitude in degrees.
It converts the origin coordinates to radians and precomputes the sine and cosine of the reference latitude.
These cached trigonometric values accelerate subsequent forward and inverse coordinate transformations across large OSM datasets.
*/
func NewLAEA(lat0Deg, lon0Deg float64) LAEA {
	lat0 := lat0Deg * math.Pi / 180.0
	lon0 := lon0Deg * math.Pi / 180.0
	sin0, cos0 := math.Sincos(lat0)
	return LAEA{lat0: lat0, lon0: lon0, sinLat0: sin0, cosLat0: cos0}
}

/*
Forward projects geographic WGS84 coordinates in degrees into planar equal-area coordinates in meters.
It computes the spherical scale factor relative to the projection origin and clamps the denominator to avoid division by zero near the antipode.
Because the Lambert Azimuthal projection preserves surface area, pixel counts in the resulting meter grid map directly to real-world hectares.
The returned X and Y offsets increase toward east and north respectively.
*/
func (p LAEA) Forward(latDeg, lonDeg float64) (x, y float64) {
	lat := latDeg * math.Pi / 180.0
	lon := lonDeg * math.Pi / 180.0
	dLon := lon - p.lon0
	sinLat, cosLat := math.Sincos(lat)
	cosDLon := math.Cos(dLon)
	denom := 1.0 + p.sinLat0*sinLat + p.cosLat0*cosLat*cosDLon
	if denom < 1e-12 {
		denom = 1e-12
	}
	k := math.Sqrt(2.0 / denom)
	x = EarthRadiusM * k * cosLat * math.Sin(dLon)
	y = EarthRadiusM * k * (p.cosLat0*sinLat - p.sinLat0*cosLat*cosDLon)
	return x, y
}

/*
Inverse converts planar LAEA meter coordinates back into geographic latitude and longitude in degrees.
When the radial distance from the origin is effectively zero, it returns the projection center directly to prevent numerical instability.
Intermediate inverse-sine arguments are clamped to the valid unit interval before transforming back to spherical angles.
This method is used to recover WGS84 polygon rings, centroids, and bounding boxes for GeoJSON and CSV export.
*/
func (p LAEA) Inverse(x, y float64) (latDeg, lonDeg float64) {
	rho := math.Hypot(x, y)
	if rho < 1e-9 {
		return p.lat0 * 180.0 / math.Pi, p.lon0 * 180.0 / math.Pi
	}
	arg := rho / (2.0 * EarthRadiusM)
	if arg > 1.0 {
		arg = 1.0
	}
	c := 2.0 * math.Asin(arg)
	sinC, cosC := math.Sincos(c)

	sinLat := cosC*p.sinLat0 + (y*sinC*p.cosLat0)/rho
	if sinLat > 1.0 {
		sinLat = 1.0
	} else if sinLat < -1.0 {
		sinLat = -1.0
	}
	lat := math.Asin(sinLat)
	lon := p.lon0 + math.Atan2(x*sinC, rho*p.cosLat0*cosC-y*p.sinLat0*sinC)
	return lat * 180.0 / math.Pi, lon * 180.0 / math.Pi
}

/*
RingsBBox computes the axis-aligned planar bounding box enclosing all vertices across a slice of polygon rings.
It initializes the extrema to positive and negative infinities and scans every point in linear time.
Callers use the resulting extent to size the target raster grid and to estimate candidate boundary areas.
*/
func RingsBBox(rings [][]Pt64) (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, r := range rings {
		for _, p := range r {
			if p.X < minX {
				minX = p.X
			}
			if p.X > maxX {
				maxX = p.X
			}
			if p.Y < minY {
				minY = p.Y
			}
			if p.Y > maxY {
				maxY = p.Y
			}
		}
	}
	return
}

/*
RingAreaM2 computes the unsigned planar area of a polygon ring in square meters using the shoelace formula.
Vertex coordinates are shifted relative to the first vertex to preserve floating-point precision far from the projection origin.
Both explicitly closed and open vertex slices are handled identically; rings with fewer than three vertices return zero.
*/
func RingAreaM2(ring []Pt64) float64 {
	n := len(ring)
	if n < 3 {
		return 0
	}
	x0, y0 := ring[0].X, ring[0].Y
	var sum float64
	j := n - 1
	for i := 0; i < n; i++ {
		sum += (ring[j].X-x0)*(ring[i].Y-y0) - (ring[i].X-x0)*(ring[j].Y-y0)
		j = i
	}
	return math.Abs(sum) * 0.5
}

/*
NetAreaHa computes the net area in hectares of outer polygon rings minus interior hole rings in planar meter coordinates.
Because LAEA coordinates preserve real-world surface area, dividing square meters by 10,000 yields exact hectares.
If degenerate or mislabeled inner rings exceed the outer ring sum, the positive outer ring area is returned as a conservative fallback.
*/
func NetAreaHa(outerRings, innerRings [][]Pt64) float64 {
	var outerM2, innerM2 float64
	for _, r := range outerRings {
		outerM2 += RingAreaM2(r)
	}
	for _, r := range innerRings {
		innerM2 += RingAreaM2(r)
	}
	netM2 := outerM2 - innerM2
	if netM2 <= 0 {
		if outerM2 > 0 {
			return outerM2 / 10000.0
		}
		return 0
	}
	return netM2 / 10000.0
}

/*
Round6 rounds a floating-point coordinate value to six decimal places.
For WGS84 degrees, this corresponds to roughly eleven centimeters of precision at the equator while keeping serialized output compact.
It is applied consistently to GeoJSON vertices, gap centroids, and bounding-box coordinates.
*/
func Round6(v float64) float64 {
	return math.Round(v*1e6) / 1e6
}
