# Firstlume

Plataforma de automação de Infraestrutura como Código (Terraform, OpenTofu e Terragrunt), no estilo do env0, instalável via Helm em qualquer cluster Kubernetes.

> **Status: fase 2.** Já existem organizações, pastas e stacks com configurações herdadas, runs de plan/apply com revisão por recurso e aprovação configurável, drift detection agendado, a conexão com o Gitea, o state backend próprio (por unit, com lock e versões), uma UI web, o agent que cria Jobs no Kubernetes e o chart Helm. Ainda não existem usuários e login. Veja o [roadmap](#roadmap).

## Arquitetura

```
             ┌──────────────────────────────┐      ┌──────────┐
  usuário ──▶│ server (API + UI)            │◀────▶│ Postgres │
  (UI/API)   │  /api/v1   usuários (admin)  │      └──────────┘
             │  /agent/v1 agents            │
             │  /exec/v1  executores        │
             │  /state/v1 state (Terraform) │
             └──────▲───────────────▲───────┘
                    │ claim (pull)  │ spec, logs, plano, resultado
             ┌──────┴───────┐       │ (token válido só para 1 fase)
             │ agent        │──cria──▶ Job "flr-<run>-plan" ─┘
             └──────────────┘          └─ firstlume exec
```

- **Um binário, uma imagem.** `firstlume server | agent | exec | migrate | keygen`. A UI (React, em `web/`) é embutida no binário e servida pelo server no mesmo endereço da API.
- **O agent busca o trabalho (modelo pull).** Ele só faz requisições de saída, então pode rodar em outro cluster ou na rede do cliente sem que a API precise alcançá-lo.
- **Cada fase de um run roda num Job efêmero.** O pod é endurecido: não roda como root, tem rootfs somente leitura, não pode escalar privilégios e o Job nunca é retentado. As credenciais de nuvem vêm da identidade do pod (IRSA, Workload Identity), via anotações no ServiceAccount do executor.
- **O lock é por ambiente.** Só um run por vez executa ou aguarda aprovação em cada ambiente. Os demais esperam na fila, em ordem de chegada.
- **O apply roda exatamente o que foi planejado.** O plan grava o commit SHA e o arquivo de plano, criptografado no banco. O apply faz checkout desse commit e aplica esse arquivo.
- **Segredos criptografados.** Variáveis sensíveis, tokens git e planos são cifrados com AES-256-GCM, usando uma chave derivada de `FIRSTLUME_SECRET_KEY`. Valores sensíveis são mascarados nos logs e nunca são devolvidos pela API.

### Ciclo de vida de um run

```
queued ─▶ planning ─┬─▶ no_changes                       (sem mudanças)
                    ├─▶ planned                          (type=plan)
                    ├─▶ awaiting_approval ─┬─▶ apply_queued ─▶ applying ─▶ applied
                    │                      └─▶ discarded
                    ├─▶ apply_queued                     (auto_approve)
                    └─▶ drifted                          (type=drift)
qualquer fase em execução ─▶ failed  |  queued/apply_queued ─▶ canceled
```

A aprovação é configurável por ambiente, na UI ou com `PATCH /api/v1/environments/{id}` (`auto_approve`). O padrão é exigir aprovação.

### Drift detection

Cada ambiente pode ter um intervalo de verificação (`drift_interval_minutes`, de 15 min a 7 dias). O server cria um run `drift` quando a verificação vence e o ambiente está livre. Esse run é um `plan` que **só reporta**: nunca aplica, não pede aprovação e não guarda o arquivo de plano. O resultado (`in_sync`, `drifted` ou `error`) fica no ambiente e aparece na UI. Também dá para verificar na hora, com um run do tipo `drift`. Mudanças na branch que ainda não foram aplicadas também aparecem como drift.

Se o executor parar de enviar heartbeat por `FIRSTLUME_HEARTBEAT_TIMEOUT` (10 min por padrão), o run é marcado como `failed`. Se o pod morrer (por exemplo, por OOM ou deadline), o agent reporta a falha.

### State

O Firstlume é o backend de state dos stacks ([spec 0002](docs/specs/0002-state-backend.md)). Ele usa o backend `http` do Terraform e do OpenTofu, sem plugin. O código declara o backend e o Firstlume preenche o endereço e a credencial em cada run:

```hcl
terraform {
  backend "http" {}
}
```

No Terragrunt, cada unit tem o seu state. O `root.hcl` monta o endereço de cada uma a partir de `FIRSTLUME_STATE_URL`:

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

- **Lock por unit.** Num `run --all`, cada unit grava o seu state quando o apply dela termina: se uma unit falha, as outras ficam salvas.
- **Plan não grava.** A credencial da fase de plan (e do drift) só lê e trava o state. Só o apply grava.
- **Versões.** Cada gravação vira uma versão imutável, cifrada com uma chave própria. Gravações que perderiam state (serial menor, lineage de outra infraestrutura) são recusadas.
- **As travas de um run** são soltas quando a fase termina, de qualquer jeito que termine.
- **Código com outro backend** (por exemplo `s3`) continua funcionando: as variáveis do Firstlume são ignoradas. Código **sem backend** falha no Init, porque o state ficaria no disco do runner e se perderia.
- **Um stack com recursos no state não pode ser excluído.** Rode um destroy ou exclua o state antes (`DELETE /api/v1/orgs/{org}/stacks/{id}/state`). O state excluído fica guardado por 30 dias.

#### Onde o state fica

Configurado uma vez, na instalação. O Firstlume sempre cifra cada versão antes de gravar, e a chave do objeto só tem ids (nada de nome de stack ou unit).

| Variável | Padrão | |
|---|---|---|
| `FIRSTLUME_STATE_STORAGE` | `postgres` | `postgres` ou `s3` (qualquer S3 compatível, testado com Garage) |
| `FIRSTLUME_STATE_S3_ENDPOINT` | | por exemplo `http://garage.garage.svc:3900` |
| `FIRSTLUME_STATE_S3_BUCKET` | | |
| `FIRSTLUME_STATE_S3_REGION` | `garage` | |
| `FIRSTLUME_STATE_S3_PREFIX` | | para dividir um bucket |
| `FIRSTLUME_STATE_S3_ACCESS_KEY_ID`, `FIRSTLUME_STATE_S3_SECRET_ACCESS_KEY` | | a chave precisa ler, gravar, apagar e listar no bucket |
| `FIRSTLUME_STATE_S3_PATH_STYLE` | `true` | o Garage precisa de path-style |
| `FIRSTLUME_STATE_KEEP_VERSIONS` | `0` | versões guardadas por unit; `0` guarda todas |
| `FIRSTLUME_STATE_MAX_MIB` | `64` | tamanho máximo de uma versão |

Com `s3`, o server grava, lê e apaga um objeto de teste ao subir. Se falhar, ele não sobe e diz o motivo: endpoint fora do ar, credencial recusada, bucket inexistente ou chave sem permissão. No Helm, use `controlPlane.state.storage: s3` e `controlPlane.state.s3.*`, com a chave num Secret existente.

Trocar de storage não move as versões já gravadas.

> **Backend `s3` direto no Garage:** o Garage (testado na v2.4.1) ignora a escrita condicional (`If-None-Match`), então o `use_lockfile` do backend `s3` do Terraform **não trava nada** nele: um apply passa por cima da trava de outro. Com o backend do Firstlume, a trava fica no Postgres e funciona com qualquer storage.

#### Usar o state fora dos runs ("só state")

Uma pipeline que ainda não roda no Firstlume (Woodpecker, GitLab CI, etc.) pode usar o state dele com um **token de state** do stack. Assim ela ganha lock e versões antes de os runs migrarem.

```bash
curl -X POST -H "Authorization: Bearer $ADMIN" "$API/api/v1/orgs/$ORG/stacks/$STACK/state/tokens" \
  -d '{"name": "woodpecker", "write": true}'   # "expires_at" é opcional
```

O token aparece só nessa resposta. Ele pode ser revogado com `DELETE .../state/tokens/{id}`, e `GET .../state/tokens` mostra o último uso.

- **Leitura e escrita** (`"write": true`): lê, trava e grava. Cada versão gravada leva o nome do token. Uma trava do token não se solta sozinha: só com o `UNLOCK` do próprio Terraform.
- **Só leitura**: só lê. O plan roda com `-lock=false`.

Na pipeline, com o `root.hcl` acima:

```bash
export FIRSTLUME_STATE_URL=https://firstlume.example.com/state/v1/stacks/$STACK
export TF_HTTP_USERNAME=firstlume TF_HTTP_PASSWORD=$FIRSTLUME_STATE_TOKEN
terragrunt run --all -- plan -lock=false      # token de leitura
terragrunt run --all -- apply                 # token de escrita
```

#### Importar um state existente

Um bundle `tar.gz` (ou um `terraform.tfstate` avulso) vira o state das units:

- cada `<caminho>/terraform.tfstate` do bundle vira a unit `<caminho>`. Um state dentro do `.terragrunt-cache` de uma unit vai para a unit;
- `prefix` remove o começo dos caminhos, para que a chave fique igual ao `path_relative_to_include()`;
- backups (`*.tfstate.backup`), pastas `.terraform` e metadados do macOS (`._*`) são ignorados;
- qualquer outro arquivo, link, caminho com `..` ou state inválido recusa o bundle inteiro, com a lista do que está errado;
- a importação grava tudo ou nada.

```bash
# Simula: mostra, por unit, serial, recursos e o que aconteceria. Não grava nada.
curl -X POST -H "Authorization: Bearer $ADMIN" "$API/api/v1/orgs/$ORG/stacks/$STACK/state/import?dry_run=true" \
  -F file=@prod.tar.gz -F prefix=environments/prod
# Importa.
curl -X POST -H "Authorization: Bearer $ADMIN" "$API/api/v1/orgs/$ORG/stacks/$STACK/state/import" \
  -F file=@prod.tar.gz -F prefix=environments/prod
```

Uma unit sem state ganha a versão 1. Uma unit que já tem state aparece como `same`, `newer`, `older` ou `conflict` (outro lineage) e só muda se for marcada com `-F replace=<unit>` (uma vez por unit). Nesse caso ganha uma versão nova, e o histórico fica. Se o state importado tiver o mesmo lineage e um serial que não passa do atual, o serial sobe, para que um plano salvo antes não seja aplicado em cima dele. Uma unit travada não pode receber importação.

#### Migrar uma pipeline que guarda o state num bundle

1. Crie o stack no Firstlume apontando para a pasta do ambiente.
2. Pare os applies da pipeline. Importe o bundle atual com `dry_run=true` e confira as units e o `prefix`. Depois importe de verdade.
3. Troque o `root.hcl` pelo trecho acima e crie os tokens de state (um de leitura para o plan e um de escrita para o apply).
4. Na pipeline, exporte `FIRSTLUME_STATE_URL` e `TF_HTTP_PASSWORD` e remova os passos de baixar e subir o bundle.
5. Confira: `terragrunt run --all -- plan -lock=false` deve dar **"No changes" em todas as units**. Guarde o bundle antigo por um tempo, só para leitura.

Para experimentar localmente: `make dev-state-bundle` gera `deploy/dev/state-bundle.tar.gz` com o state do stack Terragrunt de exemplo (`acme/infra`, `environments/tg`). Importe-o com `prefix=environments/tg`.

A aba State na UI vem no próximo marco da spec.

### Arquivos e variáveis do run

Além das variáveis das configurações (`env` vira variável de ambiente, `terraform` vira `TF_VAR_`), um run recebe ([spec 0003](docs/specs/0003-arquivos-vault-registries.md)):

- **variáveis do tipo arquivo** (`"category": "file"`) nas configurações: a chave é o caminho do arquivo, relativo à pasta do stack, e o valor é o conteúdo. São herdadas como as outras e servem para quem não usa Vault, ou para um arquivo comum a muitos stacks;
- o que o **repositório declara** em `.firstlume/config.yaml`: arquivos montados em um formato, mais variáveis de ambiente e do Terraform.

```yaml
version: 1
engine: terraform
engine_version: 1.15.6
terragrunt_version: 1.1.6
run_all: true

files:
  # Um arquivo em cada pasta que casa com sp*/*; ${name} é o nome da pasta, ${dir} o caminho.
  - path: sp*/*/connection.hcl
    format: hcl
    block: locals            # read_terragrunt_config não aceita atributos soltos
    values:
      host: "${name}.lan"
      user: { var: SSH_USER } # variável das configurações do Firstlume
      port: 22
  - path: credentials.auto.tfvars.json
    format: json
    values:
      buckets: { logs: { var: LOGS_KEY } }
  - path: ssh/id_ed25519
    format: raw
    content: { var: SSH_KEY }

env:
  TF_LOG: ""
terraform:
  zone: sp1                  # vira TF_VAR_zone; mapas e listas viram JSON
```

- **Formatos:** `hcl`, `json`, `yaml`, `env` (lido pelo `source` do shell) e `raw` (o conteúdo como está). O Firstlume faz o escape. No `hcl`, um valor com `${` ou `%{` continua texto, nunca vira expressão do Terraform.
- **Fontes:** um literal, ou `{var: NOME}`, que lê uma variável `env` ou `terraform` das configurações. A fonte `{vault: caminho, field: campo}` vem com o Vault (marco F2 da spec).
- **Por pastas:** o curinga só vale nas pastas do `path`, não no nome do arquivo, e não casa com pastas ocultas (`.terragrunt-cache`). Um padrão que não casa com nenhuma pasta falha o run, para um erro de digitação não passar despercebido.
- **Quando:** os arquivos são escritos depois do checkout e antes do `init`, no passo **Prepare**, em toda fase. O apply recebe os mesmos valores do plan.
- **Segurança:**
  - um arquivo que usa uma variável sensível é sensível: sai com permissão `0600` e o conteúdo é mascarado no log, linha a linha;
  - o Firstlume não sobrescreve um arquivo que já existe no repositório, não escreve o mesmo arquivo duas vezes e não segue link simbólico para fora da pasta do stack. Nesses casos o run falha;
  - o `path` não pode ter `..` nem apontar para `.git` ou `.firstlume`.
- **O mesmo nome** em `env` ou `terraform` da declaração e nas configurações: vencem as configurações, e o log registra a troca, sem valores.
- `GET /api/v1/orgs/{org}/stacks/{id}/declaration` mostra o que o último commit declara, com as referências e sem valores.

## Rodando localmente

Requisitos: Go 1.26+, Node 22+, Docker e git.

```bash
make dev-db        # Postgres em Docker
make dev-env       # gera .env com chave mestra e tokens
make run-server    # UI e API em http://127.0.0.1:8787 (compila a UI antes)
make run-agent     # outro terminal: agent com driver local (sem Kubernetes)
```

Opcionais: `make dev-gitea` sobe um Gitea com uma organização de exemplo, e `make dev-garage` sobe um Garage e passa o state do server de dev para ele.

O driver `local` roda o executor como subprocesso, sem isolamento. Use só para desenvolvimento.

Abra http://127.0.0.1:8787 e entre com o `FIRSTLUME_ADMIN_TOKEN` do `.env`. `deploy/dev/demo.sh` cria uma pasta e um stack de exemplo (`examples/hello` deste repositório) e dispara um apply.

Para mexer na UI com hot reload, rode `make web-dev` (Vite em http://localhost:5173, com proxy para a API em 8787).

O fluxo pela API, com o token admin que está em `.env` (a organização `default` é criada na subida):

```bash
source .env
H="Authorization: Bearer $FIRSTLUME_ADMIN_TOKEN"
O=127.0.0.1:8787/api/v1/orgs/default

# 1. uma pasta (opcional: stacks também podem ficar na raiz)
F=$(curl -s -H "$H" $O/folders -d '{"name": "Ambientes"}' | jq -r .id)

# 2. variáveis e segredos da pasta, herdados pelos stacks abaixo dela
curl -s -X PUT -H "$H" $O/folders/$F/settings -d '{
  "require_approval": true,
  "variables": [
    {"key": "greeting", "category": "terraform", "value": "olá"},
    {"key": "AWS_SECRET_ACCESS_KEY", "category": "env", "sensitive": true, "value": "..."}
  ]
}'

# 3. o stack: a pasta do repositório que contém .firstlume/config.yaml
ST=$(curl -s -H "$H" $O/stacks -d "{
  \"folder_id\": \"$F\", \"name\": \"hello\",
  \"repository\": \"https://github.com/sua-org/infra.git\", \"path\": \"examples/hello\"
}" | jq -r .id)

# 4. run de apply, acompanhando os logs ao vivo (SSE)
RUN=$(curl -s -H "$H" $O/stacks/$ST/runs -d '{"type": "apply"}' | jq -r .id)
curl -N -H "$H" "$O/runs/$RUN/logs?follow=true"

# 5. revisar o que muda, recurso por recurso, e aprovar
curl -s -H "$H" $O/runs/$RUN/changes | jq .
curl -s -X POST -H "$H" $O/runs/$RUN/approve -d '{"acknowledged": []}'
```

Testes:

```bash
make test               # unitários
make test-integration   # com Postgres (requer make dev-db); usa Terraform e Terragrunt reais se achar
```

## Instalando com Helm

Tudo num cluster só, com banco gerenciado:

```bash
helm install firstlume deploy/helm/firstlume -n firstlume --create-namespace \
  --set controlPlane.database.url='postgres://user:pass@host:5432/firstlume?sslmode=require'
```

Agent adicional em outro cluster (crie o token com `POST /api/v1/agents`):

```bash
helm install firstlume-agent deploy/helm/firstlume -n firstlume --create-namespace \
  --set controlPlane.enabled=false \
  --set agent.name=cluster-b \
  --set agent.apiUrl=https://firstlume.empresa.com \
  --set agent.token=fla_...
```

A imagem é publicada pelo GitHub Actions em `ghcr.io/bsteigleder/firstlume` (`main`, `sha-<commit>` e, nas tags `vX.Y.Z`, `X.Y.Z`), para amd64 e arm64. Se o pacote for privado, crie um Secret de pull e passe `--set imagePullSecrets[0].name=<secret>`: o server, o agent e os Jobs dos runs (pela ServiceAccount do executor) passam a usá-lo. Num cluster com registry interno, prefira um proxy-cache dele apontando para o GHCR, com a credencial num lugar só.

Para testar sem banco externo, use `--set postgresql.enabled=true`. É uma instância única e sem backup, só para desenvolvimento.

Faça backup da chave `secret-key` do Secret `<release>-control-plane`. Sem ela não dá para recuperar os dados criptografados. O Secret é preservado no `helm uninstall`.

Veja todas as opções em [values.yaml](deploy/helm/firstlume/values.yaml).

## Estrutura

```
cmd/firstlume/        binário único (subcomandos)
internal/api/         tipos compartilhados e máquina de estados do run
internal/server/      API HTTP (usuário, agent, executor) e streaming de logs
internal/store/       Postgres (pgx) e criptografia de segredos
internal/agent/       loop de claim e drivers (kubernetes, local)
internal/executor/    clone, instalação de ferramentas, init/plan/apply, logs mascarados
internal/secrets/     AES-GCM, tokens de run assinados (HMAC)
web/                  UI (React + TypeScript + Vite), embutida no binário
migrations/           schema SQL (goose, aplicado no startup com advisory lock)
deploy/helm/          chart único (controlPlane + agent)
deploy/docker/        Dockerfile
```

## Limitações conhecidas

- **Autenticação só por token admin.** Usuários, grupos, SSO e aprovadores por pessoa ou grupo entram depois.
- **Cancelar um run em execução não é suportado**, de propósito: interromper um apply pode corromper o state.
- **As ferramentas são baixadas a cada run** (releases oficiais, com checksum SHA256 verificado). O executor precisa de acesso de saída à internet, ou de um mirror (ainda não configurável).
- **Licença do Terraform:** as versões 1.6 em diante são BSL. Para oferecer a plataforma comercialmente, use `engine: opentofu` ou Terraform ≤ 1.5.7.

## Roadmap

1. ✅ Runs de plan/apply, logs, aprovação configurável, drift detection agendado (só reporta), UI web, agent com Kubernetes e chart Helm
2. State backend HTTP próprio, por unit, com lock, versões e criptografia, no Postgres ou no Garage/S3 (✅ marcos S1 e S2), importação de state existente e modo "só state" para quem continua usando o CI atual (✅ marco S3); aba State na UI
3. Terragrunt nativo: descoberta de units, grafo de dependências, um run por unit e agents com labels por rede/zona
4. Arquivos e variáveis declarados no repositório e variáveis do tipo arquivo (✅ marco F1 da spec 0003); segredos vindos do Vault com JWT emitido pelo Firstlume por run, sem o Firstlume virar um segundo cofre; credenciais de registry
5. Usuários, grupos e SSO; aprovadores por pessoa ou grupo; trilha de auditoria
6. Integração Git (Gitea/Forgejo, GitHub, GitLab): plan em PR, commit status, apply no merge e gatilho por impacto
7. Observabilidade: export via OpenTelemetry (métricas, eventos, logs e traces) e notificações (webhook, Slack, Rocket.Chat)
8. Hooks por fase, tarefas agendadas, políticas (OPA), backup/restore e alta disponibilidade

Para pensar no futuro: custos (estimativa no plan e acompanhamento por ambiente).

## Licença

Copyright © 2026 Bernardo Steigleder. Todos os direitos reservados.

O código está público apenas para consulta. Nenhuma licença de uso, cópia, modificação ou distribuição é concedida.
