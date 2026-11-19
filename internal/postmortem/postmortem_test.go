package postmortem_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/thallesdaniell/sre-agent/internal/postmortem"
)

const markdown = `---
title: Checkout fora do ar por limite de memória
product: checkout
incident: INC-0981
occurredAt: 2026-08-14
rootCause: Limite de memória reduzido
tags: oom, deploy
---
## Resumo
Pods reiniciando com OOMKilled.`

func TestLeCabecalhoEUsaNomeDoArquivoComoSlug(t *testing.T) {
	pm, err := postmortem.Parse("2026-08-Checkout_OOM.md", markdown)
	if err != nil {
		t.Fatal(err)
	}
	if pm.Slug != "2026-08-checkout-oom" || pm.IncidentRef != "INC-0981" || !slices.Equal(pm.Tags, []string{"oom", "deploy"}) {
		t.Fatalf("parse errado: %+v", pm)
	}
	if !strings.Contains(pm.Body, "OOMKilled") {
		t.Fatal("corpo perdido")
	}
}

func TestRecusaArquivoSemCabecalho(t *testing.T) {
	if _, err := postmortem.Parse("x.md", "# só corpo"); err == nil || !strings.Contains(err.Error(), "cabeçalho") {
		t.Fatalf("esperava erro de cabeçalho, veio %v", err)
	}
}
