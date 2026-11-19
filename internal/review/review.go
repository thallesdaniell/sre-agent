// Package review: revisão dos relatórios, principalmente no modo sombra, quando nada vai para o
// Slack. O feedback alimenta a tool past_incidents (o agente vê onde já errou) e é a base do
// conjunto de avaliação.
//
//	review                                   lista os relatórios recentes
//	review feedback <reportId> correct --author ana
//	review feedback <reportId> wrong --author ana --cause "Pool de conexões esgotado"
package review

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/thallesdaniell/sre-agent/internal/store"
	"github.com/thallesdaniell/sre-agent/internal/telemetry"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

type FeedbackArgs struct {
	ReportID store.ReportID
	Verdict  store.Verdict
	Author   string
	Cause    string
}

// ParseFeedbackArgs aceita IDs no formato uuid; allowAnyID existe para o store em memória dos testes.
func ParseFeedbackArgs(args []string, allowAnyID bool) (FeedbackArgs, error) {
	if len(args) < 2 {
		return FeedbackArgs{}, errors.New("uso: feedback <reportId> correct|partially_correct|wrong --author <nome> [--cause \"...\"]")
	}
	f := FeedbackArgs{ReportID: store.ReportID(args[0]), Verdict: store.Verdict(args[1])}
	for i := 2; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--author":
			f.Author = args[i+1]
		case "--cause":
			f.Cause = args[i+1]
		}
	}
	if !allowAnyID && !uuidPattern.MatchString(string(f.ReportID)) {
		return f, fmt.Errorf("reportId inválido: %q", f.ReportID)
	}
	switch f.Verdict {
	case store.VerdictCorrect, store.VerdictPartiallyCorrect, store.VerdictWrong:
	default:
		return f, fmt.Errorf("veredito precisa ser correct, partially_correct ou wrong")
	}
	if f.Author == "" {
		return f, errors.New("--author é obrigatório")
	}
	if f.Verdict != store.VerdictCorrect && f.Cause == "" {
		return f, errors.New("quando o agente errou, diga a causa real com --cause: é isso que ensina o próximo run")
	}
	return f, nil
}

func Format(reports []store.ReportSummary, traceTemplate string) string {
	if len(reports) == 0 {
		return "Nenhum relatório ainda."
	}
	blocks := make([]string, len(reports))
	for i, r := range reports {
		link := "sem trace"
		if r.TraceID != "" {
			if link = telemetry.TraceURL(traceTemplate, r.TraceID); link == "" {
				link = "trace " + r.TraceID
			}
		}
		feedback := string(r.Feedback)
		if feedback == "" {
			feedback = "—"
		}
		blocks[i] = fmt.Sprintf("%s  %s (%s)  %s %d%%  entrega=%s  feedback=%s\n  %s\n  causa: %s\n  %s",
			r.ReportID, r.ExternalID, r.Product, r.Status, int(math.Round(r.Confidence*100)), r.Delivery, feedback, r.Title, r.RootCause, link)
	}
	return strings.Join(blocks, "\n\n")
}

func Run(ctx context.Context, st store.Store, args []string, traceTemplate string, allowAnyID bool) (string, error) {
	if len(args) > 0 && args[0] == "feedback" {
		f, err := ParseFeedbackArgs(args[1:], allowAnyID)
		if err != nil {
			return "", err
		}
		if err := st.RecordFeedback(ctx, store.Feedback{ReportID: f.ReportID, Verdict: f.Verdict, Author: f.Author, ActualCause: f.Cause}); err != nil {
			return "", err
		}
		return "Feedback registrado: " + string(f.Verdict), nil
	}
	reports, err := st.ListRecentReports(ctx, 20)
	if err != nil {
		return "", err
	}
	return Format(reports, traceTemplate), nil
}
