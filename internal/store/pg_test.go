package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thallesdaniell/sre-agent/internal/db"
	"github.com/thallesdaniell/sre-agent/internal/postmortem"
	"github.com/thallesdaniell/sre-agent/internal/store"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

// Testes contra um Postgres de verdade: constraints, índices e busca textual não dá para simular
// em memória. Rodam só com TEST_DATABASE_URL (ver README), porque apagam e recriam o schema.
func setup(t *testing.T) (*store.PG, *pgxpool.Pool) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url, 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return store.NewPG(pool), pool
}

func TestMigrateIdempotente(t *testing.T) {
	_, pool := setup(t)
	applied, err := db.Migrate(t.Context(), pool)
	if err != nil || len(applied) != 0 {
		t.Fatal(applied, err)
	}
}

func TestMesmoIncidenteMesmoCasoEDisparaUmaVez(t *testing.T) {
	st, _ := setup(t)
	ctx := t.Context()
	first, err := st.ClaimCase(ctx, testfixtures.Incident())
	if err != nil || first.AlreadyDispatched {
		t.Fatal(first, err)
	}
	_ = st.MarkDispatched(ctx, first.CaseID, "rca-inc-1042")
	again, _ := st.ClaimCase(ctx, testfixtures.Incident())
	if !again.AlreadyDispatched || again.CaseID != first.CaseID || again.Ref != "rca-inc-1042" {
		t.Fatal(again)
	}
	other := testfixtures.Incident()
	other.Source = "pagerduty"
	if c, _ := st.ClaimCase(ctx, other); c.CaseID == first.CaseID {
		t.Fatal("fonte diferente deveria ser outro caso")
	}
}

func TestBancoImpedeDuasInvestigacoesSimultaneas(t *testing.T) {
	st, _ := setup(t)
	ctx := t.Context()
	claim, _ := st.ClaimCase(ctx, testfixtures.Incident())
	run, err := st.StartRun(ctx, claim.CaseID, "claude-opus-5-5", "abc")
	if err != nil || run.AlreadyRunning {
		t.Fatal(run, err)
	}
	if second, _ := st.StartRun(ctx, claim.CaseID, "claude-opus-5-5", ""); !second.AlreadyRunning {
		t.Fatal("segundo run deveria ser recusado")
	}
	_ = st.FinishRun(ctx, run.RunID, store.RunOutcome{Failure: store.FailureNoReport, Detail: "x"})
	if third, _ := st.StartRun(ctx, claim.CaseID, "claude-opus-5-5", ""); third.AlreadyRunning {
		t.Fatal("depois de terminar, um novo run deveria ser aceito")
	}
}

func TestFailedSemCodigoERecusadoPelaConstraint(t *testing.T) {
	st, pool := setup(t)
	claim, _ := st.ClaimCase(t.Context(), testfixtures.Incident())
	_, err := pool.Exec(t.Context(), `INSERT INTO investigation_run (case_id, status, model, finished_at) VALUES ($1::uuid, 'FAILED', 'm', now())`, string(claim.CaseID))
	if err == nil {
		t.Fatal("constraint deveria recusar FAILED sem failure_code")
	}
}

func TestRelatorioEntregaFeedbackEHistorico(t *testing.T) {
	st, _ := setup(t)
	ctx := t.Context()
	old := testfixtures.Incident()
	old.ID, old.DeclaredAt = "INC-0981", "2026-08-14T10:12:00Z"
	claim, _ := st.ClaimCase(ctx, old)
	run, _ := st.StartRun(ctx, claim.CaseID, "claude-opus-5-5", "4bf92f3577b34da6a3ce929d0e0e4736")
	id, err := st.SaveReport(ctx, run.RunID, claim.CaseID, testfixtures.Report(), "#inc")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkDelivered(ctx, id, store.Delivery{Kind: store.DeliverySlack, SlackTS: "1731.0001"}); err != nil {
		t.Fatal(err)
	}
	_ = st.RecordFeedback(ctx, store.Feedback{ReportID: id, Verdict: store.VerdictPartiallyCorrect, Author: "ana", ActualCause: "Memória e pool"})
	_ = st.RecordFeedback(ctx, store.Feedback{ReportID: id, Verdict: store.VerdictCorrect, Author: "ana"}) // mesma pessoa: atualiza

	history, err := st.RecentIncidents(ctx, "checkout", "INC-1042", 5)
	if err != nil || len(history) != 1 {
		t.Fatal(history, err)
	}
	h := history[0]
	if h.ExternalID != "INC-0981" || h.ReportStatus != "PRELIMINARY" || *h.Confidence != 0.7 || h.Feedback != store.VerdictCorrect {
		t.Fatalf("%+v", h)
	}
	reports, _ := st.ListRecentReports(ctx, 5)
	if reports[0].Delivery != store.DeliverySlack || reports[0].TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("%+v", reports[0])
	}
}

