// Package main (gensitemap) builds frontend/static/sitemap.xml deterministically
// from the backend's authoritative city registry (internal/v2.AllCities), so the
// sitemap can never drift out of sync with which cities actually exist.
//
// SitemapCityIDs is intentionally a thin pass-through over EnabledCities() —
// there must be exactly one source of truth for "which cities are live," not
// a second, sitemap-only list that can quietly drift from it. A city appears
// in the sitemap if and only if internal/v2/city_registry.go marks it
// Enabled: true.
package main

import (
	"encoding/xml"
	"fmt"

	v2 "naroom/internal/v2"
)

// SitemapCityIDs returns every currently-enabled registry city ID, in the
// registry's stable sort order (internal/v2.AllCities), so output is
// deterministic.
func SitemapCityIDs() []string {
	var ids []string
	for _, c := range v2.EnabledCities() {
		ids = append(ids, c.ID)
	}
	return ids
}

// BuildSitemapURLs returns the full set of canonical, indexable HTTPS URLs:
// the how-it-works guide plus one board URL per sitemap city. No redirect
// URLs, no private/operational routes, no real listing URLs, no query
// strings — one canonical URL per public page, matching what <SeoHead>
// renders as canonical on those same pages.
func BuildSitemapURLs(baseURL string) []string {
	urls := []string{baseURL + "/v2/how-it-works"}
	for _, id := range SitemapCityIDs() {
		urls = append(urls, baseURL+"/v2/board/"+id)
	}
	return urls
}

type sitemapURLSet struct {
	XMLName xml.Name     `xml:"urlset"`
	Xmlns   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc string `xml:"loc"`
}

// RenderSitemapXML renders a minimal, valid sitemap.xml. No changefreq or
// priority (decorative, not load-bearing for ranking) and no lastmod: these
// URLs (a static guide page, and city boards whose listing contents change
// continuously) have no single correct "last modified" timestamp this
// generator can compute honestly at build time, so it is omitted rather than
// fabricated.
func RenderSitemapXML(urls []string) (string, error) {
	set := sitemapURLSet{Xmlns: "http://www.sitemaps.org/schemas/sitemap/0.9"}
	for _, u := range urls {
		set.URLs = append(set.URLs, sitemapURL{Loc: u})
	}
	body, err := xml.MarshalIndent(set, "", "  ")
	if err != nil {
		return "", fmt.Errorf("gensitemap: marshal: %w", err)
	}
	return xml.Header + string(body) + "\n", nil
}
