// Package pricing estima o custo de uma investigação a partir dos tokens que a API devolve.
//
// A resposta da API não traz preço, só tokens por tipo. O valor cobrado de verdade aparece no
// Console e na API de custos da organização, agregado por dia e workspace. Esta estimativa usa o
// preço de tabela e serve para acompanhar o custo por run; a conciliação é feita por fora.
package pricing

// Price em dólares por milhão de tokens.
type Price struct {
	Input     float64
	Output    float64
	CacheRead float64
}

// Multiplicadores da escrita em cache sobre o preço de entrada.
const (
	cacheWrite5mMultiplier = 1.25
	cacheWrite1hMultiplier = 2.0
)

// table: preço de tabela por modelo. Modelo fora daqui não tem custo estimado (nunca chuta).
// Atualize quando o preço mudar ou o agente passar a usar outro modelo.
var table = map[string]Price{
	"claude-opus-5-5":   {Input: 4, Output: 20, CacheRead: 0.20},
	"claude-sonnet-5-5": {Input: 2, Output: 10, CacheRead: 0.10},
	"claude-haiku-5-5":  {Input: 0.10, Output: 0.50, CacheRead: 0.01},
	"claude-opus-4-8":   {Input: 5, Output: 25, CacheRead: 0.50},
}

// Tokens de uma chamada, separados como a API devolve. Input é só a entrada sem cache.
type Tokens struct {
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
}

// Estimate devolve o custo em dólares e se o modelo tem preço conhecido.
func Estimate(model string, t Tokens) (float64, bool) {
	p, ok := table[model]
	if !ok {
		return 0, false
	}
	const perToken = 1.0 / 1_000_000
	cost := float64(t.Input)*p.Input +
		float64(t.Output)*p.Output +
		float64(t.CacheRead)*p.CacheRead +
		float64(t.CacheWrite5m)*p.Input*cacheWrite5mMultiplier +
		float64(t.CacheWrite1h)*p.Input*cacheWrite1hMultiplier
	return cost * perToken, true
}
