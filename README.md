# sre-agent

Agente de plantão que investiga incidentes em produção e publica uma **hipótese de causa raiz** no Slack.
Ele só lê: não tem shell, não escreve em arquivo, não altera o cluster. Quem confirma a causa é uma pessoa.

Projeto de estudo de agentes de IA para SRE, em **Go**, construído com o [Anthropic Go SDK](https://github.com/anthropics/anthropic-sdk-go) (Tool Runner) e Claude. Um binário, vários subcomandos, imagem distroless de ~90 MB.

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

    obs["Langfuse ou outro backend OTLP<br/>cada passo do agente"]
    job -- "spans OpenTelemetry" --> obs
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

### Em detalhe

**O caminho de um incidente.** Só o intake fica ligado, e ele não chama o modelo. Em laranja, as chamadas só de leitura; tracejado, o que é condicional.

![O webhook chega ao intake, que deduplica no Postgres e cria um Job pela API do Kubernetes. O Job lê o cluster e o GitHub só com leitura, conversa com a API da Anthropic, registra o run e envia spans; o Slack só recebe fora do modo sombra.](docs/diagramas/fluxo-do-incidente.svg)

**Dentro do job: onde cada coisa roda.** O modelo só decide qual tool chamar; as tools rodam no container do Job, com as credenciais dele. Um relatório que viola as regras volta para o modelo como erro.

![O job envia o prompt; o modelo pede list_pods; o job executa e devolve; o modelo pede rollout_history; o job executa e devolve; o modelo entrega o relatório; o job valida e, se recusar, o erro volta ao modelo.](docs/diagramas/dentro-do-job.svg)

**Os estados de uma investigação.** O relatório é salvo antes da publicação; as linhas tracejadas são as proteções contra duplicidade, garantidas pelo banco.

![O caso é recebido, o Job é disparado e o run entra em RUNNING; com relatório válido ele é salvo, publicado e termina SUCCEEDED. Falhas terminam em FAILED com failure_code. Entrega repetida responde duplicate e um segundo Job do mesmo caso é ignorado.](docs/diagramas/estados-da-investigacao.svg)

Os SVGs seguem o tema claro ou escuro do leitor. A versão navegável dos três está em [`docs/diagramas/sre-agent-por-dentro.html`](docs/diagramas/sre-agent-por-dentro.html).

- **Intake** (`internal/intake/`): o único processo sempre ligado. Autentica o webhook, valida o payload e cria um Kubernetes Job. O nome do Job vem do id do incidente, então o mesmo incidente nunca gera dois runs.
- **Job** (`internal/job/`): um processo por incidente. Conecta as integrações, roda o agente, publica e termina.
- **Agente** (`internal/agent/`): o Tool Runner do SDK cuida do loop. O agente termina chamando `submit_rca_report`, e `internal/report/report.go` impõe as regras do relatório (nunca `CONFIRMED`, confiança limitada quando a mudança só "veio antes", evidência obrigatória).
- **Integrações** (`internal/integrations/`): cada fonte de evidência é um módulo. Ver abaixo.
- **Rastreio** (`internal/telemetry/`): OpenTelemetry padrão. Cada investigação vira um trace com cada chamada ao modelo e cada tool. Ver [Rastreio](#rastreio).
- **Store** (`internal/store/`): todo acesso ao Postgres passa por uma interface (`Store`), com todo o SQL em `pg.go` e uma versão em memória (`memstore`) para os testes. Ver [Persistência](#persistência).

## Integrações

Toda integração implementa o mesmo contrato (`internal/integrations/integrations.go`) e declara de que **tipo de evidência** ela é fonte:

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

1. Crie `internal/integrations/<nome>/<nome>.go` com uma função que devolve `integrations.Integration`. Cada tool é uma struct com tags `jsonschema` (o schema que o modelo vê) e uma função criada com `integrations.NewTool`. Valide os argumentos dentro da função: erro devolvido volta para o modelo corrigir.
2. Registre em `job.Natives` (`internal/job/job.go`).
3. Habilite no `agent.config.json`: `"<nome>": { "type": "native" }`.
4. Escreva o teste ao lado (`<nome>_test.go`). Para Kubernetes use `k8s.io/client-go/kubernetes/fake`; para APIs HTTP, `httptest`.

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
- **Falha com motivo**: `CHECK` exige `failure_code` quando o run é `FAILED` (`no_report`, `refused`, `max_iterations`, `integration_error`, `publish_error`, `model_api_error`, `unknown`). `model_api_error` é a API do modelo recusando ou falhando (sem crédito, chave inválida, limite de taxa), separado de um erro nosso.
- **Relatório antes da mensagem**: o job salva o relatório e só então publica. Se o Slack falhar, o relatório não se perde e o run fica `FAILED` com `publish_error`.

### Custo

Cada run grava os tokens separados como a API devolve (entrada sem cache, leitura de cache, escrita de cache de 5 min e de 1 h, saída) e uma estimativa em dólares (`cost_usd_estimate`) pelo preço de tabela do modelo que de fato respondeu (`internal/pricing`). Modelo sem preço conhecido fica com custo `NULL`, nunca um chute. O custo também vai para o span raiz (`rca.cost_usd_estimate`), para somar por produto ou por dia no backend de rastreio.

A API não devolve preço, só tokens. O valor cobrado de verdade aparece no Console e na API de custos da organização, agregado por dia e por workspace. Para conciliar, dê ao agente um workspace próprio e compare com a soma das estimativas do dia.

Referência: a investigação de teste num cluster kind (OOMKilled depois de um deploy que baixou o limite de memória) fez 5 chamadas ao Claude Opus 5.5 e custou cerca de US$ 0,27.

### Migrations

Arquivos `.sql` em `migrations/`, embutidos no binário (`go:embed`) e aplicados em ordem por `sre-agent migrate` (`internal/db/`). Cada arquivo roda numa transação e fica registrado em `schema_migrations`; um advisory lock evita duas réplicas migrando ao mesmo tempo. **Migration aplicada não se edita**: mudança nova é arquivo novo (`002_...sql`). No Kubernetes, a migration roda como `initContainer` do intake.

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
go run ./cmd/sre-agent import-postmortems fixtures/postmortems
```

A busca usa o full-text search do Postgres em português, com peso maior para título, causa raiz e tags. Busca semântica (`pgvector`) fica para quando a textual não bastar.

## Rastreio

O banco guarda **o que** o agente concluiu. O rastreio mostra **como** ele chegou lá: cada chamada ao modelo, cada tool consultada, quanto tempo e quantos tokens cada passo gastou. Sem isso, quando o agente erra, não dá para saber se faltou uma fonte, se ele ignorou uma evidência ou se a tool devolveu lixo.

### Como é um trace

```mermaid
flowchart TD
    root["rca.investigation<br/>sessão = incidente, tags, status, entrega"]
    loop["anthropic.messages.tool_runner<br/>o loop do agente"]
    m1["anthropic.messages.create<br/>chamada 1: tokens, modelo"]
    t1["anthropic.custom_tool_use list_pods"]
    t2["anthropic.custom_tool_use rollout_history"]
    m2["anthropic.messages.create<br/>chamada 2"]
    t3["anthropic.custom_tool_use submit_rca_report"]
    m3["anthropic.messages.create<br/>chamada final"]
    root --> loop
    loop --> m1 --> t1 --> t2 --> m2 --> t3 --> m3
```

Só o span raiz (`rca.investigation`) é nosso. Os outros vêm prontos do SDK da Anthropic, que segue as convenções `gen_ai.*` do OpenTelemetry. O `trace_id` fica gravado em `investigation_run`, então dá para ir do banco (ou do `sre-agent review`) direto para o raciocínio daquela investigação.

### Por que OpenTelemetry e não o SDK de um fornecedor

O código só fala OpenTelemetry. O destino é escolhido por variável de ambiente, sem mudar uma linha:

| Destino | Configuração |
|---|---|
| Langfuse (cloud ou self-hosted) | `LANGFUSE_PUBLIC_KEY`, `LANGFUSE_SECRET_KEY`, `LANGFUSE_BASE_URL` |
| Qualquer backend OTLP/HTTP | `OTEL_EXPORTER_OTLP_ENDPOINT` (e `OTEL_EXPORTER_OTLP_HEADERS` se pedir autenticação) |
| Jaeger local, sem conta | `docker compose --profile tracing up -d` e `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` |
| Console (debug) | `OTEL_TRACES_EXPORTER=console` |
| Nada | rastreio desligado, sem custo |

Por padrão os spans levam só metadados (ids, tempos, tokens, nomes das tools). Com `TRACE_CONTENT=content` vão também os prompts, as respostas e os argumentos das tools, que são o que permite entender um erro do agente. Ligue só num backend que você controla: logs de produção vão junto.

### O que é o Langfuse

Um backend de observabilidade **feito para aplicações com LLM**, open source, que pode rodar na sua infraestrutura (Docker Compose ou Kubernetes) ou na nuvem deles. Além de mostrar o trace, ele entende o que é uma chamada de modelo:

- **Sessões**: agrupa todas as tentativas do mesmo incidente (`langfuse.session.id`).
- **Custo e tokens** por trace, por modelo e por período.
- **Scores e avaliações**: notas por trace, dadas por pessoas ou por um LLM juiz.
- **Datasets**: transformar traces reais em casos de teste. É o caminho natural para o nosso conjunto de avaliação.

Ele recebe OTLP em `/api/public/otel`, então funciona com o que já está aqui. Os atributos `langfuse.*` do span raiz viram nome, sessão, tags e metadados do trace; em outros backends aparecem como atributos comuns.

### Comparação

| Ferramenta | Tipo | Bom para | Atenção |
|---|---|---|---|
| **Langfuse** | Open source, self-hosted ou cloud | Traces de LLM, sessões, custo, avaliações e datasets no mesmo lugar | O self-hosted completo tem vários serviços (Postgres, ClickHouse, Redis, armazenamento de objetos) |
| **Arize Phoenix** | Open source, roda local | Avaliação e análise de traces, baseado em OpenTelemetry | Menos foco em operação contínua do que em análise |
| **LangSmith** | SaaS (self-hosted só no plano enterprise) | Quem já usa LangChain ou LangGraph | Mais amarrado ao ecossistema LangChain |
| **Braintrust** | SaaS | Avaliação e experimentos com prompts | Foco em avaliação, não em operação |
| **Helicone** | Open source, proxy | Logar chamadas sem mudar código (fica entre a app e a API) | Vê as chamadas ao modelo, não as tools nem o resto da investigação |
| **Datadog / New Relic / Grafana** | APM geral | Ver o agente ao lado do resto da infraestrutura, se o time já usa | Recursos específicos de LLM variam; custo por volume |
| **Jaeger / Grafana Tempo** | Open source, traces genéricos | Ver a árvore de spans sem conta nenhuma | Não entendem custo, sessões nem avaliações |

Para este projeto, a escolha é **Langfuse**: open source, roda de graça na sua máquina ou cluster e já traz datasets e avaliações, que são o próximo passo. Como o código só fala OpenTelemetry, trocar de destino é mudar variável de ambiente.

## Modo sombra e revisão

O agente começa em **modo sombra** (`PUBLISH_MODE=shadow`, o padrão): investiga, salva o relatório e o trace, mas **não publica nada**. O time compara as conclusões com o que de fato aconteceu, sem o agente influenciar o incidente. Quando a taxa de acerto justificar, troque para `PUBLISH_MODE=slack`.

```bash
go run ./cmd/sre-agent review
```

```text
9aec347d-...  INC-1042 (checkout)  PRELIMINARY 70%  entrega=shadow  feedback=—  custo≈US$0.27
  Checkout retornando 502 para parte dos usuários
  causa: Limite de memória do checkout-api reduzido de 1Gi para 512Mi na revisão 42
  https://cloud.langfuse.com/project/<id>/traces/051c6099...
```

Registre se o agente acertou. Quando ele errou, a causa real é obrigatória: é ela que a tool `past_incidents` mostra ao agente na próxima investigação do produto.

```bash
go run ./cmd/sre-agent review feedback <reportId> correct --author ana
go run ./cmd/sre-agent review feedback <reportId> wrong --author ana --cause "Pool de conexões esgotado"
```

## Estrutura do código

```text
cmd/sre-agent/          um binário, subcomandos: intake, job, migrate, review, import-postmortems
internal/
  agent/                loop do agente (Tool Runner), prompt e tool submit_rca_report
  job/                  um incidente do começo ao fim: run, relatório, publicação, span raiz
  intake/               webhook com HMAC, dedupe e disparo do Kubernetes Job
  integrations/         contrato, registro e teto de saída
    kubernetes/         pods, logs, eventos, histórico de rollout (client-go)
    github/             PRs mergeados e deployments (REST)
    history/            post-mortems e incidentes anteriores com feedback
    mcpint/             qualquer servidor MCP, com allowlist
  report/               schema, regras de validação e mensagem do Slack
  store/                interface, Postgres (pgx) e memstore para testes
  telemetry/            OpenTelemetry com destino por ambiente
  config/ incident/ scope/ review/ postmortem/ slackpub/ db/
migrations/             SQL, embutido no binário
deploy/                 Dockerfile e manifests do Kubernetes
```

## Rodando localmente

Pré-requisitos: Go 1.26 (ou só Docker), Docker Compose, acesso a um cluster (pode ser `kind` ou `k3d`) e credencial da Anthropic (`ANTHROPIC_API_KEY` ou `ant auth login`).

```bash
cp .env.example .env
cp agent.config.example.json agent.config.json
docker compose up -d
export DATABASE_URL=postgres://sre:sre@localhost:5432/sre_agent
go run ./cmd/sre-agent migrate
go run ./cmd/sre-agent import-postmortems fixtures/postmortems
go test ./...
```

Os testes do Postgres (`internal/store/pg_test.go`) só rodam com `TEST_DATABASE_URL`, porque apagam e recriam o schema. Use um banco separado:

```bash
docker compose exec postgres createdb -U sre sre_agent_test
TEST_DATABASE_URL=postgres://sre:sre@localhost:5432/sre_agent_test go test ./...
```

Investigar um incidente de exemplo (usa o `~/.kube/config`; em modo sombra só salva, com `PUBLISH_MODE=slack` e sem `SLACK_BOT_TOKEN` imprime a mensagem):

```bash
go run ./cmd/sre-agent job fixtures/incident.sample.json
```

Subir o intake em modo local (roda a investigação no mesmo processo):

```bash
go run ./cmd/sre-agent intake
```

Sem Go instalado, rode os testes pelo Docker:

```bash
docker run --rm -v "$PWD":/src -w /src golang:1.26 go test ./...
```

## Deploy

- `deploy/Dockerfile`: build multi-stage, imagem final distroless (sem shell) com usuário não-root. A mesma imagem serve o intake (padrão) e o job (`sre-agent job`).
- `deploy/k8s/rbac-readonly.yaml`: a ServiceAccount do job só tem `get`/`list` em pods, logs, eventos, deployments e replicasets. Sem secrets, sem `exec`. O intake só pode criar Jobs no próprio namespace.
- `deploy/k8s/intake.yaml`: Deployment do intake (com a migration como `initContainer`), ConfigMap com o `agent.config.json` e Service.
- Postgres gerenciado (Cloud SQL, RDS...) com `DATABASE_URL` no Secret `sre-agent-secrets`. O usuário do banco só precisa de acesso ao próprio schema.

O webhook precisa enviar os cabeçalhos `x-timestamp` (epoch em segundos) e `x-signature` (`sha256=` + HMAC-SHA256 de `<timestamp>.<corpo>` com `WEBHOOK_SECRET`). Entregas com mais de 5 minutos são recusadas.

## Próximos passos

- [ ] Conjunto de avaliação: incidentes passados com a causa conhecida, para medir se o agente acerta
- [ ] Integrações de mudança: Argo CD e Google Cloud Audit Logs
- [ ] Integração de erros (Sentry ou Rollbar)
- [x] Persistir casos, runs, relatórios e post-mortems (Postgres) e deduplicar entre réplicas do intake
- [x] Rastreio com OpenTelemetry (Langfuse ou qualquer backend OTLP) e `trace_id` no banco
- [x] Modo sombra e revisão com feedback pela linha de comando
- [ ] Coletar feedback pelo Slack (reação ou botão no relatório) e gravar em `rca_feedback`
- [ ] Runbooks como contexto (ex.: os playbooks dos monitores do Datadog)
- [ ] Adaptadores de payload por fonte (incident.io, PagerDuty, Datadog)

## Licença

[MIT](LICENSE)
