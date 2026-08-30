package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

// newTestLogger mirrors Setup's handler stack but writes to buf instead of
// stdout, so the emitted JSON can be asserted on directly.
func newTestLogger(buf *bytes.Buffer, service string) *slog.Logger {
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Key = "timestamp"
			}
			return a
		},
	})
	return slog.New(traceHandler{base}).With(slog.String("service", service))
}

func TestLogRecordHasRequiredNFROBS1Fields(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, "projection-service")

	logger.InfoContext(context.Background(), "posting committed")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v (raw: %s)", err, buf.String())
	}

	// NFR-OBS-1: logs include trace_id, span_id, service, level, msg.
	for _, key := range []string{"timestamp", "level", "service", "trace_id", "span_id", "msg"} {
		if _, ok := got[key]; !ok {
			t.Errorf("log record missing required field %q; got keys %v", key, keysOf(got))
		}
	}
	if got["service"] != "projection-service" {
		t.Errorf("service = %v, want projection-service", got["service"])
	}
	if got["msg"] != "posting committed" {
		t.Errorf("msg = %v, want %q", got["msg"], "posting committed")
	}
}

func TestUntracedContextYieldsEmptyTraceFields(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, "projection-service")

	logger.InfoContext(context.Background(), "no active span")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if got["trace_id"] != "" {
		t.Errorf("trace_id = %v, want empty string for an untraced context", got["trace_id"])
	}
	if got["span_id"] != "" {
		t.Errorf("span_id = %v, want empty string for an untraced context", got["span_id"])
	}
}

func TestDerivedLoggerKeepsTraceFields(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(&buf, "projection-service").With(slog.String("component", "outbox"))

	logger.InfoContext(context.Background(), "tick")

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if _, ok := got["trace_id"]; !ok {
		t.Error("derived logger dropped trace_id; WithAttrs must re-wrap the handler")
	}
	if got["component"] != "outbox" {
		t.Errorf("component = %v, want outbox", got["component"])
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
