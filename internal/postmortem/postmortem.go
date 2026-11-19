// Package postmortem lê post-mortems em markdown com um cabeçalho simples:
//
//	---
//	title: Checkout fora do ar por limite de memória
//	product: checkout
//	incident: INC-0981
//	occurredAt: 2026-08-14
//	rootCause: Limite de memória reduzido no deploy sem teste de carga
//	tags: oom, deploy, memoria
//	---
//	(corpo em markdown)
//
// O nome do arquivo vira o slug: reimportar o mesmo arquivo atualiza, não duplica.
package postmortem

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thallesdaniell/sre-agent/internal/store"
)

var (
	frontmatter = regexp.MustCompile(`(?s)^---\n(.*?)\n---\n?(.*)$`)
	product     = regexp.MustCompile(`^[a-z0-9-]+$`)
	nonSlug     = regexp.MustCompile(`[^a-z0-9-]`)
)

func Parse(fileName, content string) (store.Postmortem, error) {
	m := frontmatter.FindStringSubmatch(content)
	if m == nil {
		return store.Postmortem{}, fmt.Errorf("%s: cabeçalho --- ausente", fileName)
	}
	fields := map[string]string{}
	for _, line := range strings.Split(m[1], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	pm := store.Postmortem{
		Slug:        nonSlug.ReplaceAllString(strings.ToLower(strings.TrimSuffix(filepath.Base(fileName), ".md")), "-"),
		Title:       fields["title"],
		Product:     fields["product"],
		IncidentRef: fields["incident"],
		OccurredAt:  fields["occurredAt"],
		RootCause:   fields["rootCause"],
		Tags:        []string{},
		Body:        strings.TrimSpace(m[2]),
	}
	for _, t := range strings.Split(fields["tags"], ",") {
		if t = strings.TrimSpace(t); t != "" {
			pm.Tags = append(pm.Tags, t)
		}
	}
	if pm.Title == "" {
		return pm, fmt.Errorf("%s: title obrigatório", fileName)
	}
	if !product.MatchString(pm.Product) {
		return pm, fmt.Errorf("%s: product inválido", fileName)
	}
	if pm.OccurredAt != "" {
		if _, err := time.Parse("2006-01-02", pm.OccurredAt); err != nil {
			return pm, fmt.Errorf("%s: occurredAt precisa ser AAAA-MM-DD", fileName)
		}
	}
	return pm, nil
}
