package outbox

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

func TestDecodeHeaders(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []kgo.RecordHeader
		wantErr bool
	}{
		{
			name: "empty object",
			raw:  `{}`,
			want: []kgo.RecordHeader{},
		},
		{
			name: "traceparent",
			raw:  `{"traceparent":"00-abc-def-01"}`,
			want: []kgo.RecordHeader{{Key: "traceparent", Value: []byte("00-abc-def-01")}},
		},
		{
			name:    "malformed json",
			raw:     `not json`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeHeaders([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d headers, want %d", len(got), len(tc.want))
			}
			for _, w := range tc.want {
				found := false
				for _, g := range got {
					if g.Key == w.Key && string(g.Value) == string(w.Value) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("missing header %+v in %+v", w, got)
				}
			}
		})
	}
}
