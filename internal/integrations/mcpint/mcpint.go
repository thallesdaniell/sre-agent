// Package mcpint conecta qualquer servidor MCP (Datadog, Argo CD, Sentry...) declarado só por
// config. A allowlist é obrigatória: servidores prontos costumam expor tools de escrita
// (silenciar monitor, sincronizar app, reiniciar pipeline); só entram as listadas.
package mcpint

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/thallesdaniell/sre-agent/internal/config"
	"github.com/thallesdaniell/sre-agent/internal/integrations"
)

// Session é o que usamos de uma conexão MCP. Testes implementam com um servidor falso.
type Session interface {
	ListTools(ctx context.Context, params *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
	Close() error
}

type Connector func(ctx context.Context, name string, cfg config.IntegrationConfig, getenv func(string) string) (Session, error)

// resolveEnvRefs troca nomes de variável pelos valores, na hora de conectar.
func resolveEnvRefs(server string, refs map[string]string, getenv func(string) string) (map[string]string, error) {
	out := map[string]string{}
	for key, varName := range refs {
		v := getenv(varName)
		if v == "" {
			return nil, fmt.Errorf("integração %s: variável %s não definida", server, varName)
		}
		out[key] = v
	}
	return out, nil
}

type headerTransport struct {
	headers map[string]string
	base    http.RoundTripper
}

func (t headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	return t.base.RoundTrip(r)
}

// Connect é o conector real: HTTP (streamable) ou stdio (processo local).
func Connect(ctx context.Context, name string, cfg config.IntegrationConfig, getenv func(string) string) (Session, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "sre-agent:" + name, Version: "0.1.0"}, nil)
	var transport mcp.Transport
	switch cfg.Transport.Kind {
	case config.TransportHTTP:
		headers, err := resolveEnvRefs(name, cfg.Transport.HeadersFromEnv, getenv)
		if err != nil {
			return nil, err
		}
		transport = &mcp.StreamableClientTransport{
			Endpoint:   cfg.Transport.URL,
			HTTPClient: &http.Client{Transport: headerTransport{headers: headers, base: http.DefaultTransport}},
		}
	case config.TransportStdio:
		vars, err := resolveEnvRefs(name, cfg.Transport.EnvFromEnv, getenv)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(cfg.Transport.Command, cfg.Transport.Args...)
		for k, v := range vars {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	default:
		return nil, fmt.Errorf("integração %s: transporte desconhecido %q", name, cfg.Transport.Kind)
	}
	return client.Connect(ctx, transport, nil)
}

// SelectAllowed filtra pela allowlist e falha se ela citar uma tool que o servidor não tem.
func SelectAllowed(name string, available []*mcp.Tool, allow []string) ([]*mcp.Tool, error) {
	var missing []string
	for _, want := range allow {
		if !slices.ContainsFunc(available, func(t *mcp.Tool) bool { return t.Name == want }) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("integração %s: tools da allowlist não existem no servidor: %s", name, strings.Join(missing, ", "))
	}
	var out []*mcp.Tool
	for _, t := range available {
		if slices.Contains(allow, t.Name) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Integration cria a integração a partir da config. connector nil = conector real.
func Integration(name string, cfg config.IntegrationConfig, connector Connector) integrations.Integration {
	if connector == nil {
		connector = Connect
	}
	cats := make([]integrations.Category, len(cfg.Categories))
	for i, c := range cfg.Categories {
		cats[i] = integrations.Category(c)
	}
	return integrations.Integration{
		Name:       name,
		Categories: cats,
		Purpose:    cfg.Purpose,
		Connect: func(ctx context.Context, deps integrations.Deps) (integrations.Connected, error) {
			session, err := connector(ctx, name, cfg, deps.Getenv)
			if err != nil {
				return integrations.Connected{}, err
			}
			list, err := session.ListTools(ctx, nil)
			if err != nil {
				_ = session.Close()
				return integrations.Connected{}, err
			}
			allowed, err := SelectAllowed(name, list.Tools, cfg.AllowTools)
			if err != nil {
				_ = session.Close()
				return integrations.Connected{}, err
			}
			tools := make([]anthropic.BetaTool, 0, len(allowed))
			for _, t := range allowed {
				tools = append(tools, remoteTool{session: session, tool: t})
			}
			return integrations.Connected{Tools: tools, Close: session.Close}, nil
		},
	}
}

// remoteTool adapta uma tool MCP para a interface de tool do Tool Runner.
type remoteTool struct {
	session Session
	tool    *mcp.Tool
}

func (r remoteTool) Name() string        { return r.tool.Name }
func (r remoteTool) Description() string { return r.tool.Description }

func (r remoteTool) InputSchema() anthropic.BetaToolInputSchemaParam {
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if raw, err := json.Marshal(r.tool.InputSchema); err == nil {
		_ = json.Unmarshal(raw, &schema)
	}
	if schema.Properties == nil {
		schema.Properties = map[string]any{}
	}
	return anthropic.BetaToolInputSchemaParam{Properties: schema.Properties, Required: schema.Required}
}

func (r remoteTool) Execute(ctx context.Context, input json.RawMessage) ([]anthropic.BetaToolResultBlockParamContentUnion, error) {
	var args map[string]any
	if len(input) > 0 {
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, fmt.Errorf("argumentos inválidos: %w", err)
		}
	}
	res, err := r.session.CallTool(ctx, &mcp.CallToolParams{Name: r.tool.Name, Arguments: args})
	if err != nil {
		return nil, err
	}
	var texts []string
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, t.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if res.IsError {
		return nil, fmt.Errorf("%s", text)
	}
	return []anthropic.BetaToolResultBlockParamContentUnion{{OfText: &anthropic.BetaTextBlockParam{Text: text}}}, nil
}
