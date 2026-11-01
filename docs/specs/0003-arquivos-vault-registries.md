# Spec 0003: arquivos, segredos do Vault e credenciais de registry

- **Status:** aprovada em 2026-10-31; Marcos F1 a F4 implementados em 2026-10-31 (falta, do F4, mostrar as pastas em que cada padrão caiu)
- **Data:** 2026-10-31
- **Depende de:** spec 0001 (configurações herdadas e `.firstlume/config.yaml`) e spec 0002 (state), já implementadas

## 1. Contexto

Hoje um stack recebe dois tipos de variável, herdados pela árvore (organização → pastas → stack, vence o nível mais próximo):

- **`env`**: vira variável de ambiente;
- **`terraform`**: vira `TF_VAR_<nome>`.

O valor é digitado no Firstlume e guardado cifrado. O plan congela os valores e o apply recebe exatamente os mesmos.

Isso não basta para o primeiro projeto que vai rodar no Firstlume, nem para boa parte da infraestrutura on-prem:

1. **Arquivos.** O código Terragrunt lê arquivos que não estão no Git: `connection.hcl` e `join.hcl` com `read_terragrunt_config`, um `credentials.auto.tfvars.json`, a chave SSH usada pelo `remote-exec`. Hoje a pipeline os monta com `printf`, **um por máquina**, numa lista que diverge entre pipelines.
2. **Vault.** Os segredos de verdade moram no Vault. A pipeline guarda só a credencial de acesso a ele e lê os valores a cada run. Copiar os segredos para dentro do Firstlume criaria um **segundo cofre**, que ninguém quer manter.
3. **Registries privados.** O código baixa charts OCI (provider `helm`) e imagens de registries com login (Harbor, GHCR), e módulos e providers de registries Terraform privados. Hoje só dá para improvisar com variáveis sensíveis soltas.

**Objetivo desta spec:**

- o repositório **declara em YAML**, em `.firstlume/config.yaml`, os arquivos, as variáveis de ambiente e as credenciais de registry que o run precisa, e **de onde vem cada valor**: um literal, uma variável do Firstlume ou um campo do **Vault**;
- o Firstlume **monta cada arquivo no formato pedido** (`hcl`, `json`, `yaml`, `env` ou `raw`), com o escape certo, inclusive **um arquivo por pasta** a partir de uma única declaração;
- o **Vault é lido pelo executor, no pod do run**, com uma identidade **por stack e por fase** emitida pelo Firstlume. O valor nunca passa pelo servidor nem pelo banco do Firstlume;
- nas configurações herdadas do Firstlume, continuam as variáveis digitadas, agora também do tipo **arquivo**, e entram a **conexão Vault**, o **papel do Vault** e as **credenciais de registry** comuns a vários stacks.

### 1.1 Por que no repositório, e não nas configurações do Firstlume

- **O mapeamento não é segredo; o valor é.** Dizer que o `connection.hcl` de cada máquina vem de tal caminho do Vault faz parte do código. Fica no Git, passa por revisão, e uma máquina nova entra no mesmo commit que o código dela.
- **Plan e apply usam a mesma declaração**, porque ela vem do commit, como o resto do código.
- **Sem template livre.** O Firstlume gera o arquivo a partir de dados estruturados. Um valor com aspas ou quebra de linha não quebra o HCL nem muda o sentido dele.
- **A segurança não depende do repositório.** O repositório escolhe os caminhos, mas quem decide o que pode ser lido é o papel do Vault, preso ao stack e à fase pelo JWT (R3.3). Um commit não consegue ler segredo de prod a partir de um stack de dev.

## 2. Glossário

| Termo | Significado |
|---|---|
| **Declaração** | O que `.firstlume/config.yaml` diz que o run precisa: `files`, `env`, `terraform` e `registries`. |
| **Fonte** | De onde vem um valor da declaração: um literal, `{var: NOME}` (variável do Firstlume) ou `{vault: caminho, field: campo}`. |
| **Expansão por pastas** | Um `path` com curinga (`sp*/*/connection.hcl`) gera um arquivo em cada pasta do checkout que casa com ele. |
| **Conexão Vault** | Endereço e forma de login no Vault de uma organização. Uma por organização, como a conexão Git. |
| **Papel do Vault** | O `role` do método de login. Configuração herdada: a pasta `prod` pode usar um papel que só lê os segredos de produção. |
| **JWT do run** | Token assinado pelo Firstlume para uma fase de um run, com o stack, as pastas e a fase. O executor o troca por um token do Vault. |
| **Credencial de registry** | Usuário e senha (ou token) de um host de registry. |

