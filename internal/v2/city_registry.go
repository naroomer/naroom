package v2

// City represents a single enabled destination in the platform registry.
type City struct {
	ID          string // stable slug, e.g. "tbilisi"
	Label       string // public display name
	CountryCode string // ISO 3166-1 alpha-2
	SortOrder   int
	Enabled     bool
}

var countryLabels = map[string]string{
	"AE": "UAE",
	"AM": "Armenia",
	"AR": "Argentina",
	"BR": "Brazil",
	"ES": "Spain",
	"GE": "Georgia",
	"ID": "Indonesia",
	"KZ": "Kazakhstan",
	"PT": "Portugal",
	"RU": "Russia",
	"TH": "Thailand",
	"TR": "Turkey",
	"VN": "Vietnam",
}

// AllCities is the authoritative registry for all V2 destinations.
// Order is the stable public sort order. IDs and country codes never change.
var AllCities = []City{
	// Original 9
	{ID: "buenos_aires", Label: "Buenos Aires", CountryCode: "AR", SortOrder: 10, Enabled: true},
	{ID: "sao_paulo", Label: "São Paulo", CountryCode: "BR", SortOrder: 20, Enabled: true},
	{ID: "nha_trang", Label: "Nha Trang", CountryCode: "VN", SortOrder: 30, Enabled: true},
	{ID: "da_nang", Label: "Da Nang", CountryCode: "VN", SortOrder: 40, Enabled: true},
	{ID: "tbilisi", Label: "Tbilisi", CountryCode: "GE", SortOrder: 50, Enabled: true},
	{ID: "batumi", Label: "Batumi", CountryCode: "GE", SortOrder: 60, Enabled: true},
	{ID: "almaty", Label: "Almaty", CountryCode: "KZ", SortOrder: 70, Enabled: true},
	{ID: "yerevan", Label: "Yerevan", CountryCode: "AM", SortOrder: 80, Enabled: true},
	{ID: "moscow", Label: "Moscow", CountryCode: "RU", SortOrder: 90, Enabled: true},
	// New wave 1 (12 cities)
	{ID: "bangkok", Label: "Bangkok", CountryCode: "TH", SortOrder: 100, Enabled: true},
	{ID: "chiang_mai", Label: "Chiang Mai", CountryCode: "TH", SortOrder: 110, Enabled: true},
	{ID: "phuket", Label: "Phuket", CountryCode: "TH", SortOrder: 120, Enabled: true},
	{ID: "hanoi", Label: "Hanoi", CountryCode: "VN", SortOrder: 130, Enabled: true},
	{ID: "ho_chi_minh_city", Label: "Ho Chi Minh City", CountryCode: "VN", SortOrder: 140, Enabled: true},
	{ID: "istanbul", Label: "Istanbul", CountryCode: "TR", SortOrder: 150, Enabled: false},
	{ID: "antalya", Label: "Antalya", CountryCode: "TR", SortOrder: 160, Enabled: true},
	{ID: "dubai", Label: "Dubai", CountryCode: "AE", SortOrder: 170, Enabled: false},
	{ID: "bali", Label: "Bali", CountryCode: "ID", SortOrder: 180, Enabled: true},
	{ID: "lisbon", Label: "Lisbon", CountryCode: "PT", SortOrder: 190, Enabled: false},
	{ID: "valencia", Label: "Valencia", CountryCode: "ES", SortOrder: 200, Enabled: false},
	{ID: "malaga", Label: "Malaga", CountryCode: "ES", SortOrder: 210, Enabled: false},
}

// CityByID returns the City for the given ID, or (City{}, false) if not found/disabled.
func CityByID(id string) (City, bool) {
	for _, c := range AllCities {
		if c.ID == id && c.Enabled {
			return c, true
		}
	}
	return City{}, false
}

// EnabledCities returns all enabled cities in sort order.
func EnabledCities() []City {
	var result []City
	for _, c := range AllCities {
		if c.Enabled {
			result = append(result, c)
		}
	}
	return result
}

// CityCountryCode returns the country code for the given city ID, or "" if not found.
func CityCountryCode(cityID string) string {
	c, ok := CityByID(cityID)
	if !ok {
		return ""
	}
	return c.CountryCode
}

// CountryLabel returns the public English label for an enabled registry city.
// The frontend receives this value from the city endpoint and does not maintain
// a second V2 country map.
func CountryLabel(countryCode string) string {
	if label, ok := countryLabels[countryCode]; ok {
		return label
	}
	return countryCode
}
