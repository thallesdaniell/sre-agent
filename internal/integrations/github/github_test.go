package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thallesdaniell/sre-agent/internal/integrations"
	"github.com/thallesdaniell/sre-agent/internal/integrations/github"
	"github.com/thallesdaniell/sre-agent/internal/store/memstore"
	"github.com/thallesdaniell/sre-agent/internal/testfixtures"
)

func TestRecentChangesFiltraPelaJanelaEMandaToken(t *testing.T) {
	now := time.Now().UTC()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch {
		case strings.HasSuffix(r.URL.Path, "/repos/org/checkout-api/pulls"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"number": 42, "title": "reduz memória", "merged_at": now.Add(-2 * time.Hour), "html_url": "u", "user": map[string]string{"login": "ana"}, "base": map[string]string{"ref": "main"}},
				{"number": 10, "title": "antigo", "merged_at": now.Add(-100 * time.Hour)},
				{"number": 11, "title": "fechado sem merge", "merged_at": nil},
			})
		case strings.HasSuffix(r.URL.Path, "/deployments"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"environment": "production", "ref": "main", "sha": "abcdef1234567890", "created_at": now.Add(-time.Hour)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	conn, err := github.Integration(github.Client{BaseURL: srv.URL, Token: "t0k"}).Connect(t.Context(),
		integrations.Deps{Scopes: testfixtures.Scopes(), Store: memstore.New()})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := conn.Tools[0].Execute(t.Context(), json.RawMessage(`{"product":"checkout","sinceHours":24}`))
	if err != nil {
		t.Fatal(err)
	}
	out := blocks[0].OfText.Text
	if !strings.Contains(out, `"number":42`) || strings.Contains(out, `"number":10`) || strings.Contains(out, `"number":11`) {
		t.Fatalf("filtro errado: %s", out)
	}
	if !strings.Contains(out, `"sha":"abcdef123456"`) || auth != "Bearer t0k" {
		t.Fatalf("sha não encurtado ou token ausente: %s / %s", out, auth)
	}
}
