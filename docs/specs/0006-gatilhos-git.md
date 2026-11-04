# Spec 0006: gatilhos Git (push, pull request e status no Gitea)

- **Status:** aprovada em 2026-11-03, com as propostas da seção 10. Marco G1 implementado, com as telas de push dos quadros 56, 58 e 59
- **Data:** 2026-11-02
- **Depende de:** spec 0001 (conexão Gitea, herança, membros), spec 0002 (tokens de state por fase), spec 0003 (`.firstlume/config.yaml`, Vault)
- **Altera:** spec 0001, R5.2: o token de serviço deixa de ser só leitura para quem usar esta spec (R2.2)
- **Telas:** quadros 56 a 62 do canvas, aprovados em 2026-11-03

## 1. Contexto

Hoje um run só nasce pela UI, pela API ou pelo agendamento de drift. O Firstlume não sabe que um commit chegou. Quem adota precisa manter a pipeline antiga para reagir ao Git, ou lembrar de clicar. Também não há retorno no Git: quem revisa um pull request não vê o plano, e o merge não depende dele.

**Objetivo desta spec:**

1. um push na branch de um stack dispara o run, se o commit mexeu naquele stack;
2. um pull request mostra o plano de cada stack afetado, num comentário e em status, sem nunca aplicar;
3. o merge pode ser bloqueado até o plano passar;
4. sempre dá para ver por que um commit gerou, ou não gerou, um run.

Nada muda para os stacks existentes até alguém ligar um gatilho.

### 1.1 O que o Gitea faz (medido no Gitea 1.27.3)

Estes fatos vieram de um teste contra um Gitea real, com webhook, e decidem partes do desenho.

| # | Fato medido | Consequência |
|---|---|---|
| M1 | O webhook de push traz no máximo 5 commits em `commits`, mesmo com `total_commits: 8` | A lista de arquivos do payload não serve para decidir o que mudou (R4.3) |
| M2 | `GET /repos/{o}/{r}/compare/{a}...{b}` devolve os arquivos em cada commit (`commits[].files`), e devolveu os 70 commits de um teste com 70 | É a fonte dos arquivos de um push |
| M3 | Num force push, `compare/{before}...{after}` só mostra os commits novos. Os descartados aparecem em `compare/{after}...{before}` | Dá para detectar o force push, que passa a afetar todos os stacks (R4.4) |
| M4 | O `ALLOWED_HOST_LIST` padrão do Gitea recusa endereços privados, com o erro "webhook can only call allowed HTTP servers" | Instalação on-prem precisa ajustar `[webhook] ALLOWED_HOST_LIST`. O Firstlume precisa explicar isso e ter a varredura de R9 |
| M5 | A assinatura é `X-Gitea-Signature`, HMAC-SHA256 em hexadecimal do corpo. Cada entrega tem `X-Gitea-Delivery` | Autenticação e deduplicação (R1.2, R1.4) |
| M6 | Eventos e ações vistos: `push`, `pull_request` (`opened`, `synchronized`, `closed`), `create`, `delete`, `issue_comment` | Só os dois primeiros interessam. O comentário do próprio bot também gera evento e precisa ser ignorado |
| M7 | No merge, o `pull_request` `closed` chegou **antes** do `push` | Não depender da ordem entre os dois |
| M8 | `GET /repos/{o}/{r}/commits/{sha}/pull` achou o PR a partir do merge commit | Ligar o run do push ao PR, quando houver |
| M9 | Com token só de leitura, o fetch raso de um SHA e de `refs/pull/{n}/head` funcionou, inclusive em PR de fork. Com a branch apagada, o webhook passou a informar `refs/pull/{n}/head` como origem; o fetch nesse caso não foi testado e entra no G3 | Runs de PR usam `refs/pull/{n}/head` com o commit fixo (R7.3) |
| M10 | PR com `WIP:` no título chega com `draft: true` | Rascunho é reconhecível |
| M11 | Com um status obrigatório na proteção de branch, o merge é recusado se o status não existe ou está `pending`. Ele passa com esse status em `success`, mesmo com **outro** status em `failure` | Exige uma checagem agregada, sempre publicada (R6.3) |

