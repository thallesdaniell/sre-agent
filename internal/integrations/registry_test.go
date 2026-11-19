package integrations_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/thallesdaniell/sre-agent/internal/config"
	"github.com/thallesdaniell/sre-agent/internal/integrations"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

func deps() integrations.Deps {
	return integrations.Deps{Scopes: testfixtures.Scopes(), Store: memstore.New(), Getenv: func(string) string { return "" }}
}

type echoIn struct {
	Text string `json:"text"`
}

func native(name, tool string, output string) integrations.Integration {
	return integrations.Integration{
		Name: name, Categories: []integrations.Category{integrations.Changes}, Purpose: "teste",
		Connect: func(context.Context, integrations.Deps) (integrations.Connected, error) {
			t := integrations.NewTool(tool, "teste", func(context.Context, echoIn) (string, error) { return output, nil })
			return integrations.Connected{Tools: []anthropic.BetaTool{t}}, nil
		},
	}
}

func TestRecusaToolDeMesmoNomeEmDuasIntegracoes(t *testing.T) {
	_, err := integrations.ConnectAll(t.Context(), []integrations.Integration{native("a", "recent_changes", ""), native("b", "recent_changes", "")}, deps())
	if err == nil || !strings.Contains(err.Error(), "existe em a e b") {
		t.Fatalf("esperava conflito, veio %v", err)
	}
}

func TestResumeFontesPorCategoriaEApontaCategoriaSemFonte(t *testing.T) {
	ts, err := integrations.ConnectAll(t.Context(), []integrations.Integration{native("github", "recent_changes", "")}, deps())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ts.SourcesSummary, "github: teste (tools: recent_changes)") {
		t.Fatal(ts.SourcesSummary)
	}
	if !strings.Contains(ts.SourcesSummary, "Erros de aplicação (exceções, stack traces, contagens): nenhuma fonte disponível") {
		t.Fatal(ts.SourcesSummary)
	}
}

func TestAplicaTetoDeSaidaEmTodaTool(t *testing.T) {
	ts, _ := integrations.ConnectAll(t.Context(), []integrations.Integration{native("x", "big", strings.Repeat("a", 50_000))}, deps())
	blocks, err := ts.Tools[0].Execute(t.Context(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	text := blocks[0].OfText.Text
	if !strings.Contains(text, "saída truncada") || len(text) > 21_000 {
		t.Fatalf("não truncou: %d bytes", len(text))
	}
}

func TestResolveRecusaNativaDesconhecidaEIgnoraDesligadas(t *testing.T) {
	off := false
	cfg := config.AgentConfig{Integrations: map[string]config.IntegrationConfig{
		"mongo":      {Type: config.IntegrationNative},
		"kubernetes": {Type: config.IntegrationNative, Enabled: &off},
	}}
	natives := map[string]integrations.NativeFactory{"kubernetes": func() integrations.Integration { return native("kubernetes", "x", "") }}
	if _, err := integrations.Resolve(cfg, natives, nil); err == nil || !strings.Contains(err.Error(), "desconhecida: mongo") {
		t.Fatalf("esperava recusa de mongo, veio %v", err)
	}
	delete(cfg.Integrations, "mongo")
	got, err := integrations.Resolve(cfg, natives, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("desligada deveria sumir: %v %v", got, err)
	}
}

func TestToolTipadaGeraSchemaDaStruct(t *testing.T) {
	type in struct {
		Product string `json:"product" jsonschema:"required,description=Produto"`
	}
	tool := integrations.NewTool("x", "y", func(context.Context, in) (string, error) { return "", nil })
	schema, _ := json.Marshal(tool.InputSchema())
	if !strings.Contains(string(schema), `"product"`) || !strings.Contains(string(schema), `"required":["product"]`) {
		t.Fatalf("schema: %s", schema)
	}
}
