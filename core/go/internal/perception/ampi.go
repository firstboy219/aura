// Package perception is AURA's end-to-end perception path (ODI): JARVIS hands AURA a piece of media and a question; AURA authorizes it, has AMPI perceive it (through AMPI's
// AURA-only API gate), turns AMPI's observations into bounded evidence, asks NEXUS to reason over that evidence (and nothing else), and keeps the answer with the time every hop
// took. AMPI decides nothing, NEXUS authorizes nothing, and the media itself is never stored by AURA.
package perception

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"aura.local/core/internal/ids"
)

// AMPI is a client for the perception routes of AMPI's API (the gate lets nothing else through).
type AMPI struct {
	Base  string                                    // http://ampi:7700
	Token func(ctx context.Context) (string, error) // the AURA credential AMPI issued (vault connector.ampi_client_token)
	HC    *http.Client
}

// Obs is the part of an AMPI observation the evidence needs.
type Obs struct {
	Capability    string         `json:"capability"`
	EpistemicType string         `json:"epistemic_type"`
	UnknownReason string         `json:"unknown_reason"`
	Value         map[string]any `json:"value"`
	TimeWindow    struct {
		StartMs int64 `json:"start_ms"`
	} `json:"time_window"`
}

func (a *AMPI) call(ctx context.Context, method, path string, hdr map[string]string, body []byte, out any) error {
	tok, err := a.Token(ctx)
	if err != nil || tok == "" {
		return fmt.Errorf("AMPI credential is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, a.Base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Idempotency-Key", ids.New())
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if body == nil {
		req.Body = nil
		req.ContentLength = 0
	}
	hc := a.HC
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("AMPI unreachable: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("AMPI %s %s: HTTP %d %.200s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (a *AMPI) Session(ctx context.Context, modality, consent string, locale []string) (string, error) {
	mods := map[string][]string{"audio": {"audio"}, "image": {"image"}}[modality]
	// exactly the capabilities this request needs, granted by name: OCR is 'sensitive' and is not in group:vision_basic, so a group grant silently ran no OCR (found live 2026-11-17)
	body, _ := json.Marshal(map[string]any{"purpose": "batch_analysis", "source": map[string]string{"type": "file_upload"}, "modalities": mods, "consent_ref": consent,
		"granted_capabilities": Capabilities[modality], "locale_hints": locale})
	var out struct {
		ID string `json:"session_id"`
	}
	err := a.call(ctx, "POST", "/v1/sessions", map[string]string{"Content-Type": "application/json"}, body, &out)
	return out.ID, err
}

func (a *AMPI) Upload(ctx context.Context, session string, data []byte) (string, error) {
	var out struct {
		ID string `json:"media_asset_id"`
	}
	err := a.call(ctx, "POST", "/v1/media", map[string]string{"Content-Type": "application/octet-stream", "X-Session-ID": session}, data, &out)
	return out.ID, err
}

// Capabilities is what a perception request asks AMPI for, per modality (normal sensitivity only; no biometrics).
var Capabilities = map[string][]string{
	"audio": {"audio.asr", "audio.language_id"}, // no diarization: speaker turns are unreliable (DER 45-55%) and cost word-timed sequential decoding; 2.4x faster without
	"image": {"vision.object_detection", "vision.person_detection", "vision.ocr"},
}

func (a *AMPI) Job(ctx context.Context, session, media, modality string) (string, error) {
	body, _ := json.Marshal(map[string]any{"session_id": session, "inputs": []map[string]string{{"media_ref": media, "modality": map[string]string{"audio": "audio", "image": "image"}[modality]}},
		"capabilities": Capabilities[modality], "deadline_ms": 300000})
	var out struct {
		ID string `json:"job_id"`
	}
	err := a.call(ctx, "POST", "/v1/jobs", map[string]string{"Content-Type": "application/json"}, body, &out)
	return out.ID, err
}

// JobStatus returns AMPI's job status (SUCCEEDED, PARTIAL, FAILED, ... or a running state).
func (a *AMPI) JobStatus(ctx context.Context, job string) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	err := a.call(ctx, "GET", "/v1/jobs/"+url.PathEscape(job), nil, nil, &out)
	return out.Status, err
}

func (a *AMPI) Observations(ctx context.Context, session string) ([]Obs, error) {
	var out struct {
		Observations []Obs `json:"observations"`
	}
	err := a.call(ctx, "GET", "/v1/observations?session_id="+url.QueryEscape(session), nil, nil, &out)
	return out.Observations, err
}

func (a *AMPI) Close(ctx context.Context, session string) error {
	return a.call(ctx, "POST", "/v1/sessions/"+url.PathEscape(session)+"/close", map[string]string{"Content-Type": "application/json"}, []byte("{}"), nil)
}
