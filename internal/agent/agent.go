// Package agent é o loop de investigação. O Tool Runner do SDK chama o modelo, executa as tools
// que ele pedir, devolve os resultados e repete até o modelo parar de pedir tools (ou bater o
// limite de voltas). O modelo roda na API; as tools rodam aqui, com as credenciais do job.
package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/integrations"
	"github.com/thallesdaniell/sre-agent/internal/pricing"
	"github.com/thallesdaniell/sre-agent/internal/report"
	"github.com/thallesdaniell/sre-agent/internal/store"
)

const (
	Model         = "claude-opus-5-5"
	maxIterations = 30 // teto de voltas do loop: protege custo e tempo
)

// InvestigationError carrega o código de falha e os tokens gastos até ali: falhar também custa.
type InvestigationError struct {
	Code  store.FailureCode
	Msg   string
	Usage store.Usage
}

func (e *InvestigationError) Error() string { return e.Msg }

type Result struct {
	Report report.RcaReport
	Usage  store.Usage
}

// inbox guarda o relatório entregue. Tools podem rodar em paralelo, então protege com mutex.
type inbox struct {
	mu     sync.Mutex
	report *report.RcaReport
}

func (i *inbox) set(r report.RcaReport) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.report = &r
}

func (i *inbox) get() *report.RcaReport {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.report
}

// submitReportTool é a única tool do núcleo. Se a validação recusar, o erro volta para o modelo
// com os campos a corrigir, e ele chama de novo.
func submitReportTool(box *inbox) anthropic.BetaTool {
	return integrations.NewTool("submit_rca_report",
		"Entrega o relatório final de RCA. Chame UMA vez, ao terminar. Se a validação recusar, corrija os campos apontados e chame de novo.",
		func(_ context.Context, r report.RcaReport) (string, error) {
			if err := r.Validate(); err != nil {
				return "", err
			}
			box.set(r)
			return "Relatório recebido e validado. Encerre a investigação sem chamar outras tools.", nil
		})
}

// addUsage soma os tokens de uma resposta e a estimativa de custo dela. O preço vem do modelo
// que de fato respondeu (com fallback, pode não ser o pedido).
func addUsage(total *store.Usage, msg *anthropic.BetaMessage, first bool) {
	u := msg.Usage
	t := pricing.Tokens{
		Input:        u.InputTokens,
		Output:       u.OutputTokens,
		CacheRead:    u.CacheReadInputTokens,
		CacheWrite5m: u.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1h: u.CacheCreation.Ephemeral1hInputTokens,
	}
	// Respostas sem o detalhamento 5m/1h: trata toda a escrita como 5 minutos (o TTL padrão).
	if t.CacheWrite5m+t.CacheWrite1h == 0 {
		t.CacheWrite5m = u.CacheCreationInputTokens
	}
	total.InputTokens += t.Input
	total.OutputTokens += t.Output
	total.CacheReadTokens += t.CacheRead
	total.CacheWrite5mTokens += t.CacheWrite5m
	total.CacheWrite1hTokens += t.CacheWrite1h

	cost, ok := pricing.Estimate(string(msg.Model), t)
	if first {
		total.Priced = true
	}
	total.Priced = total.Priced && ok
	total.CostUSD += cost
}

// classifyCallError separa a recusa ou falha da API do modelo (sem crédito, chave inválida,
// limite de taxa, indisponibilidade) de um erro nosso.
func classifyCallError(err error, usage store.Usage) *InvestigationError {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return &InvestigationError{
			Code:  store.FailureModelAPIError,
			Msg:   fmt.Sprintf("API do modelo respondeu %d (request %s): %s", apiErr.StatusCode, apiErr.RequestID, apiErr.Message),
			Usage: usage,
		}
	}
	return &InvestigationError{Code: store.FailureUnknown, Msg: fmt.Sprintf("chamada ao modelo falhou: %v", err), Usage: usage}
}

// Investigate roda uma investigação. ctx carrega o span da investigação: os spans do SDK (loop,
// chamadas ao modelo, tools) nascem dentro dele.
func Investigate(ctx context.Context, client anthropic.Client, inc incident.Incident, toolset *integrations.Toolset) (Result, error) {
	box := &inbox{}
	tools := append(slices.Clone(toolset.Tools), submitReportTool(box))

	runner := client.Beta.Messages.NewToolRunner(tools, anthropic.BetaToolRunnerParams{
		BetaMessageNewParams: anthropic.BetaMessageNewParams{
			Model:        Model,
			MaxTokens:    16000,
			Thinking:     anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{}},
			OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortHigh},
			// Se o modelo recusar por política, a API refaz a mesma chamada num modelo de fallback.
			Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
			Fallbacks: anthropic.BetaFallbacksParamOfDefault(),
			System: []anthropic.BetaTextBlockParam{{
				Text:         SystemPrompt(toolset.SourcesSummary),
				CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
			}},
			Messages: []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(IncidentMessage(inc)))},
		},
		MaxIterations: maxIterations,
	},
		// Agrupa no backend de rastreio todas as chamadas desta investigação (gen_ai.conversation.id).
		option.WithOpenTelemetryConversationID(inc.Key()),
	)

	var usage store.Usage
	var last *anthropic.BetaMessage
	for msg, err := range runner.All(ctx) {
		if err != nil {
			return Result{}, classifyCallError(err, usage)
		}
		addUsage(&usage, msg, last == nil)
		last = msg
		if box.get() != nil {
			break // relatório já entregue: não deixa o loop seguir chamando tools
		}
	}

	if r := box.get(); r != nil {
		return Result{Report: *r, Usage: usage}, nil
	}
	switch {
	case last != nil && last.StopReason == anthropic.BetaStopReasonRefusal:
		return Result{}, &InvestigationError{Code: store.FailureRefused, Msg: "modelo recusou: " + string(last.StopDetails.Category), Usage: usage}
	case last != nil && last.StopReason == anthropic.BetaStopReasonToolUse:
		return Result{}, &InvestigationError{Code: store.FailureMaxIterations, Msg: fmt.Sprintf("atingiu %d voltas sem entregar relatório", maxIterations), Usage: usage}
	default:
		return Result{}, &InvestigationError{Code: store.FailureNoReport, Msg: "o agente terminou sem chamar submit_rca_report", Usage: usage}
	}
}
