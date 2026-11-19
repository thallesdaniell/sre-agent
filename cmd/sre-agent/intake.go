package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/thallesdaniell/sre-agent/internal/config"
	"github.com/thallesdaniell/sre-agent/internal/db"
	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/intake"
	"github.com/thallesdaniell/sre-agent/internal/integrations/kubernetes"
	"github.com/thallesdaniell/sre-agent/internal/job"
	"github.com/thallesdaniell/sre-agent/internal/store"
	"github.com/thallesdaniell/sre-agent/internal/telemetry"
)

// localDispatcher (dev): roda a investigação no próprio processo, sem esperar (responde 202 na hora).
type localDispatcher struct {
	ctx    context.Context
	shared job.Shared
}

func (l localDispatcher) Dispatch(_ context.Context, inc incident.Incident) (intake.DispatchResult, error) {
	ref := intake.JobName(inc.ID)
	go func() {
		if _, err := job.Run(l.ctx, inc, l.shared); err != nil {
			slog.Error("investigação falhou", "ref", ref, "error", err)
		}
	}()
	return intake.DispatchResult{Ref: ref}, nil
}

// runIntake: o único processo sempre ligado. Recebe o webhook e dispara a investigação.
func runIntake([]string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadIntake()
	if err != nil {
		return err
	}
	_, scopes, err := config.LoadAgentConfig(cfg.AgentConfigFile)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, 5)
	if err != nil {
		return err
	}
	st := store.NewPG(pool)
	defer st.Close()
	tel, err := telemetry.Init(ctx, "sre-agent-intake", os.Getenv, nil)
	if err != nil {
		return err
	}
	defer tel.Shutdown(context.WithoutCancel(ctx)) //nolint:errcheck

	var dispatcher intake.Dispatcher
	switch cfg.DispatchMode {
	case config.DispatchLocal:
		dispatcher = localDispatcher{ctx: ctx, shared: job.Shared{Store: st, Telemetry: tel}}
	case config.DispatchKubernetes:
		client, err := kubernetes.NewClient()
		if err != nil {
			return err
		}
		dispatcher = intake.KubernetesDispatcher{Client: client, Namespace: cfg.JobNamespace, Image: cfg.JobImage,
			ServiceAccount: cfg.JobServiceAccount, SecretName: cfg.JobSecretName, Env: intake.ForwardedEnv(os.Getenv)}
	}

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           (&intake.Server{Secret: cfg.WebhookSecret, Scopes: scopes, Store: st, Dispatcher: dispatcher}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("intake ouvindo", "port", cfg.Port, "dispatch", cfg.DispatchMode, "publish", cfg.PublishMode)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