func TestEntregaSlackSemTsERecusada(t *testing.T) {
	st, _ := setup(t)
	ctx := t.Context()
	claim, _ := st.ClaimCase(ctx, testfixtures.Incident())
	run, _ := st.StartRun(ctx, claim.CaseID, "m", "")
	id, _ := st.SaveReport(ctx, run.RunID, claim.CaseID, testfixtures.Report(), "")
	if err := st.MarkDelivered(ctx, id, store.Delivery{Kind: store.DeliverySlack}); err == nil {
		t.Fatal("constraint deveria exigir slack_ts quando delivery=slack")
	}
}

func TestBuscaPostmortemsEmPortugues(t *testing.T) {
	st, _ := setup(t)
	ctx := t.Context()
	files, _ := filepath.Glob("../../fixtures/postmortems/*.md")
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		pm, err := postmortem.Parse(f, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.UpsertPostmortem(ctx, pm); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := st.SearchPostmortems(ctx, "memória OOMKilled", "", 5)
	if err != nil || len(memory) == 0 || memory[0].Slug != "2026-08-checkout-oom" {
		t.Fatal(memory, err)
	}
	pool, _ := st.SearchPostmortems(ctx, "conexões banco", "checkout", 5)
	if len(pool) == 0 || pool[0].Slug != "2026-09-checkout-db-pool" || pool[0].Snippet == "" {
		t.Fatal(pool)
	}
	if none, _ := st.SearchPostmortems(ctx, "kafka", "", 5); len(none) != 0 {
		t.Fatal(none)
	}
	pm, _ := st.GetPostmortem(ctx, "2026-09-checkout-db-pool")
	if pm == nil || pm.OccurredAt != "2026-09-22" {
		t.Fatal(pm)
	}
}

func TestGravaCustoTokensDeCacheEModelAPIError(t *testing.T) {
	st, pool := setup(t)
	ctx := t.Context()
	claim, _ := st.ClaimCase(ctx, testfixtures.Incident())
	run, _ := st.StartRun(ctx, claim.CaseID, "claude-opus-5-5", "")
	id, _ := st.SaveReport(ctx, run.RunID, claim.CaseID, testfixtures.Report(), "")
	usage := store.Usage{InputTokens: 32273, OutputTokens: 5662, CacheReadTokens: 16564, CacheWrite5mTokens: 4141, CostUSD: 0.26635, Priced: true}
	if err := st.FinishRun(ctx, run.RunID, store.RunOutcome{Usage: usage}); err != nil {
		t.Fatal(err)
	}
	var cacheRead, cacheWrite int
	var cost float64
	_ = pool.QueryRow(ctx, `SELECT cache_read_tokens, cache_write_5m_tokens, cost_usd_estimate::float8 FROM investigation_run WHERE id = $1::uuid`, string(run.RunID)).Scan(&cacheRead, &cacheWrite, &cost)
	if cacheRead != 16564 || cacheWrite != 4141 || cost != 0.26635 {
		t.Fatalf("gravou %d %d %v", cacheRead, cacheWrite, cost)
	}
	reports, _ := st.ListRecentReports(ctx, 1)
	if reports[0].ReportID != id || reports[0].CostUSD == nil || *reports[0].CostUSD != 0.26635 {
		t.Fatalf("%+v", reports[0])
	}

	// O novo código de falha é aceito pela constraint; custo fica NULL sem preço conhecido.
	other := testfixtures.Incident()
	other.ID = "INC-3000"
	c2, _ := st.ClaimCase(ctx, other)
	r2, _ := st.StartRun(ctx, c2.CaseID, "claude-opus-5-5", "")
	if err := st.FinishRun(ctx, r2.RunID, store.RunOutcome{Failure: store.FailureModelAPIError, Detail: "400 sem crédito"}); err != nil {
		t.Fatal(err)
	}
	var code string
	var nullCost *float64
	_ = pool.QueryRow(ctx, `SELECT failure_code, cost_usd_estimate::float8 FROM investigation_run WHERE id = $1::uuid`, string(r2.RunID)).Scan(&code, &nullCost)
	if code != "model_api_error" || nullCost != nil {
		t.Fatalf("%s %v", code, nullCost)
	}
}
