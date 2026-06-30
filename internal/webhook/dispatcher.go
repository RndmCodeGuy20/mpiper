package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/rndmcodeguy20/mpiper/internal/metrics"
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type DispatcherConfig struct {
	PollInterval  time.Duration
	BatchSize     int
	Timeout       time.Duration
	MaxAttempts   int
	EncryptionKey string
	Retention     time.Duration
	// Concurrency bounds the number of webhook deliveries in flight per tick.
	Concurrency int
}

type Dispatcher struct {
	db     *sqlx.DB
	logger *zap.Logger
	client *http.Client
	cfg    DispatcherConfig
	m      *metrics.Metrics
}

func NewDispatcher(db *sqlx.DB, logger *zap.Logger, cfg DispatcherConfig, m *metrics.Metrics) *Dispatcher {
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}
	// Tune the transport so concurrent deliveries to the same receiver host
	// reuse connections. Go's default Transport caps MaxIdleConnsPerHost at 2,
	// which would serialize TLS handshakes for N concurrent POSTs to one host
	// and inflate delivery p95. Size the per-host pools to the concurrency.
	transport := &http.Transport{
		MaxIdleConns:        cfg.Concurrency * 2,
		MaxIdleConnsPerHost: cfg.Concurrency,
		MaxConnsPerHost:     cfg.Concurrency,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Dispatcher{
		db:     db,
		logger: logger,
		client: &http.Client{Timeout: cfg.Timeout, Transport: transport},
		cfg:    cfg,
		m:      m,
	}
}

type deliveryRow struct {
	ID       uuid.UUID       `db:"id"`
	Event    string          `db:"event"`
	AssetID  uuid.UUID       `db:"asset_id"`
	JobID    int64           `db:"job_id"`
	Payload  json.RawMessage `db:"payload"`
	Attempts int             `db:"attempts"`
	URL      string          `db:"url"`
	Secret   string          `db:"secret"`
}

func (d *Dispatcher) Start(ctx context.Context) {
	d.logger.Info("webhook dispatcher started", zap.Duration("interval", d.cfg.PollInterval))
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("webhook dispatcher stopped")
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) {
	rows := make([]deliveryRow, 0, d.cfg.BatchSize)
	// NOTE: FOR UPDATE ... SKIP LOCKED runs here OUTSIDE an explicit transaction,
	// so the row locks are released as soon as this SELECT returns. That is safe
	// for a SINGLE dispatcher process fanning the batch out to internal
	// goroutines (each row appears once in `rows`, delivered by one goroutine).
	// It does NOT prevent two SEPARATE dispatcher processes from claiming the
	// same row. If this is ever scaled to >1 dispatcher, wrap the claim in a tx
	// for the lifetime of delivery, or add a claimed_at/locked_by column.
	err := d.db.SelectContext(ctx, &rows,
		`SELECT wd.id, wd.event, wd.asset_id, wd.job_id, wd.payload, wd.attempts, wr.url, wr.secret
		 FROM webhook_deliveries wd
		 JOIN webhook_registrations wr ON wd.registration_id = wr.id
		 WHERE wd.status = 'pending' AND wd.next_attempt_at <= now()
		 ORDER BY wd.next_attempt_at
		 LIMIT $1
		 FOR UPDATE OF wd SKIP LOCKED`, d.cfg.BatchSize)
	if err != nil {
		d.logger.Error("webhook dispatcher: fetch failed", zap.Error(err))
		return
	}
	if len(rows) == 0 {
		return
	}

	// Deliver the batch concurrently, bounded by cfg.Concurrency. Each row is
	// independent: deliver() and its handleFailure/backoff/markFailed updates
	// are keyed by the row's own id, so concurrent delivery is race-free.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(d.cfg.Concurrency)
	for _, row := range rows {
		row := row // capture per-iteration (safe on older Go too)
		g.Go(func() error {
			d.deliver(gctx, row)
			return nil
		})
	}
	// deliver never returns an error (failures are persisted, not propagated),
	// so Wait only blocks until the batch drains.
	_ = g.Wait()
}

