# Spec 0002: state backend do Firstlume (HTTP, por unit, guardado no Garage)

- **Status:** aprovada; Marcos S1 e S2 implementados em 2026-10-30, Marco S3 em 2026-10-31
- **Data:** 2026-10-30
- **Depende de:** spec 0001 (organizações, pastas, stacks e conexão Gitea), já implementada até o Marco 2

## 1. Contexto

Hoje o Firstlume não guarda state. O código de cada stack precisa declarar um backend remoto próprio. Com o backend local, o state some quando o runner termina.

O primeiro projeto que vai rodar no Firstlume guarda o state assim:

- cada unit do Terragrunt usa o backend **local**;
- ao fim da pipeline, todos os `terraform.tfstate` do ambiente viram **um `tar.gz`**, enviado para um bucket do **Garage** (S3 compatível);
- **não há lock**: dois runs ao mesmo tempo podem corromper o state;
- o bundle é trocado inteiro, e só quando **todo** o `run --all` passou. Se um unit falha, o que os outros criaram fica fora do state, e o run seguinte quebra com "already exists".

Ele não usa o backend `s3` do Terraform direto no storage porque esse backend não funcionou no storage antigo.

**Objetivo desta spec:** o Firstlume passa a ser o backend de state:

- usa o backend **`http`** do Terraform e do OpenTofu, sem plugin;
- guarda um state **por unit**, com **lock**, **versões** e **criptografia**;
- guarda os objetos no **Garage** (ou em qualquer S3) ou no Postgres, conforme configurado uma vez na instalação;
- importa o **bundle `tar.gz`** atual;
- tem um modo **"só state"**, para a pipeline atual usar o backend do Firstlume enquanto a migração dos runs não termina.

### 1.1 O que já funciona sem esta spec

Um stack cujo código declara `backend "s3"` apontando para o Garage já roda hoje:

