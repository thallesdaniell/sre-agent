package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/report"
)

const uniqueViolation = "23505"

// PG implementa Store com Postgres. Todo SQL do projeto mora aqui, sempre parametrizado.
// Parâmetros de colunas não-texto (uuid, date, timestamptz) entram como texto e o banco converte
// ($1::uuid), para o driver não precisar adivinhar o tipo.
type PG struct{ pool *pgxpool.Pool }

func NewPG(pool *pgxpool.Pool) *PG { return &PG{pool: pool} }

func (s *PG) Close() { s.pool.Close() }

func (s *PG) ClaimCase(ctx context.Context, inc incident.Incident) (CaseClaim, error) {
	var (
		id         string
		dispatched *time.Time
		ref        *string
	)
	err := s.pool.QueryRow(ctx, `
		INSERT INTO incident_case (source, external_id, product, title, severity, declared_at, slack_channel, payload)
		VALUES ($1, $2, $3, $4, $5, $6::timestamptz, NULLIF($7, ''), $8)
		ON CONFLICT (source, external_id) DO UPDATE SET last_seen_at = now()
		RETURNING id::text, dispatched_at, dispatch_ref`,
		inc.Source, inc.ID, inc.Product, inc.Title, string(inc.Severity), inc.DeclaredAt, inc.SlackChannel, inc,
	).Scan(&id, &dispatched, &ref)
	if err != nil {
		return CaseClaim{}, err
	}
	claim := CaseClaim{CaseID: CaseID(id), AlreadyDispatched: dispatched != nil}
	if ref != nil {
		claim.Ref = *ref
	}
	return claim, nil
}

func (s *PG) MarkDispatched(ctx context.Context, caseID CaseID, ref string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE incident_case SET dispatched_at = coalesce(dispatched_at, now()), dispatch_ref = coalesce(dispatch_ref, $2)
		WHERE id = $1::uuid`, string(caseID), ref)
	return err
}

func (s *PG) StartRun(ctx context.Context, caseID CaseID, model, traceID string) (StartRun, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO investigation_run (case_id, status, model, trace_id) VALUES ($1::uuid, 'RUNNING', $2, NULLIF($3, ''))
		RETURNING id::text`, string(caseID), model, traceID).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		// O índice único parcial impede duas investigações simultâneas do mesmo caso.
		return StartRun{AlreadyRunning: true}, nil
	}
	if err != nil {
		return StartRun{}, err
	}
	return StartRun{RunID: RunID(id)}, nil
}

func (s *PG) FinishRun(ctx context.Context, runID RunID, o RunOutcome) error {
	status, code, detail := "SUCCEEDED", (*string)(nil), (*string)(nil)
	if o.Failure != "" {
		status = "FAILED"
		c, d := string(o.Failure), o.Detail
		if len(d) > 2000 {
			d = d[:2000]
		}
		code, detail = &c, &d
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE investigation_run
		   SET status = $2, failure_code = $3, failure_detail = $4, input_tokens = $5, output_tokens = $6, finished_at = now()
		 WHERE id = $1::uuid AND status = 'RUNNING'`,
		string(runID), status, code, detail, o.Usage.InputTokens, o.Usage.OutputTokens)
	return err
}

func (s *PG) SaveReport(ctx context.Context, runID RunID, caseID CaseID, r report.RcaReport, slackChannel string) (ReportID, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO rca_report (run_id, case_id, status, confidence, root_cause, change_kind, report, slack_channel)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, NULLIF($8, ''))
		RETURNING id::text`,
		string(runID), string(caseID), string(r.Status), r.RootCause.Confidence, r.RootCause.Statement,
		string(r.ChangeAttribution.Kind), r, slackChannel,
	).Scan(&id)
	return ReportID(id), err
}

func (s *PG) MarkDelivered(ctx context.Context, reportID ReportID, d Delivery) error {
	_, err := s.pool.Exec(ctx, `UPDATE rca_report SET delivery = $2, slack_ts = NULLIF($3, '') WHERE id = $1::uuid`,
		string(reportID), string(d.Kind), d.SlackTS)
	return err
}

func (s *PG) ListRecentReports(ctx context.Context, limit int) ([]ReportSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id::text, c.external_id, c.product, c.title, r.status, r.confidence::float8, r.root_cause,
		       r.delivery, coalesce(run.trace_id, ''), to_char(r.created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), coalesce(f.verdict, '')
		  FROM rca_report r
		  JOIN incident_case c ON c.id = r.case_id
		  JOIN investigation_run run ON run.id = r.run_id
		  LEFT JOIN LATERAL (
		    SELECT verdict FROM rca_feedback WHERE report_id = r.id ORDER BY created_at DESC LIMIT 1
		  ) f ON true
		 ORDER BY r.created_at DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReportSummary, error) {
		var r ReportSummary
		var delivery, verdict, id string
		err := row.Scan(&id, &r.ExternalID, &r.Product, &r.Title, &r.Status, &r.Confidence, &r.RootCause,
			&delivery, &r.TraceID, &r.CreatedAt, &verdict)
		r.ReportID, r.Delivery, r.Feedback = ReportID(id), DeliveryKind(delivery), Verdict(verdict)
		return r, err
	})
}

