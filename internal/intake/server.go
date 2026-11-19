package intake

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/scope"
	"github.com/thallesdaniell/sre-agent/internal/store"
)

const maxBodyBytes = 64 * 1024

type Server struct {
	Secret     string
	Scopes     *scope.Registry
	Store      store.Store
	Dispatcher Dispatcher
	Now        func() time.Time
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("POST /webhooks/incident", s.webhook)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body_unreadable"})
		return
	}
	if len(raw) > maxBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload_too_large"})
		return
	}

	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if check := VerifySignature(s.Secret, raw, r.Header.Get("X-Timestamp"), r.Header.Get("X-Signature"), now()); check != SignatureValid {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": string(check)})
		return
	}

	inc, err := incident.Parse(raw)
	if err != nil {
		var fields []string
		var v *incident.ValidationError
		if errors.As(err, &v) {
			fields = v.Fields // só os nomes dos campos, nunca os valores
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_payload", "fields": fields})
		return
	}
	if !slices.Contains(s.Scopes.Products(), inc.Product) {
		// Aceita (200) para o remetente não reenviar, mas não investiga produto fora do inventário.
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored", "reason": "product_not_in_scope"})
		return
	}

	ctx := r.Context()
	// O banco é quem deduplica: funciona com várias réplicas do intake e sobrevive a restart.
	claim, err := s.Store.ClaimCase(ctx, inc)
	if err != nil {
		slog.Error("claim falhou", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	if claim.AlreadyDispatched {
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate", "ref": claim.Ref})
		return
	}

	// Se o disparo falhar, o caso fica sem dispatched_at e o retry do remetente tenta de novo.
	res, err := s.Dispatcher.Dispatch(ctx, inc)
	if err != nil {
		slog.Error("dispatch falhou", "incident", inc.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "dispatch_failed"})
		return
	}
	if err := s.Store.MarkDispatched(context.WithoutCancel(ctx), claim.CaseID, res.Ref); err != nil {
		slog.Error("markDispatched falhou", "error", err)
	}
	if res.Duplicate {
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate", "ref": res.Ref})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "dispatched", "ref": res.Ref})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