Permissões medidas, com a conta do bot como colaboradora do repositório:

| Ação | Colaborador | Escopo do token |
|---|---|---|
| Clonar, ler arquivos e branches | leitura | `read:repository` |
| Publicar status de commit | **escrita** | `write:repository` |
| Comentar em PR | escrita (testado); numa issue, leitura bastou | `write:issue` |
| Criar webhook no repositório | **admin** | `write:repository` |

## 2. Glossário

| Termo | Significado |
|---|---|
| **Gatilho** | A regra herdada que diz o que fazer num push ou num PR. |
| **Evento** | Uma entrega de webhook aceita, guardada com a decisão tomada para cada stack. |
| **Stack afetado** | Stack cujos arquivos mudaram no commit, pelas regras de R4. |
| **Plano de PR** | Run do tipo `plan` nascido de um PR. Só leitura, fora da fila do stack, nunca aplicado. |
| **Run retido** | Run de PR que só começa quando um membro aprovar a execução (R7.5). |
| **Checagem agregada** | O status `firstlume/plan`, único de um PR inteiro, sempre publicado. |

## 3. Escopo

**Dentro:**
- recepção de webhook do Gitea, com assinatura, deduplicação e processamento em segundo plano;
- gatilhos herdados, desligados por padrão;
- descoberta dos stacks afetados por commit, com caminhos extras declarados no repositório;
- run em push e plano em PR, com substituição do que ficou velho;
- status de commit, checagem agregada e comentário único no PR;
- instalação do webhook, com alternativa manual;
- registro de eventos com o motivo de cada decisão;
- varredura periódica para eventos perdidos.

**Fora (fica para depois):**
- GitHub, GitLab e Forgejo. O tratamento do evento fica atrás de uma interface de provedor, e o Forgejo é o próximo a validar;
- comandos em comentário de PR;
- aplicar antes do merge;
- gatilho por tag ou por label;
- notificações fora do Git (Rocket.Chat, Slack), em spec própria;
- deduzir stacks afetados pelo grafo do Terragrunt ou pelos `source` de módulos. Por enquanto os caminhos são declarados (R4.2);
- papéis e aprovadores por grupo.

## 4. Requisitos

### 4.1 Recepção do webhook

- **R1.1** Nova rota pública: `POST /webhooks/v1/gitea/{connection_id}`. Cada conexão Git tem um segredo próprio, gerado pelo Firstlume e cifrado como os demais.
- **R1.2** A assinatura é verificada em tempo constante, antes de interpretar o corpo. Sem assinatura válida, responde `401` sem detalhes e não grava nada. O corpo tem limite de 5 MiB.
- **R1.3** Evento válido é gravado em `git_events` e recebe `202` na hora. O processamento roda num laço em segundo plano, seguro com várias réplicas (`FOR UPDATE SKIP LOCKED`), como o agendamento de drift. O Gitea nunca espera por chamadas de API.
- **R1.4** `X-Gitea-Delivery` é único por conexão: uma entrega repetida responde `202` e não faz nada. Mesmo com um reenvio sob outro id, R5.3 impede run duplicado.
- **R1.5** Só contam `push` em `refs/heads/*` e `pull_request` com `opened`, `reopened`, `synchronized` ou `closed`. O resto recebe `202` e é descartado sem gravar, inclusive tags, `create`, `delete` e `issue_comment` (M6).
- **R1.6** Do corpo são guardados só os campos usados: repositório, ref, SHAs, ação, número do PR, branch de origem, se é fork, se é rascunho, autor e remetente. O payload inteiro não é guardado.
- **R1.7** O payload não é fonte de confiança para executar nada: o clone sempre usa o repositório gravado no stack, com o token restrito ao host dele. Ref e SHA passam pela validação de sempre. Um repositório fora da conta da conexão é ignorado.

### 4.2 Conexão e webhook

