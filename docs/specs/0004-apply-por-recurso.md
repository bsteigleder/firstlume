# Spec 0004: apply acompanhado recurso por recurso

- **Status:** aprovada em 2026-10-31; Marcos P1 e P2 implementados e testados em 2026-10-31; Marco P3 (tela) implementado em 2026-11-03
- **Data:** 2026-10-31
- **Depende de:** spec 0001, seção 4.11 (revisão do plano recurso por recurso), já implementada
- **Telas:** aprovadas em 2026-10-31 no canvas de telas, quadros "16 · Apply por recurso" (rodando, terminou com erro, terminou com sucesso)

## 1. Contexto

Desde a spec 0001, o plan é revisado **recurso por recurso**: a tela da run lista o que vai ser deletado, recriado, atualizado e criado, com os atributos que mudam. Depois da aprovação, porém, essa lista fica parada. Para saber o que o apply já fez, o que ainda está rodando e onde deu erro, é preciso ler o log do passo Apply, como numa pipeline comum.

Isso pesa justamente na hora que mais importa:

1. **Durante o apply**, não dá para ver de relance quanto falta, nem o que está demorando: um `helm_release` esperando pods, uma VM subindo.
2. **Quando dá erro**, a mensagem do Terraform fica no meio do log, às vezes depois de dezenas de linhas de "Still creating...". Quem abre a run precisa achar o bloco `Error:` e ligar o `with <endereço>` ao recurso.
3. **Depois do erro**, não fica claro o que foi aplicado e o que não foi. O Terraform segue com o que é independente, mas não começa o que depende do recurso que falhou. O próximo plan só traz o que faltou, e isso não está dito em lugar nenhum.

**Objetivo desta spec:** cada card da lista de mudanças mostra o estado do seu recurso durante e depois do apply (esperando, aplicando, pronto, com erro, não aplicado), com o erro do Terraform **dentro do card do recurso**. Tudo isso vem do próprio Terraform (`apply -json`), não de ler o texto do log.

### 1.1 Por que `apply -json` e não ler o log

O texto do log muda entre versões e entre Terraform e OpenTofu, quebra linhas longas e mistura saída de provisioners. A saída `-json` do apply é estável e documentada (*machine-readable UI*). Ela informa, por endereço de recurso:

- `apply_start`: começou, com a ação (`create`, `update`, `delete`). Num recriar, chegam dois: um `delete` e um `create`, na ordem em que o Terraform os executa;
- `apply_progress`: segue rodando, com o tempo decorrido (a cada 10 s);
- `apply_complete`: terminou, com o tempo;
- `apply_errored`: falhou;
- `diagnostic`: erros e avisos. Quando o problema é de um recurso, vem com `address`, arquivo e linha.

Cada mensagem também traz `@message`, a mesma linha que o Terraform escreveria no modo texto. Por isso **o log continua legível**: o executor escreve `@message` no log e usa o resto para o estado de cada recurso.

## 2. Glossário

| Termo | Significado |
|---|---|
| **Estado do recurso** | Onde um recurso da lista de mudanças está no apply: `pending`, `applying`, `done`, `failed` ou `not_applied`. |
| **Evento de apply** | Uma mensagem do `apply -json` sobre um recurso: início, progresso, fim, erro. |
| **Diagnóstico** | Erro ou aviso do Terraform: resumo, detalhe e, quando houver, endereço, arquivo e linha. |
| **Etapa** | Num recurso recriado, cada uma das duas metades: apagar o antigo e criar o novo. |

## 3. Escopo

**Dentro:**
- apply e destroy com `-json`, para um módulo raiz e para Terragrunt `run --all`;
- estado, tempos e erro de cada recurso da lista de mudanças, guardados e devolvidos pela API;
- o log do passo Apply continua em texto legível, montado a partir do `@message` e dos diagnósticos;
- a tela aprovada: estado em cada card, barra de progresso, filtros por estado, ordem por estado dentro de cada grupo, cards prontos recolhidos, erro dentro do card, log recolhido e os três momentos (rodando, terminou com erro, terminou com sucesso).

