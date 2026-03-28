package broker

import "regexp"

// normalizeForScreening removes patterns that systematically confuse the screening LLM
// (false positives). This runs on shaped text before policy evaluation.
func normalizeForScreening(s string) string {
	if s == "" {
		return s
	}
	// Classic doc/test "credit card" example — not a real PAN.
	s = reTestCard1234.ReplaceAllString(s, "<TEST_CARD_DOC_EXAMPLE>")
	// Other common test PAN patterns (Stripe/docs).
	s = reTestCard4000.ReplaceAllString(s, "<TEST_CARD_DOC_EXAMPLE>")
	// Very long digit runs — often IDs, not emails; reduces "email pattern" hallucinations.
	s = reLongDigitRun.ReplaceAllString(s, "<NUMERIC_ID>")
	// LICENSE/Copyright: drop real name after year (reduces false PII on maintainers).
	s = reCopyrightHolder.ReplaceAllString(s, "$1<COPYRIGHT_HOLDER>")
	return s
}

var (
	reTestCard1234 = regexp.MustCompile(`(?i)1234[\s-]*5678[\s-]*9012[\s-]*3456`)
	reTestCard4000 = regexp.MustCompile(`(?i)4000[\s-]*0000[\s-]*0000[\s-]*000[26]`)
	reLongDigitRun = regexp.MustCompile(`\b\d{18,}\b`)
	// "Copyright (c) 2024 Some Name" or "Copyright 2024 Name"
	reCopyrightHolder = regexp.MustCompile(`(?i)(Copyright\s*(?:\(c\)\s*)?\d{4}\s+)([^\n]{2,120})`)
)
