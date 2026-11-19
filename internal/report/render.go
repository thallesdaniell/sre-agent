package report

import (
	"fmt"
	"math"
	"strings"

	"github.com/slack-go/slack"
)

const sectionLimit = 2900 // o Slack recusa seção acima de 3000 caracteres

func clip(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

func section(text string) slack.Block {
	return slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, clip(text, sectionLimit), false, false), nil, nil)
}

func contextLine(text string) slack.Block {
	return slack.NewContextBlock("", slack.NewTextBlockObject(slack.MarkdownType, text, false, false))
}

func bullets(items []string, max int) string {
	var b strings.Builder
	for i, item := range items {
		if i == max {
			fmt.Fprintf(&b, "_…e mais %d_", len(items)-max)
			break
		}
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("• " + item)
	}
	return b.String()
}

// Message é a mensagem pronta para o Slack: texto de fallback e blocos.
type Message struct {
	Text   string
	Blocks []slack.Block
}

// Render: o servidor, e não o agente, decide a forma da mensagem (ordem, títulos e o aviso de
// que a hipótese é preliminar). O agente só fornece o conteúdo validado.
func Render(r RcaReport, traceURL string) Message {
	label := "Hipótese preliminar"
	if r.Status == Inconclusive {
		label = "Investigação inconclusiva"
	}
	pct := int(math.Round(r.RootCause.Confidence * 100))

	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, clip(fmt.Sprintf("RCA %s: %s", r.IncidentID, label), 150), false, false)),
		contextLine("⚠️ Gerado por agente. Uma pessoa precisa confirmar a causa."),
		section(r.Summary),
		section(fmt.Sprintf("*Hipótese de causa raiz* (confiança %d%%)\n%s\n_%s_", pct, r.RootCause.Statement, r.RootCause.Reasoning)),
	}
	if r.Trigger != "" {
		blocks = append(blocks, section("*Gatilho*\n"+r.Trigger))
	}
	steps := make([]string, len(r.CausalChain))
	for i, s := range r.CausalChain {
		steps[i] = fmt.Sprintf("%d. %s", i+1, s)
	}
	blocks = append(blocks, section("*Cadeia causal*\n"+strings.Join(steps, "\n")))

	if len(r.Evidence) > 0 {
		items := make([]string, len(r.Evidence))
		for i, e := range r.Evidence {
			items[i] = fmt.Sprintf("[%s] %s _(%s)_", e.Role, e.Finding, e.Tool)
		}
		blocks = append(blocks, section("*Evidências*\n"+bullets(items, 5)))
	}
	if len(r.DiscardedHypotheses) > 0 {
		items := make([]string, len(r.DiscardedHypotheses))
		for i, h := range r.DiscardedHypotheses {
			items[i] = h.Statement + ": " + h.Reason
		}
		blocks = append(blocks, section("*Hipóteses descartadas*\n"+bullets(items, 5)))
	}

	blocks = append(blocks, slack.NewDividerBlock(), section("*Próximos passos*\n"+bullets(r.NextSteps, 5)))
	if len(r.Limitations) > 0 {
		blocks = append(blocks, section("*Limitações*\n"+bullets(r.Limitations, 5)))
	}
	if traceURL != "" {
		// Quem for validar a hipótese vê cada passo do agente: o que consultou e o que recebeu.
		blocks = append(blocks, contextLine(fmt.Sprintf("<%s|Ver a investigação passo a passo>", traceURL)))
	}

	return Message{
		Text:   fmt.Sprintf("RCA %s: %s. %s", r.IncidentID, label, r.RootCause.Statement),
		Blocks: blocks,
	}
}
