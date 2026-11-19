package mcpint_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thallesdaniell/sre-agent/internal/config"
	"github.com/thallesdaniell/sre-agent/internal/integrations"
	"github.com/thallesdaniell/sre-agent/internal/integrations/mcpint"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

// fakeSession é um servidor MCP falso: uma tool de leitura e uma de escrita.
type fakeSession struct {
	output string
	called []string
}

func (f *fakeSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}
	return &mcp.ListToolsResult{Tools: []*mcp.Tool{
		{Name: "query_metrics", Description: "lê métricas", InputSchema: schema},
		{Name: "mute_monitor", Description: "silencia um monitor", InputSchema: schema},
	}}, nil
}

func (f *fakeSession) CallTool(_ context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	f.called = append(f.called, p.Name)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: f.output}}}, nil
}

func (f *fakeSession) Close() error { return nil }

var cfg = config.IntegrationConfig{
	Type: config.IntegrationMCP, Purpose: "métricas do serviço", Categories: []string{"symptoms"},
	Transport: config.MCPTransport{Kind: config.TransportHTTP, URL: "https://mcp.example.com/mcp"}, AllowTools: []string{"query_metrics"},
}

func connect(t *testing.T, session *fakeSession, c config.IntegrationConfig) (*integrations.Toolset, error) {
	connector := func(context.Context, string, config.IntegrationConfig, func(string) string) (mcpint.Session, error) {
		return session, nil
	}
	return integrations.ConnectAll(t.Context(), []integrations.Integration{mcpint.Integration("metrics", c, connector)},
		integrations.Deps{Scopes: testfixtures.Scopes(), Store: memstore.New(), Getenv: func(string) string { return "" }})
}

func TestSoExpoeToolsDaAllowlist(t *testing.T) {
	ts, err := connect(t, &fakeSession{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts.Tools) != 1 || ts.Tools[0].Name() != "query_metrics" {
		t.Fatalf("tools: %v", ts.Tools)
	}
	if !strings.Contains(string(mustJSON(ts.Tools[0].InputSchema())), "query") {
		t.Fatal("schema do servidor MCP não foi repassado")
	}
}

func TestFalhaSeAllowlistCitaToolInexistente(t *testing.T) {
	c := cfg
	c.AllowTools = []string{"query_metrics", "nao_existe"}
	if _, err := connect(t, &fakeSession{}, c); err == nil || !strings.Contains(err.Error(), "nao_existe") {
		t.Fatalf("esperava erro, veio %v", err)
	}
}

func TestTetoDeSaidaVaileTambemParaMCP(t *testing.T) {
	session := &fakeSession{output: strings.Repeat("a", 50_000)}
	ts, _ := connect(t, session, cfg)
	blocks, err := ts.Tools[0].Execute(t.Context(), json.RawMessage(`{"query":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(blocks[0].OfText.Text, "saída truncada") || session.called[0] != "query_metrics" {
		t.Fatal("não truncou ou não chamou a tool certa")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
