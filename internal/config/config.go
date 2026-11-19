// Package config carrega a configuração. Variáveis de ambiente são validadas no boot: falha cedo,
// não no meio do incidente. O agent.config.json diz o que investigar e com quais fontes.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/thallesdaniell/sre-agent/internal/scope"
)

// PublishMode: shadow (padrão) investiga e salva, mas não publica. slack publica no canal.
type PublishMode string

const (
	PublishShadow PublishMode = "shadow"
	PublishSlack  PublishMode = "slack"
)

type DispatchMode string

const (
	DispatchLocal      DispatchMode = "local"
	DispatchKubernetes DispatchMode = "kubernetes"
)

// Job é o que o job efêmero precisa.
type Job struct {
	DatabaseURL      string
	AgentConfigFile  string
	SlackBotToken    string
	GitHubToken      string
	GitHubAPIURL     string
	PublishMode      PublishMode
	TraceURLTemplate string
}

// Intake é o que o processo sempre ligado precisa, além do que o job precisa no modo local.
type Intake struct {
	Job
	WebhookSecret     string
	DispatchMode      DispatchMode
	JobNamespace      string
	JobImage          string
	JobServiceAccount string
	JobSecretName     string
	Port              int
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func LoadJob() (Job, error) {
	cfg := Job{
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		AgentConfigFile:  env("AGENT_CONFIG_FILE", "./agent.config.json"),
		SlackBotToken:    os.Getenv("SLACK_BOT_TOKEN"),
		GitHubToken:      os.Getenv("GITHUB_TOKEN"),
		GitHubAPIURL:     env("GITHUB_API_URL", "https://api.github.com"),
		PublishMode:      PublishMode(env("PUBLISH_MODE", string(PublishShadow))),
		TraceURLTemplate: os.Getenv("TRACE_URL_TEMPLATE"),
	}
	var errs []error
	if !strings.HasPrefix(cfg.DatabaseURL, "postgres://") && !strings.HasPrefix(cfg.DatabaseURL, "postgresql://") {
		errs = append(errs, errors.New("DATABASE_URL precisa ser postgres://..."))
	}
	if cfg.PublishMode != PublishShadow && cfg.PublishMode != PublishSlack {
		errs = append(errs, errors.New("PUBLISH_MODE precisa ser shadow ou slack"))
	}
	return cfg, errors.Join(errs...)
}

func LoadIntake() (Intake, error) {
	job, jobErr := LoadJob()
	port, portErr := strconv.Atoi(env("PORT", "8080"))
	cfg := Intake{
		Job:               job,
		WebhookSecret:     os.Getenv("WEBHOOK_SECRET"),
		DispatchMode:      DispatchMode(env("DISPATCH_MODE", string(DispatchLocal))),
		JobNamespace:      env("JOB_NAMESPACE", "sre-agent"),
		JobImage:          env("JOB_IMAGE", "sre-agent:dev"),
		JobServiceAccount: env("JOB_SERVICE_ACCOUNT", "sre-agent-readonly"),
		JobSecretName:     env("JOB_SECRET_NAME", "sre-agent-secrets"),
		Port:              port,
	}
	errs := []error{jobErr}
	if portErr != nil {
		errs = append(errs, errors.New("PORT precisa ser número"))
	}
	if len(cfg.WebhookSecret) < 24 {
		errs = append(errs, errors.New("WEBHOOK_SECRET precisa de ao menos 24 caracteres"))
	}
	if cfg.DispatchMode != DispatchLocal && cfg.DispatchMode != DispatchKubernetes {
		errs = append(errs, errors.New("DISPATCH_MODE precisa ser local ou kubernetes"))
	}
	return cfg, errors.Join(errs...)
}

// ---- agent.config.json ----

type IntegrationType string

const (
	IntegrationNative IntegrationType = "native"
	IntegrationMCP    IntegrationType = "mcp"
)

type TransportKind string

const (
	TransportHTTP  TransportKind = "http"
	TransportStdio TransportKind = "stdio"
)

// MCPTransport. Segredos nunca ficam no arquivo: os mapas *FromEnv dizem o NOME da variável.
type MCPTransport struct {
	Kind           TransportKind     `json:"kind"`
	URL            string            `json:"url,omitempty"`
	HeadersFromEnv map[string]string `json:"headersFromEnv,omitempty"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	EnvFromEnv     map[string]string `json:"envFromEnv,omitempty"`
}

type IntegrationConfig struct {
	Type    IntegrationType `json:"type"`
	Enabled *bool           `json:"enabled,omitempty"`
	// Só para MCP:
	Purpose    string       `json:"purpose,omitempty"`
	Categories []string     `json:"categories,omitempty"`
	Transport  MCPTransport `json:"transport,omitempty"`
	// Allowlist obrigatória: servidores MCP prontos costumam expor tools de escrita.
	AllowTools []string `json:"allowTools,omitempty"`
}

func (c IntegrationConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

type AgentConfig struct {
	Products     []scope.ProductScope         `json:"products"`
	Integrations map[string]IntegrationConfig `json:"integrations"`
}

var (
	integrationName = regexp.MustCompile(`^[a-z0-9-]+$`)
	envVarName      = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	validCategories = map[string]bool{"symptoms": true, "changes": true, "errors": true, "context": true}
)

func (c AgentConfig) Validate() error {
	var errs []error
	if len(c.Products) == 0 {
		errs = append(errs, errors.New("products: ao menos um"))
	}
	for _, p := range c.Products {
		if err := p.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	for name, ic := range c.Integrations {
		if !integrationName.MatchString(name) {
			errs = append(errs, fmt.Errorf("integração com nome inválido: %q", name))
		}
		switch ic.Type {
		case IntegrationNative:
		case IntegrationMCP:
			errs = append(errs, validateMCP(name, ic)...)
		default:
			errs = append(errs, fmt.Errorf("integração %s: type precisa ser native ou mcp", name))
		}
	}
	return errors.Join(errs...)
}

func validateMCP(name string, ic IntegrationConfig) []error {
	var errs []error
	if ic.Purpose == "" {
		errs = append(errs, fmt.Errorf("integração %s: purpose obrigatório", name))
	}
	if len(ic.Categories) == 0 {
		errs = append(errs, fmt.Errorf("integração %s: ao menos uma categoria", name))
	}
	for _, cat := range ic.Categories {
		if !validCategories[cat] {
			errs = append(errs, fmt.Errorf("integração %s: categoria desconhecida %q", name, cat))
		}
	}
	if len(ic.AllowTools) == 0 {
		errs = append(errs, fmt.Errorf("integração %s: allowTools é obrigatória", name))
	}
	refs := ic.Transport.HeadersFromEnv
	switch ic.Transport.Kind {
	case TransportHTTP:
		if !strings.HasPrefix(ic.Transport.URL, "http://") && !strings.HasPrefix(ic.Transport.URL, "https://") {
			errs = append(errs, fmt.Errorf("integração %s: transport.url inválida", name))
		}
	case TransportStdio:
		if ic.Transport.Command == "" {
			errs = append(errs, fmt.Errorf("integração %s: transport.command obrigatório", name))
		}
		refs = ic.Transport.EnvFromEnv
	default:
		errs = append(errs, fmt.Errorf("integração %s: transport.kind precisa ser http ou stdio", name))
	}
	for key, varName := range refs {
		if !envVarName.MatchString(varName) {
			errs = append(errs, fmt.Errorf("integração %s: %s deve apontar para uma variável de ambiente, não um valor", name, key))
		}
	}
	return errs
}

func ParseAgentConfig(raw []byte) (AgentConfig, error) {
	var c AgentConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return AgentConfig{}, fmt.Errorf("agent.config.json inválido: %w", err)
	}
	return c, c.Validate()
}

func LoadAgentConfig(path string) (AgentConfig, *scope.Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return AgentConfig{}, nil, err
	}
	c, err := ParseAgentConfig(raw)
	if err != nil {
		return AgentConfig{}, nil, err
	}
	return c, scope.NewRegistry(c.Products), nil
}