**Fora (fica para depois):**
- progresso por recurso no **plan** (refresh) e no drift;
- atualização em tempo real por stream. Nesta spec a tela consulta a cada 2 s enquanto a run está ativa, como já faz com os passos;
- reexecutar só o que falhou (`-target`). "Run again" continua sendo um plan novo, que já traz só o que faltou;
- a visão por unit do Terragrunt, com runs separados e diagrama (spec futura, já anotada na 0001).

## 4. Requisitos

### 4.1 Executor

- **R1.1** O apply roda com `-json`: `apply -input=false -json -auto-approve <plan>` num módulo raiz, e `-json` depois do `--` no `terragrunt run --all`. O destroy é um apply do plano de destroy e segue a mesma regra.
  - No `run --all`, o Terragrunt repassa o `-json` de cada unit sem dizer de qual unit é (D1). Por isso o Terragrunt roda o engine **através do binário do Firstlume** (`TG_TF_PATH` aponta para um link com o nome do engine). Num `apply -json`, o binário marca cada linha com a unit, tirada do caminho do plano salvo (`<plans>/<unit>/tfplan.tfplan`, o mesmo que dá a unit no plan); em qualquer outro comando, ele vira o engine de verdade (`exec`), com os mesmos sinais e o mesmo código de saída.
- **R1.2** A saída `-json` **não vai para o log como JSON**. Para cada linha, o executor:
  - escreve `@message` no log, como o Terraform escreveria no modo texto;
  - para um `diagnostic`, escreve o bloco no formato de hoje (`Error: <resumo>`, o detalhe, `with <endereço>`, `on <arquivo> line <n>`), para que o log continue igual ao que as pessoas conhecem;
  - linhas que não são JSON (saída de provisioners, avisos do Terragrunt) vão para o log como estão.
- **R1.3** Os eventos de `apply_start`, `apply_complete` e `apply_errored` e os diagnósticos com endereço são enviados ao servidor **em lotes**: no máximo um envio por segundo, e um envio final antes de o passo Apply terminar. Cada envio leva, por recurso que mudou, os **fatos** vistos até ali (as etapas com início, fim e se falhou, e os erros), não o estado: no apply, o Terraform informa um recriar como um `delete` e um `create` separados, e só o plano diz que eram um recriar. O primeiro envio sai mesmo vazio, para avisar que a run tem progresso. O `apply_progress` não é enviado: a tela conta o tempo a partir do início.
- **R1.4** Todo texto que sai do executor (log, resumo e detalhe do erro) passa pelo mesmo mascaramento de hoje: valores sensíveis do plano, variáveis sensíveis e, com a spec 0003, valores do Vault.
- **R1.5** Recursos de **data source** (`apply_start` com ação `read`) e recursos que não estão na lista de mudanças são ignorados no estado; continuam no log.
- **R1.6** Um diagnóstico de erro **sem endereço** (provider, autenticação, backend) não pertence a nenhum card. O primeiro deles vira o `error` da run, no lugar do genérico "terraform apply exited with code 1".
- **R1.7** Se a saída não puder ser lida como JSON (versão antiga, engine que não suporta), o apply segue normalmente, o log fica como está e a run simplesmente não tem estado por recurso. Nunca falhar um apply por causa disso.

### 4.2 Estado do recurso

- **R2.1** O estado de cada mudança, identificada pela mesma chave da revisão (`unit:endereço`, ou só o endereço):

  | Estado | Quando |
  |---|---|
  | `pending` | aprovado, ainda sem `apply_start` |
  | `applying` | teve `apply_start` e ainda não terminou |
  | `done` | `apply_complete` de todas as etapas |
  | `failed` | `apply_errored`, ou um diagnóstico de erro com o endereço dele |
  | `not_applied` | o apply terminou e ele nunca começou (só acontece quando outro recurso falhou) |

