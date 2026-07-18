package outbox

import (
	"encoding/json"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// decodeHeaders unmarshals the outbox.headers JSONB column (a flat
// map[string]string, e.g. {"traceparent": "..."}) into franz-go record
// headers.
func decodeHeaders(raw []byte) ([]kgo.RecordHeader, error) {
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("unmarshal outbox headers: %w", err)
	}
	hdrs := make([]kgo.RecordHeader, 0, len(m))
	for k, v := range m {
		hdrs = append(hdrs, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	return hdrs, nil
}
