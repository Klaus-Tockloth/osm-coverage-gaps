package osmtags

import (
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"

	"github.com/paulmach/osm"
)

// CoverageMode describes whether an OSM element qualifies as a covered surface area.
type CoverageMode uint8

const (
	// CoverNone indicates the element does not count as covered area and can be discarded in Pass 1/2.
	CoverNone CoverageMode = iota
	// CoverAlways indicates the element counts as covered area regardless of polygon size.
	CoverAlways
	// CoverMaxHa indicates the element counts as covered area only when its LAEA area is <= MaxAreaHa.
	CoverMaxHa
)

// Decision holds the coverage classification result for an OSM element.
type Decision struct {
	Mode          CoverageMode
	MaxAreaHa     float64
	IgnoredByRule bool
}

// Condition matches a single OSM key=value pair, or any non-empty value when Value is "*".
type Condition struct {
	Key   string
	Value string
}

// Rule specifies a conjunctive tag matcher and an optional area threshold in hectares.
// When MaxAreaHa is 0, matching elements are excluded unconditionally; when > 0, elements larger than MaxAreaHa are excluded.
type Rule struct {
	Conditions []Condition
	MaxAreaHa  float64
}

/*
Matches reports whether all tag conditions of the rule are satisfied by the given OSM tag set.
A condition value of "*" matches any non-empty value for its key; all other values require an exact match.
*/
func (r Rule) Matches(tags osm.Tags) bool {
	if len(r.Conditions) == 0 {
		return false
	}
	for _, cond := range r.Conditions {
		v := tags.Find(cond.Key)
		if v == "" {
			return false
		}
		if cond.Value != "*" && v != cond.Value {
			return false
		}
	}
	return true
}

/*
TargetsKey reports whether any condition in the rule explicitly references the given OSM tag key.
Physical landcover sources are only affected by rules that target their own key, preserving landcover precedence on dual-tagged polygons.
*/
func (r Rule) TargetsKey(key string) bool {
	for _, cond := range r.Conditions {
		if cond.Key == key {
			return true
		}
	}
	return false
}

/*
String formats the rule in canonical CLI syntax (key=value, k1=v1&k2=v2, optionally followed by >max_ha).
*/
func (r Rule) String() string {
	var b strings.Builder
	for i, cond := range r.Conditions {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(cond.Key)
		b.WriteByte('=')
		b.WriteString(cond.Value)
	}
	if r.MaxAreaHa > 0 {
		b.WriteByte('>')
		b.WriteString(strconv.FormatFloat(r.MaxAreaHa, 'g', -1, 64))
	}
	return b.String()
}

/*
ParseRule parses a single area-exclusion rule in the syntax key=value, key=*, k1=v1&k2=v2, or <match>><max_ha>.
The optional numeric suffix after '>' sets the maximum polygon area in hectares that still counts as covered.
It validates non-empty keys and values as well as positive finite area thresholds before returning the parsed Rule.
*/
func ParseRule(spec string) (Rule, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return Rule{}, fmt.Errorf("empty ignore rule")
	}

	matchPart := trimmed
	var maxAreaHa float64
	if left, right, hasLimit := strings.Cut(trimmed, ">"); hasLimit {
		if strings.Contains(right, ">") {
			return Rule{}, fmt.Errorf("invalid ignore rule %q: multiple '>' thresholds", spec)
		}
		limitStr := strings.TrimSpace(right)
		v, err := strconv.ParseFloat(limitStr, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return Rule{}, fmt.Errorf("invalid ignore rule %q: area threshold must be a finite number > 0", spec)
		}
		matchPart = strings.TrimSpace(left)
		maxAreaHa = v
	}

	if matchPart == "" {
		return Rule{}, fmt.Errorf("invalid ignore rule %q: missing tag condition", spec)
	}

	rawConds := strings.Split(matchPart, "&")
	conds := make([]Condition, 0, len(rawConds))
	for _, raw := range rawConds {
		part := strings.TrimSpace(raw)
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			return Rule{}, fmt.Errorf("invalid ignore rule %q: condition %q must use key=value syntax", spec, part)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || strings.ContainsAny(k, " \t\r\n") {
			return Rule{}, fmt.Errorf("invalid ignore rule %q: invalid tag key %q", spec, k)
		}
		if v == "" {
			return Rule{}, fmt.Errorf("invalid ignore rule %q: empty value for key %q", spec, k)
		}
		conds = append(conds, Condition{Key: k, Value: v})
	}

	return Rule{Conditions: conds, MaxAreaHa: maxAreaHa}, nil
}

