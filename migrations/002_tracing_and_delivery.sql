-- Rastreio e modo de entrega. Migration nova em vez de editar a 001: migration aplicada não muda.

-- Liga o run ao trace no backend de observabilidade (Langfuse, Jaeger...), para ir do banco
-- direto para o raciocínio do agente naquela investigação.
ALTER TABLE investigation_run ADD COLUMN trace_id text;

-- Como o relatório chegou (ou não) às pessoas:
--   pending = salvo, entrega ainda não aconteceu (ou falhou)
--   slack   = publicado no canal do incidente
--   shadow  = modo sombra: investigou e salvou, mas de propósito não publicou
--   stdout  = desenvolvimento, sem token do Slack
ALTER TABLE rca_report
  ADD COLUMN delivery text NOT NULL DEFAULT 'pending'
    CHECK (delivery IN ('pending', 'slack', 'shadow', 'stdout'));

UPDATE rca_report SET delivery = 'slack' WHERE slack_ts IS NOT NULL;

ALTER TABLE rca_report ADD CONSTRAINT rca_report_slack_ts_only_when_slack
  CHECK ((delivery = 'slack') = (slack_ts IS NOT NULL));