## 3. Escopo

**Dentro:**
- `files`, `env`, `terraform` e `registries` em `.firstlume/config.yaml`, com fontes literal, `var` e `vault`;
- formatos `hcl`, `json`, `yaml`, `env` e `raw`, e a expansão por pastas;
- variáveis do tipo **arquivo** nas configurações herdadas (conteúdo digitado);
- conexão Vault por organização, login por JWT emitido pelo Firstlume e papel herdado;
- credenciais de registry nas configurações herdadas (por host) e na declaração do stack;
- mascaramento nos logs de tudo o que vem do Vault, de variáveis sensíveis e de senhas de registry;
- as telas (desenhadas e aprovadas antes de implementar).

**Fora (fica para depois):**
- templates livres. Se aparecer um arquivo que os formatos não cobrem, entra um formato novo;
- login no Vault com Kubernetes auth ou AppRole (D2);
- segredos dinâmicos com lease renovado (credenciais de banco, de nuvem) e **SSH CA do Vault** (certificados de minutos no lugar da chave estática);
- outros cofres (AWS Secrets Manager, 1Password, Infisical);
- hooks de fase (`before_plan`, `after_apply`), que vão enxergar os mesmos arquivos e variáveis quando existirem;
- escrever no Vault (por exemplo, publicar um kubeconfig depois do apply).

## 4. Requisitos

### 4.1 A declaração em `.firstlume/config.yaml`

Exemplo completo:

```yaml
version: 1
engine: terraform
engine_version: 1.15.6
terragrunt_version: 1.1.6
run_all: true

files:
  # Um arquivo por pasta de máquina; ${name} é o nome da pasta.
  - path: sp*/*/connection.hcl
    format: hcl
    block: locals                 # gera: locals { host = "...", ... }
    values:
      host: { vault: "secret/infra/prod/${name}", field: ip }
      user: { vault: "secret/infra/prod/${name}", field: user }
      port: 22
  - path: credentials.auto.tfvars.json
    format: json
    values:
      buckets:
        logs: { vault: secret/infra/s3/logs, field: secret_key }
  - path: ssh/id_ed25519
    format: raw                   # o conteúdo inteiro de um campo
    content: { vault: secret/infra/ci-key, field: private_key }

env:
  AWS_ACCESS_KEY_ID: { vault: secret/infra/s3/logs, field: access_key }
  TF_LOG: ""                      # literal

terraform:
  zone: sp1                       # vira TF_VAR_zone
  admin_password: { var: ADMIN_PASSWORD }

registries:
  - host: harbor.example.com
    username: robot$infra
    password: { vault: secret/infra/harbor, field: token }
```

- **R1.1** As chaves novas são todas opcionais. Um `config.yaml` sem elas continua valendo como hoje.
- **R1.2** **Fontes.** Onde a declaração aceita um valor, ele pode ser:

  | Fonte | Valor |
  |---|---|
  | literal (texto, número, booleano) | o próprio |
  | `{var: NOME}` | uma variável efetiva do stack nas configurações do Firstlume (`env` ou `terraform`, vence a mais próxima) |
  | `{vault: caminho, field: campo}` | um campo de um segredo do Vault (R3) |

  Em `values`, mapas e listas podem se aninhar, e as fontes podem aparecer em qualquer nível.
- **R1.3** A declaração é **validada** quando o executor lê o `config.yaml` do commit, como já acontece com o resto do arquivo. Uma declaração inválida falha a fase logo depois do checkout, apontando a linha e o motivo.
- **R1.4** O mesmo nome em `env` ou `terraform` da declaração e nas configurações do Firstlume: **vence a configuração do Firstlume**, e o log da fase registra a troca, sem valores (D6).

### 4.2 Arquivos

