package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Webhook is one configured inbound webhook (Blueprint §8.11, ADR-0034).
type Webhook struct {
	Secret   []byte
	EventKey string
}

const (
	webhookMaxBody = 16 << 10
	webhookSkew    = 5 * time.Minute
)

// webhook accepts a request signed by an external system:
//
//	X-Aura-Timestamp: <unix seconds>            (within ±5 minutes)
//	X-Aura-Signature: sha256=<hex HMAC-SHA256(secret, timestamp + "." + body)>
//	X-Aura-Delivery:  <optional unique id>      (deduplication)
//
// The body (a JSON value, ≤ 16 KiB) becomes the payload of the webhook's
// external event, which wakes tasks waiting for it. The payload is untrusted
// data; a webhook grants nothing and cannot start or change a task.
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	hook, ok := s.Webhooks[name]
	if !ok {
		s.fail(w, r, 404, "not_found", "no such webhook")
		return
	}
	if !s.lim.allow() {
		s.fail(w, r, 429, "rate_limited", "too many requests")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, webhookMaxBody+1))
	if err != nil || len(body) > webhookMaxBody {
		s.fail(w, r, 413, "payload_too_large", "webhook body must be at most 16 KiB")
		return
	}
	tsRaw := r.Header.Get("X-Aura-Timestamp")
	ts, err := strconv.ParseInt(tsRaw, 10, 64)
	sig, hasPrefix := strings.CutPrefix(r.Header.Get("X-Aura-Signature"), "sha256=")
	given, decErr := hex.DecodeString(sig)
	mac := hmac.New(sha256.New, hook.Secret)
	mac.Write([]byte(tsRaw + "." + string(body)))
	age := time.Since(time.Unix(ts, 0))
	if err != nil || !hasPrefix || decErr != nil || !hmac.Equal(given, mac.Sum(nil)) || age > webhookSkew || age < -webhookSkew {
		s.audit(r, "WEBHOOK_AUTH_FAILED", map[string]any{"webhook": name, "remote": r.RemoteAddr})
		s.fail(w, r, 401, "unauthorized", "missing, stale or invalid webhook signature")
		return
	}
	if !json.Valid(body) {
		s.fail(w, r, 400, "invalid_request", "webhook body must be JSON")
		return
	}
	delivery := r.Header.Get("X-Aura-Delivery")
	if delivery == "" {
		delivery = tsRaw + "." + string(body)
	}
	d := sha256.Sum256([]byte(delivery))
	id, dup, err := s.Engine.ExternalEvent(r.Context(), hook.EventKey, "webhook:"+name+":"+hex.EncodeToString(d[:16]), body)
	if err != nil {
		s.mapErr(w, r, err)
		return
	}
	s.audit(r, "WEBHOOK_RECEIVED", map[string]any{"webhook": name, "event_key": hook.EventKey, "duplicate": dup, "bytes": len(body)})
	status := http.StatusAccepted
	if dup {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"id": id, "duplicate": dup})
}
