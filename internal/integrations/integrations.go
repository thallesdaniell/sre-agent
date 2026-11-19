// Package integrations define o contrato de toda fonte de evidência, nativa ou MCP, e o registro
// que as conecta. Adicionar uma fonte nova = implementar Integration (ou só declarar um servidor
// MCP no agent.config.json) e registrar. O resto do agente não muda.
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/toolrunner"

	"github.com/thallesdaniell/sre-agent/internal/scope"
	"github.com/thallesdaniell/sre-agent/internal/store"
)

// Category é o tipo de evidência. O prompt agrupa as fontes por categoria para o agente saber
// onde procurar cada coisa, e principalmente onde procurar MUDANÇAS: agentes de RCA tendem a
// parar no sintoma e nunca atribuir a causa a um deploy ou config.
type Category string

const (
	Symptoms Category = "symptoms"
	Changes  Category = "changes"
	Errors   Category = "errors"
	Context  Category = "context"
)

// Categories em ordem fixa, com o texto que vai para o prompt.
var Categories = []struct {
	ID    Category
	Label string
}{
	{Symptoms, "Sintomas (estado, saúde, métricas, logs)"},
	{Changes, "Mudanças (deploys, rollouts, PRs, alterações de config)"},
	{Errors, "Erros de aplicação (exceções, stack traces, contagens)"},
	{Context, "Contexto (incidentes anteriores, conversa do incidente, documentação)"},
}

// Deps é o que uma integração recebe para se conectar.
type Deps struct {
	Scopes            *scope.Registry
	Store             store.Store
	CurrentIncidentID string // para o histórico não devolver o próprio incidente
	Getenv            func(string) string
}

type Connected struct {
	Tools []anthropic.BetaTool
	Close func() error
}

type Integration struct {
	Name       string
	Categories []Category
	Purpose    string // uma frase que vai para o prompt: o que esta fonte responde
	Connect    func(ctx context.Context, deps Deps) (Connected, error)
}

// NewTool cria uma tool tipada: o schema vem das tags jsonschema da struct T e a saída é texto.
// Erro devolvido pela função vira resultado com is_error, para o modelo corrigir o pedido.
func NewTool[T any](name, description string, fn func(ctx context.Context, in T) (string, error)) anthropic.BetaTool {
	tool, err := toolrunner.NewBetaToolFromJSONSchema(name, description,
		func(ctx context.Context, in T) (anthropic.BetaToolResultBlockParamContentUnion, error) {
			text, err := fn(ctx, in)
			if err != nil {
				return anthropic.BetaToolResultBlockParamContentUnion{}, err
			}
			return anthropic.BetaToolResultBlockParamContentUnion{OfText: &anthropic.BetaTextBlockParam{Text: text}}, nil
		})
	if err != nil {
		// Só acontece com struct mal anotada: erro de programação, não de runtime.
		panic(fmt.Sprintf("tool %s: schema inválido: %v", name, err))
	}
	return tool
}

// ---- limites ----

// Limites de toda tool de evidência. Duas razões: segurança (o agente não escolhe janelas ou
// volumes arbitrários) e contexto (saída grande demais enterra a evidência útil e custa tokens).
const (
	MaxWindowMinutes     = 6 * 60
	MaxLogLines          = 400
	MaxRecentChangesHour = 72
	MaxListItems         = 100
	MaxOutputChars       = 20_000
)

func Truncate(text string) string {
	if len(text) <= MaxOutputChars {
		return text
	}
	cut := text[:MaxOutputChars]
	for len(cut) > 0 && !utf8.ValidString(cut) { // não corta um caractere UTF-8 no meio
		cut = cut[:len(cut)-1]
	}
	return fmt.Sprintf("%s\n[saída truncada: %d bytes omitidos; refine a consulta]", cut, len(text)-len(cut))
}

// JSON serializa o resultado de uma tool de forma compacta e já truncada.
func JSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return Truncate(string(b)), nil
}

// Clamp devolve o valor dentro dos limites, usando def quando não informado (zero).
func Clamp(v, def, minV, maxV int) int {
	if v == 0 {
		return def
	}
	return max(minV, min(v, maxV))
}