- **R2.1** Cada item de `files` tem:
  - **`path`**: relativo ao diretório do stack;
  - **`format`**: `hcl`, `json`, `yaml`, `env` ou `raw`;
  - **`values`** (todos os formatos menos `raw`) ou **`content`** (só `raw`);
  - **`block`** (só `hcl`, opcional): envolve os atributos num bloco, como `locals`. O `read_terragrunt_config` não aceita atributos soltos no topo do arquivo;
  - **`sensitive`** (opcional): o padrão é `true` se alguma fonte do arquivo vem do Vault ou de uma variável sensível, e `false` se não. Define a permissão: `0600` se sensível, `0644` se não.
- **R2.2** **Formatos:**

  | Formato | Gera |
  |---|---|
  | `hcl` | `chave = valor` por item de `values`. Mapas viram objetos (`{ a = 1 }`) e listas viram tuplas. Strings são escapadas como HCL, inclusive `${` e `%{`, para o Terraform não interpretar o valor |
  | `json` | um objeto JSON com `values` (bom para `*.auto.tfvars.json`) |
  | `yaml` | um documento YAML com `values` |
  | `env` | `CHAVE=valor` por linha, com aspas quando preciso; só valores escalares |
  | `raw` | o valor de `content`, sem nenhuma transformação |

- **R2.3** **Expansão por pastas:** a parte de diretório do `path` pode ter curingas (`*`, `?`, `[...]`, sem `**`), como `sp*/*/connection.hcl`. O executor escreve o arquivo em **cada pasta do checkout** que casa com o padrão. Em `path`, nos caminhos do Vault e em literais de texto da declaração, `${dir}` é o caminho da pasta (`sp1/n01`) e `${name}` o último trecho (`n01`). Em qualquer texto, `${stack_dir}` é o caminho absoluto da pasta do stack no run (emenda de 2026-10-31: o código Terragrunt roda dentro do `.terragrunt-cache` e precisa do caminho absoluto de um arquivo gerado, como a chave SSH). Um único item cobre dezenas de máquinas, e uma máquina nova recebe o arquivo sem ninguém mexer no Firstlume.
- **R2.4** Um padrão que não casa com nenhuma pasta **falha** a fase no passo de preparação, citando o padrão. Motivo: um erro de digitação não pode virar "arquivo que não existe" no meio do plan.
- **R2.5** Os arquivos são escritos depois do checkout e antes do `init`, em todas as fases (plan, apply, drift).
- **R2.6** O `path` é recusado (R1.3) se:
  - for absoluto ou tiver `..`;
  - passar por `.git` ou `.firstlume`;
  - tiver mais de 512 caracteres.

  Na fase, se o arquivo de destino **já existe no checkout** (versionado no Git), ou se dois itens geram o mesmo arquivo, a fase falha: o Firstlume não troca código em silêncio (D5).
- **R2.7** Limites por fase: até **1.000 arquivos** depois da expansão, **256 KiB** por arquivo e **16 MiB** no total.
- **R2.8** **Variáveis do tipo arquivo nas configurações herdadas:** uma nova categoria **`file`**, ao lado de `env` e `terraform`, para quem não usa Vault ou para um arquivo que vale para muitos stacks. A chave é o `path` (sem curingas, com as regras de R2.6) e o valor é o conteúdo, digitado, escrito como `raw`. A herança é por caminho. Se a declaração do repositório e as configurações geram o mesmo arquivo, a fase falha.

### 4.3 Vault

- **R3.1** **Conexão por organização:** endereço, CA opcional (PEM), namespace opcional (Vault Enterprise), caminho do método de login (padrão `jwt`) e `audience` (padrão `vault`). Uma por organização, editável só por admin.
- **R3.2** **Papel herdado:** `vault_role` é uma configuração como `require_approval`: definida na organização, numa pasta ou no stack, e vence o nível mais próximo. O repositório **não** escolhe o papel. Sem papel, uma declaração que usa `vault` falha a fase com "nenhum papel do Vault configurado para este stack".
- **R3.3** **JWT do run:** para cada fase, o servidor emite um JWT (ES256), entregue ao executor junto com a especificação da fase:

  | Claim | Valor |
  |---|---|
  | `iss` | a URL pública do Firstlume (`FIRSTLUME_PUBLIC_URL`, nova; no Helm, vem do host do ingress) |
  | `aud` | a da conexão (R3.1) |
  | `sub` | `stack:<id do stack>` |
  | `org` | slug da organização |
  | `stack_id`, `stack_name` | o stack |
  | `stack_path` | pastas e nome do stack, como `infra/prod/network` |
  | `folder_ids` | ids das pastas, da raiz até o stack |
  | `run_id`, `run_type`, `phase` | `plan`, `apply` ou `drift` |
  | `exp` | 15 minutos (só para o login; a duração do token do Vault é do papel) |

  O papel do Vault restringe com `bound_claims` (por exemplo `stack_path = "infra/prod/*"` e `phase = "apply"`). Assim, um stack de dev não lê segredos de prod, e o plan pode ter um papel mais restrito que o apply.
