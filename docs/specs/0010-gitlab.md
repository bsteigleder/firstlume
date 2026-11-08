# Spec 0010: GitLab como provedor Git

- **Status:** aprovada em 2026-11-07, com as propostas da seção 10. Implementada na branch `gitlab`; conferida em 2026-11-08 contra um GitLab CE 19.5.3 real (conexão, plan e apply, comando, importação, push, merge request, teste do webhook e login)
- **Data:** 2026-11-07
- **Depende de:** spec 0001 (conexão Git, descoberta de `.firstlume`, login OIDC, membros), spec 0006 (gatilho por push, registro de eventos)
- **Altera:** spec 0001, R5.1 (o provedor deixa de ser só Gitea); spec 0006, R1.1 (nova rota de webhook)
- **Telas:** quadros 71 a 73 do canvas, aprovados em 2026-11-07

## 1. Contexto

Hoje o Firstlume só fala com o Gitea. O roadmap do README promete GitLab no item 6, e as notas de divulgação dizem que só Gitea é um dos bloqueios para usar o Firstlume fora da empresa. O GitLab é o provedor mais comum em infra on-prem depois do próprio Gitea, nas versões gitlab.com e self-managed.

**Objetivo desta spec:** quem usa GitLab tem o mesmo que quem usa Gitea tem hoje:

1. conectar uma organização a um grupo ou usuário do GitLab, com um token de serviço;
2. criar stacks a partir dos `.firstlume` encontrados nos projetos, inclusive em subgrupos;
3. clonar nos runs, rodar comandos, identificar states na importação;
4. disparar runs por push (spec 0006, Marco G1), com o mesmo registro de eventos;
5. entrar no Firstlume com a conta do GitLab, usando os grupos para admin e acesso;
6. ver links de commit, arquivo e merge request que abrem no GitLab.

Plano em merge request, status de commit e comentário (Marcos G2 e G3 da spec 0006) **não** entram aqui: eles ainda não existem nem para o Gitea. Esta spec cria a interface de provedor que G2 e G3 vão usar, e eles saem para os dois provedores juntos (D1).

### 1.1 O que o GitLab faz (medido no GitLab CE 19.5.3)

Medido em 2026-11-07 num GitLab descartável, com grupo `acme`, subgrupos `acme/platform` e `acme/platform/network`, um bot com papel Reporter em `acme/platform` e pushes reais, como os fatos do Gitea na spec 0006.

