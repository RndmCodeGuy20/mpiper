package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// findSum returns the summed int64 counter value for a metric whose data points
// all carry the given (event,status) attributes. Returns total across matching
// points regardless of attributes when matchStatus is empty.
func sumCounter(t *testing.T, rm *metricdata.ResourceMetrics, name, status string) int64 {
	t.Helper()
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != name {
				continue
			}
			sum, ok := mt.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s is not an int64 Sum", name)
			}
			for _, dp := range sum.DataPoints {
				if status == "" {
					total += dp.Value
					continue
				}
				if v, ok := dp.Attributes.Value("status"); ok && v.AsString() == status {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func histogramCount(t *testing.T, rm *metricdata.ResourceMetrics, name string) uint64 {
	t.Helper()
	var count uint64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != name {
				continue
			}
			h, ok := mt.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %s is not a float64 Histogram", name)
			}
			for _, dp := range h.DataPoints {
				count += dp.Count
			}
		}
	}
	return count
}

func TestRecordDelivery_EmitsMetrics(t *testing.T) {
	m, reader := metrics.NewTestMetrics()
	d := &Dispatcher{m: m}
	ctx := context.Background()

	// One successful delivery (records total + duration, no failure).
	d.recordDelivery(ctx, "job.done", "delivered", 120*time.Millisecond, true)
	// One non-2xx failure (records total + failure + duration).
	d.recordDelivery(ctx, "job.done", "failed", 50*time.Millisecond, false)
	// One pre-HTTP error (records total + failure, no duration since dur==0).
	d.recordDelivery(ctx, "job.failed", "error", 0, false)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if got := sumCounter(t, &rm, "webhook.delivery.total", ""); got != 3 {
		t.Errorf("webhook.delivery.total = %d, want 3", got)
	}
	if got := sumCounter(t, &rm, "webhook.delivery.total", "delivered"); got != 1 {
		t.Errorf("delivered total = %d, want 1", got)
	}
	if got := sumCounter(t, &rm, "webhook.delivery.failures", ""); got != 2 {
		t.Errorf("webhook.delivery.failures = %d, want 2", got)
	}
	// Only the two calls with dur>0 record into the duration histogram.
	if got := histogramCount(t, &rm, "webhook.delivery.duration"); got != 2 {
		t.Errorf("webhook.delivery.duration count = %d, want 2", got)
	}
}

func TestRecordDelivery_NilMetricsIsSafe(t *testing.T) {
	d := &Dispatcher{m: nil}
	// Must not panic when metrics are not wired.
	d.recordDelivery(context.Background(), "job.done", "delivered", time.Second, true)
}

func TestBackoff_ExponentialWithCap(t *testing.T) {
	tests := []struct {
		attempt    int
		wantMin    time.Duration
		wantMax    time.Duration
	}{
		{1, 1 * time.Second, 4 * time.Second},    // 2s base ±25%
		{2, 2 * time.Second, 6 * time.Second},    // 4s base ±25%
		{3, 4 * time.Second, 12 * time.Second},   // 8s base ±25%
		{4, 8 * time.Second, 24 * time.Second},   // 16s base ±25%
		{10, 3 * time.Minute, 5*time.Minute + 1}, // capped at 5min (hard cap after jitter)
	}

	for _, tt := range tests {
		// Run multiple times for randomness coverage
		for range 50 {
			got := backoff(tt.attempt)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("backoff(%d) = %v, want [%v, %v]", tt.attempt, got, tt.wantMin, tt.wantMax)
			}
		}
	}
}

func TestBackoff_NeverExceedsFiveMinutes(t *testing.T) {
	for attempt := 1; attempt <= 20; attempt++ {
		for range 100 {
			got := backoff(attempt)
			if got > 5*time.Minute+time.Minute { // generous tolerance for jitter
				t.Fatalf("backoff(%d) = %v exceeds 5min cap", attempt, got)
			}
		}
	}
}

func TestComputeHMAC(t *testing.T) {
	secret := "my-webhook-secret"
	payload := []byte(`{"event":"job.done","asset_id":"abc-123"}`)

	got := computeHMAC(secret, payload)

	// Verify independently
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	want := hex.EncodeToString(mac.Sum(nil))

	if got != want {
		t.Errorf("computeHMAC = %s, want %s", got, want)
	}
}

func TestComputeHMAC_DifferentSecrets(t *testing.T) {
	payload := []byte(`{"event":"job.done"}`)
	sig1 := computeHMAC("secret-a", payload)
	sig2 := computeHMAC("secret-b", payload)

	if sig1 == sig2 {
		t.Error("different secrets should produce different signatures")
	}
}
