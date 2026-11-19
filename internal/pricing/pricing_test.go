package pricing_test

import (
	"math"
	"testing"

	"github.com/thallesdaniell/sre-agent/internal/pricing"
)

// Os números da investigação real no cluster kind (5 chamadas ao claude-opus-5-5).
func TestEstimaAInvestigacaoRealDoLaboratorio(t *testing.T) {
	cost, ok := pricing.Estimate("claude-opus-5-5", pricing.Tokens{Input: 32273, Output: 5662, CacheRead: 16564, CacheWrite5m: 4141})
	if !ok || math.Abs(cost-0.26635) > 0.000001 {
		t.Fatalf("custo %.5f (ok=%v), esperado ≈ 0.26635", cost, ok)
	}
}

func TestEscritaDeCacheDeUmaHoraCustaODobroDaEntrada(t *testing.T) {
	cost, _ := pricing.Estimate("claude-opus-5-5", pricing.Tokens{CacheWrite1h: 1_000_000})
	if cost != 8 {
		t.Fatalf("esperado 8, veio %v", cost)
	}
}

func TestModeloDesconhecidoNaoTemEstimativa(t *testing.T) {
	if _, ok := pricing.Estimate("modelo-novo", pricing.Tokens{Input: 1000}); ok {
		t.Fatal("não deveria chutar preço de modelo fora da tabela")
	}
}
