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
	"github.com/rndmcodeguy20/mpiper/pkg/utils"
	"go.uber.org/zap"
)

type DispatcherConfig struct {
	PollInterval  time.Duration
	BatchSize     int
	Timeout       time.Duration
	MaxAttempts   int
	EncryptionKey string
	Retention     time.Duration
}

type Dispatcher struct {
	db     *sqlx.DB
	logger *zap.Logger
	client *http.Client
	cfg    DispatcherConfig
}

func NewDispatcher(db *sqlx.DB, logger *zap.Logger, cfg DispatcherConfig) *Dispatcher {
	return &Dispatcher{
		db:     db,
		logger: logger,
		client: &http.Client{Timeout: cfg.Timeout},
		cfg:    cfg,
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

	for _, row := range rows {
		d.deliver(ctx, row)
	}
}

func (d *Dispatcher) deliver(ctx context.Context, row deliveryRow) {
	secret, err := utils.DecryptToken(row.Secret, d.cfg.EncryptionKey)
	if err != nil {
		d.logger.Error("webhook: decrypt secret failed", zap.String("delivery_id", row.ID.String()), zap.Error(err))
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
		d.handleFailure(ctx, row)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Signature", "sha256="+sig)

	resp, err := d.client.Do(req)
	if err != nil {
		d.logger.Warn("webhook: request failed", zap.String("url", row.URL), zap.Error(err))
		d.handleFailure(ctx, row)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = d.db.ExecContext(ctx,
			`UPDATE webhook_deliveries SET status = 'delivered', delivered_at = now() WHERE id = $1`, row.ID)
		d.logger.Debug("webhook delivered", zap.String("id", row.ID.String()), zap.String("url", row.URL))
	} else {
		d.logger.Warn("webhook: non-2xx response", zap.String("url", row.URL), zap.Int("status", resp.StatusCode))
		d.handleFailure(ctx, row)
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
