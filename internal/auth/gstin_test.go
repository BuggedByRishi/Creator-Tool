package auth

import "testing"

func TestValidGSTIN(t *testing.T) {
	cases := map[string]bool{
		"27AAPFU0939F1ZV": true,  // valid check digit
		"27AAPFU0939F1ZX": false, // wrong check digit
		"27AAPFU0939F1Z":  false, // too short
		"27aapfu0939f1zv": false, // must be upper-case (callers upper-case first)
		"":                false,
	}
	for in, want := range cases {
		if got := ValidGSTIN(in); got != want {
			t.Errorf("ValidGSTIN(%q) = %v, want %v", in, got, want)
		}
	}
}
