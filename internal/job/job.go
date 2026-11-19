// Package job: um processo, um incidente. Todo desfecho fica registrado no banco e no trace: o run
// sempre termina SUCCEEDED ou FAILED com motivo, e o relatório é salvo ANTES de publicar, para
// não se perder se o Slack falhar.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/thallesdaniell/sre-agent/internal/agent"
	"github.com/thallesdaniell/sre-agent/internal/config"
	"github.com/thallesdaniell/sre-agent/internal/db"
	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/integrations"
	"github.com/thallesdaniell/sre-agent/internal/integrations/github"
	"github.com/thallesdaniell/sre-agent/internal/integrations/history"
	"github.com/thallesdaniell/sre-agent/internal/integrations/kubernetes"
	"github.com/thallesdaniell/sre-agent/internal/integrations/mcpint"
	"github.com/thallesdaniell/sre-agent/internal/report"
	"github.com/thallesdaniell/sre-agent/internal/slackpub"
	"github.com/thallesdaniell/sre-agent/internal/store"
	"github.com/thallesdaniell/sre-agent/internal/telemetry"
)

// Deps são as peças trocáveis: em produção as reais, nos testes as falsas.
type Deps struct {
	Store       store.Store
	Connect     func(ctx context.Context) (*integrations.Toolset, error)
	Investigate func(ctx context.Context, inc incident.Incident, ts *integrations.Toolset) (agent.Result, error)
	Publish     func(ctx context.Context, r report.RcaReport, traceURL string) (store.Delivery, error)
	PublishMode config.PublishMode
	Tracer      trace.Tracer
	ContentMode option.OpenTelemetryContentMode
	TraceURLFor func(traceID string) string
}

type Result struct {
	Skipped  bool // outra investigação do mesmo incidente já está rodando
	RunID    store.RunID
	Delivery store.DeliveryKind
	TraceID  string
}

// IntegrationError: falha ao conectar uma integração (credencial, rede, allowlist).
type IntegrationError struct{ Err error }

func (e *IntegrationError) Error() string { return e.Err.Error() }
func (e *IntegrationError) Unwrap() error { return e.Err }

// rootAttributes: os langfuse.* viram nome, sessão, tags e metadados do trace no Langfuse; em
// outros backends aparecem como atributos comuns, sem prejuízo.
func rootAttributes(inc incident.Incident, mode config.PublishMode) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("langfuse.trace.name", "rca-investigation"),
		attribute.String("langfuse.session.id", inc.Key()), // agrupa as tentativas do mesmo incidente
		attribute.StringSlice("langfuse.trace.tags", []string{inc.Product, string(inc.Severity), string(mode)}),
		attribute.String("langfuse.trace.metadata.product", inc.Product),
		attribute.String("langfuse.trace.metadata.severity", string(inc.Severity)),
		attribute.String("langfuse.trace.metadata.publish_mode", string(mode)),
		attribute.String("incident.id", inc.ID),
		attribute.String("incident.source", inc.Source),
		attribute.String("incident.product", inc.Product),
	}
}

func Execute(ctx context.Context, inc incident.Incident, d Deps) (Result, error) {
	ctx, span := d.Tracer.Start(ctx, "rca.investigation", trace.WithAttributes(rootAttributes(inc, d.PublishMode)...))
	defer span.End()

	res, err := investigateAndRecord(ctx, inc, d, span)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return res, err
	}
	span.SetStatus(codes.Ok, "")
	return res, nil
}