- **R2.1** A instalação do webhook é por **repositório**, sem exigir direitos de dono na organização. O botão "Install webhook" fica na seção Git das configurações da organização, que lista os repositórios com stacks e o estado de cada um: instalado, com falha, sem evento recebido.
- **R2.2** Instalar exige que o bot seja admin do repositório (tabela da seção 1.1). Sem isso, o Firstlume não falha em silêncio: mostra a URL, o segredo e os eventos para instalação manual, além da permissão que faltou. O token só de leitura continua válido para quem não usa gatilhos.
- **R2.3** Os eventos pedidos são `push`, `pull_request` e `pull_request_sync`, tipo `gitea`, `content_type: json`.
- **R2.4** O segredo pode ser trocado. A troca atualiza os webhooks instalados pelo Firstlume e mostra quais precisam de ajuste manual. Ele só é exibido na geração ou troca.
- **R2.5** Ao excluir a conexão, o Firstlume tenta remover os webhooks que instalou. Falhar nisso não impede a exclusão.
- **R2.6** Quando um repositório com gatilho ligado não recebe nada, a tela explica o M4: o que ajustar no `app.ini` do Gitea e como reenviar o teste (`POST .../hooks/{id}/tests`).
- **R2.7** Sem `FIRSTLUME_PUBLIC_URL`, instalar o webhook é recusado com essa explicação.

### 4.3 Gatilhos

- **R3.1** Duas configurações novas, herdadas como as demais (o nível mais próximo vence):

  | Chave | Valores | Padrão |
  |---|---|---|
  | `git_on_push` | `off`, `plan`, `apply` | `off` |
  | `git_on_pull_request` | `off`, `on` | `off` |

- **R3.2** Valem só para stacks clonados pela conexão Git (`connection_id` preenchido) e, em push, só na branch do stack (`ref`).
- **R3.3** `git_on_push: apply` cria um run `apply` comum: plan, aprovação conforme `require_approval`, apply. O gatilho nunca dispensa a aprovação. `plan` cria um run `plan`, sem apply.
- **R3.4** Ligar um gatilho não dispara run. A base é o commit atual da branch, gravado na hora.
- **R3.5** Run `destroy` nunca nasce de gatilho.

### 4.4 Stacks afetados

- **R4.1** Um stack é afetado quando algum arquivo mudou dentro de `path`. Com `path` na raiz, todo arquivo conta.
- **R4.2** O repositório pode declarar mais, em `.firstlume/config.yaml`, com glob `**` relativo à raiz do repositório:

  ```yaml
  triggers:
    paths:                # afeta também quando estes mudam
      - modules/**
      - root.hcl
    ignore:               # não conta quando só estes mudaram
      - "**/*.md"
  ```

  A configuração é lida no commit do evento. `ignore` vale também sobre `path`. Módulos fora de `path` e não declarados **não** disparam run; isso precisa estar escrito no README.
- **R4.3** Os arquivos de um push vêm de `compare/{before}...{after}` (M1, M2). Os de um PR vêm de `GET .../pulls/{n}/files`, com paginação.
- **R4.4** Sem como saber o que mudou, **todos os stacks da branch contam como afetados**, com o motivo registrado:
  - force push: `compare/{after}...{before}` tem commits (M3);
  - branch nova, `before` zerado;
  - falha no compare, ou menos commits devolvidos que `total_commits`.
  Rodar a mais é seguro, porque um plan sem mudanças termina em `no_changes` e o apply continua sujeito à aprovação. Rodar a menos deixaria mudança sem aplicar.
- **R4.5** `.firstlume/config.yaml` inválido ou ilegível no commit conta como afetado, para o run falhar e mostrar o erro em vez de sumir.
- **R4.6** Branch apagada (`after` zerado) é ignorada.

### 4.5 Run por push

- **R5.1** O run fica preso ao commit do evento: `ref` é a branch, `commit_sha` é o `after`. Um commit posterior não entra nele.
- **R5.2** `created_by` é o login do remetente no Gitea, e o run ganha `trigger` (seção 5). Se o commit vier de um PR (M8), `trigger.pull_request` é preenchido. Falhar nessa consulta não impede o run.
- **R5.3** No máximo um run por stack e push, identificado pelos commits de antes e de depois. Um reenvio não cria outro run. Voltar a um commit antigo com force push é um push novo e cria run, porque a branch mudou de novo.
- **R5.4** **Substituição.** Um push novo na branch:
  - cancela runs de gatilho em `queued` no mesmo stack;
  - descarta runs de gatilho em `awaiting_approval`, com a mensagem "superseded by <sha>", porque esse run seguraria a fila do novo (D2);
  - não mexe em quem está executando, nem em runs manuais. Esses terminam, e o novo espera na fila.
