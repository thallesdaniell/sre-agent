// Package history é a memória do time: post-mortems escritos por pessoas e incidentes anteriores
// com o que o agente concluiu e o que o time respondeu. Lê só do nosso Postgres.
package history

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/thallesdaniell/sre-agent/internal/integrations"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9-]{1,120}$`)

func Integration() integrations.Integration {
	return integrations.Integration{
		Name:       "history",
		Categories: []integrations.Category{integrations.Context},
		Purpose:    "post-mortems anteriores e incidentes passados do mesmo produto, com o feedback do time",
		Connect: func(_ context.Context, deps integrations.Deps) (integrations.Connected, error) {
			products := "Produto do inventário: " + strings.Join(deps.Scopes.Products(), ", ")

			type searchIn struct {
				Query   string `json:"query" jsonschema:"required,minLength=2,maxLength=200"`
				Product string `json:"product,omitempty"`
				Limit   int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=10,description=Quantidade (padrão 5)"`
			}
			search := integrations.NewTool("search_postmortems",
				`Busca textual em post-mortems (português). Use termos do sintoma ou do componente, ex.: "OOMKilled checkout" ou "timeout banco". Ajuda a reconhecer um padrão que já aconteceu. `+products,
				func(ctx context.Context, in searchIn) (string, error) {
					if len(in.Query) < 2 || len(in.Query) > 200 {
						return "", fmt.Errorf("query precisa ter entre 2 e 200 caracteres")
					}
					if in.Product != "" {
						if _, err := deps.Scopes.Get(in.Product); err != nil {
							return "", err
						}
					}
					hits, err := deps.Store.SearchPostmortems(ctx, in.Query, in.Product, integrations.Clamp(in.Limit, 5, 1, 10))
					if err != nil {
						return "", err
					}
					if len(hits) == 0 {
						return "Nenhum post-mortem encontrado para esses termos.", nil
					}
					return integrations.JSON(hits)
				})

			type getIn struct {
				Slug string `json:"slug" jsonschema:"required,description=Slug retornado por search_postmortems"`
			}
			get := integrations.NewTool("get_postmortem",
				"Lê um post-mortem completo pelo slug retornado em search_postmortems.",
				func(ctx context.Context, in getIn) (string, error) {
					if !slugPattern.MatchString(in.Slug) {
						return "", fmt.Errorf("slug inválido: %q", in.Slug)
					}
					pm, err := deps.Store.GetPostmortem(ctx, in.Slug)
					if err != nil {
						return "", err
					}
					if pm == nil {
						return "", fmt.Errorf("post-mortem não encontrado: %s", in.Slug)
					}
					return integrations.JSON(pm)
				})

			type pastIn struct {
				Product string `json:"product" jsonschema:"required"`
				Limit   int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=20,description=Quantidade (padrão 10)"`
			}
			past := integrations.NewTool("past_incidents",
				"Incidentes anteriores do produto com a conclusão do agente, o veredito do time (correct, partially_correct, wrong) e a causa real quando o agente errou. Use para não repetir um erro já apontado. "+products,
				func(ctx context.Context, in pastIn) (string, error) {
					if _, err := deps.Scopes.Get(in.Product); err != nil {
						return "", err
					}
					incidents, err := deps.Store.RecentIncidents(ctx, in.Product, deps.CurrentIncidentID, integrations.Clamp(in.Limit, 10, 1, 20))
					if err != nil {
						return "", err
					}
					if len(incidents) == 0 {
						return "Nenhum incidente anterior registrado para este produto.", nil
					}
					return integrations.JSON(incidents)
				})

			return integrations.Connected{Tools: []anthropic.BetaTool{search, get, past}}, nil
		},
	}
}