func investigateAndRecord(ctx context.Context, inc incident.Incident, d Deps, span trace.Span) (Result, error) {
	traceID := ""
	if sc := span.SpanContext(); sc.IsValid() {
		traceID = sc.TraceID().String()
	}

	claim, err := d.Store.ClaimCase(ctx, inc)
	if err != nil {
		return Result{}, err
	}
	start, err := d.Store.StartRun(ctx, claim.CaseID, agent.Model, traceID)
	if err != nil {
		return Result{}, err
	}
	if start.AlreadyRunning {
		span.SetAttributes(attribute.String("rca.outcome", "skipped_already_running"))
		return Result{Skipped: true}, nil
	}
	runID := start.RunID
	span.SetAttributes(attribute.String("langfuse.trace.metadata.run_id", string(runID)),
		attribute.String("langfuse.trace.metadata.case_id", string(claim.CaseID)))
	if d.ContentMode == option.OpenTelemetryContentModeContent {
		span.SetAttributes(attribute.String("langfuse.observation.input", mustJSON(inc)))
	}

	fail := func(code store.FailureCode, usage store.Usage, cause error) (Result, error) {
		span.SetAttributes(attribute.String("rca.outcome", "failed"), attribute.String("rca.failure_code", string(code)))
		if err := d.Store.FinishRun(context.WithoutCancel(ctx), runID, store.RunOutcome{Failure: code, Detail: cause.Error(), Usage: usage}); err != nil {
			return Result{}, errors.Join(cause, err)
		}
		return Result{RunID: runID, TraceID: traceID}, cause
	}

	toolset, err := d.Connect(ctx)
	if err != nil {
		return fail(store.FailureIntegrationError, store.Usage{}, &IntegrationError{err})
	}
	result, err := d.Investigate(ctx, inc, toolset)
	_ = toolset.Close()
	if err != nil {
		var ie *agent.InvestigationError
		if errors.As(err, &ie) {
			return fail(ie.Code, ie.Usage, err)
		}
		return fail(store.FailureUnknown, store.Usage{}, err)
	}

	r := result.Report
	span.SetAttributes(
		attribute.String("rca.outcome", "succeeded"),
		attribute.String("rca.status", string(r.Status)),
		attribute.Float64("rca.confidence", r.RootCause.Confidence),
		attribute.String("rca.change_kind", string(r.ChangeAttribution.Kind)),
	)
	if d.ContentMode == option.OpenTelemetryContentModeContent {
		span.SetAttributes(attribute.String("langfuse.observation.output", mustJSON(r)))
	}

	reportID, err := d.Store.SaveReport(ctx, runID, claim.CaseID, r, inc.SlackChannel)
	if err != nil {
		return fail(store.FailureUnknown, result.Usage, err)
	}

	traceURL := ""
	if d.TraceURLFor != nil {
		traceURL = d.TraceURLFor(traceID)
	}
	delivery, err := d.Publish(ctx, r, traceURL)
	if err != nil {
		return fail(store.FailurePublishError, result.Usage, err)
	}
	if err := d.Store.MarkDelivered(ctx, reportID, delivery); err != nil {
		return Result{}, err
	}
	span.SetAttributes(attribute.String("rca.delivery", string(delivery.Kind)))

	if err := d.Store.FinishRun(ctx, runID, store.RunOutcome{Usage: result.Usage}); err != nil {
		return Result{}, err
	}
	return Result{RunID: runID, Delivery: delivery.Kind, TraceID: traceID}, nil
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Shared permite que o intake em modo local reaproveite store e telemetria.
type Shared struct {
	Store     store.Store
	Telemetry *telemetry.Telemetry
}

// Natives são as integrações nativas que este binário conhece.
func Natives(cfg config.Job) map[string]integrations.NativeFactory {
	return map[string]integrations.NativeFactory{
		"kubernetes": func() integrations.Integration { return kubernetes.Integration(nil) },
		"github": func() integrations.Integration {
			return github.Integration(github.Client{BaseURL: cfg.GitHubAPIURL, Token: cfg.GitHubToken})
		},
		"history": history.Integration,
	}
}

// Run monta as dependências reais a partir do ambiente e executa.
func Run(ctx context.Context, inc incident.Incident, shared Shared) (Result, error) {
	cfg, err := config.LoadJob()
	if err != nil {
		return Result{}, err
	}
	agentCfg, scopes, err := config.LoadAgentConfig(cfg.AgentConfigFile)
	if err != nil {
		return Result{}, err
	}
	if _, err := scopes.Get(inc.Product); err != nil { // recusa cedo se o produto não está no inventário
		return Result{}, err
	}

	// Registrado antes da primeira chamada ao Claude, senão os spans do SDK não nascem dentro do nosso.
	tel := shared.Telemetry
	if tel == nil {
		if tel, err = telemetry.Init(ctx, "sre-agent-job", os.Getenv, nil); err != nil {
			return Result{}, err
		}
		defer tel.Shutdown(context.WithoutCancel(ctx)) //nolint:errcheck // exporta os spans pendentes
	}

	st := shared.Store
	if st == nil {
		pool, err := db.Open(ctx, cfg.DatabaseURL, 2)
		if err != nil {
			return Result{}, err
		}
		pg := store.NewPG(pool)
		defer pg.Close()
		st = pg
	}

	client := anthropic.NewClient(
		option.WithTracerProvider(tel.Provider),
		option.WithOpenTelemetryTracesContentMode(tel.ContentMode),
	)
	resolved, err := integrations.Resolve(agentCfg, Natives(cfg), func(name string, ic config.IntegrationConfig) integrations.Integration {
		return mcpint.Integration(name, ic, nil)
	})
	if err != nil {
		return Result{}, err
	}

	started := time.Now()
	res, err := Execute(ctx, inc, Deps{
		Store: st,
		Connect: func(ctx context.Context) (*integrations.Toolset, error) {
			return integrations.ConnectAll(ctx, resolved, integrations.Deps{Scopes: scopes, Store: st, CurrentIncidentID: inc.ID, Getenv: os.Getenv})
		},
		Investigate: func(ctx context.Context, inc incident.Incident, ts *integrations.Toolset) (agent.Result, error) {
			return agent.Investigate(ctx, client, inc, ts)
		},
		Publish: func(ctx context.Context, r report.RcaReport, traceURL string) (store.Delivery, error) {
			return slackpub.Publish(ctx, r, slackpub.Options{Mode: cfg.PublishMode, Channel: inc.SlackChannel, Token: cfg.SlackBotToken, TraceURL: traceURL})
		},
		PublishMode: cfg.PublishMode,
		Tracer:      tel.Tracer,
		ContentMode: tel.ContentMode,
		TraceURLFor: func(id string) string { return telemetry.TraceURL(cfg.TraceURLTemplate, id) },
	})
	slog.Info("rca.finished", "incident", inc.ID, "skipped", res.Skipped, "run", res.RunID, "delivery", res.Delivery,
		"trace", res.TraceID, "duration_ms", time.Since(started).Milliseconds(), "error", errString(err))
	return res, err
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err)
}
