package broker

import (
	"strings"
	"testing"
)

func TestNormalizeForScreening_TestCard(t *testing.T) {
	in := `pay: 1234 5678 9012 3456`
	out := normalizeForScreening(in)
	if strings.Contains(out, "1234") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "TEST_CARD_DOC_EXAMPLE") {
		t.Fatal(out)
	}
}

func TestNormalizeForScreening_LongDigits(t *testing.T) {
	in := `note: 283748234623764872364`
	out := normalizeForScreening(in)
	if strings.Contains(out, "283748234623764872364") {
		t.Fatal(out)
	}
}

func TestNormalizeForScreening_CopyrightLine(t *testing.T) {
	in := "Copyright (c) 2024 Some Maintainer Name\n"
	out := normalizeForScreening(in)
	if strings.Contains(out, "Maintainer Name") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "COPYRIGHT_HOLDER") {
		t.Fatal(out)
	}
}
