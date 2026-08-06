// gensitemap regenerates frontend/static/sitemap.xml from the authoritative
// backend city registry (internal/v2.AllCities). Run from the repo root:
//
//	go run ./cmd/gensitemap
//
// Re-run this whenever internal/v2/city_registry.go changes (a city is
// enabled/disabled) so the sitemap never drifts out of sync again.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	baseURL := flag.String("base-url", "https://naroom.net", "canonical site origin, no trailing slash")
	out := flag.String("out", "frontend/static/sitemap.xml", "output path, relative to the current working directory (repo root)")
	flag.Parse()

	urls := BuildSitemapURLs(*baseURL)
	xmlDoc, err := RenderSitemapXML(urls)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gensitemap:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(xmlDoc), 0644); err != nil {
		fmt.Fprintln(os.Stderr, "gensitemap: write:", err)
		os.Exit(1)
	}
	fmt.Printf("gensitemap: wrote %d URLs to %s\n", len(urls), *out)
}
