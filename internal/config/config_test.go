package config_test

import (
	"strings"
	"testing"

	"github.com/thallesdaniell/sre-agent/internal/config"
)

const base = `{"products":[{"product":"checkout","namespaces":["checkout"],"repository":"org/x","environment":"production"}],`

func TestAceitaConfigDeExemplo(t *testing.T) {
	_, err := config.ParseAgentConfig([]byte(base + `"integrations":{"kubernetes":{"type":"native"},"history":{"type":"native"}}}`))
	if err != nil {
		t.Fatal(err)
	}
}

func TestMCPExigeAllowlistECategorias(t *testing.T) {
	_, err := config.ParseAgentConfig([]byte(base + `"integrations":{"dd":{"type":"mcp","purpose":"x","transport":{"kind":"http","url":"https://x"}}}}`))
	if err == nil || !strings.Contains(err.Error(), "allowTools") || !strings.Contains(err.Error(), "categoria") {
		t.Fatal(err)
	}
}

func TestSegredoNoArquivoERecusado(t *testing.T) {
	_, err := config.ParseAgentConfig([]byte(base + `"integrations":{"dd":{"type":"mcp","purpose":"x","categories":["symptoms"],"allowTools":["q"],
		"transport":{"kind":"http","url":"https://x","headersFromEnv":{"DD-API-KEY":"abc123-chave-real"}}}}}`))
	if err == nil || !strings.Contains(err.Error(), "variável de ambiente") {
		t.Fatal(err)
	}
}