| # | Fato medido | Consequência |
|---|---|---|
| L1 | `GET /namespaces/{caminho}` devolve `kind: group` ou `kind: user` e o `full_path`. O nome curto de um subgrupo (`platform`) não é encontrado | Uma chamada descobre o tipo da conta. A tela pede o caminho completo (R2.3) |
| L2 | `GET /groups/{caminho}/projects?include_subgroups=true` lista os projetos de todos os níveis. Por padrão vem com os arquivados e com projetos **de fora** compartilhados com o grupo (`with_shared=true`): um projeto de `acme/outside` compartilhado apareceu em `acme/platform` | Usar `archived=false&with_shared=false` (R2.4) |
| L3 | Um token só com `read_repository` clona, mas a API responde `403 insufficient_scope`. Token inválido dá `401` | O teste da conexão diz qual escopo falta (R2.5) |
| L4 | Clonar com `Authorization: Basic` de `x-access-token:<token>` funciona, com `read_api` + `read_repository` e também só com `read_repository` | O executor não muda (R4.1) |
| L5 | `GET /projects/{caminho}/repository/tree?recursive=true&per_page=1000` devolveu 252 entradas numa página, com `X-Total` | A descoberta lê a árvore como no Gitea (R1.2) |
| L6 | `GET .../repository/files/{caminho}/raw?ref=` devolve o arquivo; inexistente dá `404 File Not Found`. Projeto fora do alcance do token dá `404 Project Not Found` | Mesmos erros de hoje (R1.3) |
| L7 | O push hook traz no máximo **20** commits em `commits`, com `total_commits_count: 25` num push de 25 | Como no Gitea (M1), a lista do payload não serve para achar os arquivos (R5.6) |
| L8 | `GET .../repository/compare?from=&to=` devolve os arquivos já somados em `diffs`, não commit a commit, e achou o arquivo do primeiro dos 25 commits. Ele para em **1000 arquivos** (`diff_max_files`) sem avisar: um commit de 1100 arquivos voltou com 1000 e nenhum campo de estouro. `compare_timeout` existe | Com 1000 arquivos ou mais, ou com `compare_timeout`, o compare conta como incompleto (R5.6) |
| L9 | Force push: o payload não tem campo que o indique. `compare` de `after` para `before` lista os commits descartados, como no Gitea (M3) | Detecção igual à do Gitea (R5.7) |
| L10 | Branch nova chega com `before` zerado. Branch apagada, com `after` zerado e `checkout_sha: null`. Tag chega como `X-Gitlab-Event: Tag Push Hook` | Mesmas regras da spec 0006 (R5.3) |
| L11 | Cabeçalhos de toda entrega: `X-Gitlab-Event`, `X-Gitlab-Token` (o "Secret token", em claro), `X-Gitlab-Event-UUID`, `Idempotency-Key`, `webhook-id` (igual ao `Idempotency-Key`), `webhook-timestamp`, `X-Gitlab-Instance` | |
| L12 | Reenviar uma entrega pela API manteve o `Idempotency-Key` e trocou o `X-Gitlab-Event-UUID` | A deduplicação usa o `Idempotency-Key` (R5.4) |
| L13 | O GitLab 19 tem **signing token** (coluna criada em abril de 2026): com ele, cada entrega traz `webhook-signature: v1,<base64>`, HMAC-SHA256 de `<webhook-id>.<webhook-timestamp>.<corpo>`, o formato do Standard Webhooks. A assinatura conferiu com a chave. O token precisa ser exatamente `whsec_` + base64 de 32 bytes | Autenticação com assinatura e proteção contra repetição, como a do Gitea (R5.2) |
| L14 | O teste de push (`POST .../hooks/{id}/test/push_events`, o botão Test) manda os 3 últimos commits da branch padrão, com `before` e `after` reais e cabeçalhos iguais aos de um push. Não dá para diferenciar | Sem cuidado, o teste cria run de verdade (R5.9) |
| L15 | Criar webhook para um endereço da rede local é recusado na criação ("Invalid url given" pela API). Com o host na allowlist de Admin › Settings › Network › Outbound requests, passa | Explicar isso no cartão (R5.11) |
| L16 | Webhook de grupo dá `404` no CE (é dos planos pagos). Um bot Reporter com `read_api` não cria webhook de projeto (`403`, pede `api`) | Webhook manual por projeto agora; instalar pelo bot fica para o G2, com mais permissão |
| L17 | `GET .../repository/commits/{sha}/merge_requests` achou o MR pelo merge commit, com `web_url` e `references.full` (`acme/platform/infra!1`) | Link do MR no run (R5.8) |
| L18 | O discovery OIDC tem `issuer` sem barra no fim, escopos `openid profile email` e os claims `groups` e `groups_direct` | Login com o cliente atual (R6). Os valores dos grupos são conferidos no L4 com um login real |

## 2. Glossário

| Termo | Significado |
|---|---|
| **Provedor** | Gitea ou GitLab. Fica gravado na conexão da organização e não muda enquanto houver stacks. |
| **Conta** | No Gitea, organização ou usuário. No GitLab, **grupo** (com subgrupos) ou usuário, pelo caminho completo (`acme` ou `acme/platform`). |
| **Repositório** | No GitLab, **projeto**. É identificado pelo caminho completo (`acme/platform/infra`), que pode ter vários níveis. |
| **Pull request** | No GitLab, **merge request** (MR). A tela usa o nome de cada provedor. |

## 3. Escopo

**Dentro:**
- interface de provedor no servidor, com Gitea e GitLab atrás dela, sem mudar o comportamento do Gitea;
- conexão com gitlab.com ou GitLab self-managed, por grupo (com subgrupos) ou usuário;
- descoberta de `.firstlume`, branches, leitura de arquivos e clone nos runs;
- webhook de push do GitLab entrando no mesmo pipeline de eventos da spec 0006;
- login pelo GitLab com o cliente OIDC que já existe, com os textos da tela usando o nome do provedor;
- links de commit, arquivo, branch e MR no formato do GitLab;
- README: conexão, token, webhook, login e o modo "só state" a partir do GitLab CI.

**Fora (fica para depois):**
- plano em MR, status de commit e comentário (spec 0006, G2 e G3, para os dois provedores);
- instalação automática do webhook (G2);
- webhook de grupo, que no GitLab é só dos planos pagos (L16). Quem tiver um pode usar a mesma URL e o mesmo segredo;
- GitHub;
- mais de uma conexão Git por organização;
- clone por SSH;
- importar pipelines do GitLab CI para stacks.

