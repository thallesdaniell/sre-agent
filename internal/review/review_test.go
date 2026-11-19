package review_test

import (
	"strings"
	"testing"

	"github.com/thallesdaniell/sre-agent/internal/review"
	"github.com/thallesdaniell/sre-agent/internal/store"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

func withReport(t *testing.T) (*memstore.Store, store.ReportID) {
	st := memstore.New()
	ctx := t.Context()
	claim, _ := st.ClaimCase(ctx, testfixtures.Incident())
	run, _ := st.StartRun(ctx, claim.CaseID, "m", "4bf92f3577b34da6a3ce929d0e0e4736")
	id, _ := st.SaveReport(ctx, run.RunID, claim.CaseID, testfixtures.Report(), "")
	_ = st.MarkDelivered(ctx, id, store.Delivery{Kind: store.DeliveryShadow})
	_ = st.FinishRun(ctx, run.RunID, store.RunOutcome{Usage: store.Usage{CostUSD: 0.26635, Priced: true}})
	return st, id
}

func TestListaComEntregaFeedbackELinkDoTrace(t *testing.T) {
	st, id := withReport(t)
	out, err := review.Run(t.Context(), st, nil, "https://lf/project/p1/traces/{traceId}", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{string(id), "entrega=shadow", "custo≈US$0.27", "https://lf/project/p1/traces/4bf92f3577b34da6a3ce929d0e0e4736"} {
		if !strings.Contains(out, want) {
			t.Errorf("faltou %s", want)
		}
	}
}

func TestRegistraFeedbackEApareceNaListagem(t *testing.T) {
	st, id := withReport(t)
	if _, err := review.Run(t.Context(), st, []string{"feedback", string(id), "wrong", "--author", "ana", "--cause", "Pool esgotado"}, "", true); err != nil {
		t.Fatal(err)
	}
	if st.Feedback[0].ActualCause != "Pool esgotado" {
		t.Fatal(st.Feedback)
	}
	if out, _ := review.Run(t.Context(), st, nil, "", true); !strings.Contains(out, "feedback=wrong") {
		t.Fatal(out)
	}
}

func TestExigeCausaRealQuandoOAgenteErrou(t *testing.T) {
	id := "6f1e2d3c-4b5a-4f6e-8d7c-1a2b3c4d5e6f"
	if _, err := review.ParseFeedbackArgs([]string{id, "wrong", "--author", "ana"}, false); err == nil || !strings.Contains(err.Error(), "--cause") {
		t.Fatal(err)
	}
	if _, err := review.ParseFeedbackArgs([]string{id, "correct", "--author", "ana"}, false); err != nil {
		t.Fatal(err)
	}
}
