# sre-agent

Agente de plantão que investiga incidentes em produção e publica uma **hipótese de causa raiz** no Slack.
Ele só lê: não tem shell, não escreve em arquivo, não altera o cluster. Quem confirma a causa é uma pessoa.

Projeto de estudo de agentes de IA para SRE, construído com o [Anthropic TypeScript SDK](https://github.com/anthropics/anthropic-sdk-typescript) (Tool Runner) e Claude.

## Como funciona

### Visão geral

```mermaid
flowchart LR
    src["incident.io / PagerDuty / Datadog"] -- "webhook assinado (HMAC)" --> intake

    subgraph cluster["Cluster Kubernetes"]
        intake["Intake<br/>sempre ligado, sem LLM"]
        job["Job efêmero<br/>um por incidente"]
        intake -- "cria Job<br/>(nome = id do incidente)" --> job
    end

    job <-->|"loop do agente<br/>(Tool Runner)"| claude["Claude API"]

    subgraph fontes["Integrações (só leitura)"]
        k8s["Kubernetes<br/>pods, logs, eventos, rollouts"]
        gh["GitHub<br/>PRs e deployments"]
        mcp["Servidores MCP<br/>Datadog, Argo CD, Sentry..."]
    end

    job --> k8s
    job --> gh
    job --> mcp
    job -- "relatório validado<br/>e renderizado" --> slack["Canal do incidente<br/>no Slack"]

    db[("Postgres<br/>casos, runs, relatórios,<br/>feedback, post-mortems")]
    intake -- "deduplica" --> db
    job -- "registra o run<br/>e lê o histórico" --> db
```

### Uma investigação, passo a passo

```mermaid
sequenceDiagram
    autonumber
    participant F as Ferramenta de incidentes
    participant I as Intake
    participant K as Kubernetes API
    participant D as Postgres
    participant J as Job do agente
    participant C as Claude
    participant T as Integrações
    participant S as Slack

    F->>I: POST /webhooks/incident (x-timestamp, x-signature)
    I->>I: valida HMAC, idade ≤ 5 min, payload e inventário
    I->>D: claimCase (já disparado? responde duplicate)
    I->>K: cria Job rca-ID (409 = já existe, ignora)
    I->>D: markDispatched
    I-->>F: 202 Accepted
    K->>J: inicia o container com a ServiceAccount só de leitura
    J->>D: startRun (RUNNING, um por incidente, garantido por índice)
    J->>T: conecta integrações habilitadas (MCP filtrado pela allowlist)
    loop até entregar o relatório (máx. 30 voltas)
        J->>C: prompt + fontes por categoria + resultados das tools
        C-->>J: pede uma tool de evidência
        J->>T: executa com argumentos validados e saída limitada
        T-->>J: evidência
    end
    C-->>J: submit_rca_report
    J->>J: valida o schema (nunca CONFIRMED, regras de confiança)
    J->>D: saveReport (antes de publicar: não se perde se o Slack falhar)
    J->>S: publica a mensagem renderizada pelo servidor
    J->>D: finishRun SUCCEEDED (ou FAILED com failure_code)
    Note over K,J: o Job termina e é apagado depois de 1 h
```

- **Intake** (`src/intake/`): o único processo sempre ligado. Autentica o webhook, valida o payload e cria um Kubernetes Job. O nome do Job vem do id do incidente, então o mesmo incidente nunca gera dois runs.
- **Job** (`src/job.ts`): um processo por incidente. Conecta as integrações, roda o agente, publica e termina.
- **Agente** (`src/agent/`): o Tool Runner do SDK cuida do loop. O agente termina chamando `submit_rca_report`, e o schema em `src/report/schema.ts` impõe as regras do relatório (nunca `CONFIRMED`, confiança limitada quando a mudança só "veio antes", evidência obrigatória).
- **Integrações** (`src/integrations/`): cada fonte de evidência é um módulo. Ver abaixo.
- **Store** (`src/store/`): todo acesso ao Postgres passa por uma interface (`Store`), com todo o SQL em `pg-store.ts`. Ver [Persistência](#persistência).

## Integrações

Toda integração implementa o mesmo contrato (`src/integrations/types.ts`) e declara de que **tipo de evidência** ela é fonte:

| Categoria | Pergunta que responde |
|---|---|
| `symptoms` | O que está quebrado? (estado, métricas, logs) |
| `changes` | O que mudou? (deploys, rollouts, PRs, config) |
| `errors` | Que erro a aplicação está dando? (exceções, contagens) |
| `context` | Já aconteceu antes? O que o time está falando? |

O prompt mostra ao agente as fontes agrupadas por categoria. Categoria sem fonte aparece como "nenhuma fonte disponível", e o agente é instruído a registrar isso como limitação em vez de supor.

Há dois tipos:

- **Nativa**: código deste repositório, com argumentos validados e saída limitada. Ex.: `kubernetes`, `github`, `history`.
- **MCP**: um servidor MCP pronto, declarado só no `agent.config.json`, com **allowlist obrigatória** de tools. Servidores MCP costumam expor tools de escrita (silenciar monitor, sincronizar app, reiniciar pipeline); só entram as que você listar.

Toda tool, nativa ou MCP, passa pelo mesmo teto de tamanho de saída.

### Catálogo planejado

| Integração | Categoria | Tipo provável | Prioridade | Observação |
|---|---|---|---|---|
| Kubernetes (qualquer cluster, inclusive GKE) | symptoms, changes | nativa ✅ | alta | Pods, logs, eventos, histórico de rollout. Substitui `kubectl`: sem shell |
| GitHub (repositórios) | changes | nativa ✅ | alta | PRs mergeados e deployments |
| Argo CD | changes | MCP ou nativa | alta | Histórico de sync é a melhor evidência de "o que foi para produção e quando" |
| Google Cloud: Logging, Monitoring, Audit Logs | symptoms, changes | nativa | alta | Audit Log mostra mudanças de infra e IAM; Monitoring cobre também Cloud SQL e GKE (nós, autoscaler) |
| Datadog | symptoms | MCP | alta | Métricas, monitores disparados, logs, APM |
| Sentry / Rollbar | errors | MCP | alta | Contagem de ocorrências costuma ser a evidência que fecha a causa |
| Histórico e post-mortems (`history`) | context | nativa ✅ | alta | Busca textual em post-mortems e incidentes anteriores com o veredito do time. Lê só do nosso Postgres |
| Slack (canal do incidente) | context | nativa | média | O que o time já descobriu durante o incidente |
| Feature flags (LaunchDarkly, Unleash...) | changes | MCP ou nativa | média | Flag ligada ou desligada é uma causa comum e invisível no deploy |
| CircleCI / GitHub Actions | changes | nativa | baixa | Só se o deploy acontece no CI; com Argo CD, ele já cobre |
| Cloud SQL / MongoDB | symptoms | nativa | baixa | **Só metadados**: queries lentas, conexões, replicação. Nunca ler dados da aplicação |

Regras para qualquer integração nova:
1. Credencial só de leitura e com o menor escopo possível. A garantia é a permissão da credencial, não o prompt.
2. Nada de shell ou CLI genérica (`kubectl`, `gcloud`, `mongosh`): cada operação vira uma tool com argumentos validados.
3. Janela de tempo, número de linhas e tamanho de saída limitados.
4. Banco de dados: só métricas e metadados, nunca dados de cliente.

### Como adicionar uma integração

**Servidor MCP pronto** (sem código):

```json
"integrations": {
  "datadog": {
    "type": "mcp",
    "purpose": "métricas, monitores disparados e logs do serviço",
    "categories": ["symptoms"],
    "transport": {
      "kind": "http",
      "url": "https://<endpoint MCP do fornecedor>",
      "headersFromEnv": { "DD-API-KEY": "DATADOG_API_KEY" }
    },
    "allowTools": ["<tool de leitura>", "<outra tool de leitura>"]
  }
}
```

Segredos nunca ficam no arquivo: `headersFromEnv` diz o **nome** da variável de ambiente. Se uma tool da allowlist não existir no servidor, o job falha no boot.

**Integração nativa**:

1. Crie `src/integrations/<nome>/client.ts` (a chamada à API externa) e `src/integrations/<nome>/index.ts` (as tools, com `betaZodTool`).
2. Registre em `NATIVE_INTEGRATIONS` (`src/integrations/registry.ts`).
3. Habilite no `agent.config.json`: `"<nome>": { "type": "native" }`.
4. Escreva o teste em `tests/`.

## Persistência

Um Postgres guarda o estado durável. Cada tabela responde uma pergunta:

| Tabela | Pergunta |
|---|---|
| `incident_case` | Este incidente já foi recebido? A investigação já saiu? |
| `investigation_run` | Quantas investigações rodaram, quanto tempo e quantos tokens gastaram, e por que falharam? |
| `rca_report` | O que o agente concluiu? A mensagem saiu no Slack? |
| `rca_feedback` | O time concordou? Qual era a causa real? (É daqui que sai o conjunto de avaliação.) |
| `postmortem` | O que aprendemos em incidentes anteriores? |

```mermaid
erDiagram
    incident_case ||--o{ investigation_run : "tem"
    investigation_run ||--o| rca_report : "produz"
    incident_case ||--o{ rca_report : "agrupa"
    rca_report ||--o{ rca_feedback : "recebe"

    incident_case {
        uuid id PK
        text source "UNIQUE com external_id"
        text external_id
        text product
        text severity
        timestamptz declared_at
        timestamptz dispatched_at "null = ainda não disparou"
        jsonb payload
    }
    investigation_run {
        uuid id PK
        uuid case_id FK
        text status "RUNNING, SUCCEEDED, FAILED"
        text failure_code "obrigatório quando FAILED"
        int input_tokens
        int output_tokens
    }
    rca_report {
        uuid id PK
        uuid run_id FK
        text status "PRELIMINARY, INCONCLUSIVE"
        numeric confidence
        jsonb report
        text slack_ts
    }
    rca_feedback {
        uuid id PK
        uuid report_id FK
        text verdict "correct, partially_correct, wrong"
        text actual_cause
        text author
    }
    postmortem {
        uuid id PK
        text slug "UNIQUE"
        text product
        text root_cause
        tsvector search "busca em português"
    }
```

Regras que ficam no banco, e não só no código:

- **Deduplicação**: `UNIQUE (source, external_id)` em `incident_case`. Retry do remetente ou várias réplicas do intake caem na mesma linha. Se o disparo falhar, `dispatched_at` fica nulo e o próximo retry tenta de novo.
- **Um run por vez**: índice único parcial `WHERE status = 'RUNNING'`. Dois Jobs do mesmo incidente nunca investigam juntos.
- **Falha com motivo**: `CHECK` exige `failure_code` quando o run é `FAILED` (`no_report`, `refused`, `max_iterations`, `integration_error`, `publish_error`, `unknown`).
- **Relatório antes da mensagem**: o job salva o relatório e só então publica. Se o Slack falhar, o relatório não se perde e o run fica `FAILED` com `publish_error`.

### Migrations

Arquivos `.sql` em `migrations/`, aplicados em ordem por `npm run db:migrate` (`src/db/migrate.ts`). Cada arquivo roda numa transação e fica registrado em `schema_migrations`; um advisory lock evita duas réplicas migrando ao mesmo tempo. **Migration aplicada não se edita**: mudança nova é arquivo novo (`002_...sql`). No Kubernetes, a migration roda como `initContainer` do intake.

### Post-mortems

Escreva cada post-mortem em markdown com um cabeçalho e importe a pasta. O nome do arquivo vira o `slug`, então reimportar atualiza em vez de duplicar. Exemplos em `fixtures/postmortems/`.

```markdown
---
title: Checkout fora do ar por limite de memória
product: checkout
incident: INC-0981
occurredAt: 2026-08-14
rootCause: Limite de memória reduzido no deploy, sem teste de carga
tags: oom, deploy, memoria
---
## Resumo
...
```

```bash
npm run postmortems:import -- fixtures/postmortems
```

A busca usa o full-text search do Postgres em português, com peso maior para título, causa raiz e tags. Busca semântica (`pgvector`) fica para quando a textual não bastar.

## Rodando localmente

Pré-requisitos: Node 22 (ou Docker), Docker Compose, acesso a um cluster (pode ser `kind` ou `k3d`) e credencial da Anthropic (`ANTHROPIC_API_KEY` ou `ant auth login`).

```bash
npm install
cp .env.example .env
cp agent.config.example.json agent.config.json
docker compose up -d
npm run db:migrate
npm run postmortems:import -- fixtures/postmortems
npm test
```

Os testes do Postgres (`tests/pg-store.test.ts`) só rodam com `TEST_DATABASE_URL`, porque apagam e recriam o schema. Use um banco separado:

```bash
docker compose exec postgres createdb -U sre sre_agent_test
TEST_DATABASE_URL=postgres://sre:sre@localhost:5432/sre_agent_test npm test
```

Investigar um incidente de exemplo (usa o `~/.kube/config` e imprime a mensagem no stdout se não houver `SLACK_BOT_TOKEN`):

```bash
npm run job -- fixtures/incident.sample.json
```

Subir o intake em modo local (roda a investigação no mesmo processo):

```bash
npm run intake
```

Sem Node instalado, rode tudo pelo Docker:

```bash
docker run --rm -v "$PWD":/app -w /app node:22-slim sh -c "npm ci && npm test"
```

## Deploy

- `deploy/Dockerfile`: a mesma imagem serve o intake e o job.
- `deploy/k8s/rbac-readonly.yaml`: a ServiceAccount do job só tem `get`/`list` em pods, logs, eventos, deployments e replicasets. Sem secrets, sem `exec`. O intake só pode criar Jobs no próprio namespace.
- `deploy/k8s/intake.yaml`: Deployment do intake (com a migration como `initContainer`), ConfigMap com o `agent.config.json` e Service.
- Postgres gerenciado (Cloud SQL, RDS...) com `DATABASE_URL` no Secret `sre-agent-secrets`. O usuário do banco só precisa de acesso ao próprio schema.

O webhook precisa enviar os cabeçalhos `x-timestamp` (epoch em segundos) e `x-signature` (`sha256=` + HMAC-SHA256 de `<timestamp>.<corpo>` com `WEBHOOK_SECRET`). Entregas com mais de 5 minutos são recusadas.

## Próximos passos

- [ ] Conjunto de avaliação: incidentes passados com a causa conhecida, para medir se o agente acerta
- [ ] Integrações de mudança: Argo CD e Google Cloud Audit Logs
- [ ] Integração de erros (Sentry ou Rollbar)
- [x] Persistir casos, runs, relatórios e post-mortems (Postgres) e deduplicar entre réplicas do intake
- [ ] Coletar feedback pelo Slack (reação ou botão no relatório) e gravar em `rca_feedback`
- [ ] Adaptadores de payload por fonte (incident.io, PagerDuty, Datadog)
