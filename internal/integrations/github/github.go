// Package github é a integração nativa com o GitHub: o que mudou no repositório do produto.
// Fala REST direto (dois endpoints não justificam uma dependência). Token só de leitura.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/thallesdaniell/sre-agent/internal/integrations"
)

type Client struct {
	BaseURL string // https://api.github.com, ou um servidor de teste
	Token   string
	HTTP    *http.Client
}

type pullRequest struct {
	Number   int        `json:"number"`
	Title    string     `json:"title"`
	MergedAt *time.Time `json:"merged_at"`
	HTMLURL  string     `json:"html_url"`
	User     struct {
		Login string `json:"login"`
	} `json:"user"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type deployment struct {
	Environment string    `json:"environment"`
	Ref         string    `json:"ref"`
	SHA         string    `json:"sha"`
	CreatedAt   time.Time `json:"created_at"`
}

func (c Client) get(ctx context.Context, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub respondeu %d para %s", resp.StatusCode, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type Change struct {
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	MergedAt   time.Time `json:"mergedAt"`
	Author     string    `json:"author"`
	BaseBranch string    `json:"baseBranch"`
	URL        string    `json:"url"`
}

type Deploy struct {
	Environment string    `json:"environment"`
	Ref         string    `json:"ref"`
	SHA         string    `json:"sha"`
	CreatedAt   time.Time `json:"createdAt"`
}

type RecentChanges struct {
	Since              time.Time `json:"since"`
	Hours              int       `json:"hours"`
	MergedPullRequests []Change  `json:"mergedPullRequests"`
	Deployments        []Deploy  `json:"deployments"`
}

func (c Client) RecentChanges(ctx context.Context, repository string, hours int, now time.Time) (RecentChanges, error) {
	since := now.Add(-time.Duration(hours) * time.Hour)
	out := RecentChanges{Since: since, Hours: hours, MergedPullRequests: []Change{}, Deployments: []Deploy{}}

	var pulls []pullRequest
	if err := c.get(ctx, "/repos/"+repository+"/pulls", url.Values{
		"state": {"closed"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"50"},
	}, &pulls); err != nil {
		return out, err
	}
	for _, pr := range pulls {
		if pr.MergedAt != nil && !pr.MergedAt.Before(since) {
			out.MergedPullRequests = append(out.MergedPullRequests, Change{
				Number: pr.Number, Title: pr.Title, MergedAt: *pr.MergedAt, Author: pr.User.Login, BaseBranch: pr.Base.Ref, URL: pr.HTMLURL,
			})
		}
	}

	var deploys []deployment
	if err := c.get(ctx, "/repos/"+repository+"/deployments", url.Values{"per_page": {"30"}}, &deploys); err != nil {
		return out, err
	}
	for _, d := range deploys {
		if !d.CreatedAt.Before(since) {
			out.Deployments = append(out.Deployments, Deploy{Environment: d.Environment, Ref: d.Ref, SHA: d.SHA[:min(12, len(d.SHA))], CreatedAt: d.CreatedAt})
		}
	}
	return out, nil
}

func Integration(client Client) integrations.Integration {
	return integrations.Integration{
		Name:       "github",
		Categories: []integrations.Category{integrations.Changes},
		Purpose:    "PRs mergeados e deployments registrados no repositório do produto",
		Connect: func(_ context.Context, deps integrations.Deps) (integrations.Connected, error) {
			type in struct {
				Product    string `json:"product" jsonschema:"required"`
				SinceHours int    `json:"sinceHours,omitempty" jsonschema:"minimum=1,maximum=72,description=Janela em horas (padrão 24)"`
			}
			tool := integrations.NewTool("recent_changes",
				"PRs mergeados e deployments do GitHub no repositório do produto. Consulte quando houver suspeita de mudança. Lembre: ter acontecido antes não prova causa. Produto do inventário: "+strings.Join(deps.Scopes.Products(), ", "),
				func(ctx context.Context, input in) (string, error) {
					s, err := deps.Scopes.Get(input.Product)
					if err != nil {
						return "", err
					}
					changes, err := client.RecentChanges(ctx, s.Repository, integrations.Clamp(input.SinceHours, 24, 1, integrations.MaxRecentChangesHour), time.Now())
					if err != nil {
						return "", err
					}
					return integrations.JSON(changes)
				})
			return integrations.Connected{Tools: []anthropic.BetaTool{tool}}, nil
		},
	}
}
