# Spec 0005: storage de state por organização

- **Status:** aprovada em 2026-10-31; Marco SS1 implementado em 2026-10-31 (sem o teste com MinIO)
- **Data:** 2026-10-31
- **Depende de:** spec 0002 (state backend do Firstlume), Marcos S1 a S3 implementados; spec 0003, Marco F2 (conexão do Vault da organização), implementado
- **Telas:** a desenhar e aprovar antes do Marco SS4

## 1. Contexto

Pela spec 0002, o Firstlume é o backend de state dos stacks: lock, versões, criptografia e um state por unit. Os bytes de cada versão ficam num **storage** escolhido **uma vez, na instalação**: Postgres ou um bucket S3 (R4.1 da 0002).

Isso não serve quando:

- a instalação atende mais de uma organização, e cada uma quer o state **no próprio bucket**, com a própria credencial, por contrato, custo ou auditoria;
- a organização já tem um storage S3 (Garage, MinIO, Ceph, AWS) e não quer o state dela no banco do Firstlume;
- a instalação subiu com Postgres e a organização quer passar para o bucket dela sem reinstalar.

**Objetivo desta spec:** cada organização escolhe **onde ficam os bytes do state** dos stacks dela. O Firstlume continua sendo o backend: o protocolo HTTP, o lock, as versões, a criptografia, os tokens de state e a importação não mudam. Muda só onde o objeto cifrado é gravado.

### 1.1 O que esta spec não é

- **Não** é usar o backend do Terraform de outro lugar (`backend "s3"`, `gcs`, `azurerm`). Isso já funciona sem o Firstlume no caminho (R1.4 da 0002) e continua igual.
- **Não** muda o padrão de segurança: o state continua cifrado, a não ser que a organização escolha guardar em claro no bucket dela (R5.2).

## 2. Glossário

| Termo | Significado |
|---|---|
| **Storage da instalação** | O storage configurado no Helm (`FIRSTLUME_STATE_STORAGE`), como hoje. É o padrão de toda organização. |
| **Storage da organização** | Um bucket S3 que a organização configura para os states dela. Substitui o da instalação para os stacks da organização. |
| **Credencial do storage** | Chave de acesso e chave secreta do bucket: guardadas cifradas no Firstlume ou lidas do Vault da organização. |
| **Mudança de storage** | Trocar o storage de uma organização que já tem versões gravadas. |
| **Mover versões** | Copiar as versões já gravadas de um storage para outro, conferindo cada uma. |
| **Em claro** | Versão gravada como o `terraform.tfstate` que o Terraform gera, sem a criptografia do Firstlume. Só num storage de organização, se ela escolher. |

## 3. Escopo

**Dentro:**
- storage S3 por organização (testado com Garage e MinIO), configurado por um admin da organização;
- credencial cifrada no Firstlume ou lida do Vault da organização;
- teste do storage antes de salvar;
- cada versão sabe em qual storage está, para trocar o storage sem perder nada;
- mover as versões existentes para o storage novo;
- tela de configuração da organização (depois do desenho aprovado).

**Fora (próximas specs):**
- storage diferente por pasta ou por stack (o modelo de dados já fica pronto para isso: cada versão aponta para o seu storage);
- outros tipos além de S3 (GCS, Azure Blob);
- criptografia com chave do cliente (BYOK);
- planos e logs no storage da organização (continuam no Postgres).

## 4. Requisitos

### 4.1 Configuração

- **R1.1** Toda organização começa com o **storage da instalação**. Nada muda para quem não configurar.
- **R1.2** Um admin da organização configura um **storage S3** com:
  - `endpoint` (URL), `bucket`, `region` (padrão `us-east-1`), `folder` (padrão `firstlume`) e `path_style` (padrão `true`);
  - `ca_cert` opcional, para endpoints com certificado de CA própria;
  - a credencial (R2).
