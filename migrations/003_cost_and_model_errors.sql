-- Custo estimado por investigação e código próprio para falhas da API do modelo.

-- input_tokens continua sendo só a entrada sem cache (como a API devolve). Os tokens de cache
-- passam a ser gravados também: sem eles o custo calculado pelo banco fica abaixo do real.
ALTER TABLE investigation_run
  ADD COLUMN cache_read_tokens      integer NOT NULL DEFAULT 0,
  ADD COLUMN cache_write_5m_tokens  integer NOT NULL DEFAULT 0,
  ADD COLUMN cache_write_1h_tokens  integer NOT NULL DEFAULT 0,
  -- Estimativa pelo preço de tabela; NULL quando o modelo não tem preço conhecido.
  -- O valor cobrado de verdade vem do Console ou da API de custos, agregado por dia.
  ADD COLUMN cost_usd_estimate      numeric(10, 6);

-- model_api_error: a API do modelo recusou ou falhou (sem crédito, chave inválida, limite de
-- taxa, indisponibilidade). Separado de unknown para dar para contar e alertar à parte.
ALTER TABLE investigation_run DROP CONSTRAINT investigation_run_failure_code_check;
ALTER TABLE investigation_run ADD CONSTRAINT investigation_run_failure_code_check
  CHECK (failure_code IN ('no_report', 'refused', 'max_iterations', 'integration_error', 'publish_error', 'model_api_error', 'unknown'));
