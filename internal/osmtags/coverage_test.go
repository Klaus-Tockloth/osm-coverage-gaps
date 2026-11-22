package osmtags

import (
	"testing"

	"github.com/paulmach/osm"
)

func makeTags(pairs ...string) osm.Tags {
	tags := make(osm.Tags, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		tags = append(tags, osm.Tag{Key: pairs[i], Value: pairs[i+1]})
	}
	return tags
}

func TestParseRule(t *testing.T) {
	valid := []struct {
		spec      string
		canonical string
		maxHa     float64
	}{
		{"landuse=military", "landuse=military", 0},
		{"military=*", "military=*", 0},
		{"power=plant&plant:source=wind", "power=plant&plant:source=wind", 0},
		{" landuse = military > 50 ", "landuse=military>50", 50},
		{"aeroway=aerodrome>12.5", "aeroway=aerodrome>12.5", 12.5},
		{"power=plant&plant:source=wind>20", "power=plant&plant:source=wind>20", 20},
	}

	for _, tc := range valid {
		r, err := ParseRule(tc.spec)
		if err != nil {
			t.Fatalf("ParseRule(%q) unexpected error: %v", tc.spec, err)
		}
		if r.String() != tc.canonical {
			t.Fatalf("ParseRule(%q).String() = %q, want %q", tc.spec, r.String(), tc.canonical)
		}
		if r.MaxAreaHa != tc.maxHa {
			t.Fatalf("ParseRule(%q).MaxAreaHa = %v, want %v", tc.spec, r.MaxAreaHa, tc.maxHa)
		}
	}

	invalid := []string{
		"",
		"   ",
		"landuse",
		"=military",
		"landuse=",
		"land use=military",
		"landuse=military>",
		"landuse=military>0",
		"landuse=military>-10",
		"landuse=military>NaN",
		"landuse=military>Inf",
		"landuse=military>50>10",
		">50",
		"landuse=military&",
	}

	for _, spec := range invalid {
		if _, err := ParseRule(spec); err == nil {
			t.Fatalf("ParseRule(%q) succeeded, expected error", spec)
		}
	}
}

func TestClassifierDefaultsAndLandcoverPrecedence(t *testing.T) {
	def := DefaultClassifier()
	raw := NewClassifier(false, nil)

	tests := []struct {
		name            string
		c               *Classifier
		tags            osm.Tags
		wantMode        CoverageMode
		wantIgnoredFlag bool
	}{
		{
			name:     "small barracks remains covered",
			c:        def,
			tags:     makeTags("landuse", "military", "military", "barracks"),
			wantMode: CoverAlways,
		},
		{
			name:     "untyped landuse=military remains covered without area rule",
			c:        def,
			tags:     makeTags("landuse", "military"),
			wantMode: CoverAlways,
		},
		{
			name:            "training area dual-tagged with landuse=military is ignored by default",
			c:               def,
			tags:            makeTags("landuse", "military", "military", "training_area"),
			wantMode:        CoverNone,
			wantIgnoredFlag: true,
		},
		{
			name:            "danger_area is ignored by default",
			c:               def,
			tags:            makeTags("landuse", "military", "military", "danger_area"),
			wantMode:        CoverNone,
			wantIgnoredFlag: true,
		},
		{
			name:            "range is ignored by default",
			c:               def,
			tags:            makeTags("military", "range"),
			wantMode:        CoverNone,
			wantIgnoredFlag: true,
		},
		{
			name:     "landcover takes precedence over functional training_area overlay",
			c:        def,
			tags:     makeTags("landuse", "military", "military", "training_area", "natural", "heath"),
			wantMode: CoverAlways,
		},
		{
			name:            "winter_sports ignored by default",
			c:               def,
			tags:            makeTags("landuse", "winter_sports"),
			wantMode:        CoverNone,
			wantIgnoredFlag: true,
		},
		{
			name:            "conservation ignored by default",
			c:               def,
			tags:            makeTags("landuse", "conservation"),
			wantMode:        CoverNone,
			wantIgnoredFlag: true,
		},
		{
			name:     "defaults disabled keeps training_area and winter_sports covered",
			c:        raw,
			tags:     makeTags("landuse", "military", "military", "training_area"),
			wantMode: CoverAlways,
		},
		{
			name:     "topographic natural=peninsula excluded regardless of defaults",
			c:        raw,
			tags:     makeTags("natural", "peninsula"),
			wantMode: CoverNone,
		},
		{
			name:     "topographic natural=mountain_range excluded regardless of defaults",
			c:        def,
			tags:     makeTags("natural", "mountain_range"),
			wantMode: CoverNone,
		},
		{
			name:     "nature_reserve excluded without rule flag",
			c:        def,
			tags:     makeTags("leisure", "nature_reserve"),
			wantMode: CoverNone,
		},
		{
			name:     "highway with area=yes covered",
			c:        def,
			tags:     makeTags("highway", "pedestrian", "area", "yes"),
			wantMode: CoverAlways,
		},
		{
			name:     "building alone not covered",
			c:        def,
			tags:     makeTags("building", "yes"),
			wantMode: CoverNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.c.Classify(tc.tags)
			if got.Mode != tc.wantMode || got.IgnoredByRule != tc.wantIgnoredFlag {
				t.Fatalf("Classify() = %+v, want Mode=%v IgnoredByRule=%v", got, tc.wantMode, tc.wantIgnoredFlag)
			}
		})
	}
}