- **R5.5** Se o push não afeta o stack, nada é criado, e o evento registra o motivo.

### 4.6 Status de commit

- **R6.1** Só o servidor publica no Gitea. O token de escrita nunca vai ao executor.
- **R6.2** Cada run de gatilho publica no commit o contexto `firstlume/<pastas>/<stack>`:

  | Run | Estado | Descrição (exemplo) |
  |---|---|---|
  | `queued`, `planning`, `apply_queued`, `applying` | `pending` | "Planning" |
  | `awaiting_approval` | `pending` | "+2 ~1 -1, waiting for approval" |
  | retido | `pending` | "Waiting for a member to start it" |
  | `no_changes` | `success` | "No changes" |
  | `planned` | `success` | "+2 ~1 -1" |
  | `applied` | `success` | "Applied +2 ~1 -1" |
  | `failed` | `failure` | "Plan failed" |
  | `discarded` | `error` | "Plan discarded" |
  | `canceled` | `error` | "Canceled" |

  `target_url` é a página do run.
- **R6.3** **Checagem agregada `firstlume/plan`.** Publicada em **todo** PR de um repositório com stack em `git_on_pull_request: on`, mesmo sem stack afetado (`success`, "No stacks affected"). Sem isso, um PR que não toca em stack nenhum ficaria sem merge se o status for obrigatório (M11). Regra: algum `failure` ou plano descartado dá `failure`; senão, algo pendente dá `pending`; senão, `success`. Um plano retido que um membro descarta não rodou, então o PR não pode ter merge como se tivesse passado. É este contexto que se marca como obrigatório na proteção de branch, porque os demais dependem de quais stacks foram afetados.
- **R6.4** Só o commit mais novo do PR recebe a agregada. Um run velho que termina atualiza só o contexto dele, no commit dele.
- **R6.5** As publicações ficam numa fila no banco e são repetidas com espera crescente por até 24 horas. Falhar em publicar não afeta o run. A falha aparece no evento e na seção Git da organização, com a permissão que falta.

### 4.7 Plano de PR

- **R7.1** Um PR `opened`, `reopened` ou `synchronized` cria um run `plan` em cada stack afetado cuja `ref` seja a branch de destino do PR e com `git_on_pull_request: on`.
- **R7.2** **Fora da fila do stack.** Um plano de PR não espera lock de ninguém, nem bloqueia runs, drift, mover ou renomear. Quando há agents ocupados, runs comuns têm prioridade.
- **R7.3** O checkout usa `refs/pull/{n}/head` com o commit fixo (M9), então também funciona em fork. O token nunca vai para o repositório do fork.
- **R7.4** **Só leitura e sem guardar plano.** A credencial de state lê e não trava (`-lock=false`), e o arquivo de plano não é guardado, como no drift. A lista de mudanças é guardada para a revisão. A tela não oferece aprovar nem aplicar, e a API recusa.
- **R7.5** **Quem pode rodar sem clique.** Um plan executa código do repositório com as credenciais do stack. Hoje só um membro da organização consegue fazer isso, escolhendo a `ref` de um run. Para não ampliar esse poder:
  - o plano roda sozinho se **o autor do PR e o remetente do evento** forem membros da organização, por login do Gitea, incluindo convites pendentes;
  - nos outros casos, inclusive em **todo fork**, o run fica **retido**. O comentário e o status dizem isso, e um membro decide na página do run, com o commit e o autor à vista, se inicia ou descarta;
  - o remetente conta porque alguém de fora pode empurrar commit no PR de um membro.
- **R7.6** O JWT do Vault ganha `trigger` (`manual`, `drift`, `push`, `pull_request`), `ref` e, no PR, `pull_request`. Assim um papel do Vault pode recusar segredos de produção em planos de PR. O README traz o exemplo.
- **R7.7** **Substituição.** Commit novo no PR cancela o plano em fila ou retido do commit anterior, no mesmo stack. Se estiver executando, ele termina, mas só o mais novo aparece no comentário.
- **R7.8** PR fechado ou com merge cancela os planos em fila e retidos dele. O apply do merge vem do evento de push, em qualquer ordem de chegada (M7).
- **R7.9** Rascunhos são tratados como os demais (D3).