- as credenciais entram como variáveis de ambiente sensíveis e herdadas (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`);
- o backend precisa de `use_path_style = true`, `skip_credentials_validation`, `skip_region_validation` e `skip_requesting_account_id`.

O que falta nesse caminho:

- **lock:** o `use_lockfile` depende de escrita condicional no S3, e o Garage (v2.4.1) a ignora: nele, o `use_lockfile` não trava nada (D4);
- **versões e rollback por unit;**
- **tela e importação.**

Esse caminho continua aceito: o Firstlume não força o seu backend (R4.1).

## 2. Glossário

| Termo | Significado |
|---|---|
| **Unit** | Um state. Num stack de Terraform puro há um só, o da raiz. Num stack Terragrunt com `run --all`, há um por unit, identificado pelo caminho relativo dele. |
| **Chave da unit** | Caminho relativo que identifica a unit dentro do stack, como `net` ou `zona-a/node01`. A raiz tem chave vazia. |
| **Versão** | Cada escrita aceita de um state. Numerada de 1 em diante, por unit. Guarda serial, lineage, tamanho, hash e quem escreveu. |
| **Lock** | Trava de uma unit, pedida pelo Terraform antes de ler para mudar e devolvida ao fim. |
| **Storage** | Onde os bytes do state ficam: Postgres ou um bucket S3 (Garage). Os metadados (versões, locks) ficam sempre no Postgres. |
| **Token de state** | Credencial de um stack para uso fora dos runs do Firstlume, como uma pipeline existente ou a máquina de alguém. Só leitura, ou leitura e escrita. |
| **Bundle** | Arquivo `tar.gz` com um `terraform.tfstate` por unit, no formato atual do primeiro projeto. |

## 3. Escopo

**Dentro:**
- backend HTTP compatível com Terraform e OpenTofu (ler, gravar, travar, destravar);
- states por unit, com lock, versões e criptografia em repouso;
- storage Postgres ou S3 (testado com Garage), configurado na instalação;
- uso automático pelos runs, com credencial de curta duração por run;
- conferência do backend depois do `init`;
- importação de bundle `tar.gz` e de um `terraform.tfstate` avulso;
- tokens de state por stack (modo "só state");
- aba **State** no stack: units, lock, histórico, diferença entre versões, rollback, download, destravar à força, importação e tokens;
- Garage no ambiente de desenvolvimento.

**Fora (próximas specs):**
- ler o state de outro stack (`terraform_remote_state` entre stacks);
- workspaces do Terraform (o backend `http` não os tem);
- planos e logs no S3 (continuam no Postgres);
- backup e restauração do Firstlume;
- rotação da chave mestra;
- pipelines separadas por unit do Terragrunt (spec própria; esta já deixa o state por unit pronto para ela).

## 4. Regras

### 4.1 O código escolhe o backend

- **R1.1** O Firstlume **não reescreve** o backend do código. O código declara `backend "http" {}` (Terraform puro) ou um `remote_state` do Terragrunt com `backend = "http"`. O Firstlume preenche endereço e credencial por variáveis de ambiente.
- **R1.2** Em todo run, o executor exporta:
  - `TF_HTTP_ADDRESS`, `TF_HTTP_LOCK_ADDRESS`, `TF_HTTP_UNLOCK_ADDRESS`: o endereço da unit raiz;
  - `TF_HTTP_USERNAME=firstlume` e `TF_HTTP_PASSWORD=<token do run>`;
  - `FIRSTLUME_STATE_URL`: a base do stack, para o Terragrunt montar o endereço de cada unit.
- **R1.3** Exemplo de `root.hcl` (Terragrunt), que vai para o README:

  ```hcl
  remote_state {
    backend  = "http"
    generate = { path = "backend.tf", if_exists = "overwrite_terragrunt" }
    config = {
      address        = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
      lock_address   = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
      unlock_address = "${get_env("FIRSTLUME_STATE_URL")}/${path_relative_to_include()}"
    }
  }
  ```

  Usuário e senha vêm de `TF_HTTP_USERNAME` e `TF_HTTP_PASSWORD`, então não aparecem no código.
- **R1.4** Um código com `backend "s3"` (ou outro) ignora essas variáveis e continua funcionando como hoje (seção 1.1).

### 4.2 Conferência do backend depois do `init`

- **R2.1** Depois do `init`, o executor lê o tipo de backend que cada unit realmente usou (em `.terraform/terraform.tfstate`; no Terragrunt, em cada unit).
- **R2.2** Se alguma unit ficou com backend **local** (não declarou nenhum, ou declarou `local`), o plan **falha** no passo Init. No `run --all`, em que o Terragrunt inicializa as units durante o plan, falha ao fim do passo Plan. A mensagem cita a unit e mostra o trecho de R1.1 ou R1.3. Motivo: o state local some com o runner.
- **R2.3** A run grava o backend de cada unit, e a tela da run mostra "state: Firstlume", "state: s3" e assim por diante (card State, quadro 28 do canvas). Um backend `http` sem endereço (vem de `TF_HTTP_ADDRESS`) ou com endereço sob a URL de state do stack conta como Firstlume; outro endereço aparece como "http · another server". Só o plan grava; o apply mostra o que o seu plan encontrou.

### 4.3 O protocolo

O endereço de uma unit é `<URL da API>/state/v1/stacks/<id do stack>/<chave da unit>`. A raiz é o próprio `/state/v1/stacks/<id>`. A chave é normalizada: `.` e `./x` viram vazio e `x`; `..`, barra no começo, caracteres de controle e mais de 512 caracteres são recusados.

| Operação | Pedido | Resposta |
|---|---|---|
| Ler | `GET` | 200 com o state; 204 se a unit ainda não tem state |
| Gravar | `POST` com o state; `?ID=<lock>` quando travado | 200; 409/423 conforme R3.3 a R3.6 |
| Travar | `LOCK` com o `LockInfo` do Terraform | 200 travou; **423** já travada, com o `LockInfo` de quem tem a trava |
| Destravar | `UNLOCK` com o `LockInfo` | 200; 409 se o `ID` não é o da trava |

- **R3.1** A autenticação é **Basic**. A senha é um token de run (R5) ou um token de state (R7). Qualquer outra resposta é 401, sem dizer se o stack existe.
- **R3.2** Um token só vale para o stack dele. Pedir outro stack dá 404.
- **R3.3** **Lock:** uma trava por unit, no Postgres. Travar uma unit livre funciona. Travar uma unit travada devolve 423 com quem trava, desde quando, a operação e a run ou o token.
- **R3.4** **Gravar com trava:** se a unit está travada, a gravação só é aceita com o `ID` da trava. Sem `ID`, ou com outro, dá 423.
- **R3.5** **Serial e lineage:**
  - a primeira versão aceita qualquer lineage;
  - depois, um lineage diferente do atual é recusado (409, "este state é de outra infraestrutura");
  - um serial **menor** que o atual é recusado (409, "state desatualizado");
  - o **mesmo serial** com o mesmo conteúdo é aceito sem criar versão;
  - o mesmo serial com conteúdo diferente é recusado (409).

  Trocar o state por outro de verdade é feito por importação ou rollback (R8), nunca pelo protocolo.
- **R3.6** O corpo precisa ser um state válido, com `version`, `serial`, `lineage` e `resources`, de até **64 MiB** (configurável). Fora disso: 413 ou 422. Se o `Content-MD5` vier, é conferido.
- **R3.7** Cada gravação aceita cria uma **versão**. Ela é imutável e registra:
  - número, serial, lineage, sha256, tamanho e número de recursos;
  - quem gravou: run e fase, ou nome do token;
  - quando.

### 4.4 Storage

- **R4.1** Configurado uma vez, na instalação (Helm ou variáveis de ambiente):
  - `FIRSTLUME_STATE_STORAGE`: `postgres` (padrão) ou `s3`;
  - para `s3`:
    - `_S3_ENDPOINT`, `_S3_BUCKET`, `_S3_REGION` (padrão `garage`) e `_S3_PREFIX`;
    - `_S3_ACCESS_KEY_ID` e `_S3_SECRET_ACCESS_KEY`, vindos de um Secret existente;
    - `_S3_PATH_STYLE` (padrão `true`).
- **R4.2** **Criptografia sempre:** o Firstlume cifra cada versão com AES-256-GCM antes de gravar. A chave é derivada da chave mestra, separada da usada nas variáveis. Nem o Postgres nem o bucket guardam state em claro.
- **R4.3** **Ordem da escrita:** primeiro o objeto, depois os metadados, numa transação. Um objeto sem metadados (queda no meio) é lixo, que uma limpeza periódica apaga depois de 24 h. Uma leitura nunca vê uma versão pela metade.
- **R4.4** Na subida com `s3`, o servidor grava, lê e apaga um objeto de teste. Se falhar, **não sobe** e diz o que falhou: endpoint, credencial, bucket ou permissão.
- **R4.5** A chave do objeto não tem nome de stack nem de unit, só ids: `<prefixo>/<org id>/<stack id>/<id da versão>`. Renomear não move nada, e o bucket não revela a estrutura.
- **R4.6** **Retenção:** `FIRSTLUME_STATE_KEEP_VERSIONS` define quantas versões guardar por unit. O padrão `0` guarda todas. A versão atual nunca é apagada.

### 4.5 Runs

- **R5.1** Cada fase de run recebe um token de state que vale **só enquanto a fase está ativa** e só para o stack da run.
- **R5.2** O que cada fase pode fazer:

  | Fase | Pode |
  |---|---|
  | plan e drift | ler, travar e destravar; **gravar dá 403** |
  | apply e destroy | tudo |

  Um plan nunca muda o state.
- **R5.3** Quando a fase termina, com qualquer resultado (sucesso, falha, cancelamento ou timeout), o servidor **solta as travas** que a run ainda tinha. Fica registrado "liberada ao fim da run X".
- **R5.4** **Por unit:** num `run --all`, cada unit grava a própria versão quando o apply dela termina. Se uma unit falha, as outras **ficam salvas**: acabou a catraca do bundle.
- **R5.5** Um apply com plano salvo cujo state mudou depois do plan é recusado pelo próprio Terraform ("saved plan is stale"). A run falha com essa mensagem e sugere planejar de novo.
- **R5.6** O token de state nunca aparece em log de servidor, agent ou run, nem na linha de comando dos processos (como em CA2.9 da spec 0001).

### 4.6 Excluir um stack

- **R6.1** Excluir um stack com state no Firstlume é recusado (409) enquanto alguma unit tiver recursos. A mensagem manda rodar destroy ou excluir o state.
- **R6.2** "Excluir state" fica na aba State, só para admin, e pede para digitar o nome do stack. Os objetos ficam guardados por **30 dias** e depois são apagados.
- **R6.3** (emenda de 2026-11-02, quadro 55 do canvas) O diálogo de excluir stack diz quantos recursos o state ainda gerencia, oferece exportar o state (R8.9), sem obrigar, e exclui o stack e o state **na mesma transação**, depois de digitar o nome do stack. Pela API, é o `DELETE` do stack com `{"delete_state": true, "confirm": "<nome>"}`. Sem esse corpo, R6.1 continua valendo. Run ativa ou unit travada recusam a exclusão (409) em qualquer caso.

### 4.7 Tokens de state (modo "só state")

- **R7.1** Um admin cria, no stack, tokens com nome, permissão (**leitura** ou **leitura e escrita**) e validade opcional.
- **R7.2** O token aparece uma vez. Só o hash fica guardado. O token pode ser revogado, e a tela mostra o último uso.
- **R7.3** Um token de leitura só faz `GET`, então a pipeline usa `-lock=false` no plan. Um token de escrita faz tudo.
- **R7.4** Uma trava pedida por token **não** se solta sozinha: só com `UNLOCK` ou destravando à força (R8.4).
- **R7.5** Toda versão gravada por um token registra o nome dele.
- **R7.6** É assim que a pipeline atual passa a usar o Firstlume antes de os runs migrarem:
  - o `root.hcl` passa a usar o trecho de R1.3;
  - a pipeline exporta `FIRSTLUME_STATE_URL` e `TF_HTTP_PASSWORD`;
  - os passos de baixar e subir o bundle saem.

### 4.8 Aba State do stack

O desenho das telas vem antes da implementação, como na spec 0001. A aba tem o seguinte.

- **R8.1** **Units:** chave, versão atual, serial, número de recursos, última gravação (quem e quando, com link para a run) e situação da trava.
- **R8.2** **Unit:** (os recursos ficaram numa aba própria do stack, **Resources**, desenhada nos quadros 21 e 22 do canvas de telas e aprovada em 2026-10-31; ver R8.8)
  - **recursos** do state atual: endereço, tipo e provider, **sem valores** de atributos;
  - **histórico** de versões;
  - **diferença** entre duas versões: recursos que entraram, saíram ou mudaram, só por endereço.
- **R8.3** **Rollback:** escolhe uma versão antiga e cria uma **nova** versão com o conteúdo dela e o serial acima do atual. Só funciona com a unit destravada, pede confirmação e fica registrado. Nada do histórico é apagado.
- **R8.4** **Destravar à força:** só admin, mostra quem tem a trava e desde quando, e fica registrado.
- **R8.5** **Download** de uma versão: só admin. Avisa que o state pode ter segredos e fica registrado.
- **R8.6** **Importar** (R9) e **tokens** (R7).
- **R8.7** Toda ação de R8.3 a R8.6, mais criar e revogar token, entra num registro de eventos do state, visível na aba.
- **R8.8** **Aba Resources** (emenda de 2026-10-31):
  - lista, por unit, cada instância de recurso do state atual: endereço (com módulo e índice), tipo, provider e dependências, **sem valores** de atributos;
  - mostra a última mudança de cada recurso e as últimas cinco, vindas das runs de apply (um apply que falhou conta só o que terminou);
  - mostra o que a última checagem de drift encontrou, até um apply terminar depois dela;
  - cada recurso tem um ícone: o logo do serviço (EKS, RDS, Cloudflare, PostgreSQL…) quando o tipo é conhecido, senão um desenho da categoria na cor do provider; data sources aparecem tracejados;
  - uma unit cujo state não pode ser lido aparece com o motivo, sem esconder as outras; as units que a última run encontrou com o state em outro backend (R2.3) aparecem no fim, com o backend e essa run (quadro 29 do canvas); se nenhuma run gravou os backends (runs antigas), a aba deduz como antes: sem state no Firstlume depois de um apply, o código guarda o state em outro backend.
- **R8.9** **Exportar** (emenda de 2026-11-02, quadro 54 do canvas): botão na página do stack que baixa o state **atual**, em claro, sem travar nada.
  - O stack inteiro vem em `.tar.gz` com um `<unit>/terraform.tfstate` por unit, o mesmo formato que a importação lê (R9). Uma unit só vem como `.tfstate`.
  - É tudo ou nada: se uma unit não puder ser lida, nada é baixado.
  - Avisa que o arquivo tem segredos em texto puro e fica registrado nos eventos, por unit (R8.7). Enquanto só existir um papel (spec 0001), todo membro da organização pode exportar.
  - Escolher uma versão antiga continua em R8.5.

### 4.9 Importação

- **R9.1** Aceita um **bundle `tar.gz`** ou um `terraform.tfstate` avulso, pela API e pela aba State.
- **R9.2** No bundle, cada `<caminho>/terraform.tfstate` vira a unit `<caminho>`. Um state em `<caminho>/.terragrunt-cache/…` também vai para a unit `<caminho>`, porque é lá que o Terragrunt guarda o state local. Um **prefixo a remover** permite alinhar as chaves com o que o `path_relative_to_include()` vai gerar. Backups (`*.tfstate.backup`), pastas `.terraform` e metadados do macOS (`._*`) são ignorados e listados.
- **R9.3** Antes, a importação **simula**. Mostra por unit:
  - a chave;
  - o serial, o lineage e o número de recursos;
  - se a unit já tem state, e nesse caso se é **igual**, **mais novo** ou **conflito** (lineage diferente).
- **R9.4** Por padrão, importa só units sem state. Substituir uma unit existente precisa de marcação explícita por unit. Isso cria uma nova versão, e o histórico fica.
- **R9.5** Arquivos que não são state, caminhos com `..`, links e tamanhos acima do limite são recusados e listados na simulação. Nada é gravado.
- **R9.6** A importação é **atômica** por bundle: grava tudo ou nada.

#### Emenda de 2026-11-02: importar pela tela, com o Firstlume organizando os states

Quadros 49 a 52 do canvas. A emenda só acrescenta: a API de R9 continua igual para quem já a usa.

- **R9.7** O assistente de novo stack pergunta se a infraestrutura já existe. Se sim, recebe os states antes de criar o stack. Nada é gravado até o último botão. Se a importação falhar, o stack criado nesse passo é removido.
- **R9.8** O envio aceita vários arquivos ao mesmo tempo: `tar.gz` e `.tfstate` avulsos, em qualquer organização de pastas. Dentro de um bundle enviado assim, qualquer `*.tfstate` conta como state.
- **R9.9** O state não diz de onde veio, então o Firstlume **deduz** o lugar de cada um, nesta ordem:
  1. **lineage:** o mesmo lineage já existe numa unit deste stack;
  2. **caminho:** o caminho no arquivo termina no caminho de uma unit, ou o nome do arquivo é o nome da pasta dela;
  3. **recursos:** os endereços do state estão nos blocos `resource` da pasta, seguindo módulos locais e o `source` do Terragrunt;
  4. **identificadores:** em pastas empatadas (mesmo código), o `id` e os nomes do state são comparados com os valores que só uma delas tem (`inputs`, `tfvars`, nome da pasta).
- **R9.10** Um caminho que bate não vale se a maioria dos recursos não estiver no código daquela pasta. Módulos remotos não dão para ler: esses recursos não contam nem a favor nem contra.
- **R9.11** O que não for decidido fica para a pessoa escolher a pasta ou deixar de fora, com o motivo. Isso inclui dois states na mesma unit; se tiverem o mesmo lineage, o de serial mais alto fica no lugar e o outro é marcado como cópia antiga. Toda colocação mostra o motivo e pode ser trocada. A importação só acontece depois da confirmação.
- **R9.12** A identificação lê do state só uma lista fixa de atributos identificadores (`internal/reslabel`), nunca um atributo sensível, e não grava nem devolve esses valores. O código é lido pela API do Git, sem clonar. Sem conexão Git ou sem conseguir ler o código, vale só o caminho, e a pessoa digita a unit.
- **R9.13** Um state cujo lineage já está em outro stack da organização aparece com esse aviso, porque dois stacks escreveriam na mesma infraestrutura.
- **R9.14** Se nenhum state combinar com o código, a tela avisa que a pasta ou a branch podem estar erradas e pede uma confirmação explícita para importar assim mesmo.
- **R9.15** Depois de importar, o stack mostra quantos states e recursos entraram, lembra de parar a pipeline antiga e de declarar `backend "http" {}`, e pede um plan, que deve dar "No changes". É o plan que confirma a dedução.
- **R9.16** O stack ganha o botão "Import state…", com a mesma identificação e depois a comparação de R9.3 e R9.4 (substituir por unit).

## 5. API

Rotas de usuário, todas sob `/api/v1/orgs/{org}/stacks/{id}/state`:

| Rota | O que faz |
|---|---|
| `GET /state` | Units, com versão atual e trava |
| `GET /state/units/{chave}/versions` | Histórico |
| `GET /state/units/{chave}/versions/{n}` | Metadados e recursos (sem valores) |
| `GET /state/units/{chave}/versions/{n}/download` | O state em claro (só admin; registrado) |
| `GET /state/units/{chave}/diff?from=&to=` | Diferença por endereço |
| `POST /state/units/{chave}/rollback` | `{version}` |
| `DELETE /state/units/{chave}/lock` | Destrava à força |
| `POST /state/import?dry_run=true\|false` | Multipart: arquivo, prefixo e units a substituir; ou vários arquivos e `placement` (JSON com `source`, `unit` ou `skip`) |
| `POST /state/identify` | Multipart: arquivos. Diz onde cada state vai (R9.9), sem gravar nada. Para um stack novo, fica em `/orgs/{org}/state/identify`, com repositório, branch e pasta |
| `GET /state/export` | O state atual em claro: o stack inteiro em `.tar.gz`, ou uma unit com `?unit=<chave>` (R8.9; registrado) |
| `DELETE /state` | Exclui o state (R6.2) |
| `GET\|POST\|DELETE /state/tokens` | Tokens de state |
| `GET /state/events` | Registro de eventos |

O protocolo do Terraform fica em `/state/v1/stacks/{id}/…` (seção 4.3), fora de `/api`, com autenticação própria.

## 6. Ambiente de desenvolvimento

- `docker-compose` ganha o **Garage** no profile `garage`.
- `make dev-garage`:
  - sobe o Garage;
  - aplica o layout de um nó;
  - cria o bucket `firstlume-state` e uma chave de acesso;
  - grava no `.env` as variáveis de R4.1.
- O `acme/infra` do Gitea local ganha um stack `environments/tg` com Terragrunt (`root.hcl` de R1.3 e duas units com dependência).
- O stack de Terraform puro passa a declarar `backend "http" {}`.
- Um bundle de exemplo para testar a importação: `deploy/dev/state-bundle.tar.gz`, gerado pelo script.

## 7. Entregas e critérios de aceite

### Marco S1: backend HTTP, versões e runs (storage Postgres)

Testes: `internal/server/state_test.go` (protocolo, e Terraform e Terragrunt reais) e `internal/executor` (conferência do backend). Também verificado ponta a ponta com o agent e o Gitea de dev.

- [x] **CAS1.1** Um stack de **Terraform real** com `backend "http" {}`:
  - o plan e o apply funcionam;
  - o segundo plan mostra "No changes";
  - a unit raiz tem 1 versão por apply com mudança.
- [x] **CAS1.2** Um stack **Terragrunt real** com `run --all` e o `root.hcl` de R1.3:
  - cria uma unit por pasta, com chave igual ao caminho;
  - `dependency` com outputs entre units funciona.
- [x] **CAS1.3** Num `run --all` em que a unit B falha no apply, a unit A (já aplicada) tem a versão nova gravada. O run seguinte não dá "already exists" em A.
- [x] **CAS1.4** Um código **sem backend** falha no Init com a unit citada e o trecho de R1.3. Um código com `backend "s3"` não é afetado.
- [x] **CAS1.5** **Lock:** com a unit travada por um token, um apply do Firstlume falha com "travada por <token> desde <hora>". Depois de `UNLOCK`, roda. (Integração com o Terraform real. No S1 a trava vem de outro dono pelo protocolo; com os tokens do S3, vale também para eles.)
- [x] **CAS1.6** Um run de **plan** que tenta `POST` (teste direto com o token da fase) recebe 403.
- [x] **CAS1.7** Uma run que termina com as travas ainda presas (executor morto no meio) deixa as units livres ao fim da fase.
- [x] **CAS1.8** O protocolo recusa:
  - serial menor (409);
  - lineage diferente (409);
  - mesmo serial com conteúdo diferente (409);
  - corpo que não é state (422);
  - corpo acima do limite (413);
  - `ID` errado com trava (423);
  - token de outro stack (404);
  - token vencido (401).

  O mesmo serial com o mesmo conteúdo não cria versão.
- [x] **CAS1.9** O token do run não aparece em logs nem em `ps`. O state no Postgres não contém, em claro, um valor sensível conhecido do state de teste.
- [x] **CAS1.10** Excluir um stack com recursos no state dá 409. Depois de "excluir state", o stack é excluído.

### Marco S2: storage no Garage

Testes: `internal/s3` (cliente, contra o Garage real) e os testes de state de `internal/server`, que rodam no Postgres e, com `make dev-garage`, também no Garage. Também verificado ponta a ponta com o agent.

- [x] **CAS2.1** Com `FIRSTLUME_STATE_STORAGE=s3` apontando para o Garage do `make dev-garage`, os CAS1.1 a CAS1.3 passam iguais.
- [x] **CAS2.2** Os objetos no bucket não contêm o valor sensível do state de teste. As chaves não têm nome de stack nem de unit.
- [x] **CAS2.3** Com credencial errada, bucket inexistente ou endpoint fora do ar, o servidor não sobe e o log diz qual dos três.
- [x] **CAS2.4** Uma queda entre gravar o objeto e gravar os metadados (simulada no teste) não muda a versão atual. O objeto órfão some na limpeza.
- [x] **CAS2.5** Com `KEEP_VERSIONS=3`, depois de 5 applies a unit tem 3 versões e o bucket tem 3 objetos dela.

### Marco S3: importação e modo "só state"

Testes: `internal/server/stateimport_test.go` (bundles montados no teste, tokens, e Terraform e Terragrunt reais), no Postgres e no Garage. Também verificado ponta a ponta com o agent: o bundle de `make dev-state-bundle` importado no stack `environments/tg` do Gitea de dev, e o plan seguinte deu "No changes" nas duas units.

- [x] **CAS3.1** Importar o bundle de exemplo:
  - com simulação, mostra as units, os seriais e o número de recursos, sem gravar nada;
  - confirmado, cria a versão 1 de cada unit.
- [x] **CAS3.2** Depois de importar e trocar o `root.hcl` para R1.3, o `terragrunt run --all plan` dá **"No changes" em todas as units**, tanto pelo Firstlume quanto por fora com um token de leitura e `-lock=false`.
- [x] **CAS3.3** Uma unit que já tem state aparece como igual, mais nova ou conflito. Sem marcação, não é substituída. Com marcação, ganha uma versão nova e mantém o histórico.
- [x] **CAS3.4** Um bundle com `../x/terraform.tfstate`, um link simbólico ou um arquivo que não é state é recusado inteiro, com a lista do que está errado.
- [x] **CAS3.5** **Só state:** o `terragrunt run --all apply` rodado por fora com um token de escrita grava versões com o nome do token. Revogado o token, o próximo pedido dá 401.
- [x] **CAS3.6** Dois `apply` por fora ao mesmo tempo na mesma unit: um espera ou falha com a trava; o state não se corrompe.

### Marco S4: aba State (depois do desenho aprovado)

- [ ] **CAS4.1** A aba mostra as units com versão, recursos, última gravação (link para a run ou nome do token) e trava.
- [ ] **CAS4.2** A diferença entre as versões 1 e 3 de uma unit lista por endereço o que entrou, saiu e mudou, **sem valores**. Nenhum valor sensível aparece em resposta da API da aba, exceto no download.
- [ ] **CAS4.3** O rollback para a versão 1 cria a versão 4 com o conteúdo da 1. O plan seguinte mostra o que voltou. Com a unit travada, o rollback é recusado.
- [ ] **CAS4.4** Destravar à força, baixar, importar, fazer rollback, criar ou revogar token e excluir o state aparecem nos eventos, com quem e quando.
- [x] **CAS4.5** A tela da run mostra o backend usado por unit (R2.3).
- [x] **CAS4.6** A aba Resources lista os recursos de cada unit com endereço, tipo e provider; nenhum valor de atributo aparece na resposta da API (R8.8).
- [x] **CAS4.7** Um recurso criado por uma run mostra essa run como última mudança; um drift aberto marca os recursos afetados até o próximo apply.
- [x] **CAS4.8** Um stack que já fez apply sem gravar state no Firstlume mostra o aviso de state em outro backend.
- [x] **CAS4.9** Exportar um stack de várias units e importar o arquivo em outro stack recria as mesmas units com o mesmo conteúdo. Uma unit sozinha vem como `.tfstate`. Stack sem state dá 404, e cada unit exportada aparece nos eventos com quem e quando (R8.9).
- [x] **CAS4.10** Excluir um stack com recursos: sem corpo ou com o nome errado é recusado, e o stack e o state continuam; com o nome certo, os dois saem juntos. Com run ativa ou unit travada, é recusado nos dois casos (R6.3).

### Marco S5: importar pela tela (emenda R9.7 a R9.16)

Testes: `internal/stateid/stateid_test.go` e `TestIdentifyAndPlaceStates`, no Postgres com um Gitea falso. Também verificado no navegador contra o Gitea de dev: states reais gerados com Terraform, enviados em caminhos e nomes diferentes, no stack `environments/tg`.

- [x] **CAS5.1** Enviar um bundle com caminhos de outro repositório e `.tfstate` soltos coloca cada state na unit certa e mostra o motivo, sem gravar nada.
- [x] **CAS5.2** Duas pastas com o mesmo módulo: o state vai para a que tem o valor exclusivo. Sem valor exclusivo, a tela pergunta.
- [x] **CAS5.3** Um caminho que bate com recursos que não estão no código não é aceito sozinho (R9.10).
- [x] **CAS5.4** Nenhum valor de atributo aparece na resposta da identificação (R9.12).
- [x] **CAS5.5** Criar o stack importando grava as units escolhidas e deixa de fora as marcadas; se a importação falhar, o stack não fica.
- [x] **CAS5.6** Num stack que já tem state, "Import state…" mostra igual, mais novo, mais velho e conflito, e só substitui o que for marcado.
- [x] **CAS5.7** O plan depois da importação dá "No changes" num stack real, com Terragrunt e agent (`environments/tg` no Gitea de dev). Falta repetir num stack da empresa.

### Critérios gerais

- [ ] **CGS1** `make vet`, os testes unitários, os de integração e o `tsc` passam.
- [ ] **CGS2** Os testes de protocolo usam o **Terraform e o Terragrunt reais** (como no Marco 1b), não só chamadas HTTP feitas à mão.
- [x] **CGS3** README com:
  - os trechos de R1.1 e R1.3;
  - as variáveis de R4.1;
  - o passo a passo de migração de um bundle (R7.6 e R9).
- [x] **CGS4** Helm: `state.storage` e `state.s3.*` com Secret existente. `helm lint` e `helm template` passam com Postgres e com S3.
- [ ] **CGS5** Nada de dados reais da empresa no repositório público.

## 8. Decisões pendentes

| # | Pergunta | Proposta |
|---|---|---|
| D1 | Onde ficam os bytes do state na instalação da empresa? | **Garage**, num bucket só do Firstlume, como pedido. O Postgres continua padrão para instalações pequenas e para dev. |
| D2 | O primeiro projeto migra primeiro só o state (pipeline atual + token) ou já os runs? | **Primeiro só o state** (R7.6): ganha lock e versões sem trocar de pipeline. Os runs migram depois. |
| D3 | Retenção padrão | Guardar todas (`0`). Revisar quando houver volume. |
| D4 | O `backend "s3"` com `use_lockfile` funciona no Garage? | **Testado no S2: não trava.** O Garage v2.4.1 ignora `If-None-Match` (um `PUT` condicional sobre objeto existente devolve 200), e um apply passou por cima da trava de outro. Registrado no README. |
| D5 | Quem pode baixar state, que tem segredos? | Só admin nesta versão. Vira uma permissão própria na spec de papéis. |
| D6 | O token de leitura pode travar? | Não (R7.3). Um plan por fora usa `-lock=false`. Assim um token de leitura nunca segura uma unit. |
