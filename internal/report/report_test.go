package report_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/thallesdaniell/sre-agent/internal/report"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

func issuePaths(t *testing.T, err error) []string {
	t.Helper()
	var v *report.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("esperava ValidationError, veio %v", err)
	}
	var paths []string
	for _, i := range v.Issues {
		paths = append(paths, i.Path)
	}
	return paths
}

func TestAceitaRelatorioPreliminarBemFundamentado(t *testing.T) {
	if err := testfixtures.Report().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLimitaConfiancaQuandoMudancaSoVeioAntes(t *testing.T) {
	r := testfixtures.Report()
	r.ChangeAttribution = report.ChangeAttribution{Kind: report.ChangeDeploy, EvidenceStrength: report.StrengthTemporalOnly}
	if paths := issuePaths(t, r.Validate()); paths[0] != "rootCause.confidence" {
		t.Fatalf("esperava rootCause.confidence, veio %v", paths)
	}
}

func TestPreliminaryExigeEvidenciaDeCausaRaiz(t *testing.T) {
	r := testfixtures.Report()
	r.Evidence = []report.Evidence{{Tool: "k8s_events", Finding: "BackOff", Role: report.RoleSymptom}}
	if err := r.Validate(); err == nil {
		t.Fatal("esperava recusa")
	}
}

func TestNaoAceitaConfirmed(t *testing.T) {
	r := testfixtures.Report()
	r.Status = "CONFIRMED"
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "pessoa") {
		t.Fatalf("esperava recusa de CONFIRMED, veio %v", err)
	}
}

func TestInconclusiveExigeConfiancaBaixa(t *testing.T) {
	r := testfixtures.Report()
	r.Status = report.Inconclusive
	if err := r.Validate(); err == nil {
		t.Fatal("esperava recusa")
	}
}

func TestRenderSempreAvisaQuePrecisaDeConfirmacaoHumana(t *testing.T) {
	msg := report.Render(testfixtures.Report(), "https://lf.example.com/traces/abc")
	blocks, _ := json.Marshal(msg.Blocks)
	for _, want := range []string{"Uma pessoa precisa confirmar a causa", "Ver a investigação passo a passo"} {
		if !strings.Contains(string(blocks), want) {
			t.Errorf("faltou %q", want)
		}
	}
	if !strings.Contains(msg.Text, "Hipótese preliminar") {
		t.Errorf("texto sem status: %s", msg.Text)
	}
}

func TestRenderCortaSecoesAcimaDoLimiteDoSlack(t *testing.T) {
	r := testfixtures.Report()
	r.RootCause.Reasoning = strings.Repeat("y", 5000)
	raw, _ := json.Marshal(report.Render(r, "").Blocks)
	var blocks []map[string]any
	_ = json.Unmarshal(raw, &blocks)
	for _, b := range blocks {
		if text, ok := b["text"].(map[string]any); ok && b["type"] == "section" {
			if n := len([]rune(text["text"].(string))); n > 2900 {
				t.Fatalf("seção com %d caracteres", n)
			}
		}
	}
}
