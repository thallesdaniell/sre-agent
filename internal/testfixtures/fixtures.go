// Package testfixtures tem os dados de exemplo usados nos testes de vários pacotes.
package testfixtures

import (
	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/report"
	"github.com/thallesdaniell/sre-agent/internal/scope"
)

func Incident() incident.Incident {
	return incident.Incident{
		Source: "webhook", ID: "INC-1042", Title: "Checkout 502", Product: "checkout",
		Severity: incident.Sev2, DeclaredAt: "2026-11-19T14:05:00Z", SlackChannel: "#inc-1042",
	}
}

func Report() report.RcaReport {
	return report.RcaReport{
		IncidentID: "INC-1042",
		Status:     report.Preliminary,
		Summary:    "O checkout passou a responder 502 após a revisão 42 reduzir o limite de memória.",
		RootCause: report.RootCause{
			Statement:  "Limite de memória do checkout-api reduzido de 1Gi para 512Mi na revisão 42",
			Confidence: 0.7,
			Reasoning:  "Pods da revisão 42 terminam com OOMKilled; a revisão 41 não tinha nenhum.",
		},
		CausalChain:         []string{"Revisão 42 sobe com 512Mi", "Pods estouram memória no pico", "Ingress devolve 502"},
		Evidence:            []report.Evidence{{Tool: "list_pods", Finding: "12 restarts OOMKilled só na revisão 42", Role: report.RoleRootCause}},
		DiscardedHypotheses: []report.DiscardedHypothesis{{Statement: "Banco lento", Reason: "Latência do banco estável na janela"}},
		ChangeAttribution:   report.ChangeAttribution{Kind: report.ChangeDeploy, Ref: "revisão 42", EvidenceStrength: report.StrengthDirect},
		Limitations:         []string{"Não houve acesso às métricas do banco"},
		NextSteps:           []string{"Voltar o limite para 1Gi e acompanhar os restarts"},
	}
}

func Scopes() *scope.Registry {
	return scope.NewRegistry([]scope.ProductScope{
		{Product: "checkout", Namespaces: []string{"checkout"}, Repository: "org/checkout-api", Environment: scope.Production},
	})
}