## 4. Requisitos

### 4.1 Interface de provedor

- **R1.1** O servidor deixa de chamar o cliente do Gitea direto. Um pacote `internal/gitprovider` define o que um provedor faz: checar a conta, listar repositórios e branches, resolver branch, ler a árvore e um arquivo num commit, comparar dois commits e achar o PR/MR de um commit. Gitea e GitLab implementam esse contrato.
- **R1.2** A descoberta de `.firstlume` (hoje `gitea.Discover`) passa a funcionar sobre qualquer provedor, com os mesmos limites: 200 pastas por repositório, 200 mil entradas na árvore, arquivo de até 256 KiB.
- **R1.3** Erros do provedor viram os mesmos erros de hoje (token recusado, não encontrado, árvore grande demais), e as mensagens dizem o nome do provedor ("GitLab rejected the service token…").
- **R1.4** Para o Gitea nada muda: mesmas rotas, mesmas respostas, mesmos testes passando.

### 4.2 Conexão

- **R2.1** `provider` aceita `gitea` e `gitlab`. Migração **00021** amplia o `CHECK` de `git_connections.provider`.
- **R2.2** URL: `https://gitlab.com` por padrão na tela, ou a de um self-managed. A URL pode ter caminho (`https://host/gitlab`), e o Firstlume usa `<url>/api/v4`. `http://` vale como no Gitea: o administrador escolheu.
- **R2.3** Conta é o caminho completo de um grupo (`acme`, `acme/platform`) ou um nome de usuário. O Firstlume descobre qual é por `GET /namespaces/{caminho}` (L1). Caminho de grupo aceita até 20 níveis, com as regras de nome do GitLab.
- **R2.4** Os repositórios da conta são os projetos do grupo **e dos subgrupos**, sem os arquivados e sem projetos de fora compartilhados com o grupo (`archived=false&with_shared=false`, L2). Para um usuário, os projetos dele. O nome mostrado é o caminho completo.
- **R2.5** Token de serviço: token de acesso pessoal de uma conta bot, token de grupo ou de projeto, com escopos `read_api` e `read_repository` e papel Reporter no grupo. A tela e o README dizem isso. Um token sem `read_api` dá um erro que nomeia o escopo (L3). Nenhum escopo de escrita é pedido nesta spec.
- **R2.6** Trocar o provedor de uma conexão com stacks é recusado, como hoje trocar a conta: é preciso desconectar antes.

### 4.3 Repositórios com caminho de vários níveis

- **R3.1** As rotas `GET /git/repos/{owner}/{repo}/branches` e `.../discover` continuam para o Gitea. Rotas novas, para os dois provedores, recebem o repositório inteiro: `GET /git/repo/branches?repo=acme/platform/infra` e `GET /git/repo/discover?repo=…&ref=…`. A UI passa a usar as novas.
- **R3.2** `git_repository` ao criar stack aceita o caminho completo. Ele precisa estar dentro da conta: igual a `<conta>/<projeto>` ou começar por `<conta>/` seguido de subgrupos, sem `..`, sem barras duplas, cada parte com as regras de nome do GitLab.
- **R3.3** O nome do repositório de um stack sai da URL de clone gravada, tirando o caminho da URL base da conexão (para self-managed em subcaminho) e o `.git`. Hoje isso supõe `host/dono/nome`.

### 4.4 Clone, comando e importação

- **R4.1** O executor clona com o token do jeito de hoje (Basic com usuário fixo e o token como senha), que o GitLab aceita (L4). Nada muda no executor.
- **R4.2** Run de comando (spec 0009) e importação de state (spec 0002, R9) usam o provedor da conexão para resolver a branch e ler as units.

### 4.5 Webhook de push

- **R5.1** Rota pública nova: `POST /webhooks/v1/gitlab/{connection_id}`. O segredo é o da conexão (spec 0006, R1.1). Numa conexão GitLab ele é gerado no formato que o GitLab aceita como signing token: `whsec_` + base64 de 32 bytes (L13). Trocar o provedor de uma conexão apaga o segredo.
- **R5.2** A autenticação aceita, antes de ler o JSON e em tempo constante, uma de duas formas:
  - **assinatura** (GitLab 19 ou mais novo, recomendada): `webhook-signature` confere com o HMAC de `webhook-id.webhook-timestamp.corpo` (L13), e `webhook-timestamp` está a no máximo 5 minutos do relógio do servidor;
  - **token** (GitLab mais antigo): `X-Gitlab-Token` igual ao segredo. Ele viaja em claro em cada entrega, por isso o cartão só o sugere quando o signing token não existe.
  Sem nenhuma das duas, `401` sem detalhes e nada gravado. Limite de 5 MiB, como o Gitea.