- **R2.2** Cada mudança guarda, além do estado: início, fim, duração e, num recriar, as duas etapas na ordem em que rodaram (apagar e criar, ou criar e apagar com `create_before_destroy`), cada uma com o seu tempo.
- **R2.3** O erro de um recurso guarda: o resumo, o detalhe, o arquivo e a linha (quando houver), já mascarados. Se o Terraform mandar mais de um diagnóstico para o mesmo endereço, ficam todos, na ordem.
- **R2.4** Quando o apply termina, todo `pending` vira `not_applied`, e todo `applying` sem fim vira `failed` com o erro "o apply terminou antes de este recurso terminar". Isso cobre o executor morto no meio do apply.
- **R2.5** Runs anteriores a esta spec, e runs em que R1.7 aconteceu, não têm estado. A tela mostra a lista como hoje, sem selos.

### 4.3 Servidor e API

- **R3.1** Nova rota do executor, com o token da fase de apply: `POST /exec/v1/apply-progress` (como `/exec/v1/steps` e `/exec/v1/changes`, a run vem do token) com um lote de `{key, state, started_at, finished_at, steps[], errors[]}`. Chave que não está na lista de mudanças da run é recusada no lote (o resto é aceito) e registrada no log do servidor.
- **R3.2** Os fatos ficam numa tabela própria, `run_change_progress (run_id, key, steps jsonb, errors jsonb)`, e não dentro de `runs.changes`: a lista do plan continua imutável depois da aprovação. `runs.apply_tracked` marca as runs cujo apply mandou progresso. O **estado é calculado na leitura** (R2.1 e R2.4), a partir dos fatos, da ação do plano e de a run ter terminado. Assim o fim do apply vale igual quando o executor termina, falha ou morre, sem passos extras em cada caminho.
- **R3.3** `GET …/runs/{id}/changes` devolve cada mudança com um campo `apply`, ausente quando não há estado:
  `{state, started_at, finished_at, duration_ms, steps: [{action, started_at, finished_at}], errors: [{summary, detail, file, line}]}`.
- **R3.4** Ao ler uma run cujo apply terminou e mandou progresso, a API devolve os totais por estado (`done`, `failed`, `not_applied`), calculados na hora, para o aviso do topo.

### 4.4 Tela

Segue o desenho aprovado (quadros "16 · Apply por recurso").

- **R4.1** **Selo de estado** à direita de cada card: "Waiting", "Creating…/Updating…/Deleting…" com o tempo correndo, "Created/Updated/Deleted/Replaced · <duração>", "Failed · after <duração>" e "Not applied".
- **R4.2** **Recriar** mostra as duas etapas, com o estado e o tempo de cada uma.
- **R4.3** **Erro dentro do card:** título (o resumo), a primeira linha do detalhe, "Show more" para o detalhe inteiro com `with` e `on`, o arquivo e a linha.
  - "Open <arquivo>:<linha> in Git" abre o arquivo no commit da run, quando o arquivo existe no checkout. Em módulos baixados (registry, `.terragrunt-cache`) mostra só o texto, sem link.
  - "Show in log" seleciona o passo Apply, expande o log e rola até o bloco do erro.
- **R4.4** **Não aplicado:** o card fica esmaecido, com o texto "Not applied: the apply stopped before reaching it. It will be in the next plan." (ver D3 sobre dizer de qual recurso ele depende).
- **R4.5** **Barra de progresso e filtros** no topo da lista, **fixos ao rolar**:
  - a barra mostra prontos, com erro, aplicando e não aplicados;
  - os filtros são "All", "Failed", "Applying", "Not applied" e "Done", com contagem e a cor da barra. Filtro com zero não aparece, exceto "All";
  - com um filtro ativo, cada grupo mostra "N of M".