### 4.8 Comentário no PR

- **R8.1** Um único comentário por PR, identificado pelo id gravado e editado a cada mudança. Ele só existe se algum stack foi afetado.
- **R8.2** Traz uma linha por stack: nome com link para o run, estado e `+ ~ ± -`. Abaixo, os recursos apagados e recriados por stack, até 20 no total, com "and N more".
- **R8.3** **Nunca traz atributos nem valores.** Só endereço e nome da lista permitida de `internal/reslabel`. O texto do erro não entra, só o link.
- **R8.4** Tudo o que vem do repositório (endereços, nomes) sai escapado, para o código de um PR não injetar Markdown ou links no comentário do bot.
- **R8.5** Em inglês, como a UI.

### 4.9 Eventos perdidos

- **R9.1** A cada 5 minutos, para cada branch com stack em `git_on_push` diferente de `off`, o Firstlume compara o commit atual com o último visto (`GET .../branches/{b}`, uma vez por repositório e branch). Se diferir, processa como um push, registrado com a origem `sweep`.
- **R9.2** Isso cobre o Firstlume fora do ar e o webhook bloqueado por M4. Com a varredura, um repositório funciona até sem webhook, só que com até 5 minutos de atraso e sem PRs.
- **R9.3** Um PR perdido não é varrido. O registro de eventos mostra o que chegou.

### 4.10 Registro de eventos

- **R10.1** A API e a tela listam os eventos recentes, por organização e por stack: quando, repositório, tipo, ref, commit, remetente e, **por stack**, o resultado:
  `run_created`, `not_affected` (com os arquivos considerados), `trigger_off`, `other_branch`, `duplicate` (o push já tinha run), `superseded`, `held` ou `error`.
- **R10.2** A página do stack mostra a última decisão. É a resposta a "por que não rodou?".
- **R10.3** Eventos ficam guardados por 30 dias.

## 5. API

| Rota | O que muda |
|---|---|
| `POST /webhooks/v1/gitea/{connection_id}` | nova, autenticada pela assinatura |
| `GET .../orgs/{org}/git/webhooks` | repositórios com stacks e estado do webhook |
| `POST .../orgs/{org}/git/webhooks/{owner}/{repo}` | instala; sem permissão, devolve os dados para instalação manual |
| `POST .../orgs/{org}/git/webhook-secret` | troca o segredo e o devolve uma única vez |
| `GET .../orgs/{org}/git/events` | registro de eventos, com filtro `?stack=` |
| `POST .../orgs/{org}/runs/{id}/release` | membro inicia um run retido; `discard` continua descartando |
| `GET`/`PUT .../settings` | `git_on_push`, `git_on_pull_request` |
| `GET .../runs/{id}` | ganha `trigger`: `{kind, actor, event_id, before_sha, pull_request, pull_request_url, head_ref, fork}` |

- Novo estado de run: `held`. Ele não segura o lock do stack.
- Novos `trigger.kind`: `manual`, `drift`, `push`, `pull_request`, `sweep`. Runs antigos são lidos como `manual` ou `drift`.

Migração `00017_git_triggers.sql`: segredo e estado do webhook na conexão, `git_events` e o resultado por stack, fila de publicações, comentário por PR, último commit visto por repositório e branch, configurações novas e colunas de gatilho em `runs`, com um índice único para R5.3.

Pontos do código existente que precisam mudar: `activeStatuses`, `ClaimRun` e `ScheduleDriftRuns` precisam deixar planos de PR de fora do lock (R7.2), e `validateRef` precisa aceitar `refs/pull/{n}/head`.

## 6. Telas

Desenhadas nos quadros 56 a 62 do canvas, nesta ordem:

