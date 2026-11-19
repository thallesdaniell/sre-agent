---
title: Checkout fora do ar por limite de memória
product: checkout
incident: INC-0981
occurredAt: 2026-08-14
rootCause: Limite de memória do checkout-api reduzido para 512Mi no deploy, sem teste de carga
tags: oom, deploy, memoria, kubernetes
---
## Resumo
Entre 10:12 e 10:47 o checkout respondeu 502 para cerca de 30% das requisições.

## Linha do tempo
- 09:58 deploy da revisão 37 do checkout-api, que reduziu o limite de memória de 1Gi para 512Mi.
- 10:12 pods começam a reiniciar com OOMKilled no pico da manhã.
- 10:40 rollback para a revisão 36.
- 10:47 taxa de erro volta ao normal.

## Causa raiz
O limite de memória foi reduzido para economizar custo sem teste de carga. No pico, o consumo passa de 700Mi.

## O que mudamos
- Teste de carga obrigatório para mudanças de recursos.
- Alerta de restarts por OOMKilled por deployment.
