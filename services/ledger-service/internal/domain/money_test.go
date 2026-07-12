package domain

import "testing"

// SPEC.md §3.4: wire format is a JSON number with exactly 2 decimal places.
// Clients sending "100" or "100.5" must be rejected, not silently accepted.
func TestNewMoney_ExactlyTwoDecimalPlaces(t *testing.T) {
	valid := []string{"100.00", "0.01", "999999999999999.99"}
	for _, s := range valid {
		if _, err := NewMoney(s); err != nil {
			t.Errorf("NewMoney(%q) = error %v, want nil", s, err)
		}
	}

	invalid := []string{"100", "100.5", "0.001", "100.500"}
	for _, s := range invalid {
		if _, err := NewMoney(s); err == nil {
			t.Errorf("NewMoney(%q) = nil error, want a decimal-places error", s)
		}
	}
}

func TestNewMoney_RejectsNonPositive(t *testing.T) {
	for _, s := range []string{"0.00", "-1.00", "-0.01"} {
		if _, err := NewMoney(s); err == nil {
			t.Errorf("NewMoney(%q) = nil error, want a non-positive error", s)
		}
	}
}

func TestNewMoney_RejectsUnparseable(t *testing.T) {
	for _, s := range []string{"", "abc", "1.2.3", "NaN"} {
		if _, err := NewMoney(s); err == nil {
			t.Errorf("NewMoney(%q) = nil error, want a parse error", s)
		}
	}
}

func TestMoney_StringRoundTrips(t *testing.T) {
	m, err := NewMoney("100.00")
	if err != nil {
		t.Fatalf("NewMoney(\"100.00\") = error %v, want nil", err)
	}
	if got := m.String(); got != "100.00" {
		t.Errorf("String() = %q, want %q", got, "100.00")
	}
}
