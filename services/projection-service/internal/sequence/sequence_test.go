package sequence

import "testing"

func TestDecide(t *testing.T) {
	cases := []struct {
		name         string
		seq, lastSeq int64
		want         Decision
	}{
		{"first entry of a new wallet", 1, 0, Apply},
		{"the next entry", 5, 4, Apply},
		{"a redelivery of the last applied", 4, 4, Skip},
		{"an old entry covered by a rebuild", 2, 9, Skip},
		{"one ahead of the gap", 6, 4, Park},
		{"far ahead", 100, 4, Park},
		{"seq 0 can never apply", 0, 0, Skip},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.seq, tc.lastSeq); got != tc.want {
				t.Errorf("Decide(%d, %d) = %s, want %s", tc.seq, tc.lastSeq, got, tc.want)
			}
		})
	}
}
