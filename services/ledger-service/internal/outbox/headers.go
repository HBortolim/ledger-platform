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

// headerCarrier adapts a franz-go record header slice to OTel's
// TextMapCarrier so W3C trace context can be read back out of a row's stored
// headers. Extract-only: Set is intentionally a no-op, since nothing here
// ever writes context back into an already-persisted row.
type headerCarrier []kgo.RecordHeader

func (c headerCarrier) Get(key string) string {
	for _, h := range c {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c headerCarrier) Set(string, string) {}

func (c headerCarrier) Keys() []string {
	keys := make([]string, len(c))
	for i, h := range c {
		keys[i] = h.Key
	}
	return keys
}