- **R3.4** **Chave de assinatura:** gerada uma vez e guardada cifrada com a chave mestra. Trocar a chave mestra gera uma chave de assinatura nova, que precisa ser dada ao Vault de novo. A chave pública é publicada:
  - em `GET /.well-known/openid-configuration` e `GET /.well-known/jwks.json`, sem autenticação, para o Vault usar `oidc_discovery_url` ou `jwks_url`;
  - em PEM, na tela da conexão, para quando o Vault não alcança o Firstlume (`jwt_validation_pubkeys`).
- **R3.5** **Quem lê é o executor.** Ele faz o login, lê os segredos, monta os arquivos e revoga o próprio token do Vault ao fim da fase (`auth/token/revoke-self`). Os valores não passam pelo servidor, não vão para o banco e não entram nos artefatos do run. O servidor só fala com o Vault no teste de conexão (seção 5), que confere permissões sem ler valores.
- **R3.6** `{vault: caminho, field: campo}`:
  - aceita o caminho lógico, como no `vault kv get` (`secret/infra/prod/n01`), e detecta se o mount é KV v1 ou v2;
  - também aceita um caminho que não é KV (por exemplo `pki/...` ou `database/creds/...`), lendo o campo de `data` da resposta;
  - lê cada caminho uma vez por fase, mesmo que apareça em muitos itens;
  - um valor que veio de `${dir}` ou `${name}` não pode criar um segmento `..` no caminho.
- **R3.7** Um campo ausente, um caminho sem permissão ou o Vault fora do ar **falham a fase** no passo de preparação, dizendo qual item da declaração, qual caminho e qual campo, nunca o valor.
- **R3.8** **Plan e apply:** a declaração vem do commit, igual nas duas fases, e os valores literais e de `var` continuam congelados no plan. Os que vêm do Vault são **relidos no apply**. O plan guarda o **hash** de cada valor lido do Vault. Se no apply algum hash mudou, o apply **falha** com "o segredo em <caminho>#<campo> mudou desde o plan: planeje de novo", sem mostrar valores (D3).
- **R3.9** O JWT do run e o token do Vault nunca aparecem em log, nem na linha de comando dos processos, nem no ambiente do Terraform e do Terragrunt.

### 4.4 Credenciais de registry

- **R4.1** Credenciais de registry podem vir de dois lugares:
  - **configurações herdadas** do Firstlume: uma lista **registries** em cada nível, para o Harbor da empresa valer para todos os stacks sem repetir nos repositórios. A senha é digitada (sensível) ou uma referência `{vault: caminho, field: campo}`, lida pelo executor do mesmo jeito que na declaração;
  - **declaração** do stack (`registries` em `config.yaml`), com as fontes de R1.2.
- **R4.2** Cada credencial tem:
  - **host**, como `harbor.example.com` ou `ghcr.io`;
  - **tipo**: `oci` (registries Docker e OCI, o padrão) ou `terraform` (registry privado de módulos e providers);
  - **usuário** (só `oci`) e **senha ou token**, sempre sensível.
- **R4.3** A combinação é **por host**: a organização define o Harbor, uma pasta acrescenta o GHCR, e o nível mais próximo troca a credencial de um host. A declaração do repositório entra por último, e vale a regra de R1.4: para o mesmo host, vence a configuração do Firstlume.
- **R4.4** Na fase, o executor gera, no HOME efêmero do run:
  - `config.json` no formato do Docker, com `DOCKER_CONFIG` apontando para ele (provider `docker` e outras ferramentas);
  - o mesmo conteúdo em `HELM_REGISTRY_CONFIG` (provider `helm` e o próprio `helm`);
  - para `terraform`, a variável `TF_TOKEN_<host>` no formato do Terraform e do OpenTofu (pontos viram `_`, hífens viram `__`).