/*
DefaultIgnoreRules returns the built-in overlay exclusion rules applied unless disabled via CLI configuration.
These rules exclude large functional base polygons such as military training ranges, winter sports areas, and conservation overlays
while keeping small barracks and physically mapped landcover inside them intact.
*/
func DefaultIgnoreRules() []Rule {
	return []Rule{
		{Conditions: []Condition{{Key: "military", Value: "training_area"}}},
		{Conditions: []Condition{{Key: "military", Value: "danger_area"}}},
		{Conditions: []Condition{{Key: "military", Value: "range"}}},
		{Conditions: []Condition{{Key: "landuse", Value: "winter_sports"}}},
		{Conditions: []Condition{{Key: "landuse", Value: "conservation"}}},
	}
}

// Classifier evaluates OSM tag sets against built-in surface coverage definitions and configurable exclusion rules.
// A constructed Classifier is immutable and safe for concurrent calls across PBF worker goroutines.
type Classifier struct {
	rules []Rule
}

var defaultClassifier = NewClassifier(true, nil)

/*
NewClassifier builds an immutable Classifier combining optional built-in overlay defaults with caller-supplied exclusion rules.
All rule slices are copied defensively so subsequent caller mutations cannot affect concurrent classification.
*/
func NewClassifier(includeDefaults bool, extraRules []Rule) *Classifier {
	var rules []Rule
	if includeDefaults {
		rules = append(rules, DefaultIgnoreRules()...)
	}
	for _, r := range extraRules {
		conds := make([]Condition, len(r.Conditions))
		copy(conds, r.Conditions)
		rules = append(rules, Rule{
			Conditions: conds,
			MaxAreaHa:  r.MaxAreaHa,
		})
	}
	return &Classifier{rules: rules}
}

/*
DefaultClassifier returns the shared Classifier configured with the built-in overlay exclusion defaults.
*/
func DefaultClassifier() *Classifier {
	return defaultClassifier
}

/*
Rules returns a defensive copy of all active exclusion rules configured on the Classifier.
*/
func (c *Classifier) Rules() []Rule {
	if c == nil || len(c.rules) == 0 {
		return nil
	}
	out := make([]Rule, len(c.rules))
	for i, r := range c.rules {
		conds := make([]Condition, len(r.Conditions))
		copy(conds, r.Conditions)
		out[i] = Rule{Conditions: conds, MaxAreaHa: r.MaxAreaHa}
	}
	return out
}

const (
	physLanduse uint8 = 1 << iota
	physLandcover
	physNatural
	physWater
	physWaterway
	physPlace
	physHighway
	physRailway
)

var physBitToKey = [8]string{
	"landuse",
	"landcover",
	"natural",
	"water",
	"waterway",
	"place",
	"highway",
	"railway",
}

const (
	ruleUnrestricted uint8 = iota
	ruleAreaLimited
	ruleIgnored
)

/*
applyRule tightens an individual coverage source state using the strictest matching exclusion rule.
Unconditional exclusions supersede area limits, and smaller area limits supersede larger ones.
*/
func applyRule(state *uint8, limit *float64, ruleMaxHa float64) {
	if ruleMaxHa <= 0 {
		*state = ruleIgnored
		*limit = 0
		return
	}
	switch *state {
	case ruleUnrestricted:
		*state = ruleAreaLimited
		*limit = ruleMaxHa
	case ruleAreaLimited:
		if ruleMaxHa < *limit {
			*limit = ruleMaxHa
		}
	}
}

/*
isPhysicalLanduse reports whether a landuse tag value represents physical ground cover or built-up surface
rather than a purely functional or institutional container overlay.
*/
func isPhysicalLanduse(v string) bool {
	switch v {
	case "forest", "farmland", "farmyard", "meadow", "grass", "orchard",
		"vineyard", "allotments", "plant_nursery", "greenhouse_horticulture",
		"flowerbed", "village_green", "residential", "industrial", "commercial",
		"retail", "garages", "railway", "construction", "brownfield",
		"greenfield", "cemetery", "quarry", "landfill", "basin", "reservoir",
		"salt_pond", "aquaculture", "peat_cutting", "harbour", "port",
		"depot", "greenery", "shrubs", "scrub", "heath", "wood",
		"logging", "clearing", "highway", "surface_mining", "courtyard", "plaza":
		return true
	default:
		return false
	}
}

/*
isPhysicalNatural reports whether a natural tag value describes an areal physical surface
while excluding point features, linear features, and large topographic landform regions.
*/
func isPhysicalNatural(v string) bool {
	if v == "" {
		return false
	}
	switch v {
	case "no", "tree", "tree_row", "cliff", "ridge", "peak", "saddle",
		"spring", "cave_entrance", "stone", "rock", "sinkhole",
		"coastline", "valley", "cape",
		"peninsula", "mountain_range", "massif", "plateau",
		"archipelago", "isthmus", "arete", "hill", "gorge", "canyon":
		return false
	default:
		return true
	}
}

