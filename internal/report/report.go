// Package report define o contrato do relatório de RCA.
//
// O agente NÃO escreve texto livre para o Slack: ele chama a tool submit_rca_report com este
// objeto, o servidor valida e só então renderiza a mensagem. As tags jsonschema geram o schema
// que o modelo vê; Validate impõe as regras que um schema não expressa.
package report

import (
	"fmt"
	"strings"
)

type Status string

const (
	Preliminary  Status = "PRELIMINARY"
	Inconclusive Status = "INCONCLUSIVE"
)

type EvidenceRole string

const (
	RoleRootCause    EvidenceRole = "root_cause"
	RoleTrigger      EvidenceRole = "trigger"
	RoleContributing EvidenceRole = "contributing"
	RoleSymptom      EvidenceRole = "symptom"
	RoleContradicts  EvidenceRole = "contradicts"
)

type Evidence struct {
	Tool    string       `json:"tool" jsonschema:"required,description=Nome da tool que produziu a evidência, ex.: rollout_history"`
	Finding string       `json:"finding" jsonschema:"required,description=O que foi observado, com nomes, números e horários"`
	Role    EvidenceRole `json:"role" jsonschema:"required,enum=root_cause,enum=trigger,enum=contributing,enum=symptom,enum=contradicts,description=Papel da evidência na cadeia causal"`
}

type RootCause struct {
	Statement  string  `json:"statement" jsonschema:"required"`
	Confidence float64 `json:"confidence" jsonschema:"required,minimum=0,maximum=1"`
	Reasoning  string  `json:"reasoning" jsonschema:"required"`
}

type DiscardedHypothesis struct {
	Statement string `json:"statement" jsonschema:"required"`
	Reason    string `json:"reason" jsonschema:"required"`
}

type ChangeKind string

const (
	ChangeDeploy      ChangeKind = "deploy"
	ChangePullRequest ChangeKind = "pull_request"
	ChangeConfig      ChangeKind = "config"
	ChangeNoneFound   ChangeKind = "none_found"
)

type EvidenceStrength string

const (
	StrengthDirect       EvidenceStrength = "direct"
	StrengthTemporalOnly EvidenceStrength = "temporal_only"
)

type ChangeAttribution struct {
	Kind             ChangeKind       `json:"kind" jsonschema:"required,enum=deploy,enum=pull_request,enum=config,enum=none_found"`
	Ref              string           `json:"ref,omitempty" jsonschema:"description=PR, revisão do rollout ou commit, quando houver"`
	EvidenceStrength EvidenceStrength `json:"evidenceStrength" jsonschema:"required,enum=direct,enum=temporal_only,description=direct = a evidência liga a mudança ao sintoma; temporal_only = só aconteceu antes"`
}

type RcaReport struct {
	IncidentID          string                `json:"incidentId" jsonschema:"required"`
	Status              Status                `json:"status" jsonschema:"required,enum=PRELIMINARY,enum=INCONCLUSIVE,description=Nunca CONFIRMED: quem confirma a causa é uma pessoa"`
	Summary             string                `json:"summary" jsonschema:"required,maxLength=600,description=Resumo em PT-BR, 2 a 4 frases"`
	RootCause           RootCause             `json:"rootCause" jsonschema:"required"`
	Trigger             string                `json:"trigger,omitempty"`
	CausalChain         []string              `json:"causalChain" jsonschema:"required,minItems=1,description=Do gatilho ao sintoma, em ordem"`
	Evidence            []Evidence            `json:"evidence" jsonschema:"required"`
	DiscardedHypotheses []DiscardedHypothesis `json:"discardedHypotheses" jsonschema:"required"`
	ChangeAttribution   ChangeAttribution     `json:"changeAttribution" jsonschema:"required"`
	Limitations         []string              `json:"limitations" jsonschema:"required"`
	NextSteps           []string              `json:"nextSteps" jsonschema:"required,minItems=1"`
}

// Issue aponta o campo e o motivo, para o modelo corrigir e reenviar.
type Issue struct {
	Path    string
	Message string
}

type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		parts[i] = fmt.Sprintf("%s: %s", issue.Path, issue.Message)
	}
	return "relatório recusado: " + strings.Join(parts, "; ")
}

// Validate impõe as regras do relatório. Devolve nil ou um *ValidationError com todos os problemas.
func (r RcaReport) Validate() error {
	var issues []Issue
	add := func(path, msg string) { issues = append(issues, Issue{path, msg}) }

	if strings.TrimSpace(r.IncidentID) == "" {
		add("incidentId", "obrigatório")
	}
	if strings.TrimSpace(r.Summary) == "" || len(r.Summary) > 600 {
		add("summary", "obrigatório, até 600 caracteres")
	}
	if strings.TrimSpace(r.RootCause.Statement) == "" || strings.TrimSpace(r.RootCause.Reasoning) == "" {
		add("rootCause", "statement e reasoning são obrigatórios")
	}
	if len(r.CausalChain) == 0 {
		add("causalChain", "ao menos um passo")
	}
	if len(r.NextSteps) == 0 {
		add("nextSteps", "ao menos um próximo passo")
	}

	switch r.ChangeAttribution.Kind {
	case ChangeDeploy, ChangePullRequest, ChangeConfig, ChangeNoneFound:
	default:
		add("changeAttribution.kind", "valor desconhecido")
	}
	switch r.ChangeAttribution.EvidenceStrength {
	case StrengthDirect, StrengthTemporalOnly:
	default:
		add("changeAttribution.evidenceStrength", "valor desconhecido")
	}
	for i, e := range r.Evidence {
		switch e.Role {
		case RoleRootCause, RoleTrigger, RoleContributing, RoleSymptom, RoleContradicts:
		default:
			add(fmt.Sprintf("evidence[%d].role", i), "valor desconhecido")
		}
	}

	confidence := r.RootCause.Confidence
	if confidence < 0 || confidence > 1 {
		add("rootCause.confidence", "entre 0 e 1")
	}

	// "O deploy veio antes" não é causa. Sem evidência direta, confiança baixa.
	if r.ChangeAttribution.Kind != ChangeNoneFound && r.ChangeAttribution.EvidenceStrength == StrengthTemporalOnly && confidence > 0.5 {
		add("rootCause.confidence", "mudança atribuída só por proximidade temporal: confiança máxima 0.5")
	}

	switch r.Status {
	case Preliminary:
		if !r.hasEvidence(RoleRootCause) {
			add("evidence", "PRELIMINARY exige ao menos uma evidência com role=root_cause")
		}
	case Inconclusive:
		if confidence >= 0.5 {
			add("rootCause.confidence", "INCONCLUSIVE exige confiança abaixo de 0.5")
		}
	default:
		add("status", "só PRELIMINARY ou INCONCLUSIVE: quem confirma a causa é uma pessoa")
	}

	if confidence >= 0.8 && len(r.Limitations) == 0 {
		add("limitations", "confiança alta sem nenhuma limitação declarada: diga o que não foi verificado")
	}

	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

func (r RcaReport) hasEvidence(role EvidenceRole) bool {
	for _, e := range r.Evidence {
		if e.Role == role {
			return true
		}
	}
	return false
}