- **R4.5** As senhas são mascaradas no log, inclusive na forma base64 `usuário:senha` que vai no `config.json`.
- **R4.6** O token da conexão Git continua só para o host do repositório (commit `20def23`). Credencial de registry não vale para clonar Git, e vice-versa.

### 4.5 Mascaramento e segurança

- **R5.1** São mascarados no log, por valor e linha a linha (para conteúdos de várias linhas, como chaves privadas):
  - tudo o que vem do Vault;
  - variáveis sensíveis do Firstlume, inclusive quando entram na declaração por `{var: …}`;
  - senhas de registry, também em base64.

  Linhas com menos de 6 caracteres não são mascaradas sozinhas, para não estragar o log.
- **R5.2** A API nunca devolve o valor de variável sensível nem senha de registry. A declaração em si e as referências ao Vault (`caminho#campo`) não são segredo e aparecem na tela.
- **R5.3** Arquivos sensíveis são escritos com `0600`. O diretório de trabalho do run já é apagado ao fim da fase (pod efêmero no Kubernetes; diretório apagado no driver local).

## 5. API

As configurações continuam em `GET|PUT /api/v1/orgs/{org}/(folders|stacks)/{id}/settings` e no nível da organização. Mudanças:

| Campo | O que muda |
|---|---|
| `variables[].category` | aceita `file` (R2.8) |
| `vault_role` | nova configuração herdada (R3.2) |
| `registries[]` | `{host, kind: oci\|terraform, username, password \| password_vault: {path, field}}`; a senha nunca é devolvida. Sem o campo, as credenciais do nível ficam como estão |

Novas rotas:

| Rota | O que faz |
|---|---|
| `GET\|PUT\|DELETE /api/v1/orgs/{org}/vault` | Conexão Vault (R3.1) |
| `GET /api/v1/orgs/{org}/vault/public-key` | Chave pública em PEM (R3.4) |
| `POST /api/v1/orgs/{org}/stacks/{id}/vault/check` | `{phase, paths}`: o servidor faz um login de teste com o JWT e o papel efetivo do stack e consulta `sys/capabilities-self` para cada caminho. Responde, por caminho, se o papel pode ler, sem ler nenhum valor. Revoga o token em seguida |
| `GET /api/v1/orgs/{org}/stacks/{id}/declaration` | A declaração do último commit conhecido: os itens, as referências ao Vault de cada um e, quando o commit já passou por uma fase, em que pastas cada padrão caiu. Sem valores |
| `GET /.well-known/openid-configuration`, `GET /.well-known/jwks.json` | Descoberta para o Vault (R3.4) |

O `ExecSpec` ganha o JWT do run, a conexão Vault, o papel efetivo, as variáveis `file` e as credenciais de registry das configurações. Para o apply, também os hashes do plan (R3.8). A declaração o executor lê do próprio checkout.

## 6. Ambiente de desenvolvimento

- `docker-compose` ganha:
  - **Vault** em modo dev, no profile `vault`;
  - um **registry OCI com login** (`registry:2` com htpasswd), no profile `registry`.
- `make dev-vault`:
  - sobe o Vault;
  - habilita o método `jwt` apontando para o Firstlume local;
  - cria um KV v2 com segredos de exemplo, políticas e dois papéis (`dev`, ligado a `stack_path = "*/dev/*"`, e `prod`);
  - grava em `.env` o que o servidor precisa.
- `make dev-registry`: sobe o registry, publica um chart de exemplo e grava a credencial em `.env`.
- O `acme/infra` do Gitea local ganha:
  - um stack Terragrunt com várias pastas de máquina, que lê `connection.hcl` com `read_terragrunt_config` e o declara em `config.yaml` com expansão por pastas;
  - um stack que renderiza um chart OCI do registry com `data "helm_template"`.

## 7. Entregas e critérios de aceite

### Marco F1: declaração e arquivos (sem Vault)

Testes: `internal/stackconfig` (a declaração e os erros com a linha), `internal/executor/files_test.go` (arquivos, formatos, recusas, e Terraform e Terragrunt reais lendo o que foi gerado) e `internal/server/declaration_test.go` (variáveis de arquivo e a rota da declaração). A verificação ponta a ponta com o agent fica para o cluster.

