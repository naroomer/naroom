package v2

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxNormalizedContactBytes is the domain-level ceiling after ContactValidator
// normalizes the raw contact. Raw and normalized values are never included in errors.
const maxNormalizedContactBytes = 256

var cityCountry = map[string]string{
	"buenos_aires": "AR", "sao_paulo": "BR", "nha_trang": "VN",
	"da_nang": "VN", "tbilisi": "GE", "batumi": "GE",
	"almaty": "KZ", "yerevan": "AM", "moscow": "RU",
}

var validDependencyTypes = map[string]bool{
	"alcohol": true, "opioids": true, "stimulants": true, "cannabis": true,
	"cocaine": true, "mephedrone": true, "benzodiazepines": true,
	"polysubstance": true, "gambling": true,
}

var validHelpTypes = map[string]bool{
	"crisis": true, "relapse_prevention": true, "motivation": true,
	"just_talk": true, "recovery_plan": true,
}

var validUrgencies = map[string]bool{
	"urgent": true, "soon": true, "can_wait": true,
}

var validLanguages = map[string]bool{
	"en": true, "ru": true, "ka": true, "es": true,
}

// ContactValidator is injectable; production impl enforces Telegram/Signal syntax.
type ContactValidator interface {
	ValidateContact(contactType, rawContact string) (normalized string, err error)
}

// ListingInput carries the raw, unvalidated listing fields from the caller.
// RawContact is never logged or included in errors.
type ListingInput struct {
	City, DependencyType, HelpType, Urgency string
	Languages                               []string
	ContactType                             string
	RawContact                              string // never logged or included in errors
}

type validatedListing struct {
	city, countryCode, dependencyType, helpType, urgency string
	languagesJSON                                        string
	contactType                                          string
	normalizedContact                                    string
}

func validateListingInput(input ListingInput, cv ContactValidator) (validatedListing, error) {
	city := strings.TrimSpace(strings.ToLower(input.City))
	country, ok := cityCountry[city]
	if !ok {
		return validatedListing{}, fmt.Errorf("%w: unknown city", ErrInvalidListingInput)
	}
	dep := strings.TrimSpace(strings.ToLower(input.DependencyType))
	if !validDependencyTypes[dep] {
		return validatedListing{}, fmt.Errorf("%w: unknown dependency_type", ErrInvalidListingInput)
	}
	help := strings.TrimSpace(strings.ToLower(input.HelpType))
	if !validHelpTypes[help] {
		return validatedListing{}, fmt.Errorf("%w: unknown help_type", ErrInvalidListingInput)
	}
	urg := strings.TrimSpace(strings.ToLower(input.Urgency))
	if !validUrgencies[urg] {
		return validatedListing{}, fmt.Errorf("%w: unknown urgency", ErrInvalidListingInput)
	}
	if len(input.Languages) < 1 || len(input.Languages) > 4 {
		return validatedListing{}, fmt.Errorf("%w: languages must have 1-4 entries", ErrInvalidListingInput)
	}
	seen := make(map[string]bool)
	var langs []string
	for _, l := range input.Languages {
		l = strings.TrimSpace(strings.ToLower(l))
		if !validLanguages[l] {
			return validatedListing{}, fmt.Errorf("%w: unknown language", ErrInvalidListingInput)
		}
		if seen[l] {
			return validatedListing{}, fmt.Errorf("%w: duplicate language", ErrInvalidListingInput)
		}
		seen[l] = true
		langs = append(langs, l)
	}
	sort.Strings(langs)
	langJSON, _ := json.Marshal(langs)

	ct := strings.TrimSpace(strings.ToLower(input.ContactType))
	if ct != "telegram" && ct != "signal" {
		return validatedListing{}, fmt.Errorf("%w: contact_type must be telegram or signal", ErrInvalidListingInput)
	}
	normalized, err := cv.ValidateContact(ct, input.RawContact)
	if err != nil {
		return validatedListing{}, fmt.Errorf("%w: contact validation failed", ErrInvalidListingInput)
	}

	// Domain boundary: independently verify the normalized result regardless of
	// what the validator returned. Raw and normalized contact values are never
	// included in error messages.
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return validatedListing{}, fmt.Errorf("%w: normalized contact is empty", ErrInvalidListingInput)
	}
	if !utf8.ValidString(normalized) {
		return validatedListing{}, fmt.Errorf("%w: normalized contact is not valid UTF-8", ErrInvalidListingInput)
	}
	if len(normalized) > maxNormalizedContactBytes {
		return validatedListing{}, fmt.Errorf("%w: normalized contact exceeds allowed length", ErrInvalidListingInput)
	}

	return validatedListing{
		city: city, countryCode: country, dependencyType: dep, helpType: help, urgency: urg,
		languagesJSON: string(langJSON), contactType: ct, normalizedContact: normalized,
	}, nil
}
