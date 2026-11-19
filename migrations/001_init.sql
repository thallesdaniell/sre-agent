-- Estado durável do agente. Quatro perguntas que o banco responde:
--   1. Este incidente já foi recebido/disparado?        -> incident_case
--   2. Quantas investigações rodaram, quanto custaram?   -> investigation_run
--   3. O que o agente concluiu e o time concordou?        -> rca_report + rca_feedback
--   4. O que aprendemos em incidentes anteriores?         -> postmortem

CREATE TABLE incident_case (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  source         text NOT NULL,                    -- quem avisou: incident.io, pagerduty, webhook...
  external_id    text NOT NULL,                    -- id do incidente na origem
  product        text NOT NULL,
  title          text NOT NULL,
  severity       text NOT NULL CHECK (severity IN ('sev1', 'sev2', 'sev3', 'sev4')),
  declared_at    timestamptz NOT NULL,
  slack_channel  text,
  payload        jsonb NOT NULL,                   -- o incidente como chegou, para auditoria
  dispatched_at  timestamptz,                      -- null = recebido mas a investigação ainda não saiu
  dispatch_ref   text,                             -- nome do Job
  first_seen_at  timestamptz NOT NULL DEFAULT now(),
  last_seen_at   timestamptz NOT NULL DEFAULT now(),
  -- A mesma entrega repetida (retry do remetente, réplicas do intake) cai na mesma linha.
  UNIQUE (source, external_id)
);
CREATE INDEX incident_case_product_declared_idx ON incident_case (product, declared_at DESC);

CREATE TABLE investigation_run (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  case_id        uuid NOT NULL REFERENCES incident_case (id) ON DELETE CASCADE,
  status         text NOT NULL CHECK (status IN ('RUNNING', 'SUCCEEDED', 'FAILED')),
  -- Falha sempre carrega o motivo: um FAILED sem código não ensina nada.
  failure_code   text CHECK (failure_code IN ('no_report', 'refused', 'max_iterations', 'integration_error', 'publish_error', 'unknown')),
  failure_detail text,
  model          text NOT NULL,
  input_tokens   integer NOT NULL DEFAULT 0,
  output_tokens  integer NOT NULL DEFAULT 0,
  started_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz,
  CHECK ((status = 'FAILED') = (failure_code IS NOT NULL)),
  CHECK ((status = 'RUNNING') = (finished_at IS NULL))
);
-- No máximo uma investigação em andamento por incidente, garantido pelo banco e não pelo código.
CREATE UNIQUE INDEX investigation_run_one_running_per_case ON investigation_run (case_id) WHERE status = 'RUNNING';
CREATE INDEX investigation_run_case_idx ON investigation_run (case_id, started_at DESC);

CREATE TABLE rca_report (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id         uuid NOT NULL UNIQUE REFERENCES investigation_run (id) ON DELETE CASCADE,
  case_id        uuid NOT NULL REFERENCES incident_case (id) ON DELETE CASCADE,
  status         text NOT NULL CHECK (status IN ('PRELIMINARY', 'INCONCLUSIVE')),
  confidence     numeric(3, 2) NOT NULL CHECK (confidence BETWEEN 0 AND 1),
  root_cause     text NOT NULL,
  change_kind    text NOT NULL,
  report         jsonb NOT NULL,                   -- o relatório completo, como validado pelo schema
  slack_channel  text,
  slack_ts       text,                             -- preenchido depois que a mensagem sai
  created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rca_report_case_idx ON rca_report (case_id, created_at DESC);

-- O que o time achou do relatório. É daqui que sai o conjunto de avaliação.
CREATE TABLE rca_feedback (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  report_id      uuid NOT NULL REFERENCES rca_report (id) ON DELETE CASCADE,
  verdict        text NOT NULL CHECK (verdict IN ('correct', 'partially_correct', 'wrong')),
  actual_cause   text,                             -- a causa real, quando o agente errou
  author         text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (report_id, author)
);

-- Coluna gerada só aceita função IMMUTABLE; array_to_string é STABLE, então embrulhamos.
CREATE FUNCTION tags_to_text(tags text[]) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE
  AS $$ SELECT array_to_string(tags, ' ') $$;

CREATE TABLE postmortem (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug           text NOT NULL UNIQUE,             -- nome do arquivo de origem: reimportar atualiza, não duplica
  product        text NOT NULL,
  title          text NOT NULL,
  incident_ref   text,
  occurred_at    date,
  root_cause     text,
  tags           text[] NOT NULL DEFAULT '{}',
  body           text NOT NULL,
  -- Busca textual em português, com peso maior para título e causa raiz.
  search         tsvector GENERATED ALWAYS AS (
                   setweight(to_tsvector('portuguese'::regconfig, title), 'A') ||
                   setweight(to_tsvector('portuguese'::regconfig, coalesce(root_cause, '')), 'B') ||
                   setweight(to_tsvector('portuguese'::regconfig, tags_to_text(tags)), 'B') ||
                   setweight(to_tsvector('portuguese'::regconfig, body), 'C')
                 ) STORED,
  updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX postmortem_search_idx ON postmortem USING gin (search);
CREATE INDEX postmortem_product_idx ON postmortem (product, occurred_at DESC);