- [x] **CAF1.1** Um `config.yaml` com `files` em `hcl` com `block: locals`, de fontes literal e `var`, gera o arquivo antes do `init`. Um Terragrunt real lê com `read_terragrunt_config` e o plan funciona.
- [ ] **CAF1.2** `path: sp*/*/connection.hcl` com `${name}` escreve um arquivo por pasta de máquina, cada um com o próprio nome. Uma pasta nova no Git recebe o arquivo no run seguinte, sem mudar nada no Firstlume. Um padrão que não casa com nada falha a fase citando o padrão. (Testado no `prepare`; falta a pasta nova num run de verdade.)
- [x] **CAF1.3** Os formatos `hcl`, `json`, `yaml`, `env` e `raw` geram arquivos válidos para valores com aspas, quebras de linha, `${`, `%{`, acentos e números. Teste com o Terraform real lendo o `hcl` e o `json` (`*.auto.tfvars.json`).
- [x] **CAF1.4** Uma declaração inválida (formato desconhecido, `path` com `..`, em `.git`, `values` em `raw`, fonte malformada) falha a fase apontando a linha. Um arquivo que já existe no Git ou gerado duas vezes falha a fase.
- [x] **CAF1.5** `env` e `terraform` da declaração chegam ao Terraform. O mesmo nome nas configurações do Firstlume vence, e o log registra a troca sem valores.
- [ ] **CAF1.6** Uma variável `file` herdada da pasta aparece no checkout com o conteúdo digitado. Sensível, tem `0600` e o conteúdo (uma chave privada de teste, de várias linhas) não aparece no log, nem num `cat` feito pelo próprio código Terraform. (Testado no `prepare` e no mascarador; falta o `cat` num run de verdade.)
- [x] **CAF1.7** `GET …/declaration` lista os itens da declaração do último commit, sem valores.

### Marco F2: Vault

Testes: `internal/executor/vaultsource_test.go` e `internal/server/vault_test.go`, contra o Vault 2.0.4 real do `make dev-vault`. Também verificado ponta a ponta com o agent: o stack `environments/machines` do Gitea de dev numa pasta `dev`, com os valores mascarados no log e um segredo trocado entre o plan e o apply fazendo o apply falhar.

- [x] **CAF2.1** Com o Vault do `make dev-vault`, `{vault: secret/infra/dev/n01, field: ip}` num arquivo e em `terraform` é lido pelo executor. O valor não aparece em log, API, banco nem artefatos do run.
- [x] **CAF2.2** O papel `dev` lê `secret/infra/dev/*` e **não** lê `secret/infra/prod/*`: um stack em `dev` cuja declaração aponta para prod falha com "permissão negada" citando o item e o caminho, sem valor.
- [x] **CAF2.3** Um papel ligado a `phase = apply` não serve para o plan: o plan falha com a mensagem do Vault.
- [x] **CAF2.4** O JWT emitido valida com a chave de `/.well-known/jwks.json` e com a PEM da tela. Vence em 15 minutos. O token do Vault é revogado ao fim da fase.
- [x] **CAF2.5** Um segredo trocado no Vault entre o plan e o apply faz o apply falhar com "o segredo mudou desde o plan", sem valores. Planejado de novo, aplica.
- [x] **CAF2.6** Vault fora do ar, papel inexistente ou campo ausente falham a fase com mensagem que diz qual dos três, e qual item.
- [x] **CAF2.7** `POST …/vault/check` diz "pode ler" para `secret/infra/dev/n01` com o papel `dev` e "não pode" para o caminho de prod, sem ler valores. Com o papel inexistente, devolve o erro do Vault.
- [x] **CAF2.8** Com `${name}` vindo de uma pasta, o caminho do Vault não consegue sair do prefixo declarado (R3.6).

### Marco F3: credenciais de registry

Testes: `internal/executor/registries_test.go` (arquivos e variáveis, precedência, mascaramento, e `helm pull` real contra o registry do `make dev-registry`, com a senha nas configurações e no Vault) e `internal/server/registries_test.go` (validação, herança por host, senha nunca devolvida, apply com as credenciais do plan). Também verificado ponta a ponta com o agent: o stack `environments/charts` do Gitea de dev, com `data "helm_template"` do provider `helm`, falha com "basic credential not found" sem credencial e aplica com a credencial da organização.

