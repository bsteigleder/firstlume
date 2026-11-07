# Spec 0008: parar um run que está executando

- **Status:** telas aprovadas em 2026-11-06. Implementada
- **Data:** 2026-11-06
- **Depende de:** spec 0004 (apply recurso por recurso), spec 0002 R5.3 (travas soltas no fim da fase)
- **Telas:** quadros 66 e 67 do canvas, aprovados em 2026-11-06

## 1. Contexto

Até aqui só um run na fila podia ser cancelado. Um plan ou apply executando ia até o fim, porque matar o Terraform no meio pode perder o state do que ele acabou de criar.

**Objetivo:** parar um run executando do jeito que o Ctrl+C para o Terraform: ele não começa mais nada, pede para o que está rodando parar, salva o state e solta a trava.

## 2. Regras

- **R1** O mesmo endpoint de hoje, `POST /runs/{id}/cancel`, vale para `queued`, `apply_queued`, `planning` e `applying`. Na fila, o run vira `canceled` na hora, como antes. Executando, o servidor só registra o pedido (`stop_requested_at`, `stop_requested_by`) e o status não muda. Pedir de novo mantém o primeiro pedido. Em `awaiting_approval` continua valendo Discard (409 aqui).
- **R2** Qualquer membro da organização pode parar um run, como pode iniciar e aprovar. O run guarda quem pediu, também quando cancelado na fila.
- **R3** O executor manda heartbeat a cada 5 segundos (era 30). A resposta diz se alguém pediu para parar. O executor escreve no log "Stop requested by X: interrupting the run" e manda SIGINT ao processo em andamento (Terraform, OpenTofu ou Terragrunt, que repassa às units). Os passos que ainda não começaram são marcados como pulados, e nenhum comando novo começa.
- **R4** No apply, o executor não mata o Terraform depois que pediu para parar: espera o tempo que for preciso. No plan, mata depois de 2 minutos, como já fazia. Não há botão de "parar agora".
- **R5** Quando o executor reporta o fim da fase depois de um pedido de parar:
  - um plan termina `canceled`, mesmo que tenha terminado com sucesso (não vai para aprovação nem para o apply);
  - um apply que falhou termina `canceled`;
  - um apply que terminou todas as mudanças antes de parar termina `applied`.
- **R6** As travas do state são soltas pelo trigger que já existe ao sair de `planning`/`applying` (spec 0002 R5.3).
- **R7** Se o executor não responde (agent fora do ar, Job preso em `Pending`), o run fica "Stopping" até o timeout do heartbeat ou da colocação e falha como antes.

## 3. Tela

- Cabeçalho do run: "Cancel run" na fila; "Stop plan" (ou "Stop drift check") no plan, sem confirmação; "Stop apply" em vermelho no apply, com confirmação que conta os aplicados, o que está rodando (com nome e há quanto tempo) e o que não vai começar.
- Enquanto para: badge "Stopping" na página, na lista do stack, na pasta e na gaveta de runs ativos; aviso com quem pediu e quando; cards com "Stopping…" e "Won't start".
- Parado: badge "Canceled"; aviso com quem parou e em qual fase. No plan: nada mudou e a trava foi solta. No apply: quantas mudanças foram aplicadas, cortadas no meio e não começaram. Cards com "Cut halfway" e "Didn't start", e o passo interrompido aparece como parado, não como falha.

## 4. Critérios de aceite

- [x] **CAP1** Run na fila cancelado registra quem cancelou.
- [x] **CAP2** Plan executando: o pedido não muda o status; o heartbeat seguinte avisa o executor; o plan termina `canceled` mesmo com sucesso.
- [x] **CAP3** Apply interrompido termina `canceled`; apply que terminou antes de parar termina `applied`.
- [x] **CAP4** Depois do pedido, nenhum passo novo começa e o comando em andamento recebe SIGINT.
- [x] **CAP5** Ponta a ponta com agent local e Terraform 1.15.6: plan parado no meio de um data source lento; apply parado no meio de um recurso lento, com a trava solta e o plan seguinte mostrando o que sobrou.
- [ ] **CAP6** Terragrunt `run --all` parado no meio (o repasse do sinal está no wrapper, mas não foi testado de ponta a ponta).
