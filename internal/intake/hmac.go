// Package intake é o único componente sempre ligado. Faz o mínimo: autentica, valida, filtra,
// deduplica e dispara. Nada de LLM aqui; a investigação roda no job.
package intake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

const ToleranceSeconds = 300

// SignatureCheck é estado explícito, nunca bool: válido ou inválido com motivo.
type SignatureCheck string

const (
	SignatureValid          SignatureCheck = "valid"
	SignatureMissingHeaders SignatureCheck = "missing_headers"
	SignatureStale          SignatureCheck = "stale_timestamp"
	SignatureBad            SignatureCheck = "bad_signature"
)

func Sign(secret, timestamp string, rawBody []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(rawBody)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature confere a assinatura sobre o corpo BRUTO (antes de qualquer parse) e recusa
// entregas velhas, o que impede reenviar um webhook capturado.
func VerifySignature(secret string, rawBody []byte, timestamp, signature string, now time.Time) SignatureCheck {
	if timestamp == "" || signature == "" {
		return SignatureMissingHeaders
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || abs(now.Unix()-ts) > ToleranceSeconds {
		return SignatureStale
	}
	if !hmac.Equal([]byte(Sign(secret, timestamp, rawBody)), []byte(signature)) {
		return SignatureBad
	}
	return SignatureValid
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
