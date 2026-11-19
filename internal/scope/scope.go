// Package scope é o inventário: quais produtos o agente pode investigar e ONDE pode olhar.
// Tudo que não está aqui é recusado antes de qualquer chamada externa.
package scope

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	repoPattern = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)
)

type Environment string

const (
	Production Environment = "production"
	Staging    Environment = "staging"
)

type ProductScope struct {
	Product     string      `json:"product"`
	Namespaces  []string    `json:"namespaces"`
	Repository  string      `json:"repository"`
	Environment Environment `json:"environment"`
}

func (p ProductScope) Validate() error {
	if !namePattern.MatchString(p.Product) {
		return fmt.Errorf("produto inválido: %q", p.Product)
	}
	if len(p.Namespaces) == 0 {
		return fmt.Errorf("%s: ao menos um namespace", p.Product)
	}
	for _, ns := range p.Namespaces {
		if !namePattern.MatchString(ns) {
			return fmt.Errorf("%s: namespace inválido %q", p.Product, ns)
		}
	}
	if !repoPattern.MatchString(p.Repository) {
		return fmt.Errorf("%s: repository precisa ser owner/repo", p.Product)
	}
	if p.Environment != Production && p.Environment != Staging {
		return fmt.Errorf("%s: environment precisa ser production ou staging", p.Product)
	}
	return nil
}

// Error é um erro esperado: volta para o modelo como resultado da tool, para ele corrigir o pedido.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

type Registry struct {
	byProduct map[string]ProductScope
	order     []string
}

func NewRegistry(scopes []ProductScope) *Registry {
	r := &Registry{byProduct: map[string]ProductScope{}}
	for _, s := range scopes {
		r.byProduct[s.Product] = s
		r.order = append(r.order, s.Product)
	}
	return r
}

func (r *Registry) Products() []string { return slices.Clone(r.order) }

func (r *Registry) Get(product string) (ProductScope, error) {
	s, ok := r.byProduct[product]
	if !ok {
		return ProductScope{}, &Error{fmt.Sprintf("produto fora do inventário: %s", product)}
	}
	return s, nil
}

// Namespace resolve o namespace pedido; vazio usa o primeiro do produto.
func (r *Registry) Namespace(product, requested string) (string, error) {
	s, err := r.Get(product)
	if err != nil {
		return "", err
	}
	if requested == "" {
		return s.Namespaces[0], nil
	}
	if !slices.Contains(s.Namespaces, requested) {
		return "", &Error{fmt.Sprintf("namespace %s não pertence a %s. Permitidos: %s", requested, product, strings.Join(s.Namespaces, ", "))}
	}
	return requested, nil
}
