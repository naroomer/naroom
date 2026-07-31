package v2

// TestFrontend_NoDuplicateV2CityRegistry statically proves that no V2 frontend
// route imports the full static CITIES array from frontend/src/lib/cities.js.
// internal/v2/city_registry.go is the single authoritative
// registry; V2 pages must fetch /api/v2/board/cities instead of duplicating it.
//
// frontend/src/lib/cities.js is intentionally allowed to keep its full CITIES
// array: V1 routes (frontend/src/routes/new, /helper, /board) still import it and
// V1 behavior must not be touched. This test only asserts that V2 (frontend/src/
// routes/v2/**) imports only FALLBACK_CITY_ID from that module. Country labels
// and the full city list must both arrive through /api/v2/board/cities.
import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var citiesImportRe = regexp.MustCompile(`import\s*\{[^}]*\bCITIES\b[^}]*\}\s*from\s*['"]\$lib/cities(\.js)?['"]`)
var citiesModuleImportRe = regexp.MustCompile(`import\s*\{([^}]*)\}\s*from\s*['"]\$lib/cities(\.js)?['"]`)

func TestFrontend_NoDuplicateV2CityRegistry(t *testing.T) {
	v2RoutesDir := filepath.Join("..", "..", "frontend", "src", "routes", "v2")
	info, err := os.Stat(v2RoutesDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("v2 routes dir not found at %s: %v", v2RoutesDir, err)
	}

	var offenders []string
	walkErr := filepath.Walk(v2RoutesDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() || !strings.HasSuffix(path, ".svelte") {
			return nil
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if citiesImportRe.Match(b) {
			offenders = append(offenders, path)
		}
		for _, match := range citiesModuleImportRe.FindAllSubmatch(b, -1) {
			for _, imported := range strings.Split(string(match[1]), ",") {
				name := strings.TrimSpace(strings.SplitN(imported, " as ", 2)[0])
				if name != "" && name != "FALLBACK_CITY_ID" {
					offenders = append(offenders, path+" imports "+name)
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", v2RoutesDir, walkErr)
	}
	if len(offenders) > 0 {
		t.Errorf("V2 route(s) import the full frontend CITIES registry (must use /api/v2/board/cities instead): %v", offenders)
	}

	// Every V2 surface that presents or links city-specific journeys must read
	// the enabled registry from the backend endpoint.
	cityConsumers := []string{
		filepath.Join(v2RoutesDir, "board", "[city]", "+page.svelte"),
		filepath.Join(v2RoutesDir, "new", "+page.svelte"),
		filepath.Join(v2RoutesDir, "informer", "+page.svelte"),
		filepath.Join(v2RoutesDir, "helper", "purchases", "+page.svelte"),
		filepath.Join(v2RoutesDir, "how-it-works", "+page.svelte"),
	}
	for _, path := range cityConsumers {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read city consumer %s: %v", path, err)
		}
		if !strings.Contains(string(b), "/api/v2/board/cities") {
			t.Errorf("%s must load enabled cities from /api/v2/board/cities", path)
		}
	}

	// Sanity-check the pattern actually matches something (fails loudly if the
	// regex or path assumptions ever drift, instead of silently passing forever).
	fixture := "import { CITIES } from '$lib/cities.js';"
	if !citiesImportRe.MatchString(fixture) {
		t.Fatal("citiesImportRe sanity check failed to match a known-bad import — test is not effective")
	}

	// Cross-check the approved public destination set so registry drift does
	// not go unnoticed alongside this consolidation.
	enabled := EnabledCities()
	if len(enabled) != 16 {
		t.Errorf("internal/v2/city_registry.go AllCities: want 16 enabled cities, got %d", len(enabled))
	}
}