- **R5.3** Só conta `X-Gitlab-Event: Push Hook` em `refs/heads/*` (L10). Tag Push Hook, Merge Request Hook e os outros recebem `202` e são descartados sem gravar. MR entra no G3.
- **R5.4** A deduplicação usa o `Idempotency-Key`, que se mantém num reenvio (L12). O `X-Gitlab-Event-UUID` muda a cada tentativa e não serve. Sem `Idempotency-Key`, o hash do corpo, como no Gitea.
- **R5.5** O projeto do evento (`project.path_with_namespace`) precisa estar dentro da conta, pelas regras de R3.2. Fora dela, descarta.
- **R5.6** Os arquivos do push vêm da API de compare do GitLab, nunca da lista de commits do payload (L7). O compare conta como **incompleto** com 1000 arquivos ou mais, ou com `compare_timeout` (L8). Incompleto ou com falha afeta todos os stacks da branch, com o motivo no evento, como no Gitea.
- **R5.7** Force push é detectado como no Gitea, comparando ao contrário (L9). Branch nova e branch apagada seguem a spec 0006, R4.
- **R5.8** O run de um merge acha o MR pelo commit (L17) e mostra o link, como o PR do Gitea.
- **R5.9** Um push que não reescreve histórico, com commit final (`after`) que já teve decisão para um stack a partir de outro `before`, não cria outro run nele: o evento registra "already handled". Isso cobre o teste do GitLab, que repete o último push com SHAs reais (L14), e o teste do Gitea, que hoje cria run de verdade. A mesma entrega reenviada continua "duplicate", com o run dela, e um force push de volta a um commit antigo continua sendo um push novo (spec 0006, CAG1.10). Para rodar de novo o mesmo commit, existe o botão de run.
- **R5.10** Hoje o segredo só é gerado pela API (o G1 não teve tela de webhook, que ficou para o G2, quadro 57). Esta spec traz um cartão **Webhook** na seção Git da organização, para os dois provedores: gerar ou trocar o segredo (mostrado só nessa hora), a URL de destino e os passos do provedor da conexão. É a instalação manual do quadro 57; o G2 acrescenta a tabela por repositório e o botão de instalar.
- **R5.11** Os passos do GitLab: Settings › Webhooks do projeto, URL, o segredo em **Signing token** (ou em Secret token num GitLab sem signing token), só "Push events", filtro de branch opcional e SSL. O cartão também explica o bloqueio de rede local do GitLab self-managed (L15): pôr o host do Firstlume na allowlist de Admin › Settings › Network › Outbound requests. Os do Gitea: os do quadro 57, com o `ALLOWED_HOST_LIST`.

### 4.6 Login pelo GitLab

- **R6.1** O cliente OIDC que existe serve ao GitLab sem mudança de protocolo: issuer é a URL do GitLab, app criado em Admin › Applications (ou no grupo), com os escopos `openid profile email`.
- **R6.2** Os grupos vêm do claim `groups` do userinfo (L18), que a documentação do GitLab descreve com caminhos completos (`acme/devops`), subgrupos incluídos. `adminGroups` e `allowedGroups` usam esses caminhos. O L4 confere isso com um login real.
- **R6.3** O Helm ganha `auth.oidc.name` no exemplo do GitLab, e toda tela que hoje diz "Gitea" para login, grupos, convite e usuário usa esse nome: botão de entrar, "signed in with GitLab", "GitLab groups", "Invite by GitLab username", a tela de sem acesso.
- **R6.4** O login não precisa ser do mesmo provedor da conexão Git, como já é hoje.

### 4.7 Links

- **R7.1** Commit: `<web>/-/commit/<sha>`. Arquivo: `<web>/-/blob/<sha>/<path>` (já existe). Branch: `<web>/-/tree/<branch>`. MR: o `web_url` que a API devolve.
- **R7.2** O provedor do link vem da conexão do stack, não de procurar "gitlab" na URL. Repositório sem conexão continua com a regra de hoje.

## 5. API