- **R1.3** Uma organização tem **um** storage configurado por vez. Voltar para o da instalação é uma mudança de storage (R4) como outra qualquer.
- **R1.4** Os metadados (units, versões, locks, eventos, tokens) ficam **sempre no Postgres** do Firstlume. Por isso o lock não depende de escrita condicional no bucket, que o Garage não tem (D4 da 0002).
- **R1.5** **A pasta do Firstlume no bucket.** O Firstlume usa uma pasta dentro do bucket (`folder`) e nada fora dela: grava, lista e apaga só lá dentro. O bucket pode ter outras coisas.
- **R1.6** **Dona da pasta.** Ao salvar, o Firstlume grava `<folder>/.firstlume` com o id da organização. Uma pasta cujo `.firstlume` é de outra organização é recusada ("esta pasta já guarda states de outra organização"). Duas organizações podem usar o mesmo bucket, cada uma na sua pasta.
- **R1.7** **Dentro da pasta, os nomes do Firstlume.** Cada versão fica em
  `<folder>/<pastas>/<stack>/<unit>/v<número>-<id da versão>.tfstate.enc`, com os nomes de pastas e stack no momento da gravação. Exemplo:

  ```
  firstlume/
    .firstlume
    infra/prod/cluster/
      zona-a/node01/app/
        v000012-0f2c91ab-….tfstate.enc
      access/
        v000004-91ab07de-….tfstate.enc
    dns/records/
      v000002-5d1e….tfstate.enc
  ```

  - A unit raiz (Terraform puro) fica direto na pasta do stack.
  - O id da versão no nome garante que **nenhum arquivo é sobrescrito**: nem por um stack novo com o nome de um apagado (guardado por 30 dias, R6.2 da 0002), nem quando caminhos diferentes dão o mesmo texto (a pasta `dns` com o stack `records`, e um stack `dns` com a unit `records`).
  - **Renomear** uma pasta ou um stack não move nada: as versões novas vão para o caminho novo, e as antigas continuam legíveis onde estão (cada versão guarda o próprio caminho).
  - O arquivo é cifrado (`.tfstate.enc`) ou em claro (`.tfstate`), conforme R5.2.
  - Um caminho com mais de 1024 bytes (limite do S3) faz a gravação falhar com a unit citada.
- **R1.8** O storage da instalação não muda: continua com a chave só de ids (R4.5 da 0002).

### 4.2 Credencial

- **R2.1** A credencial vem de uma de duas fontes:
  - **literal:** chave de acesso e chave secreta enviadas na configuração. Ficam cifradas com a chave mestra, como as variáveis sensíveis. A chave secreta nunca volta pela API; enviada vazia, mantém a guardada;
  - **Vault:** `{vault: caminho, field: campo}` para cada uma, lidas pelo **servidor** com a conexão do Vault da organização (spec 0003, F2).
- **R2.2** Para ler o Vault, o servidor assina um JWT como o dos runs (ES256, 15 minutos), com `sub` = `org:<id>`, `org` e `purpose` = `state-storage`, sem `stack_id` nem `phase`. O papel vem de `state_storage_vault_role` na configuração do storage. Assim, um papel de Vault feito para runs não serve para o storage, e vice-versa.
- **R2.3** O servidor guarda a credencial lida do Vault **só em memória**, por no máximo 15 minutos, e relê quando ela expira ou quando o bucket recusa a credencial (403).
- **R2.4** A credencial nunca aparece em log, em resposta da API, em evento ou em mensagem de erro.

### 4.3 Teste e falhas

- **R3.1** Salvar a configuração **testa o storage** antes: grava, lê e apaga um objeto de teste em `<folder>/.firstlume-check/`, e confere a dona da pasta (R1.6). Se algo falhar, não salva e diz o que falhou: endpoint, certificado, credencial, Vault, bucket ou permissão.
- **R3.2** `POST /orgs/{org}/state-storage/check` faz o mesmo teste sem salvar nada.
- **R3.3** Se o storage da organização cai depois de configurado:
  - o protocolo de state responde **503** com "o storage de state da organização não respondeu: <motivo>";
  - o run falha no Init com essa mensagem;
  - o lock e os metadados não mudam: nenhuma versão é registrada sem objeto (R4.3 da 0002).
- **R3.4** O servidor **sobe** mesmo com o storage de uma organização fora do ar. Só o storage da instalação continua obrigatório na subida (R4.4 da 0002). O problema de uma organização não derruba as outras.

### 4.4 Mudança de storage