func (s *PG) RecordFeedback(ctx context.Context, f Feedback) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rca_feedback (report_id, verdict, actual_cause, author) VALUES ($1::uuid, $2, NULLIF($3, ''), $4)
		ON CONFLICT (report_id, author) DO UPDATE SET verdict = EXCLUDED.verdict, actual_cause = EXCLUDED.actual_cause, created_at = now()`,
		string(f.ReportID), string(f.Verdict), f.ActualCause, f.Author)
	return err
}

func (s *PG) UpsertPostmortem(ctx context.Context, pm Postmortem) error {
	tags := pm.Tags
	if tags == nil {
		tags = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO postmortem (slug, product, title, incident_ref, occurred_at, root_cause, tags, body)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, '')::date, NULLIF($6, ''), $7, $8)
		ON CONFLICT (slug) DO UPDATE SET product = EXCLUDED.product, title = EXCLUDED.title, incident_ref = EXCLUDED.incident_ref,
		  occurred_at = EXCLUDED.occurred_at, root_cause = EXCLUDED.root_cause, tags = EXCLUDED.tags, body = EXCLUDED.body, updated_at = now()`,
		pm.Slug, pm.Product, pm.Title, pm.IncidentRef, pm.OccurredAt, pm.RootCause, tags, pm.Body)
	return err
}

func (s *PG) SearchPostmortems(ctx context.Context, query, product string, limit int) ([]PostmortemHit, error) {
	rows, err := s.pool.Query(ctx, `
		WITH q AS (SELECT websearch_to_tsquery('portuguese', $1) AS query)
		SELECT slug, product, title, coalesce(incident_ref, ''), coalesce(to_char(occurred_at, 'YYYY-MM-DD'), ''),
		       coalesce(root_cause, ''),
		       ts_headline('portuguese', body, q.query, 'MaxWords=40, MinWords=15, MaxFragments=2'),
		       ts_rank(search, q.query)::float8
		  FROM postmortem, q
		 WHERE search @@ q.query AND (NULLIF($2, '') IS NULL OR product = $2)
		 ORDER BY 8 DESC, occurred_at DESC NULLS LAST
		 LIMIT $3`, query, product, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PostmortemHit, error) {
		var h PostmortemHit
		err := row.Scan(&h.Slug, &h.Product, &h.Title, &h.IncidentRef, &h.OccurredAt, &h.RootCause, &h.Snippet, &h.Rank)
		return h, err
	})
}

func (s *PG) GetPostmortem(ctx context.Context, slug string) (*Postmortem, error) {
	var pm Postmortem
	err := s.pool.QueryRow(ctx, `
		SELECT slug, product, title, coalesce(incident_ref, ''), coalesce(to_char(occurred_at, 'YYYY-MM-DD'), ''),
		       coalesce(root_cause, ''), tags, body
		  FROM postmortem WHERE slug = $1`, slug,
	).Scan(&pm.Slug, &pm.Product, &pm.Title, &pm.IncidentRef, &pm.OccurredAt, &pm.RootCause, &pm.Tags, &pm.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

func (s *PG) RecentIncidents(ctx context.Context, product, excludeExternalID string, limit int) ([]PastIncident, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.external_id, c.title, to_char(c.declared_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       coalesce(r.status, ''), coalesce(r.root_cause, ''), r.confidence::float8,
		       coalesce(f.verdict, ''), coalesce(f.actual_cause, '')
		  FROM incident_case c
		  LEFT JOIN LATERAL (
		    SELECT id, status, root_cause, confidence FROM rca_report WHERE case_id = c.id ORDER BY created_at DESC LIMIT 1
		  ) r ON true
		  LEFT JOIN LATERAL (
		    SELECT verdict, actual_cause FROM rca_feedback WHERE report_id = r.id ORDER BY created_at DESC LIMIT 1
		  ) f ON true
		 WHERE c.product = $1 AND c.external_id <> $2
		 ORDER BY c.declared_at DESC
		 LIMIT $3`, product, excludeExternalID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PastIncident, error) {
		var p PastIncident
		var verdict string
		err := row.Scan(&p.ExternalID, &p.Title, &p.DeclaredAt, &p.ReportStatus, &p.RootCause, &p.Confidence, &verdict, &p.ActualCause)
		p.Feedback = Verdict(verdict)
		return p, err
	})
}
