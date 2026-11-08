package enrichment

import "strings"

// Price extraction.
//
// A price is an optional enrichment result. It is read only from metadata a page
// publishes for exactly that purpose: the Open Graph product price tags. Nothing
// here scans arbitrary numbers out of the page, serialized application state,
// variant lists or campaign fields, because those routinely produce a value that
// belongs to something other than the product the page is about.
//
// The amount is carried as a string and never as a float. Monetary values are
// exact; converting one to a binary float would make a stored price differ from
// the price the page displayed.

// selectPrice reads the optional product price from a parsed document.
//
// The price exists only when a valid amount was found. An amount that is missing,
// malformed, or ambiguous yields no price at all rather than a partially usable
// one. Currency normalization is independent: a valid amount with an unknown or
// absent currency is still a price, just one whose currency is empty.
func selectPrice(doc *document) *Price {
	amount := normalizePriceAmount(doc.candidates[metaProductPriceAmount])
	if amount == "" {
		return nil
	}

	return &Price{
		Amount:   amount,
		Currency: normalizeCurrency(doc.candidates[metaProductPriceCurrency]),
	}
}

// normalizePriceAmount returns value as an exact decimal string, or "" when it is
// not one.
//
// The accepted shape is a plain decimal: one or more digits, optionally followed
// by a single dot and one or more digits. No sign, spaces, grouping separators,
// currency symbols or surrounding text are accepted. A value that is not exactly
// this shape is ignored rather than partially interpreted, so "24.500.000" is not
// silently read as "24.5" and "Rp24.500.000" is not torn apart into a number.
func normalizePriceAmount(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}

	if !isExactDecimal(trimmed) {
		return ""
	}

	return trimmed
}

// isExactDecimal reports whether value matches ^\d+(\.\d+)?$ without using a
// regular expression.
func isExactDecimal(value string) bool {
	if value == "" {
		return false
	}

	dotSeen := false
	digits := 0

	for index := 0; index < len(value); index++ {
		character := value[index]

		switch {
		case character >= '0' && character <= '9':
			digits++

		// The dot may appear once, and never as the leading character, so ".99"
		// is rejected rather than accepted as a decimal.
		case character == '.' && !dotSeen && index > 0:
			dotSeen = true

		default:
			return false
		}
	}

	if digits == 0 {
		return false
	}

	// A trailing dot describes no fractional part, so "1." is rejected.
	if dotSeen && value[len(value)-1] == '.' {
		return false
	}

	return true
}

// currencyMappings maps a source currency label to its canonical ISO 4217 code.
//
// Only explicit mappings are applied, and the lookup is case-insensitive with
// surrounding whitespace ignored. A label that is not here maps to nothing rather
// than being guessed at or passed through as a code. A bare "$" is deliberately
// absent: it is ambiguous across several currencies, so it must not produce USD or
// anything else.
var currencyMappings = map[string]string{
	"rp":         "IDR",
	"idr":        "IDR",
	"usd":        "USD",
	"us$":        "USD",
	"us dollar":  "USD",
	"us dollars": "USD",
}

// normalizeCurrency returns the canonical currency code for a source label, or ""
// when the label has no explicit mapping.
func normalizeCurrency(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}

	return currencyMappings[normalized]
}