1. **Configurações herdadas:** os dois gatilhos, de onde vêm, com o aviso de que quem pode dar push na branch executa código com as credenciais do stack.
2. **Seção Git da organização:** repositórios, estado do webhook, instalar, instalação manual, trocar segredo, permissões que faltam, ajuda do M4.
3. **Registro de eventos**, da organização e do stack, com o resultado por stack.
4. **Run nascido de gatilho:** "push by …" ou "PR #12", link para o PR, e como aparece na lista de runs.
5. **Plano de PR:** a revisão por recurso, sem aprovar, com o aviso "This plan is a preview. It will not be applied".
6. **Run retido:** quem é o autor, qual o commit, o aviso sobre as credenciais, "Run plan" e "Discard".
7. **Comentário no PR**, como ficará no Gitea.

## 7. Ambiente de desenvolvimento

- `make dev-gitea` liga `GITEA__webhook__ALLOWED_HOST_LIST=private` e dá ao bot as permissões da seção 1.1. Um segundo bot só de leitura serve para testar R2.2.
- O server de dev precisa ser alcançável pelo container. Conferir no primeiro marco se basta `host.docker.internal` com o server em `127.0.0.1`.
- Os payloads reais de cada cenário ficam em `testdata`.

## 8. Entregas e critérios de aceite

Os testes usam um Gitea real e Terraform real. Ordem pensada para entregar o caminho push → plan → aprovação → apply primeiro.

### Marco G1: push dispara run

- [x] **CAG1.1** Um webhook com assinatura inválida ou ausente recebe `401` e não grava nada. Com assinatura válida recebe `202`, e a mesma entrega repetida não duplica nada.
- [x] **CAG1.2** Com gatilhos em `off`, nada muda em nenhum stack existente. Ligar um gatilho não cria run.
- [x] **CAG1.3** `git_on_push` definido numa pasta vale para os stacks abaixo dela, e o stack pode sobrescrever.
- [x] **CAG1.4** Num repositório com dois stacks, um push num só cria run só nele. O outro registra `not_affected` com os arquivos considerados.
- [x] **CAG1.5** Num push com 8 commits em que só o primeiro toca o stack, o run é criado. Esse é o caso que o payload esconde (M1).
- [x] **CAG1.6** `triggers.paths` dispara o stack por um arquivo fora de `path`. `triggers.ignore` impede o run num commit só de `.md`.
- [x] **CAG1.7** Force push, branch nova e falha simulada no compare afetam todos os stacks da branch, com o motivo no evento.
- [x] **CAG1.8** Tag, branch apagada e outra branch não criam run.
- [x] **CAG1.9** `git_on_push: apply` com `require_approval` para em `awaiting_approval`, no commit do evento. Aprovar aplica exatamente aquele commit.
- [x] **CAG1.10** Dois pushes seguidos: o `queued` é cancelado, o `awaiting_approval` é descartado com "superseded by", o que executa termina e o run manual fica intacto.
- [x] **CAG1.11** `GET .../git/events` mostra o resultado por stack.

### Marco G2: status e instalação

- [ ] **CAG2.1** Com um bot admin, "Install webhook" cria o webhook, e um push real chega assinado.
- [ ] **CAG2.2** Sem permissão de admin, a API devolve URL, segredo e eventos, e a instalação manual funciona.
- [ ] **CAG2.3** Cada transição da tabela de R6.2 chega ao Gitea com o contexto e o link certos.
- [ ] **CAG2.4** Com um token só de leitura, o run acontece normalmente, e a falha de status aparece com a permissão que falta.
- [ ] **CAG2.5** Com o Gitea fora do ar, a publicação é repetida e chega quando ele volta.
- [ ] **CAG2.6** Nenhum run recebe o token de escrita: conferido no spec que o executor recebe.
- [ ] **CAG2.7** Trocar o segredo faz a assinatura antiga ser recusada, e os webhooks instalados continuam funcionando.

### Marco G3: plano de PR

