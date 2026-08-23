package secrets

import "testing"

func TestDisplayValue(t *testing.T) {
	if got := DisplayValue("super-secret", true); got != RedactedValue {
		t.Fatalf("DisplayValue(present) = %q, want %q", got, RedactedValue)
	}
	if got := DisplayValue("", false); got != UnsetValue {
		t.Fatalf("DisplayValue(absent) = %q, want %q", got, UnsetValue)
	}
}