func TestClassifierCustomAndAreaRules(t *testing.T) {
	mustRule := func(spec string) Rule {
		t.Helper()
		r, err := ParseRule(spec)
		if err != nil {
			t.Fatalf("ParseRule(%q): %v", spec, err)
		}
		return r
	}

	c := NewClassifier(true, []Rule{
		mustRule("landuse=military>50"),
		mustRule("power=plant&plant:source=wind"),
		mustRule("landuse=farmland>200"),
	})

	// Untyped landuse=military receives 50 ha area limit.
	if got := c.Classify(makeTags("landuse", "military")); got.Mode != CoverMaxHa || got.MaxAreaHa != 50 {
		t.Fatalf("untyped landuse=military = %+v, want CoverMaxHa(50)", got)
	}

	// Barracks tagged with landuse=military also receives 50 ha area limit (functional coupling).
	if got := c.Classify(makeTags("landuse", "military", "military", "barracks")); got.Mode != CoverMaxHa || got.MaxAreaHa != 50 {
		t.Fatalf("barracks with landuse=military = %+v, want CoverMaxHa(50)", got)
	}

	// Default unconditional training_area rule is stricter than 50 ha limit -> CoverNone.
	if got := c.Classify(makeTags("landuse", "military", "military", "training_area")); got.Mode != CoverNone || !got.IgnoredByRule {
		t.Fatalf("training_area with area rule = %+v, want CoverNone(IgnoredByRule=true)", got)
	}

	// Conjunctive rule excludes wind farm but keeps solar farm covered.
	if got := c.Classify(makeTags("power", "plant", "plant:source", "wind")); got.Mode != CoverNone || !got.IgnoredByRule {
		t.Fatalf("wind plant = %+v, want CoverNone(IgnoredByRule=true)", got)
	}
	if got := c.Classify(makeTags("power", "plant", "plant:source", "solar")); got.Mode != CoverAlways {
		t.Fatalf("solar plant = %+v, want CoverAlways", got)
	}

	// Explicit rule targeting physical landuse=farmland applies 200 ha limit.
	if got := c.Classify(makeTags("landuse", "farmland")); got.Mode != CoverMaxHa || got.MaxAreaHa != 200 {
		t.Fatalf("farmland = %+v, want CoverMaxHa(200)", got)
	}

	// Explicit unconditional exclusion on physical landcover works as expected.
	cPhys := NewClassifier(false, []Rule{mustRule("natural=scrub")})
	if got := cPhys.Classify(makeTags("natural", "scrub")); got.Mode != CoverNone || !got.IgnoredByRule {
		t.Fatalf("explicit natural=scrub = %+v, want CoverNone(IgnoredByRule=true)", got)
	}

	// Multi-source union: natural=heath>100 and landuse=military>50 yields max(100, 50) = 100.
	cUnion := NewClassifier(false, []Rule{mustRule("natural=heath>100"), mustRule("landuse=military>50")})
	if got := cUnion.Classify(makeTags("natural", "heath", "landuse", "military")); got.Mode != CoverMaxHa || got.MaxAreaHa != 100 {
		t.Fatalf("multi-source union = %+v, want CoverMaxHa(100)", got)
	}
}

