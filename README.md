# Firstlume

Plataforma de automação de Infraestrutura como Código (Terraform, OpenTofu e Terragrunt), no estilo do env0, instalável via Helm em qualquer cluster Kubernetes.

> **Status: fase 1 (MVP).** Já existem os runs de plan/apply com logs e aprovação, o agent que cria Jobs no Kubernetes e o chart Helm. Ainda não existem o state backend próprio, a integração com GitHub, o drift detection e a UI. Veja o [roadmap](#roadmap).

## Arquitetura

```
             ┌──────────────────────────────┐      ┌──────────┐
  usuário ──▶│ server (API)                 │◀────▶│ Postgres │
  (API/CLI)  │  /api/v1   usuários (admin)  │      └──────────┘
             │  /agent/v1 agents            │
             │  /exec/v1  executores        │
             └──────▲───────────────▲───────┘
                    │ claim (pull)  │ spec, logs, plano, resultado
             ┌──────┴───────┐       │ (token válido só para 1 fase)
             │ agent        │──cria──▶ Job "flr-<run>-plan" ─┘
             └──────────────┘          └─ firstlume exec
```

- **Um binário, uma imagem.** `firstlume server | agent | exec | migrate | keygen`.
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
                    └─▶ apply_queued                     (auto_approve)
qualquer fase em execução ─▶ failed  |  queued/apply_queued ─▶ canceled
```

Se o executor parar de enviar heartbeat por `FIRSTLUME_HEARTBEAT_TIMEOUT` (10 min por padrão), o run é marcado como `failed`. Se o pod morrer (por exemplo, por OOM ou deadline), o agent reporta a falha.

## Rodando localmente

Requisitos: Go 1.26+, Docker e git.

```bash
make dev-db        # Postgres em Docker
make dev-env       # gera .env com chave mestra e tokens
make run-server    # API em 127.0.0.1:8787
make run-agent     # outro terminal: agent com driver local (sem Kubernetes)
```

O driver `local` roda o executor como subprocesso, sem isolamento. Use só para desenvolvimento.

Exemplo de um fluxo completo, com o token admin que está em `.env`:

```bash
source .env
H="Authorization: Bearer $FIRSTLUME_ADMIN_TOKEN"

# 1. template: onde está o código e quais versões usar
TPL=$(curl -s -H "$H" 127.0.0.1:8787/api/v1/templates -d '{
  "name": "hello", "repository": "https://github.com/sua-org/infra.git",
  "path": "examples/hello", "default_ref": "main",
  "engine": "terraform", "engine_version": "1.9.8"
}' | jq -r .id)

# 2. ambiente
ENV=$(curl -s -H "$H" 127.0.0.1:8787/api/v1/environments -d "{
  \"template_id\": \"$TPL\", \"name\": \"dev\"
}" | jq -r .id)

# 3. variáveis (as sensíveis são criptografadas e mascaradas nos logs)
curl -s -X PUT -H "$H" 127.0.0.1:8787/api/v1/environments/$ENV/variables -d '{
  "variables": [
    {"key": "greeting", "category": "terraform", "value": "olá"},
    {"key": "AWS_SECRET_ACCESS_KEY", "category": "env", "sensitive": true, "value": "..."}
  ]
}'

# 4. run de apply, acompanhando os logs ao vivo (SSE)
RUN=$(curl -s -H "$H" 127.0.0.1:8787/api/v1/environments/$ENV/runs -d '{"type": "apply"}' | jq -r .id)
curl -N -H "$H" "127.0.0.1:8787/api/v1/runs/$RUN/logs?follow=true"

# 5. aprovar o plano
curl -s -X POST -H "$H" 127.0.0.1:8787/api/v1/runs/$RUN/approve
```

Testes:

```bash
make test               # unitários
make test-integration   # com Postgres (requer make dev-db)
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
migrations/           schema SQL (goose, aplicado no startup com advisory lock)
deploy/helm/          chart único (controlPlane + agent)
deploy/docker/        Dockerfile
```

## Limitações conhecidas (fase 1)

- **Ainda não há state backend.** Os templates precisam declarar um backend remoto próprio, como S3 ou GCS. Com o backend local, o state se perde ao fim de cada pod.
- **Autenticação só por token admin.** Usuários, SSO e RBAC entram depois.
- **Cancelar um run em execução não é suportado**, de propósito: interromper um apply pode corromper o state.
- **As ferramentas são baixadas a cada run** (releases oficiais, com checksum SHA256 verificado). O executor precisa de acesso de saída à internet, ou de um mirror (ainda não configurável).
- **Licença do Terraform:** as versões 1.6 em diante são BSL. Para oferecer a plataforma comercialmente, use `engine: opentofu` ou Terraform ≤ 1.5.7.

## Roadmap

1. ✅ Runs de plan/apply, logs, aprovação, agent com Kubernetes e chart Helm
2. State backend HTTP próprio, com lock e versionamento
3. GitHub App: plan automático em PR, comentário e apply no merge
4. Drift detection agendado
5. UI web, usuários e SSO/RBAC, credenciais OIDC, políticas OPA, custos

## Licença

Copyright © 2026 Bernardo Steigleder. Todos os direitos reservados.

O código está público apenas para consulta. Nenhuma licença de uso, cópia, modificação ou distribuição é concedida.
