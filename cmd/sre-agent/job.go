package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/job"
)

// runJob: um processo, um incidente. Lê de INCIDENT_JSON ou de um arquivo, investiga e sai.
func runJob(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	raw := []byte(os.Getenv("INCIDENT_JSON"))
	if len(args) > 0 {
		var err error
		if raw, err = os.ReadFile(args[0]); err != nil {
			return err
		}
	}
	if len(raw) == 0 {
		return errors.New("passe o incidente em INCIDENT_JSON ou como caminho de arquivo")
	}
	inc, err := incident.Parse(raw)
	if err != nil {
		return err
	}
	_, err = job.Run(ctx, inc, job.Shared{})
	return err
}
