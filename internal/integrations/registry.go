package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/thallesdaniell/sre-agent/internal/config"
)

// NativeFactory cria uma integração nativa. MCPFactory cria uma a partir de um servidor MCP.
type (
	NativeFactory func() Integration
	MCPFactory    func(name string, cfg config.IntegrationConfig) Integration
)

// Resolve transforma o agent.config.json em integrações, em ordem alfabética: a ordem das tools
// precisa ser estável para o cache de prompt funcionar.
func Resolve(cfg config.AgentConfig, natives map[string]NativeFactory, mcp MCPFactory) ([]Integration, error) {
	names := make([]string, 0, len(cfg.Integrations))
	for name := range cfg.Integrations {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []Integration
	for _, name := range names {
		ic := cfg.Integrations[name]
		if !ic.IsEnabled() {
			continue
		}
		if ic.Type == config.IntegrationMCP {
			out = append(out, mcp(name, ic))
			continue
		}
		factory, ok := natives[name]
		if !ok {
			known := make([]string, 0, len(natives))
			for k := range natives {
				known = append(known, k)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("integração nativa desconhecida: %s. Conhecidas: %s", name, strings.Join(known, ", "))
		}
		out = append(out, factory())
	}
	return out, nil
}

// Toolset é o conjunto pronto para o agente.
type Toolset struct {
	Tools          []anthropic.BetaTool
	SourcesSummary string // fontes agrupadas por categoria, para o prompt
	closers        []func() error
}

func (t *Toolset) Close() error {
	var errs []error
	for _, c := range t.closers {
		errs = append(errs, c())
	}
	return errors.Join(errs...)
}

// ConnectAll conecta as integrações. Toda tool, nativa ou MCP, passa pelo mesmo teto de saída.
// Nome de tool repetido entre integrações é erro: o modelo não teria como escolher.
func ConnectAll(ctx context.Context, integrations []Integration, deps Deps) (*Toolset, error) {
	ts := &Toolset{}
	owner := map[string]string{}
	toolsOf := map[string][]string{}

	for _, in := range integrations {
		conn, err := in.Connect(ctx, deps)
		if err != nil {
			_ = ts.Close()
			return nil, fmt.Errorf("integração %s: %w", in.Name, err)
		}
		if conn.Close != nil {
			ts.closers = append(ts.closers, conn.Close)
		}
		for _, tool := range conn.Tools {
			if prev, dup := owner[tool.Name()]; dup {
				_ = ts.Close()
				return nil, fmt.Errorf("tool %s existe em %s e %s: renomeie ou tire da allowlist", tool.Name(), prev, in.Name)
			}
			owner[tool.Name()] = in.Name
			toolsOf[in.Name] = append(toolsOf[in.Name], tool.Name())
			ts.Tools = append(ts.Tools, Limit(tool))
		}
	}
	ts.SourcesSummary = summarize(integrations, toolsOf)
	return ts, nil
}

func summarize(integrations []Integration, toolsOf map[string][]string) string {
	var b strings.Builder
	for i, cat := range Categories {
		if i > 0 {
			b.WriteString("\n")
		}
		var lines []string
		for _, in := range integrations {
			if slices.Contains(in.Categories, cat.ID) {
				lines = append(lines, fmt.Sprintf("  - %s: %s (tools: %s)", in.Name, in.Purpose, strings.Join(toolsOf[in.Name], ", ")))
			}
		}
		if len(lines) == 0 {
			fmt.Fprintf(&b, "%s: nenhuma fonte disponível", cat.Label)
			continue
		}
		fmt.Fprintf(&b, "%s:\n%s", cat.Label, strings.Join(lines, "\n"))
	}
	return b.String()
}

// limited aplica o teto de saída a qualquer tool. Servidores MCP não conhecem nossos limites.
type limited struct{ anthropic.BetaTool }

func Limit(t anthropic.BetaTool) anthropic.BetaTool { return limited{t} }

func (l limited) Execute(ctx context.Context, input json.RawMessage) ([]anthropic.BetaToolResultBlockParamContentUnion, error) {
	blocks, err := l.BetaTool.Execute(ctx, input)
	for _, block := range blocks {
		if block.OfText != nil {
			block.OfText.Text = Truncate(block.OfText.Text)
		}
	}
	return blocks, err
}