- [x] **CAF3.1** Com o registry do `make dev-registry`, um stack com `data "helm_template"` de um chart `oci://` desse registry funciona com a credencial herdada da organização e falha sem ela.
- [x] **CAF3.2** Uma credencial no stack substitui a da organização para o mesmo host; hosts diferentes se somam; a da declaração entra quando o host não está nas configurações.
- [x] **CAF3.3** Uma credencial `terraform` vira `TF_TOKEN_<host>` com o nome no formato do Terraform (teste unitário com host com ponto e hífen).
- [x] **CAF3.4** A senha vinda do Vault funciona, tanto nas configurações quanto na declaração. Nem ela nem o base64 `usuário:senha` aparecem no log.

### Marco F4: telas (depois do desenho aprovado)

Desenho: telas 17 a 20 do canvas de telas. Verificado no navegador contra o servidor de dev, com o Vault do `make dev-vault`.

- [ ] **CAF4.1** O stack mostra a declaração do último commit: os arquivos que serão gerados, em que pastas, com o formato e as referências ao Vault de cada valor, sem valores. (Pronto, menos as pastas: o executor ainda não informa em quais pastas cada padrão caiu.)
- [x] **CAF4.2** O editor de variáveis tem a categoria arquivo (caminho, conteúdo em várias linhas, sensível).
- [x] **CAF4.3** Tela da conexão Vault, com a chave pública, o papel efetivo do stack e o teste de leitura (`vault/check`) a partir das referências da declaração.
- [x] **CAF4.4** Lista de registries herdada, com a origem de cada credencial, como nas variáveis.

### Critérios gerais

- [ ] **CGF1** `make vet`, os testes unitários, os de integração e o `tsc` passam.
- [ ] **CGF2** Os testes usam Terraform, Terragrunt, Vault e registry reais, não só chamadas HTTP feitas à mão.
- [ ] **CGF3** README com: a declaração em `config.yaml` (formatos, fontes, expansão por pastas), a configuração do Vault (método `jwt`, papel com `bound_claims`, exemplo de política) e as credenciais de registry.
- [ ] **CGF4** Nada de dados reais da empresa no repositório público.

## 8. Decisões pendentes

| # | Pergunta | Proposta |
|---|---|---|
| D1 | Como o Firstlume se autentica no Vault? | **JWT emitido pelo Firstlume** (R3.3). É o único dos três que dá identidade **por stack e por fase** sem uma conta de serviço por stack. Pede que o Vault aceite um método novo (pergunta já aberta com a equipe do primeiro projeto). |
| D2 | E se o Vault não puder aceitar o método `jwt`? | Acrescentar **Kubernetes auth** com a ServiceAccount do executor num marco seguinte. Funciona sem configurar nada no Firstlume, mas a identidade é a mesma para todos os stacks daquele agent. AppRole fica por último: guardaria um segredo fixo no Firstlume. |
| D3 | Segredo do Vault que muda entre plan e apply | **Falhar o apply** (R3.8): o apply roda o que foi revisado. Quando vierem segredos dinâmicos (lease, SSH CA), eles terão uma marcação para ficar fora dessa conferência. |
| D4 | Declaração estruturada em YAML ou template livre? | **YAML estruturado** (seção 1.1), decidido em 2026-10-31. Template livre só se aparecer um arquivo que os formatos não cobrem. |
| D5 | Arquivo que já existe no Git | **Falhar.** Sobrescrever código versionado em silêncio esconde erros. Se aparecer um caso real (um exemplo versionado que o run deve trocar), uma marcação explícita `replace: true` resolve. |
| D6 | O mesmo nome na declaração do repositório e nas configurações do Firstlume | **Vence o Firstlume**, com registro no log: quem opera o ambiente consegue trocar um valor sem mexer no código. A alternativa é falhar a fase, mais segura e mais chata na migração. |
| D7 | O `stack_path` muda quando alguém renomeia uma pasta. Um admin pode, renomeando, colocar um stack sob o papel de prod? | Sim, e hoje todo usuário é admin. Os papéis podem usar `folder_ids` (não mudam com renomear). Quando vierem os papéis de usuário, renomear e mover passam a ser permissões próprias. |
