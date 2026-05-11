package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/tabloy/keygate/internal/middleware"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
)

type WebhookService struct {
	store      *store.Store
	logger     *slog.Logger
	client     *http.Client
	maxRetries int
	sem        chan struct{} // concurrency limiter
}

func NewWebhookService(s *store.Store, logger *slog.Logger, httpTimeout time.Duration, maxRetries int) *WebhookService {
	return &WebhookService{
		store:      s,
		logger:     logger,
		client:     &http.Client{Timeout: httpTimeout},
		maxRetries: maxRetries,
		sem:        make(chan struct{}, 20), // max 20 concurrent deliveries
	}
}

func (s *WebhookService) Dispatch(ctx context.Context, productID, event string, data map[string]any) {
	if err := s.DispatchWithLog(ctx, productID, event, data); err != nil {
		s.logger.Error("webhook dispatch failed", "event", event, "product_id", productID, "error", err)
	}
}

// DispatchWithLog dispatches webhook events and returns any error that occurs during setup.
// Use this when the caller needs to handle or log dispatch failures explicitly.
func (s *WebhookService) DispatchWithLog(ctx context.Context, productID, event string, data map[string]any) error {
	webhooks, err := s.store.FindWebhooksForEvent(ctx, productID, event)
	if err != nil {
		return fmt.Errorf("find webhooks: %w", err)
	}
	if len(webhooks) == 0 {
		return nil
	}

	payload := map[string]any{
		"event":     event,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data":      data,
	}

	for _, wh := range webhooks {
		delivery := &model.WebhookDelivery{
			WebhookID: wh.ID,
			Event:     event,
			Payload:   payload,
			Status:    "pending",
		}
		if err := s.store.CreateWebhookDelivery(ctx, delivery); err != nil {
			s.logger.Error("webhook delivery create failed", "webhook_id", wh.ID, "error", err)
			continue
		}
		go func() {
			s.sem <- struct{}{}        // acquire
			defer func() { <-s.sem }() // release
			s.deliver(wh, delivery)
		}()
	}
	return nil
}

func (s *WebhookService) deliver(wh *model.Webhook, delivery *model.WebhookDelivery) {
	ctx := context.Background()
	body, _ := json.Marshal(delivery.Payload)
	sig := signPayload(body, wh.Secret)

	req, err := http.NewRequestWithContext(ctx, "POST", wh.URL, bytes.NewReader(body))
	if err != nil {
		s.failDelivery(ctx, delivery, 0, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Keygate-Event", delivery.Event)
	req.Header.Set("X-Keygate-Signature", "sha256="+sig)
	req.Header.Set("X-Keygate-Delivery", delivery.ID)

	resp, err := s.client.Do(req)
	if err != nil {
		s.failDelivery(ctx, delivery, 0, err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	delivery.ResponseCode = resp.StatusCode
	delivery.ResponseBody = string(respBody)
	delivery.Attempts++

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now()
		delivery.Status = "delivered"
		delivery.DeliveredAt = &now
		middleware.WebhookDeliveries.WithLabelValues("delivered").Inc()
	} else {
		s.scheduleRetry(delivery)
	}
	_ = s.store.UpdateWebhookDelivery(ctx, delivery)
}

func (s *WebhookService) scheduleRetry(d *model.WebhookDelivery) {
	if d.Attempts >= s.maxRetries {
		d.Status = "failed"
		middleware.WebhookDeliveries.WithLabelValues("failed").Inc()
		return
	}
	backoff := time.Duration(1<<uint(d.Attempts)) * 30 * time.Second
	next := time.Now().Add(backoff)
	d.NextRetry = &next
	d.Status = "pending"
	middleware.WebhookDeliveries.WithLabelValues("retrying").Inc()
}

func (s *WebhookService) failDelivery(ctx context.Context, d *model.WebhookDelivery, code int, body string) {
	d.Attempts++
	d.ResponseCode = code
	d.ResponseBody = body
	s.scheduleRetry(d)
	_ = s.store.UpdateWebhookDelivery(ctx, d)
}

// ErrWebhookDeliveryNotResendable is returned by Redispatch when the
// target delivery exists but the parent webhook has been deleted or
// disabled. Surfaces as 409 so the admin UI can disable the button
// instead of silently failing.
var ErrWebhookDeliveryNotResendable = fmt.Errorf("webhook delivery is not resendable")

// Redispatch fires a fresh delivery using the payload of an existing
// one. Industry-standard "resend" behaviour: the receiver sees the
// SAME `data` (so its idempotency dedup still works) but a new
// `X-Keygate-Delivery` header and a new row in the deliveries table
// — so retries, response codes, and timestamps are tracked
// independently of the original attempt.
//
// Returns the new delivery on success. Caller should audit-log the
// admin action with both delivery IDs.
func (s *WebhookService) Redispatch(ctx context.Context, deliveryID string) (*model.WebhookDelivery, error) {
	orig, err := s.store.FindWebhookDeliveryByID(ctx, deliveryID)
	if err != nil {
		return nil, err
	}
	wh, err := s.store.FindWebhookByID(ctx, orig.WebhookID)
	if err != nil {
		return nil, ErrWebhookDeliveryNotResendable
	}
	if !wh.Active {
		return nil, ErrWebhookDeliveryNotResendable
	}
	fresh := &model.WebhookDelivery{
		WebhookID: wh.ID,
		Event:     orig.Event,
		Payload:   orig.Payload, // byte-identical replay
		Status:    "pending",
	}
	if err := s.store.CreateWebhookDelivery(ctx, fresh); err != nil {
		return nil, err
	}
	go func() {
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		s.deliver(wh, fresh)
	}()
	return fresh, nil
}

func (s *WebhookService) ProcessRetries(ctx context.Context) {
	deliveries, err := s.store.ListPendingDeliveries(ctx, 50)
	if err != nil || len(deliveries) == 0 {
		return
	}
	for _, d := range deliveries {
		wh, err := s.store.FindWebhookByID(ctx, d.WebhookID)
		if err != nil {
			continue
		}
		go func(wh *model.Webhook, d *model.WebhookDelivery) {
			s.sem <- struct{}{}
			defer func() { <-s.sem }()
			s.deliver(wh, d)
		}(wh, d)
	}
}

func (s *WebhookService) StartRetryLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ProcessRetries(ctx)
		}
	}
}

func GenerateWebhookSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func signPayload(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return hex.EncodeToString(h.Sum(nil))
}