func (d *Dispatcher) deliver(ctx context.Context, row deliveryRow) {
	secret, err := utils.DecryptToken(row.Secret, d.cfg.EncryptionKey)
	if err != nil {
		d.logger.Error("webhook: decrypt secret failed", zap.String("delivery_id", row.ID.String()), zap.Error(err))
		d.recordDelivery(ctx, row.Event, "error", 0, false)
		d.markFailed(ctx, row.ID)
		return
	}

	payloadBytes, _ := json.Marshal(row.Payload)
	sig := computeHMAC(secret, payloadBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, row.URL, io.NopCloser(
		bytesReader(payloadBytes),
	))
	if err != nil {
		d.logger.Error("webhook: build request failed", zap.Error(err))
		d.recordDelivery(ctx, row.Event, "error", 0, false)
		d.handleFailure(ctx, row)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", "sha256="+sig)

	start := time.Now()
	resp, err := d.client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		d.logger.Warn("webhook: request failed", zap.String("url", row.URL), zap.Error(err))
		d.recordDelivery(ctx, row.Event, "error", elapsed, false)
		d.handleFailure(ctx, row)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = d.db.ExecContext(ctx,
			`UPDATE webhook_deliveries SET status = 'delivered', delivered_at = now() WHERE id = $1`, row.ID)
		d.recordDelivery(ctx, row.Event, "delivered", elapsed, true)
		d.logger.Debug("webhook delivered", zap.String("id", row.ID.String()), zap.String("url", row.URL))
	} else {
		d.logger.Warn("webhook: non-2xx response", zap.String("url", row.URL), zap.Int("status", resp.StatusCode))
		d.recordDelivery(ctx, row.Event, "failed", elapsed, false)
		d.handleFailure(ctx, row)
	}
}

// recordDelivery records per-delivery metrics. Labels are restricted to the
// low-cardinality event name and a status bucket (delivered/failed/error) —
// asset_id and url are deliberately excluded to keep metric cardinality bounded.
// Duration is only recorded when an HTTP call was actually made (dur > 0).
func (d *Dispatcher) recordDelivery(ctx context.Context, event, status string, dur time.Duration, success bool) {
	if d.m == nil {
		return
	}
	attrs := otelmetric.WithAttributes(
		attribute.String("event", event),
		attribute.String("status", status),
	)
	d.m.WebhookDeliveryTotal.Add(ctx, 1, attrs)
	if !success {
		d.m.WebhookDeliveryFailures.Add(ctx, 1, attrs)
	}
	if dur > 0 {
		d.m.WebhookDeliveryDuration.Record(ctx, dur.Seconds(), attrs)
	}
}

func (d *Dispatcher) handleFailure(ctx context.Context, row deliveryRow) {
	newAttempts := row.Attempts + 1
	if newAttempts >= d.cfg.MaxAttempts {
		d.markFailed(ctx, row.ID)
		return
	}
	next := backoff(newAttempts)
	_, _ = d.db.ExecContext(ctx,
		`UPDATE webhook_deliveries SET attempts = $2, next_attempt_at = now() + $3::interval WHERE id = $1`,
		row.ID, newAttempts, fmt.Sprintf("%d seconds", int(next.Seconds())))
}

func (d *Dispatcher) markFailed(ctx context.Context, id uuid.UUID) {
	_, _ = d.db.ExecContext(ctx,
		`UPDATE webhook_deliveries SET status = 'failed' WHERE id = $1`, id)
}

// StartCleanup deletes delivered rows older than retention.
func (d *Dispatcher) StartCleanup(ctx context.Context) {
	interval := d.cfg.Retention / 24
	if interval < time.Minute {
		interval = time.Minute
	}
	d.logger.Info("webhook cleanup started", zap.Duration("retention", d.cfg.Retention))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			res, err := d.db.ExecContext(ctx,
				`DELETE FROM webhook_deliveries WHERE status = 'delivered' AND delivered_at < now() - $1::interval`,
				fmt.Sprintf("%d hours", int(d.cfg.Retention.Hours())))
			if err != nil {
				d.logger.Error("webhook cleanup failed", zap.Error(err))
				continue
			}
			if n, _ := res.RowsAffected(); n > 0 {
				d.logger.Info("webhook cleanup: deleted old rows", zap.Int64("count", n))
			}
		}
	}
}

// backoff returns exponential backoff with jitter, capped at 5 minutes.
func backoff(attempt int) time.Duration {
	base := 1 * time.Second
	maxBackoff := 5 * time.Minute
	b := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
	if b > maxBackoff {
		b = maxBackoff
	}
	// Add jitter: ±25%
	jitter := time.Duration(rand.Int63n(int64(b/2))) - (b / 4)
	result := b + jitter
	if result > maxBackoff {
		result = maxBackoff
	}
	if result < 0 {
		result = base
	}
	return result
}

func computeHMAC(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func bytesReader(b []byte) io.Reader {
	return &bytesReaderImpl{data: b}
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
