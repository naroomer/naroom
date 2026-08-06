package main

import (
	"encoding/xml"
	"strings"
	"testing"

	v2 "naroom/internal/v2"
)

func TestSitemapCityIDs_IncludesNhaTrangAndDaNang(t *testing.T) {
	// Nha Trang and Da Nang are Enabled: true, real board cities — the
	// sitemap must not carry a second, hand-maintained exclusion list that
	// could drift from internal/v2.EnabledCities().
	ids := SitemapCityIDs()
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	for _, id := range []string{"nha_trang", "da_nang"} {
		if !idSet[id] {
			t.Errorf("SitemapCityIDs() must contain enabled city %q", id)
		}
	}
}

func TestSitemapCityIDs_ExcludesDisabledRegistryCities(t *testing.T) {
	ids := SitemapCityIDs()
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	for _, c := range v2.AllCities {
		if !c.Enabled && idSet[c.ID] {
			t.Errorf("SitemapCityIDs() contains disabled city %q", c.ID)
		}
	}
}

func TestSitemapCityIDs_IncludesAllEnabledCities(t *testing.T) {
	ids := SitemapCityIDs()
	idSet := map[string]bool{}
	for _, id := range ids {
		idSet[id] = true
	}
	for _, c := range v2.AllCities {
		if c.Enabled && !idSet[c.ID] {
			t.Errorf("SitemapCityIDs() is missing enabled city %q", c.ID)
		}
	}
	if len(ids) != len(v2.EnabledCities()) {
		t.Errorf("SitemapCityIDs() returned %d cities, want exactly len(EnabledCities()) = %d", len(ids), len(v2.EnabledCities()))
	}
}

func TestBuildSitemapURLs_OnlyHTTPSCanonicalV2URLs(t *testing.T) {
	urls := BuildSitemapURLs("https://naroom.net")
	if len(urls) < 2 {
		t.Fatalf("expected at least how-it-works + 1 city URL, got %d", len(urls))
	}
	for _, u := range urls {
		if !strings.HasPrefix(u, "https://naroom.net/v2/") {
			t.Errorf("URL %q is not a canonical https .../v2/ URL", u)
		}
		if strings.Contains(u, "?") {
			t.Errorf("URL %q must not contain a query string", u)
		}
	}
	if urls[0] != "https://naroom.net/v2/how-it-works" {
		t.Errorf("first URL = %q, want how-it-works", urls[0])
	}
}

func TestBuildSitemapURLs_NoPrivateOrRedirectRoutes(t *testing.T) {
	urls := BuildSitemapURLs("https://naroom.net")
	// Every URL must start with exactly one of these two allowed prefixes —
	// no private V2 flow, no legacy V1 redirect path, no real listing URL.
	allowedPrefixes := []string{
		"https://naroom.net/v2/how-it-works",
		"https://naroom.net/v2/board/",
	}
	for _, u := range urls {
		ok := false
		for _, p := range allowedPrefixes {
			if strings.HasPrefix(u, p) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("URL %q does not match an allowed public canonical prefix", u)
		}
	}
}

func TestBuildSitemapURLs_ExactCount(t *testing.T) {
	urls := BuildSitemapURLs("https://naroom.net")
	want := 1 + len(v2.EnabledCities()) // how-it-works + one per enabled city
	if len(urls) != want {
		t.Errorf("BuildSitemapURLs() returned %d URLs, want %d (how-it-works + %d enabled cities)", len(urls), want, len(v2.EnabledCities()))
	}
}

func TestRenderSitemapXML_ValidAndMatchesURLCount(t *testing.T) {
	urls := BuildSitemapURLs("https://naroom.net")
	doc, err := RenderSitemapXML(urls)
	if err != nil {
		t.Fatalf("RenderSitemapXML: %v", err)
	}
	if !strings.HasPrefix(doc, xml.Header) {
		t.Errorf("sitemap XML missing standard XML header")
	}

	var parsed sitemapURLSet
	if err := xml.Unmarshal([]byte(doc), &parsed); err != nil {
		t.Fatalf("generated sitemap.xml does not parse as valid XML: %v", err)
	}
	if parsed.Xmlns != "http://www.sitemaps.org/schemas/sitemap/0.9" {
		t.Errorf("xmlns = %q, want the standard sitemap namespace", parsed.Xmlns)
	}
	if len(parsed.URLs) != len(urls) {
		t.Errorf("parsed %d <url> entries, want %d", len(parsed.URLs), len(urls))
	}
	for i, u := range parsed.URLs {
		if u.Loc != urls[i] {
			t.Errorf("entry %d loc = %q, want %q", i, u.Loc, urls[i])
		}
	}
	if strings.Contains(doc, "<changefreq>") || strings.Contains(doc, "<priority>") {
		t.Errorf("sitemap must not contain decorative changefreq/priority")
	}
	if strings.Contains(doc, "<lastmod>") {
		t.Errorf("sitemap must not fabricate a lastmod value")
	}
}