/*
Classify evaluates an OSM tag set and returns whether the element counts as covered surface area.
Physical landcover sources take precedence over functional overlay exclusions unless an exclusion rule explicitly targets the landcover key.
Matching exclusion rules suppress all functional coverage tags on the same element so dual-tagged overlays such as
landuse=military combined with military=training_area are excluded cleanly without leaking coverage through secondary functional keys.
*/
func (c *Classifier) Classify(tags osm.Tags) Decision {
	var physMask uint8
	var hasFunctional bool
	var hasAreaYes, hasHighway, hasRailway bool

	for _, tag := range tags {
		v := tag.Value
		if v == "" {
			continue
		}
		switch tag.Key {
		case "landuse":
			if v == "no" {
				continue
			}
			if isPhysicalLanduse(v) {
				physMask |= physLanduse
			} else {
				hasFunctional = true
			}
		case "landcover":
			if v != "no" {
				physMask |= physLandcover
			}
		case "natural":
			if isPhysicalNatural(v) {
				physMask |= physNatural
			}
		case "water":
			if v != "no" {
				physMask |= physWater
			}
		case "waterway":
			switch v {
			case "riverbank", "dock", "boatyard", "dam":
				physMask |= physWaterway
			}
		case "place":
			if v == "square" {
				physMask |= physPlace
			}
		case "area":
			if v == "yes" {
				hasAreaYes = true
			}
		case "highway":
			hasHighway = true
		case "railway":
			hasRailway = true
		case "leisure":
			switch v {
			case "no", "nature_reserve", "track", "slipway", "picnic_table",
				"firepit", "bird_hide", "fishing":
			default:
				hasFunctional = true
			}
		case "amenity":
			switch v {
			case "school", "university", "college", "hospital", "clinic", "parking",
				"grave_yard", "kindergarten", "childcare", "prison", "marketplace",
				"bus_station", "fire_station", "police", "social_facility",
				"community_centre", "place_of_worship", "townhall", "recycling",
				"waste_transfer_station", "fuel", "research_institute",
				"exhibition_centre", "events_venue":
				hasFunctional = true
			}
		case "aeroway":
			switch v {
			case "aerodrome", "apron", "helipad", "heliport", "runway", "taxiway", "terminal", "hangar":
				hasFunctional = true
			}
		case "tourism":
			switch v {
			case "camp_site", "caravan_site", "zoo", "theme_park", "picnic_site", "resort":
				hasFunctional = true
			}
		case "power":
			switch v {
			case "plant", "substation", "generator":
				hasFunctional = true
			}
		case "man_made":
			switch v {
			case "works", "wastewater_plant", "water_works", "storage_tank", "silo",
				"reservoir_covered", "pier", "breakwater", "groyne", "tailings_pond", "quarry":
				hasFunctional = true
			}
		case "military":
			if v != "no" && v != "bunker" {
				hasFunctional = true
			}
		}
	}

	if hasAreaYes {
		if hasHighway {
			physMask |= physHighway
		}
		if hasRailway {
			physMask |= physRailway
		}
	}

	if physMask == 0 && !hasFunctional {
		return Decision{Mode: CoverNone}
	}
	if c == nil || len(c.rules) == 0 {
		return Decision{Mode: CoverAlways}
	}

	var physKeys [8]string
	var physState [8]uint8
	var physLimit [8]float64
	physCount := 0
	for m := physMask; m != 0; m &= m - 1 {
		bit := bits.TrailingZeros8(m)
		physKeys[physCount] = physBitToKey[bit]
		physCount++
	}

	var funcState uint8
	var funcLimit float64

	for i := range c.rules {
		r := &c.rules[i]
		if !r.Matches(tags) {
			continue
		}
		if hasFunctional {
			applyRule(&funcState, &funcLimit, r.MaxAreaHa)
		}
		for pi := 0; pi < physCount; pi++ {
			if r.TargetsKey(physKeys[pi]) {
				applyRule(&physState[pi], &physLimit[pi], r.MaxAreaHa)
			}
		}
	}

	bestMode := CoverNone
	bestLimit := 0.0
	consider := func(state uint8, limit float64) {
		switch state {
		case ruleUnrestricted:
			bestMode = CoverAlways
		case ruleAreaLimited:
			if bestMode == CoverNone || (bestMode == CoverMaxHa && limit > bestLimit) {
				bestMode = CoverMaxHa
				bestLimit = limit
			}
		}
	}

	for pi := 0; pi < physCount; pi++ {
		consider(physState[pi], physLimit[pi])
	}
	if hasFunctional {
		consider(funcState, funcLimit)
	}

	switch bestMode {
	case CoverAlways:
		return Decision{Mode: CoverAlways}
	case CoverMaxHa:
		return Decision{Mode: CoverMaxHa, MaxAreaHa: bestLimit}
	default:
		return Decision{Mode: CoverNone, IgnoredByRule: true}
	}
}

/*
IsCoveredArea reports whether an OSM tag set counts as covered surface area under the default Classifier.
*/
func IsCoveredArea(tags osm.Tags) bool {
	return defaultClassifier.Classify(tags).Mode != CoverNone
}
