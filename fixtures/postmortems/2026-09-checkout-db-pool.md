---
title: Lentidão no checkout por esgotamento do pool de conexões
product: checkout
incident: INC-1007
occurredAt: 2026-09-22
rootCause: Pool de conexões com o banco esgotado após aumento de réplicas sem ajustar max_connections
tags: banco, conexoes, latencia, escala
---
## Resumo
Latência p99 do checkout subiu de 300 ms para 9 s por 25 minutos.

## Causa raiz
O HPA subiu o deployment de 6 para 14 réplicas. Cada réplica abre 20 conexões, e o banco aceita 200.
As réplicas novas ficaram esperando conexão e as requisições estouraram o timeout.

## O que mudamos
- PgBouncer na frente do banco.
- Limite de réplicas do HPA ligado à capacidade de conexões.
