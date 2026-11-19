// Package incident define o formato interno de um incidente, independente de quem avisou
// (incident.io, PagerDuty, Datadog). Cada fonte ganha um adaptador que converte para isto.
package incident

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	sourcePattern  = regexp.MustCompile(`^[a-z0-9._-]{1,40}$`)
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	productPattern = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// Severity é uma união fechada: só estes quatro valores existem.
type Severity string

const (
	Sev1 Severity = "sev1"
	Sev2 Severity = "sev2"
	Sev3 Severity = "sev3"
	Sev4 Severity = "sev4"
)

type Incident struct {
	Source       string   `json:"source"`
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Product      string   `json:"product"`
	Severity     Severity `json:"severity"`
	DeclaredAt   string   `json:"declaredAt"`
	Summary      string   `json:"summary"`
	SlackChannel string   `json:"slackChannel,omitempty"`
}

// Key identifica o incidente entre fontes: o mesmo id em fontes diferentes são incidentes diferentes.
func (i Incident) Key() string { return i.Source + ":" + i.ID }

// ValidationError lista os campos inválidos, nunca os valores (podem conter dados sensíveis).
type ValidationError struct{ Fields []string }

func (e *ValidationError) Error() string {
	return "incidente inválido: " + strings.Join(e.Fields, ", ")
}

// Parse decodifica e valida. É a borda: depois daqui o incidente é confiável.
func Parse(raw []byte) (Incident, error) {
	var inc Incident
	if err := json.Unmarshal(raw, &inc); err != nil {
		return Incident{}, fmt.Errorf("json inválido: %w", err)
	}
	if inc.Source == "" {
		inc.Source = "webhook"
	}
	return inc, inc.Validate()
}

func (i Incident) Validate() error {
	var bad []string
	check := func(ok bool, field string) {
		if !ok {
			bad = append(bad, field)
		}
	}
	check(sourcePattern.MatchString(i.Source), "source")
	check(idPattern.MatchString(i.ID), "id")
	check(len(i.Title) > 0 && len(i.Title) <= 300, "title")
	check(productPattern.MatchString(i.Product), "product")
	switch i.Severity {
	case Sev1, Sev2, Sev3, Sev4:
	default:
		bad = append(bad, "severity")
	}
	_, err := time.Parse(time.RFC3339, i.DeclaredAt)
	check(err == nil, "declaredAt")
	check(len(i.Summary) <= 4000, "summary")
	if len(bad) > 0 {
		return &ValidationError{Fields: bad}
	}
	return nil
}

// IsValidationError diz se o erro é de payload (400), e não de infraestrutura.
func IsValidationError(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}