| Método | Rota | Mudança |
|---|---|---|
| `PUT` | `/orgs/{org}/git/connection` | aceita `provider: gitlab` |
| `GET` | `/orgs/{org}/git/repo/branches?repo=` | nova, vale para os dois |
| `GET` | `/orgs/{org}/git/repo/discover?repo=&ref=` | nova, vale para os dois |
| `POST` | `/webhooks/v1/gitlab/{connection_id}` | nova |
| `GET` | `/orgs/{org}/git/webhook-secret` (POST) | a resposta diz os passos do provedor da conexão |
| `GET` | `/auth/config` | já tem `oidc.name`; a UI passa a usá-lo em todo lugar |


## 6. Telas

A desenhar nos quadros 71 a 73, depois desta spec aprovada:

- **71 · Organização › Git: conectar ao GitLab.** Provedor com GitLab ativo, URL, grupo ou usuário com caminho completo, token com os escopos, teste mostrando quantos projetos e de quantos subgrupos. Lista de repositórios no novo stack com caminho completo.
- **72 · Organização › Git: webhook manual.** O cartão de R5.10, com os passos do GitLab e do Gitea (R5.11), o aviso de rede local e o registro de eventos com os motivos em texto do GitLab.
- **73 · Login e membros com GitLab.** Botão "Sign in with GitLab", menu do usuário, convite e tela sem acesso com o nome do provedor; links de commit e MR na run.

## 7. Ambiente de desenvolvimento

- `make dev-gitlab`: GitLab CE numa versão fixa, perfil `gitlab` do `docker-compose.yaml`, porta `127.0.0.1:8929`. Precisa de uns 4 GB de memória no Docker e leva alguns minutos para subir.
- `deploy/dev/gitlab-setup.sh`: cria o token do root, o grupo `acme`, o subgrupo `acme/platform`, um bot com papel Reporter, o token dele e um projeto com `.firstlume`, como o `gitea-setup.sh`. Liga "requests to the local network" para o webhook chegar ao server de dev.
- Os testes unitários usam um GitLab falso (`httptest`), como os do Gitea. Os de ponta a ponta rodam contra o GitLab real.

## 8. Entregas e critérios de aceite

### Marco L1: interface de provedor

- [x] **CAL1.1** O servidor só fala com o Git pela interface. `grep` por `gitea.` fora do pacote do provedor não acha nada além da criação dele.
- [x] **CAL1.2** Toda a suíte de hoje passa sem mudar teste do Gitea, inclusive os gatilhos de push e a descoberta.
- [x] **CAL1.3** As rotas novas de R3.1 respondem igual às antigas para um repositório do Gitea.

### Marco L2: conexão, descoberta e clone

- [x] **CAL2.1** Conectar a um grupo com subgrupos lista os projetos de todos os níveis, sem os arquivados. Conectar a um usuário lista os projetos dele.
- [x] **CAL2.2** Token errado, grupo inexistente e token sem `read_api` dão erros claros, com o nome do GitLab.
- [x] **CAL2.3** O novo stack acha os `.firstlume` de `acme/platform/infra` e cria os stacks. Um repositório fora da conta é recusado.
- [x] **CAL2.4** Um plan e um apply reais clonam do GitLab com o token de serviço, num projeto privado. O token não aparece no log.
- [x] **CAL2.5** Run de comando e importação de state funcionam num stack do GitLab.
- [x] **CAL2.6** Self-managed num subcaminho (`/gitlab`) funciona na conexão, no clone e no nome do repositório (teste com servidor falso).
- [x] **CAL2.7** Trocar o provedor de uma conexão com stacks é recusado.

### Marco L3: push dispara run

- [x] **CAL3.1** Sem assinatura e sem token, com assinatura errada, com `webhook-timestamp` de mais de 5 minutos ou com token errado: `401` e nada gravado. Com assinatura certa ou token certo: `202`. Um reenvio com o mesmo `Idempotency-Key` não duplica.
- [x] **CAL3.2** Os casos do G1 valem para o GitLab: dois stacks num repositório e push em um só; push com muitos commits em que só o primeiro toca o stack; `triggers.paths` e `triggers.ignore`; force push, branch nova e compare falho afetam todos; tag, branch apagada e outra branch não criam run.
- [x] **CAL3.3** O merge de um MR cria o run com o link do MR.
- [x] **CAL3.4** O botão Test do GitLab, depois de um push que já rodou, não cria run e aparece como "already handled". O teste do Gitea também não cria mais run.
- [x] **CAL3.6** Um commit de mais de 1000 arquivos afeta todos os stacks da branch, com o motivo "compare incompleto".
- [x] **CAL3.5** Um projeto fora da conta é descartado, mesmo com o token certo.