func TestClassifierTagMatrixAndEdgeCases(t *testing.T) {
	// Empty rule never matches; TargetsKey matches exact keys.
	if (Rule{}).Matches(makeTags("landuse", "forest")) {
		t.Fatalf("empty Rule matched non-empty tags")
	}
	r := Rule{Conditions: []Condition{{Key: "natural", Value: "*"}}}
	if !r.TargetsKey("natural") || r.TargetsKey("landuse") {
		t.Fatalf("unexpected TargetsKey result")
	}

	// Nil receiver handles Classify and Rules safely.
	var nilC *Classifier
	if got := nilC.Classify(makeTags("landuse", "forest")); got.Mode != CoverAlways {
		t.Fatalf("nil Classifier.Classify(forest) = %+v, want CoverAlways", got)
	}
	if got := nilC.Rules(); got != nil {
		t.Fatalf("nil Classifier.Rules() = %v, want nil", got)
	}

	// Defensive copy immutability on NewClassifier and Rules().
	inRules := []Rule{{Conditions: []Condition{{Key: "landuse", Value: "military"}}, MaxAreaHa: 40}}
	c := NewClassifier(false, inRules)
	inRules[0].Conditions[0].Value = "forest"
	inRules[0].MaxAreaHa = 999
	exported := c.Rules()
	exported[0].Conditions[0].Value = "meadow"
	if got := c.Classify(makeTags("landuse", "military")); got.Mode != CoverMaxHa || got.MaxAreaHa != 40 {
		t.Fatalf("external mutation altered Classifier: %+v", got)
	}

	// Multiple area limits on the SAME source apply the strictest (minimum) limit.
	cMin := NewClassifier(false, []Rule{
		{Conditions: []Condition{{Key: "landuse", Value: "military"}}, MaxAreaHa: 100},
		{Conditions: []Condition{{Key: "landuse", Value: "military"}}, MaxAreaHa: 25},
	})
	if got := cMin.Classify(makeTags("landuse", "military")); got.Mode != CoverMaxHa || got.MaxAreaHa != 25 {
		t.Fatalf("same-source min limit = %+v, want CoverMaxHa(25)", got)
	}

	// Dual physical landcover tags retain CoverAlways if only one physical key is excluded.
	cWood := NewClassifier(false, []Rule{{Conditions: []Condition{{Key: "natural", Value: "wood"}}}})
	if got := cWood.Classify(makeTags("natural", "wood", "landuse", "forest")); got.Mode != CoverAlways {
		t.Fatalf("dual physical tags with single-key exclusion = %+v, want CoverAlways", got)
	}

	// IsCoveredArea wrapper and remaining tag-matrix samples.
	if !IsCoveredArea(makeTags("landuse", "forest")) || IsCoveredArea(makeTags("military", "training_area")) {
		t.Fatalf("unexpected IsCoveredArea result")
	}

	def := DefaultClassifier()
	matrix := []struct {
		name string
		tags osm.Tags
		want CoverageMode
	}{
		{"landuse=no", makeTags("landuse", "no"), CoverNone},
		{"landcover=no", makeTags("landcover", "no"), CoverNone},
		{"water=no", makeTags("water", "no"), CoverNone},
		{"natural=no", makeTags("natural", "no"), CoverNone},
		{"leisure=no", makeTags("leisure", "no"), CoverNone},
		{"military=no", makeTags("military", "no"), CoverNone},
		{"military=bunker", makeTags("military", "bunker"), CoverNone},
		{"highway without area=yes", makeTags("highway", "pedestrian"), CoverNone},
		{"railway without area=yes", makeTags("railway", "platform"), CoverNone},
		{"railway with area=yes", makeTags("railway", "platform", "area", "yes"), CoverAlways},
		{"waterway=riverbank", makeTags("waterway", "riverbank"), CoverAlways},
		{"waterway=river", makeTags("waterway", "river"), CoverNone},
		{"place=square", makeTags("place", "square"), CoverAlways},
		{"place=city", makeTags("place", "city"), CoverNone},
		{"leisure=park", makeTags("leisure", "park"), CoverAlways},
		{"leisure=track", makeTags("leisure", "track"), CoverNone},
		{"amenity=school", makeTags("amenity", "school"), CoverAlways},
		{"amenity=bench", makeTags("amenity", "bench"), CoverNone},
		{"aeroway=aerodrome", makeTags("aeroway", "aerodrome"), CoverAlways},
		{"aeroway=gate", makeTags("aeroway", "gate"), CoverNone},
		{"tourism=camp_site", makeTags("tourism", "camp_site"), CoverAlways},
		{"tourism=hotel", makeTags("tourism", "hotel"), CoverNone},
		{"man_made=works", makeTags("man_made", "works"), CoverAlways},
		{"man_made=tower", makeTags("man_made", "tower"), CoverNone},
	}

	for _, tc := range matrix {
		t.Run(tc.name, func(t *testing.T) {
			if got := def.Classify(tc.tags).Mode; got != tc.want {
				t.Fatalf("Classify(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
