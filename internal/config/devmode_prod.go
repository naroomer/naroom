//go:build !dev

package config

// DevModeAllowed is false in production builds (no -tags dev).
const DevModeAllowed = false
