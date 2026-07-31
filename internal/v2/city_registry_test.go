package v2

import "testing"

// TestCityRegistry_AllOriginalCitiesPresent verifies all 9 original city IDs remain.
func TestCityRegistry_AllOriginalCitiesPresent(t *testing.T) {
	original := []string{
		"buenos_aires", "sao_paulo", "nha_trang", "da_nang",
		"tbilisi", "batumi", "almaty", "yerevan", "moscow",
	}
	for _, id := range original {
		c, ok := CityByID(id)
		if !ok {
			t.Errorf("original city %q not found in registry", id)
			continue
		}
		if c.CountryCode == "" {
			t.Errorf("original city %q has empty country_code", id)
		}
	}
}

// TestCityRegistry_EnabledWaveCitiesPresent verifies the enabled wave 1 cities.
func TestCityRegistry_EnabledWaveCitiesPresent(t *testing.T) {
	newCities := []struct {
		id          string
		countryCode string
	}{
		{"bangkok", "TH"},
		{"chiang_mai", "TH"},
		{"phuket", "TH"},
		{"hanoi", "VN"},
		{"ho_chi_minh_city", "VN"},
		{"antalya", "TR"},
		{"bali", "ID"},
	}
	for _, tc := range newCities {
		c, ok := CityByID(tc.id)
		if !ok {
			t.Errorf("new city %q not found in registry", tc.id)
			continue
		}
		if c.CountryCode != tc.countryCode {
			t.Errorf("city %q: country_code = %q, want %q", tc.id, c.CountryCode, tc.countryCode)
		}
	}
}

func TestCityRegistry_RemovedDestinationsDisabled(t *testing.T) {
	disabled := []string{"istanbul", "dubai", "lisbon", "valencia", "malaga"}
	for _, id := range disabled {
		if _, ok := CityByID(id); ok {
			t.Errorf("removed destination %q must not be available", id)
		}
	}
}

// TestCityRegistry_UnknownCityNotFound verifies unknown and disabled city IDs return false.
func TestCityRegistry_UnknownCityNotFound(t *testing.T) {
	unknowns := []string{"", "atlantis", "new_york", "TBILISI"}
	for _, id := range unknowns {
		if _, ok := CityByID(id); ok {
			t.Errorf("city %q should not be found", id)
		}
	}
}

// TestCityRegistry_TotalCount verifies exactly 16 enabled cities.
func TestCityRegistry_TotalCount(t *testing.T) {
	enabled := EnabledCities()
	if len(enabled) != 16 {
		t.Errorf("EnabledCities: got %d, want 16", len(enabled))
	}
}

// TestCityRegistry_SameCountryAllowed verifies same-country cities all map to the same code.
func TestCityRegistry_SameCountryAllowed(t *testing.T) {
	samePairs := [][2]string{
		{"tbilisi", "batumi"},     // GE
		{"nha_trang", "da_nang"},  // VN
		{"bangkok", "chiang_mai"}, // TH
	}
	for _, pair := range samePairs {
		c1, ok1 := CityByID(pair[0])
		c2, ok2 := CityByID(pair[1])
		if !ok1 || !ok2 {
			t.Errorf("same-country pair %v: one not found", pair)
			continue
		}
		if c1.CountryCode != c2.CountryCode {
			t.Errorf("same-country pair %v: %q vs %q", pair, c1.CountryCode, c2.CountryCode)
		}
	}
}

// TestCityRegistry_CrossCountryDifferent verifies cross-country pairs have different codes.
func TestCityRegistry_CrossCountryDifferent(t *testing.T) {
	crossPairs := [][2]string{
		{"tbilisi", "buenos_aires"}, // GE vs AR
		{"bangkok", "tbilisi"},      // TH vs GE
		{"antalya", "bali"},         // TR vs ID
	}
	for _, pair := range crossPairs {
		c1, ok1 := CityByID(pair[0])
		c2, ok2 := CityByID(pair[1])
		if !ok1 || !ok2 {
			t.Errorf("cross-country pair %v: one not found", pair)
			continue
		}
		if c1.CountryCode == c2.CountryCode {
			t.Errorf("cross-country pair %v: both have %q — should differ", pair, c1.CountryCode)
		}
	}
}

// TestCityRegistry_CityCountryCodeHelper verifies the CityCountryCode helper.
func TestCityRegistry_CityCountryCodeHelper(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"tbilisi", "GE"},
		{"bangkok", "TH"},
		{"unknown_city", ""},
	}
	for _, tc := range tests {
		got := CityCountryCode(tc.id)
		if got != tc.want {
			t.Errorf("CityCountryCode(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}