- **R4.6** **Ordem:** os grupos continuam por ação (deletar, recriar, atualizar, criar), como na revisão. Dentro de cada grupo: com erro, aplicando, não aplicado, esperando, pronto.
- **R4.7** **Cards prontos começam recolhidos** (só o cabeçalho com o selo). Com erro, aplicando e não aplicado começam abertos. A setinha abre e fecha qualquer um.
- **R4.8** **Log recolhido:** enquanto a run tem estado por recurso, o log do passo Apply mostra só as 3 últimas linhas, com "Expand log". A escolha fica guardada no navegador.
- **R4.9** **Aviso do topo:**
  - rodando, com um recurso que falhou: "<nome> failed. Changes already running will finish; what depends on it won't start.", com "Show failed";
  - terminou com erro: "Apply failed. X of N changes were applied, Y failed and Z were not applied.", com "Show failed" e "Run again" no cabeçalho;
  - terminou com sucesso: "All N changes applied.", com a duração;
  - erro sem recurso (R1.6): o aviso de erro de hoje, com a mensagem do Terraform.
- **R4.10** O passo Apply na trilha mostra "<duração> · X of N" enquanto roda.
- **R4.11** Enquanto a run está ativa, a tela recarrega as mudanças a cada 2 s, como já faz com a run e os passos.

## 5. API

| Rota | O que muda |
|---|---|
| `GET /api/v1/orgs/{org}/runs/{id}/changes` | cada item ganha `apply` (R3.3) |
| `GET /api/v1/orgs/{org}/runs/{id}` | ganha `apply_totals: {done, failed, not_applied}` quando o apply terminou e mandou progresso (R3.4) |
| `POST /exec/v1/apply-progress` | nova, só do executor, na fase de apply (R3.1) |

Migração `00010_apply_progress.sql`: a tabela `run_change_progress` e a coluna `runs.apply_tracked`.

## 6. Ambiente de desenvolvimento

O exemplo do Gitea local ganha um stack `apply-demo`, sem nuvem nem cluster, feito de `terraform_data` com `local-exec`:

- alguns recursos rápidos;
- um que demora (`sleep 40`), para ver o "Creating…" e o tempo correndo;
- um que falha (`exit 1`) conforme uma variável `fail = true`;
- um que depende do que falha, para ver o "Not applied";
- um recriado (`triggers_replace`), para ver as duas etapas.

E uma versão Terragrunt com duas units, para o `run --all`.

## 7. Entregas e critérios de aceite

### Marco P1: executor e API (módulo raiz)

Testes:
- `internal/executor/applyjson_test.go`: a saída real de um apply, gravada, e o mesmo cenário rodando Terraform 1.15.6 e OpenTofu 1.13.2 de verdade;
- `internal/store/applyprogress_test.go`: a regra de estado;
- `internal/server` (`TestApplyProgress`, com Postgres): a rota, a tabela, o fim do apply e os totais.

O executor e as regras estão cobertos. O stack `apply-demo` está no `make dev-gitea`, mas o caminho completo com o agent ainda não foi feito.

- [x] **CAP1.1** Um apply do `apply-demo` com `fail = false` deixa todas as mudanças `done`, com início, fim e duração. O recriado tem as duas etapas na ordem certa.
- [x] **CAP1.2** Com `fail = true`: o que falha fica `failed` com o resumo, o detalhe, o arquivo e a linha do Terraform; o que depende dele fica `not_applied`; os independentes ficam `done`.
- [x] **CAP1.3** Enquanto o recurso lento roda, `GET …/changes` mostra ele `applying` com `started_at`, e os já terminados como `done`, antes de o apply acabar.
- [x] **CAP1.4** O log do passo Apply não tem nenhuma linha JSON. Ele tem as mesmas linhas de recurso do modo texto ("Creating...", "Creation complete after 1s") e o bloco `Error:` com `with` e `on`.
- [x] **CAP1.5** Um valor sensível que aparece no detalhe de um erro (uma variável sensível ecoada pelo `local-exec`) sai mascarado no log e na API.
- [x] **CAP1.6** Um erro sem endereço (provider inexistente na hora do apply, simulado) vira o `error` da run, e nenhum card fica `failed` por causa dele.
- [x] **CAP1.7** Um executor morto no meio do apply deixa o recurso que rodava `failed` com "o apply terminou antes de este recurso terminar" e os pendentes `not_applied`.
- [x] **CAP1.8** O mesmo teste passa com OpenTofu.
- [x] **CAP1.9** Uma chave que não está na lista de mudanças é recusada sem derrubar o lote. O token de plan não consegue chamar a rota.

