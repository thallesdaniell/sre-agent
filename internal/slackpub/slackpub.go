// Package slackpub publica o relatório. O token do bot fica no job, nunca nas tools do agente:
// o modelo não tem como postar outra coisa nem em outro canal.
package slackpub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/slack-go/slack"

	"github.com/thallesdaniell/sre-agent/internal/config"
	"github.com/thallesdaniell/sre-agent/internal/report"
	"github.com/thallesdaniell/sre-agent/internal/store"
)

type Options struct {
	// Mode shadow = modo sombra: o relatório fica salvo (e no trace), mas nada é publicado. É como
	// se começa: o time compara as conclusões do agente com o que de fato aconteceu, sem o agente
	// influenciar o incidente, até confiar nele.
	Mode     config.PublishMode
	Channel  string
	Token    string
	TraceURL string
	Stdout   io.Writer // destino no modo sem token; padrão os.Stdout
}

func Publish(ctx context.Context, r report.RcaReport, o Options) (store.Delivery, error) {
	if o.Mode == config.PublishShadow {
		return store.Delivery{Kind: store.DeliveryShadow}, nil
	}
	msg := report.Render(r, o.TraceURL)
	if o.Token == "" || o.Channel == "" {
		out := o.Stdout
		if out == nil {
			out = os.Stdout
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"channel": o.Channel, "text": msg.Text, "blocks": msg.Blocks}); err != nil {
			return store.Delivery{}, err
		}
		return store.Delivery{Kind: store.DeliveryStdout}, nil
	}
	_, ts, err := slack.New(o.Token).PostMessageContext(ctx, o.Channel,
		slack.MsgOptionText(msg.Text, false), slack.MsgOptionBlocks(msg.Blocks...))
	if err != nil {
		return store.Delivery{}, fmt.Errorf("slack: %w", err)
	}
	return store.Delivery{Kind: store.DeliverySlack, SlackTS: ts}, nil
}