- **R4.1** Cada versão guarda **em qual storage está** (o da instalação ou um storage de organização, por id). Lê-se sempre do storage da versão, não do atual da organização.
- **R4.2** Trocar o storage vale para as **gravações seguintes**. As versões antigas continuam legíveis onde estão: nada se perde, e o rollback (R8 da 0002) funciona com versões dos dois lugares.
- **R4.3** Um storage que ainda tem versões não pode ser apagado da configuração. Ele fica guardado como "anterior", só para leitura, até que **mover versões** o esvazie.
- **R4.4** **Mover versões:** `POST /orgs/{org}/state-storage/move` copia para o storage atual todas as versões que estão em outro, ou em outro modo (cifrado ou em claro):
  - cada cópia é conferida pelo sha256 antes de trocar o registro da versão;
  - a troca é feita versão a versão, numa transação; uma unit travada é pulada e listada;
  - o objeto antigo só é apagado **7 dias** depois, pela limpeza periódica;
  - roda em segundo plano, mostra o progresso e pode ser repetido: o que já foi movido é pulado.
- **R4.5** A limpeza de órfãos (R4.3 da 0002) passa por todos os storages com versões, não só pelo da instalação. Num storage de organização, ela só olha arquivos `*.tfstate.enc` e `.firstlume-check/` dentro da pasta do Firstlume (R1.5).
- **R4.6** Toda mudança de storage e todo "mover versões" viram eventos da organização: quem, quando, de onde, para onde e quantas versões.

### 4.5 Segurança

- **R5.1** Só admin da organização configura, testa, troca ou move.
- **R5.2** **Cifrado ou em claro, a organização escolhe** (`encrypt`, padrão `true`):
  - **cifrado:** como R4.2 da 0002. Quem tem acesso ao bucket vê a estrutura de pastas, não o state;
  - **em claro:** cada versão é gravada como `v<número>-<id>.tfstate`, o JSON do Terraform sem nada em volta. Dá para ler com `mc`, `aws s3` ou outra ferramenta, e **quem lê o bucket lê os segredos do state** (senhas, chaves, tokens que os providers guardam nele);
  - para gravar em claro, o pedido precisa trazer `"encrypt": false` **e** `"plaintext_acknowledged": true`; a tela pede a mesma confirmação, dizendo o risco;
  - a escolha vale para as **gravações seguintes**. Cada versão guarda se foi cifrada, e a leitura segue o que está na versão. Trocar de cifrado para em claro (ou o contrário) não regrava as antigas; "mover versões" (R4.4) regrava todas no modo atual;
  - a troca vira evento da organização (R4.6), e o `GET` mostra o modo atual e quantas versões há em cada modo;
  - o storage da instalação é **sempre cifrado**: a opção só existe no storage da organização.
- **R5.3** O endpoint precisa ser `https://`. `http://` só é aceito se a instalação permitir (`FIRSTLUME_STATE_STORAGE_ALLOW_HTTP=true`), para storages dentro do mesmo cluster.

## 5. API

| Método e caminho | O que faz |
|---|---|
| `GET /api/v1/orgs/{org}/state-storage` | Storage atual (`installation` ou `s3` com os campos, sem segredo), storages anteriores com versões e contagem de versões por storage. |
| `PUT /api/v1/orgs/{org}/state-storage` | Configura ou troca. Testa antes (R3.1). `{"kind": "installation"}` volta ao da instalação. |
| `POST /api/v1/orgs/{org}/state-storage/check` | Testa sem salvar (R3.2). Aceita o corpo do `PUT` ou nada (testa o atual). |
| `POST /api/v1/orgs/{org}/state-storage/move` | Move as versões para o storage atual (R4.4). Responde com o id da tarefa. |
| `GET /api/v1/orgs/{org}/state-storage/move` | Progresso: movidas, puladas (e por quê), falhas. |

Exemplo de `PUT` com Vault:

```json
{
  "kind": "s3",
  "endpoint": "https://s3.example.com",
  "bucket": "infra",
  "folder": "firstlume",
  "encrypt": true,
  "path_style": true,
  "access_key_id": {"vault": "secret/firstlume/state", "field": "access_key_id"},
  "secret_access_key": {"vault": "secret/firstlume/state", "field": "secret_access_key"},
  "vault_role": "firstlume-state"
}
```

## 6. Ambiente de desenvolvimento

- `make dev-garage` cria também um segundo bucket, `firstlume-org-state`, com uma chave própria, para testar o storage de organização ao lado do da instalação.
- `make dev-vault` grava essa chave em `secret/dev/state-storage` e cria o papel `state-storage` com `bound_claims` em `purpose = state-storage`.

