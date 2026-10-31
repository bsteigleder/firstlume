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

A importação de states existentes, os tokens para uso fora dos runs e a aba State na UI vêm nos próximos marcos da spec.

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
2. State backend HTTP próprio, por unit, com lock, versões e criptografia, no Postgres ou no Garage/S3 (✅ marcos S1 e S2); importação de state existente, modo "só state" para quem continua usando o CI atual e aba State na UI
3. Terragrunt nativo: descoberta de units, grafo de dependências, um run por unit e agents com labels por rede/zona
4. Segredos vindos do Vault (JWT emitido pelo Firstlume por run, Kubernetes auth ou AppRole), sem o Firstlume virar um segundo cofre; variáveis do tipo arquivo
5. Usuários, grupos e SSO; aprovadores por pessoa ou grupo; trilha de auditoria
6. Integração Git (Gitea/Forgejo, GitHub, GitLab): plan em PR, commit status, apply no merge e gatilho por impacto
7. Observabilidade: export via OpenTelemetry (métricas, eventos, logs e traces) e notificações (webhook, Slack, Rocket.Chat)
8. Hooks por fase, tarefas agendadas, políticas (OPA), backup/restore e alta disponibilidade

Para pensar no futuro: custos (estimativa no plan e acompanhamento por ambiente).

## Licença

Copyright © 2026 Bernardo Steigleder. Todos os direitos reservados.

O código está público apenas para consulta. Nenhuma licença de uso, cópia, modificação ou distribuição é concedida.
