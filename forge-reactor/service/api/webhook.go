package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r59q/forge-tools/forge-reactor/library/reactor"
	"github.com/r59q/forge-tools/forge-reactor/library/webhook"
)

// MaxWebhookBody caps webhook bodies; larger deliveries get 413 and no event.
const MaxWebhookBody = 2 << 20

// Webhooks receives native deliveries at POST /webhooks/{source}. It does
// not use the service token: each delivery is authenticated by its
// signature over the raw body.
type Webhooks struct {
	reactor *reactor.Reactor
	sources *Sources
	timeout time.Duration
}

// webhookReply is the response body for accepted or ignored deliveries.
type webhookReply struct {
	Event     string `json:"event,omitempty"`
	Duplicate bool   `json:"duplicate,omitempty"`
	Ignored   string `json:"ignored,omitempty"`
}

// NewWebhooks returns a handler for sources.
func NewWebhooks(r *reactor.Reactor, sources *Sources, timeout time.Duration) (*Webhooks, error) {
	if r == nil || sources == nil || timeout <= 0 {
		return nil, ErrInvalid
	}

	return &Webhooks{reactor: r, sources: sources, timeout: timeout}, nil
}

// ServeHTTP verifies, normalizes and accepts one delivery. It answers 202
// once the event is durable or the delivery is a duplicate or ignored, and
// otherwise creates no event: 401 for a bad signature, 400 for a malformed
// delivery, 403 for another forge or repository, 404 for an unknown source,
// 413 for an oversized body and 503 while the delivery backlog is full.
// Responses never echo the request.
func (h *Webhooks) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	ctx, cancel := context.WithTimeout(req.Context(), h.timeout)
	defer cancel()
	id, _ := strings.CutPrefix(req.URL.Path, "/webhooks/")
	src, err := h.sources.lookup(ctx, id)
	switch {
	case errors.Is(err, reactor.ErrNotFound):
		http.NotFound(w, req)

		return
	case req.Method != http.MethodPost:
		w.Header().Set("Allow", http.MethodPost)
		reply(w, http.StatusMethodNotAllowed)

		return
	case err != nil:
		reply(w, http.StatusInternalServerError)

		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, MaxWebhookBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			reply(w, http.StatusRequestEntityTooLarge)
		} else {
			reply(w, http.StatusBadRequest)
		}

		return
	}

	answer, code := h.accept(ctx, src, req.Header, body)
	if code != http.StatusAccepted {
		reply(w, code)

		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(answer)
}

// accept parses and stores one delivery and returns the reply and status.
func (h *Webhooks) accept(ctx context.Context, src webhook.Source, header http.Header, body []byte) (webhookReply, int) {
	res, err := webhook.Parse(src, header, body)
	switch {
	case errors.Is(err, webhook.ErrSignature):
		return webhookReply{}, http.StatusUnauthorized
	case errors.Is(err, webhook.ErrMalformed):
		return webhookReply{}, http.StatusBadRequest
	case errors.Is(err, webhook.ErrMismatch):
		return webhookReply{}, http.StatusForbidden
	case err != nil:
		return webhookReply{}, http.StatusInternalServerError
	case res.Input == nil:
		return webhookReply{Ignored: res.Ignored}, http.StatusAccepted
	}

	e, duplicate, err := h.reactor.Accept(ctx, *res.Input)
	switch {
	case errors.Is(err, reactor.ErrBusy):
		return webhookReply{}, http.StatusServiceUnavailable
	case errors.Is(err, reactor.ErrInvalid):
		return webhookReply{}, http.StatusBadRequest
	case err != nil:
		return webhookReply{}, http.StatusInternalServerError
	}

	return webhookReply{Event: e.ID, Duplicate: duplicate}, http.StatusAccepted
}

// reply writes a bare status line as the body.
func reply(w http.ResponseWriter, code int) {
	http.Error(w, http.StatusText(code), code)
}