- [ ] **CAG3.1** Um PR de um membro roda o plano nos stacks afetados, e não nos outros, e cria um único comentário.
- [ ] **CAG3.2** Com um run `awaiting_approval` segurando o stack, o plano de PR roda assim mesmo. Durante ele, um apply no stack também roda.
- [ ] **CAG3.3** Um plano de PR não trava nem grava o state, e tentar gravar com a credencial dele é recusado.
- [ ] **CAG3.4** A API recusa aprovar um plano de PR.
- [ ] **CAG3.5** PR de quem não é membro e PR de fork ficam `held`, sem nenhum Job criado. "Run plan", feito por um membro, roda. O token do Git nunca chega ao host do fork.
- [ ] **CAG3.6** Um não membro que empurra commit no PR de um membro deixa o novo plano `held`.
- [ ] **CAG3.7** Um commit novo substitui o plano em fila ou retido, e o comentário mostra só o mais novo. Um plano retido ainda pode ser iniciado depois de a branch do PR ser apagada.
- [ ] **CAG3.8** Com `firstlume/plan` obrigatório na proteção de branch: um PR sem stack afetado pode ter merge; com plano pendente, com plano falho ou retido, não pode; com tudo certo, pode.
- [ ] **CAG3.9** O merge cria o apply e cancela o que restou do PR, com os eventos chegando nas duas ordens.
- [ ] **CAG3.10** Um valor sensível do plano não aparece no comentário, nem em status. Um nome de recurso com Markdown sai escapado.
- [ ] **CAG3.11** Um papel do Vault com `bound_claims` em `trigger` recusa o plano de PR, com o erro claro de sempre.

### Marco G4: varredura

- [ ] **CAG4.1** Com o webhook bloqueado (M4), um push gera o run em até 5 minutos, com a origem `sweep`.
- [ ] **CAG4.2** Webhook e varredura vendo o mesmo commit criam um único run, com réplicas ao mesmo tempo.

### Marco G5: telas

- [ ] **CAG5.1** As telas seguem os quadros aprovados (seção 6).
- [ ] **CAG5.2** Um run manual e um de drift aparecem como hoje.

### Critérios gerais

- [ ] **CGG1** `make vet`, os testes unitários, os de integração e o `tsc` passam.
- [ ] **CGG2** README: como ligar, permissões do bot, `ALLOWED_HOST_LIST`, `triggers`, proteção de branch com `firstlume/plan`, o aviso sobre credenciais e o exemplo do Vault.
- [ ] **CGG3** Nada de dados reais da empresa no repositório público.

## 9. Riscos

- **Quem dá push executa código.** Com `git_on_push` ligado, quem escreve na branch roda código com as credenciais do stack já no plan, antes de qualquer aprovação. Isso é igual a qualquer CI, mas precisa estar dito: use proteção de branch. A tela e o README avisam.
- **Módulos fora de `path`** não disparam sem declaração (R4.2). Isso pode deixar mudança sem aplicar. O drift agendado acaba mostrando a diferença.
- **Plano de PR sem lock** pode ler um state no meio de um apply. Como nunca é aplicado, o pior caso é uma prévia velha, corrigida no próximo commit.

## 10. Decisões pendentes

| # | Pergunta | Proposta |
|---|---|---|
| D1 | Qual o padrão dos gatilhos? | `off` nos dois, para nada rodar sozinho numa instalação existente. |
| D2 | Um push novo descarta sozinho um plano aguardando aprovação? | **Sim, só de runs nascidos de gatilho.** Sem isso, o novo commit espera alguém descartar o velho. A alternativa é manter e deixar aprovar um plano sabidamente velho. |
| D3 | Rascunhos rodam plano? | **Sim.** O autor costuma querer ver o plano cedo. Se custar caro, vira opção depois. |
| D4 | Numa instalação sem login, não há membros. O que fazer? | **Todo plano de PR fica retido**, à espera de um administrador. É seguro. Para rodar sozinho, é preciso ligar o login, ou criar uma lista de logins confiáveis numa emenda. |
| D5 | O contexto por stack usa pastas e nome, e muda ao renomear ou mover. Isso importa? | **Não**, porque só `firstlume/plan` é pensado para ser obrigatório. Usar id seria estável, porém ilegível no Gitea. |
| D6 | Um limite de runs por evento? | **Não no G1.** Um commit em `modules/**` pode disparar muitos stacks, e a fila serializa por stack. Só agents ocupados sentem. Medir antes. |
| D7 | Varredura para todo stack, ou só onde o webhook falhou? | **Para todos com `git_on_push`.** A consulta é uma por repositório e branch. É mais simples e cobre o Firstlume fora do ar. |
