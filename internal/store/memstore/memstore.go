// Package memstore implementa store.Store em memória, com as mesmas regras do Postgres
// (dedupe por source+id, um run RUNNING por caso), para testes unitários sem banco.
package memstore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/thallesdaniell/sre-agent/internal/incident"
	"github.com/thallesdaniell/sre-agent/internal/report"
	"github.com/thallesdaniell/sre-agent/internal/store"
)

type Case struct {
	ID         store.CaseID
	Incident   incident.Incident
	Dispatched bool
	Ref        string
}

type Run struct {
	ID      store.RunID
	CaseID  store.CaseID
	TraceID string
	Status  string // RUNNING, SUCCEEDED, FAILED
	Outcome store.RunOutcome
}

type Report struct {
	ID       store.ReportID
	RunID    store.RunID
	CaseID   store.CaseID
	Report   report.RcaReport
	Delivery store.Delivery
}

type Store struct {
	mu          sync.Mutex
	seq         int
	Cases       []*Case
	Runs        []*Run
	Reports     []*Report
	Postmortems []store.Postmortem
	Feedback    []store.Feedback
}

func New() *Store { return &Store{} }

func (s *Store) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%04d", prefix, s.seq)
}

func (s *Store) ClaimCase(_ context.Context, inc incident.Incident) (store.CaseClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.Cases {
		if c.Incident.Source == inc.Source && c.Incident.ID == inc.ID {
			return store.CaseClaim{CaseID: c.ID, AlreadyDispatched: c.Dispatched, Ref: c.Ref}, nil
		}
	}
	c := &Case{ID: store.CaseID(s.nextID("case")), Incident: inc}
	s.Cases = append(s.Cases, c)
	return store.CaseClaim{CaseID: c.ID}, nil
}

func (s *Store) MarkDispatched(_ context.Context, caseID store.CaseID, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.Cases {
		if c.ID == caseID && !c.Dispatched {
			c.Dispatched, c.Ref = true, ref
		}
	}
	return nil
}

func (s *Store) StartRun(_ context.Context, caseID store.CaseID, _ string, traceID string) (store.StartRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Runs {
		if r.CaseID == caseID && r.Status == "RUNNING" {
			return store.StartRun{AlreadyRunning: true}, nil
		}
	}
	r := &Run{ID: store.RunID(s.nextID("run")), CaseID: caseID, TraceID: traceID, Status: "RUNNING"}
	s.Runs = append(s.Runs, r)
	return store.StartRun{RunID: r.ID}, nil
}

func (s *Store) FinishRun(_ context.Context, runID store.RunID, o store.RunOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Runs {
		if r.ID == runID && r.Status == "RUNNING" {
			r.Status, r.Outcome = "SUCCEEDED", o
			if o.Failure != "" {
				r.Status = "FAILED"
			}
		}
	}
	return nil
}

func (s *Store) SaveReport(_ context.Context, runID store.RunID, caseID store.CaseID, r report.RcaReport, _ string) (store.ReportID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := &Report{ID: store.ReportID(s.nextID("report")), RunID: runID, CaseID: caseID, Report: r, Delivery: store.Delivery{Kind: store.DeliveryPending}}
	s.Reports = append(s.Reports, row)
	return row.ID, nil
}

func (s *Store) MarkDelivered(_ context.Context, reportID store.ReportID, d store.Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Reports {
		if r.ID == reportID {
			r.Delivery = d
		}
	}
	return nil
}

func (s *Store) ListRecentReports(_ context.Context, limit int) ([]store.ReportSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.ReportSummary
	for i := len(s.Reports) - 1; i >= 0 && len(out) < limit; i-- {
		r := s.Reports[i]
		c := s.caseByID(r.CaseID)
		summary := store.ReportSummary{
			ReportID: r.ID, ExternalID: c.Incident.ID, Product: c.Incident.Product, Title: c.Incident.Title,
			Status: string(r.Report.Status), Confidence: r.Report.RootCause.Confidence, RootCause: r.Report.RootCause.Statement,
			Delivery: r.Delivery.Kind, TraceID: s.runByID(r.RunID).TraceID, Feedback: s.lastVerdict(r.ID),
		}
		if u := s.runByID(r.RunID).Outcome.Usage; u.Priced {
			cost := u.CostUSD
			summary.CostUSD = &cost
		}
		out = append(out, summary)
	}
	return out, nil
}

func (s *Store) RecordFeedback(_ context.Context, f store.Feedback) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Feedback = append(s.Feedback, f)
	return nil
}

func (s *Store) UpsertPostmortem(_ context.Context, pm store.Postmortem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.Postmortems {
		if p.Slug == pm.Slug {
			s.Postmortems[i] = pm
			return nil
		}
	}
	s.Postmortems = append(s.Postmortems, pm)
	return nil
}

func (s *Store) SearchPostmortems(_ context.Context, query, product string, limit int) ([]store.PostmortemHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	terms := strings.Fields(strings.ToLower(query))
	var hits []store.PostmortemHit
	for _, p := range s.Postmortems {
		if product != "" && p.Product != product {
			continue
		}
		text := strings.ToLower(p.Title + " " + p.RootCause + " " + p.Body)
		if !slices.ContainsFunc(terms, func(t string) bool { return strings.Contains(text, t) }) {
			continue
		}
		hits = append(hits, store.PostmortemHit{Slug: p.Slug, Product: p.Product, Title: p.Title, IncidentRef: p.IncidentRef,
			OccurredAt: p.OccurredAt, RootCause: p.RootCause, Snippet: p.Body[:min(len(p.Body), 120)], Rank: 1})
		if len(hits) == limit {
			break
		}
	}
	return hits, nil
}

func (s *Store) GetPostmortem(_ context.Context, slug string) (*store.Postmortem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.Postmortems {
		if p.Slug == slug {
			return &p, nil
		}
	}
	return nil, nil
}

func (s *Store) RecentIncidents(_ context.Context, product, excludeExternalID string, limit int) ([]store.PastIncident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []store.PastIncident
	for _, c := range s.Cases {
		if c.Incident.Product != product || c.Incident.ID == excludeExternalID || len(out) == limit {
			continue
		}
		past := store.PastIncident{ExternalID: c.Incident.ID, Title: c.Incident.Title, DeclaredAt: c.Incident.DeclaredAt}
		for i := len(s.Reports) - 1; i >= 0; i-- {
			if r := s.Reports[i]; r.CaseID == c.ID {
				conf := r.Report.RootCause.Confidence
				past.ReportStatus, past.RootCause, past.Confidence = string(r.Report.Status), r.Report.RootCause.Statement, &conf
				past.Feedback = s.lastVerdict(r.ID)
				for j := len(s.Feedback) - 1; j >= 0; j-- {
					if s.Feedback[j].ReportID == r.ID {
						past.ActualCause = s.Feedback[j].ActualCause
						break
					}
				}
				break
			}
		}
		out = append(out, past)
	}
	return out, nil
}

func (s *Store) Close() {}

func (s *Store) caseByID(id store.CaseID) *Case {
	for _, c := range s.Cases {
		if c.ID == id {
			return c
		}
	}
	return &Case{}
}

func (s *Store) runByID(id store.RunID) *Run {
	for _, r := range s.Runs {
		if r.ID == id {
			return r
		}
	}
	return &Run{}
}

func (s *Store) lastVerdict(id store.ReportID) store.Verdict {
	for i := len(s.Feedback) - 1; i >= 0; i-- {
		if s.Feedback[i].ReportID == id {
			return s.Feedback[i].Verdict
		}
	}
	return ""
}