## 7. Entregas e critérios de aceite

### Marco SS1: storage por organização, credencial literal

- [x] **CASS1.1** Sem configurar nada, os states continuam no storage da instalação, e os testes da spec 0002 passam sem mudança.
- [x] **CASS1.2** Com um storage S3 configurado, um apply grava a versão no bucket da organização, em `<folder>/<pastas>/<stack>/<unit>/v<n>-<id>.tfstate.enc` (R1.7), cifrada, e nada no storage da instalação nem fora da pasta.
- [x] **CASS1.2b** Renomear o stack: a versão seguinte vai para o caminho novo, a antiga continua legível. Um stack apagado e outro criado com o mesmo nome não sobrescrevem nenhum arquivo.
- [x] **CASS1.2d** Com `"encrypt": false` e a confirmação, a versão vai para o bucket como `.tfstate` legível (o mesmo JSON que o Terraform gravou), e o Firstlume continua lendo as versões cifradas antigas. Sem a confirmação, o pedido é recusado. O storage da instalação não aceita a opção.
- [x] **CASS1.2c** Uma pasta com `.firstlume` de outra organização é recusada. A limpeza de órfãos não apaga nada fora da pasta do Firstlume, nem arquivos que não são dela.
- [x] **CASS1.3** Duas organizações com buckets diferentes gravam cada uma no seu; nenhuma consegue ler a versão da outra pelo protocolo.
- [x] **CASS1.4** Salvar com bucket inexistente, credencial errada ou endpoint fora do ar é recusado, com a causa na mensagem e nenhum segredo nela.
- [x] **CASS1.5** Com o bucket fora do ar, o protocolo responde 503, o run falha no Init com a mensagem de R3.3, e o servidor sobe normalmente.
- [x] **CASS1.6** A chave secreta nunca volta no `GET` e não aparece em log (conferido nos logs de servidor do teste).
- [ ] **CASS1.7** Testado com Garage (`make dev-garage`) e com MinIO. Garage: feito; MinIO: falta.

### Marco SS2: credencial do Vault

- [ ] **CASS2.1** Com a credencial em `{vault, field}`, o servidor faz login com o JWT de R2.2 e grava no bucket.
- [ ] **CASS2.2** Um papel do Vault preso a `purpose = state-storage` não aceita o JWT de um run, e um papel de run não aceita o do storage.
- [ ] **CASS2.3** Trocar a chave no Vault: depois de no máximo 15 minutos, ou no primeiro 403, o servidor passa a usar a nova sem reiniciar.

### Marco SS3: mudança de storage e mover versões

- [ ] **CASS3.1** Trocar o storage de uma organização com versões: as versões antigas continuam legíveis, o rollback para uma versão antiga funciona, e as novas vão para o storage novo.
- [ ] **CASS3.2** Não é possível apagar um storage que ainda tem versões.
- [ ] **CASS3.3** "Mover versões" copia todas, confere o sha256, troca o registro e pula as units travadas, listando-as. Repetir depois de destravar move o resto.
- [ ] **CASS3.4** Uma queda no meio do "mover" não perde nem duplica versão: rodar de novo termina o trabalho.
- [ ] **CASS3.5** Os objetos antigos somem depois de 7 dias pela limpeza, e não antes.
- [ ] **CASS3.6** Os eventos de R4.6 aparecem na organização.

### Marco SS4: tela (depois do desenho aprovado)

- [ ] **CASS4.1** Nas configurações da organização, a seção "State" mostra onde estão os states, deixa testar e trocar, e mostra versões por storage e o progresso do "mover".

### Critérios gerais

- [ ] `go test ./...` passa, com os testes de integração de state rodando contra Postgres e Garage reais.
- [ ] O README explica como configurar o storage da organização, com e sem Vault.

## 8. Decisões

Tomadas na aprovação (2026-10-31):

1. **Storage por pasta ou stack:** fica para depois (seção 3). O modelo já fica pronto: cada versão aponta para o seu storage.
2. **Prazo para apagar os objetos movidos:** 7 dias.
3. **`http://` no endpoint:** permitido só com `FIRSTLUME_STATE_STORAGE_ALLOW_HTTP=true` na instalação (R5.3).
