// Package store: tudo que o agente persiste passa pela interface Store. A interface deixa os
// testes rodarem com uma implementação em memória (memstore) e mantém o SQL num lugar só (pg.go).
package store

import (
	"context"

	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/report"
)

// Tipos por identificador: um CaseID nunca é passado onde se espera um RunID.
type (
	CaseID   string
	RunID    string
	ReportID string
)

// CaseClaim diz se a investigação ainda precisa sair.
type CaseClaim struct {
	CaseID            CaseID
	AlreadyDispatched bool
	Ref               string
}

type StartRun struct {
	RunID          RunID
	AlreadyRunning bool
}

type FailureCode string

const (
	FailureNoReport         FailureCode = "no_report"
	FailureRefused          FailureCode = "refused"
	FailureMaxIterations    FailureCode = "max_iterations"
	FailureIntegrationError FailureCode = "integration_error"
	FailurePublishError     FailureCode = "publish_error"
	FailureUnknown          FailureCode = "unknown"
)

type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// RunOutcome: Failure vazio = sucesso. Falha sempre carrega o código.
type RunOutcome struct {
	Failure FailureCode
	Detail  string
	Usage   Usage
}

type DeliveryKind string

const (
	DeliveryPending DeliveryKind = "pending"
	DeliverySlack   DeliveryKind = "slack"
	DeliveryShadow  DeliveryKind = "shadow" // investigou e salvou, mas de propósito não publicou
	DeliveryStdout  DeliveryKind = "stdout"
)

type Delivery struct {
	Kind    DeliveryKind
	SlackTS string // só quando Kind == DeliverySlack
}

type Verdict string

const (
	VerdictCorrect          Verdict = "correct"
	VerdictPartiallyCorrect Verdict = "partially_correct"
	VerdictWrong            Verdict = "wrong"
)

type Feedback struct {
	ReportID    ReportID
	Verdict     Verdict
	Author      string
	ActualCause string
}

type Postmortem struct {
	Slug        string   `json:"slug"`
	Product     string   `json:"product"`
	Title       string   `json:"title"`
	IncidentRef string   `json:"incidentRef,omitempty"`
	OccurredAt  string   `json:"occurredAt,omitempty"` // YYYY-MM-DD
	RootCause   string   `json:"rootCause,omitempty"`
	Tags        []string `json:"tags"`
	Body        string   `json:"body"`
}

type PostmortemHit struct {
	Slug        string  `json:"slug"`
	Product     string  `json:"product"`
	Title       string  `json:"title"`
	IncidentRef string  `json:"incidentRef,omitempty"`
	OccurredAt  string  `json:"occurredAt,omitempty"`
	RootCause   string  `json:"rootCause,omitempty"`
	Snippet     string  `json:"snippet"`
	Rank        float64 `json:"rank"`
}

type PastIncident struct {
	ExternalID   string   `json:"externalId"`
	Title        string   `json:"title"`
	DeclaredAt   string   `json:"declaredAt"`
	ReportStatus string   `json:"reportStatus,omitempty"`
	RootCause    string   `json:"rootCause,omitempty"`
	Confidence   *float64 `json:"confidence,omitempty"`
	Feedback     Verdict  `json:"feedback,omitempty"`
	ActualCause  string   `json:"actualCause,omitempty"`
}

type ReportSummary struct {
	ReportID   ReportID
	ExternalID string
	Product    string
	Title      string
	Status     string
	Confidence float64
	RootCause  string
	Delivery   DeliveryKind
	TraceID    string
	CreatedAt  string
	Feedback   Verdict
}

type Store interface {
	// ClaimCase faz upsert do caso por (source, externalId) e diz se a investigação ainda precisa sair.
	ClaimCase(ctx context.Context, inc incident.Incident) (CaseClaim, error)
	MarkDispatched(ctx context.Context, caseID CaseID, ref string) error

	StartRun(ctx context.Context, caseID CaseID, model, traceID string) (StartRun, error)
	FinishRun(ctx context.Context, runID RunID, outcome RunOutcome) error

	SaveReport(ctx context.Context, runID RunID, caseID CaseID, r report.RcaReport, slackChannel string) (ReportID, error)
	MarkDelivered(ctx context.Context, reportID ReportID, d Delivery) error
	ListRecentReports(ctx context.Context, limit int) ([]ReportSummary, error)
	RecordFeedback(ctx context.Context, f Feedback) error

	UpsertPostmortem(ctx context.Context, pm Postmortem) error
	SearchPostmortems(ctx context.Context, query, product string, limit int) ([]PostmortemHit, error)
	GetPostmortem(ctx context.Context, slug string) (*Postmortem, error)

	// RecentIncidents: incidentes anteriores do produto com o que o agente concluiu e o que o time disse.
	RecentIncidents(ctx context.Context, product, excludeExternalID string, limit int) ([]PastIncident, error)

	Close()
}
