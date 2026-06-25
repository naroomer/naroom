package middleware

import (
	"context"
	"net/http"
	"strings"
)

type langKey struct{}

var supportedLangs = map[string]bool{
	"en": true, "ru": true, "ka": true, "es": true, "de": true, "vi": true,
}

// Language extracts language from URL prefix or Accept-Language header.
func Language(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lang := "en" // default

		// Check URL prefix: /ru/board/tbilisi → lang=ru
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(parts) >= 1 && supportedLangs[parts[0]] {
			lang = parts[0]
			// Strip lang prefix from path for downstream handlers
			remaining := "/"
			if len(parts) > 1 {
				remaining = "/" + parts[1]
			}
			r.URL.Path = remaining
		} else {
			// Fallback: Accept-Language header
			if al := r.Header.Get("Accept-Language"); al != "" {
				for _, tag := range strings.Split(al, ",") {
					code := strings.TrimSpace(strings.SplitN(tag, ";", 2)[0])
					short := strings.SplitN(code, "-", 2)[0]
					if supportedLangs[short] {
						lang = short
						break
					}
				}
			}
		}

		ctx := context.WithValue(r.Context(), langKey{}, lang)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// LangFrom extracts language from context.
func LangFrom(ctx context.Context) string {
	if v, ok := ctx.Value(langKey{}).(string); ok {
		return v
	}
	return "en"
}
