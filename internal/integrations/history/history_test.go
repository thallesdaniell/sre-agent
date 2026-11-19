package history_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"

	"github.com/thallesdaniell/sre-agent/internal/integrations"
	"github.com/thallesdaniell/sre-agent/internal/integrations/history"
	"github.com/thallesdaniell/sre-agent/internal/store"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

func tools(t *testing.T, st store.Store, current string) map[string]anthropic.BetaTool {
	conn, err := history.Integration().Connect(t.Context(), integrations.Deps{Scopes: testfixtures.Scopes(), Store: st, CurrentIncidentID: current})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]anthropic.BetaTool{}
	for _, tool := range conn.Tools {
		out[tool.Name()] = tool
	}
	return out
}

func call(t *testing.T, tool anthropic.BetaTool, input string) (string, error) {
	blocks, err := tool.Execute(t.Context(), json.RawMessage(input))
	if err != nil {
		return "", err
	}
	return blocks[0].OfText.Text, nil
}

func TestBuscaPostmortems(t *testing.T) {
	st := memstore.New()
	_ = st.UpsertPostmortem(t.Context(), store.Postmortem{Slug: "checkout-oom", Product: "checkout", Title: "OOM", Body: "Pods com OOMKilled"})
	out, err := call(t, tools(t, st, "")["search_postmortems"], `{"query":"OOMKilled"}`)
	if err != nil || !strings.Contains(out, "checkout-oom") {
		t.Fatalf("%s %v", out, err)
	}
}

func TestIncidentesAnterioresTrazemVereditoEExcluemOAtual(t *testing.T) {
	st := memstore.New()
	ctx := t.Context()
	old := testfixtures.Incident()
	old.ID = "INC-0981"
	claim, _ := st.ClaimCase(ctx, old)
	run, _ := st.StartRun(ctx, claim.CaseID, "m", "")
	reportID, _ := st.SaveReport(ctx, run.RunID, claim.CaseID, testfixtures.Report(), "")
	_ = st.RecordFeedback(ctx, store.Feedback{ReportID: reportID, Verdict: store.VerdictWrong, Author: "ana", ActualCause: "Pool de conexões esgotado"})
	_, _ = st.ClaimCase(ctx, testfixtures.Incident()) // o incidente atual

	out, err := call(t, tools(t, st, "INC-1042")["past_incidents"], `{"product":"checkout"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "INC-0981") || !strings.Contains(out, "Pool de conexões esgotado") || strings.Contains(out, "INC-1042") {
		t.Fatalf("histórico errado: %s", out)
	}
}

func TestRecusaProdutoForaDoInventario(t *testing.T) {
	if _, err := call(t, tools(t, memstore.New(), "")["past_incidents"], `{"product":"billing"}`); err == nil {
		t.Fatal("esperava recusa")
	}
}