### Marco L4: login

- [x] **CAL4.1** Login real pelo GitLab cria o usuário com os grupos em caminho completo, inclusive subgrupos.
- [ ] **CAL4.2** `adminGroups: [acme/devops]` torna admin quem está nesse subgrupo; quem não está não vê organização sem convite.
- [x] **CAL4.3** Com `auth.oidc.name: GitLab`, nenhuma tela de login, menu, convite ou sem acesso diz "Gitea".

### Marco L5: telas e documentação

- [ ] **CAL5.1** As telas seguem os quadros 71 a 73 aprovados, nos temas claro e escuro.
- [ ] **CAL5.2** Links de commit, arquivo, branch e MR abrem a página certa no GitLab.
- [x] **CAL5.3** README: conexão com GitLab, token e escopos, webhook com o bloqueio de rede local, login, e um `.gitlab-ci.yml` de exemplo para o modo "só state". O item 6 do roadmap diz o que já tem GitLab.

Notas da conferência (2026-11-08):
- CAL4.2: o admin por `acme/devops` foi conferido com login real; quem não está no grupo ver nenhuma organização sem convite é a regra da spec 0001, não testada de novo com GitLab.
- CAL4.3: login e menu da conta conferidos no navegador; convite e tela sem acesso usam o mesmo nome pelo código, sem conferência no navegador.
- CAL5.1: telas conferidas no tema escuro (conexão, webhook, run, menu) e no claro (assistente). CAL5.2: links de commit e de merge request conferidos; link de arquivo pelo código; não há link de branch na UI hoje, então R7.1 para branch não se aplica.

### Critérios gerais

- [x] **CGL1** `make vet`, os testes unitários, os de integração e o `tsc` passam.
- [x] **CGL2** Nada de dados reais da empresa no repositório público.

## 9. Riscos

- **Memória do ambiente de dev.** O GitLab CE pede uns 4 GB. O perfil é opcional e fica desligado no dia a dia.
- **Diferenças entre versões do GitLab.** Os fatos da seção 1.1 valem para a versão medida. Os campos usados são antigos e estáveis na API v4, mas `X-Gitlab-Event-UUID` e os limites do compare podem variar: o código não depende deles para ser seguro, só para não duplicar e não perder arquivos.
- **Segredo em texto no header, em GitLab antigo.** Sem signing token, o GitLab manda o segredo em claro em cada entrega. Sem TLS entre o GitLab e o Firstlume, quem vê o tráfego pode forjar pushes. Um push forjado só pode disparar run de um commit que já está no repositório (spec 0006, R1.7), mas o cartão recomenda o signing token e HTTPS.

## 10. Decisões pendentes

| # | Pergunta | Proposta |
|---|---|---|
| D1 | Plano em MR, status e comentário entram agora para o GitLab? | **Não.** Eles saem com o G2 e o G3 da spec 0006, para Gitea e GitLab juntos, sobre a interface desta spec. Fazer só para o GitLab deixaria o provedor principal da empresa para trás. |
| D2 | Projetos de subgrupos entram na lista da conta? | **Sim.** Grupos com subgrupos são o normal no GitLab, e conectar cada subgrupo exigiria várias conexões, que ainda não existem. |
| D3 | Projetos arquivados aparecem? | **Não.** Não recebem push e só poluem a lista. |
| D4 | Rotas novas com `?repo=` ou caminho curinga? | **`?repo=`.** O caminho do GitLab tem barras, e a query evita ambiguidade com `/branches` e `/discover`. As rotas antigas ficam para o Gitea. |
| D5 | Medir num GitLab real antes de aprovar a spec? | **Sim**, como na spec 0006. A seção 1.1 é preenchida antes de pedir a aprovação. |
| D6 | O cartão de webhook manual entra agora, antes do G2? | **Sim**, para os dois provedores. Sem ele, ligar o gatilho no GitLab exige `curl` na API. Ele é a parte manual do quadro 57, e o G2 só acrescenta. |
| D7 | Aceitar o `X-Gitlab-Token` em claro, além da assinatura? | **Sim**, porque o signing token só existe a partir do GitLab 19 (L13) e muita instalação self-managed está atrás. O cartão sugere o signing token primeiro. |
| D8 | Ignorar um push cujo commit final já teve decisão no stack (R5.9)? | **Sim**, para os dois provedores. O teste do GitLab não tem como ser reconhecido (L14), e um push real nunca repete o commit final de outro, a não ser num force push de volta, que é raro e tem o botão de run. |