### Marco P2: Terragrunt `run --all`

Testes em `internal/executor/applyjson_test.go` com Terragrunt 1.1.6 real, com Terraform 1.15.6 e com OpenTofu 1.13.2: três units, `net` e `app` com o mesmo endereço, um erro em `net` e `web` dependendo de `net`. O `make dev-gitea` ganhou o stack `apply-demo-tg`.

- [x] **CAP2.1** No `apply-demo` em Terragrunt, cada mudança recebe o estado da sua unit (chave `unit:endereço`), inclusive quando as duas units têm recursos com o mesmo endereço.
- [x] **CAP2.2** Uma unit que falha não marca recursos de outra unit. Units que o Terragrunt não roda porque dependem da que falhou deixam os recursos `not_applied`.
- [x] **CAP2.3** O log continua com o prefixo da unit em cada linha, como hoje.

### Marco P3: tela

- [x] **CAP3.1** A tela segue os três quadros aprovados: rodando, terminou com erro e terminou com sucesso (R4.1 a R4.10).
- [ ] **CAP3.2** Os filtros, a ordem, os cards recolhidos, "Show more", "Show in log", "Expand log" e o link do arquivo no Git funcionam com o `apply-demo` de verdade.
- [x] **CAP3.3** Uma run antiga, sem estado, aparece como hoje.
- [x] **CAP3.4** A tela funciona em largura de celular: os filtros quebram linha e os cards não estouram a largura.

### Critérios gerais

- [ ] **CGP1** `make vet`, os testes unitários, os de integração e o `tsc` passam.
- [ ] **CGP2** Os testes usam Terraform, OpenTofu e Terragrunt reais.
- [ ] **CGP3** README: o que a tela mostra durante o apply e o que significa "Not applied".
- [ ] **CGP4** Nada de dados reais da empresa no repositório público.

## 8. Decisões pendentes

| # | Pergunta | Proposta |
|---|---|---|
| D1 | Como saber a unit de cada linha no `run --all`? | **Decidido em 2026-10-31.** Testado com Terragrunt 1.1.6: com `-json`, a saída de cada unit chega **sem prefixo**, e `--log-format json` só marca as mensagens do próprio Terragrunt. Ficou o binário do Firstlume como engine do Terragrunt, marcando as linhas do apply com a unit (R1.1). |
| D2 | Atualizar a tela por consulta a cada 2 s ou por stream? | **Consulta a cada 2 s**, como os passos já fazem. Basta para recursos que levam segundos ou minutos. O stream entra se a tela de runs ativas também precisar. |
| D3 | Dizer de qual recurso um "Not applied" depende, como no desenho ("Depends on helm_release.payments_api, which failed")? | **Não no P3**: o Terraform não informa isso no apply. Dá para calcular pelas referências da configuração no plano (`show -json`), mas módulos e `count`/`for_each` complicam. Fica um texto genérico (R4.4) e, se fizer falta, um marco P4 calcula a dependência. |
| D4 | Guardar o JSON bruto do apply? | **Não.** O log legível e o estado por recurso cobrem o uso. Se for preciso depurar, um artefato `apply.jsonl` cifrado pode ser adicionado depois. |
| D5 | O aviso de sucesso mostra a versão do state, como no desenho? | Só se a run já souber qual versão criou (spec 0002). Se não souber, o aviso mostra só a contagem e a duração. |
