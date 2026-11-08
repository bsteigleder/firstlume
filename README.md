# Firstlume

Plataforma de automação de Infraestrutura como Código (Terraform, OpenTofu e Terragrunt), no estilo do env0, instalável via Helm em qualquer cluster Kubernetes.

> **Status: fase 2.** Já existem organizações, pastas e stacks com configurações herdadas, runs de plan/apply com revisão por recurso e aprovação configurável, drift detection agendado, a conexão com o Gitea, o state backend próprio (por unit, com lock e versões), uma UI web, o agent que cria Jobs no Kubernetes e o chart Helm, e o login pelo Gitea (OIDC) com sessões, lista de grupos permitidos, membros e convites. Veja o [roadmap](#roadmap).

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
- **O lock é por stack.** Só um run por vez executa ou aguarda aprovação em cada stack. Os demais esperam na fila, em ordem de chegada.
- **O apply roda exatamente o que foi planejado.** O plan grava o commit SHA e o arquivo de plano, criptografado no banco. O apply faz checkout desse commit e aplica esse arquivo.
- **Segredos criptografados.** Variáveis sensíveis, tokens git e planos são cifrados com AES-256-GCM, usando uma chave derivada de `FIRSTLUME_SECRET_KEY`. Valores sensíveis são mascarados nos logs e nunca são devolvidos pela API.

### Ciclo de vida de um run

```
queued ─▶ planning ─┬─▶ no_changes                       (sem mudanças)
                    ├─▶ planned                          (type=plan)
                    ├─▶ awaiting_approval ─┬─▶ apply_queued ─▶ applying ─▶ applied
                    │                      └─▶ discarded
                    ├─▶ apply_queued                     (require_approval: false)
                    └─▶ drifted                          (type=drift)
qualquer fase em execução ─▶ failed  |  parado ou cancelado ─▶ canceled

type=command:  queued ─▶ planning ─▶ succeeded | failed                     (state_access: read)
               queued ─▶ [awaiting_approval ─▶ apply_queued ─▶] applying ─▶ succeeded | failed   (write)
```

A aprovação é a configuração `require_approval`, herdada da organização para as pastas e os stacks: vale o nível mais próximo do stack. Ela muda na UI ou com `PUT .../settings` (cuidado: esse `PUT` substitui o que o nível define, veja [Credenciais de registry](#credenciais-de-registry)). O padrão é exigir aprovação. A regra é lida quando o plan termina, então uma mudança também vale para os runs já na fila.

### Drift detection

O intervalo de verificação é a configuração herdada `drift_interval_minutes` (de 15 min a 7 dias; `0` desliga, e esse é o padrão). O server cria um run `drift` quando a verificação vence e o stack está livre. Esse run é um `plan` que **só reporta**: nunca aplica, não pede aprovação e não guarda o arquivo de plano. O resultado (`in_sync`, `drifted` ou `error`) fica no stack e aparece na UI. Também dá para verificar na hora, com um run do tipo `drift`. Mudanças na branch que ainda não foram aplicadas também aparecem como drift.

Se o executor parar de enviar heartbeat por `FIRSTLUME_HEARTBEAT_TIMEOUT` (10 min por padrão), o run é marcado como `failed`. Se o pod morrer (por exemplo, por OOM ou deadline), o agent reporta a falha.

### Parar um run

A mesma chamada cancela um run na fila e para um que está executando (na UI: Cancel run, Stop plan, Stop apply):

```bash
curl -s -X POST -H "$H" $O/runs/$RUN/cancel
```

- **Na fila** (`queued`, `apply_queued`), o run vira `canceled` na hora.
- **Executando** (`planning`, `applying`), o server registra quem pediu e quando, e o run aparece como Stopping. O executor fica sabendo no heartbeat seguinte (a cada 5 s), escreve "Stop requested by …" no log e manda SIGINT ao Terraform, OpenTofu ou Terragrunt, como um Ctrl+C: nada novo começa, o que está rodando termina ou para no meio, o state é salvo e o lock é solto.
- Um apply **nunca é morto** depois da interrupção, para poder gravar o state. Um plan é morto se não sair em 2 minutos.
- No fim, um plan termina `canceled` (não vai para aprovação nem para o apply). Um apply termina `canceled`, ou `applied` se terminou todas as mudanças antes de parar.

Em `awaiting_approval` vale `discard`. Se o executor não responde (agent fora do ar), o run fica Stopping até o timeout do heartbeat e falha. Detalhes na [spec 0008](docs/specs/0008-parar-um-run.md).

### Rodar um comando no stack

Um run do tipo `command` roda subcomandos do Terraform, OpenTofu ou Terragrunt no diretório do stack, com o mesmo preparo de um plan (checkout, arquivos, Vault, registries, ferramentas, `init`) e o backend do state já configurado. Serve para o que plan e apply não cobrem: `state mv`, `state rm`, `import`, `force-unlock`, ler outputs. Na UI, é a opção **Run a command** do diálogo de run; na API, `POST .../stacks/{id}/runs` com `{"type": "command", "command": "...", "state_access": "read"}`, e `POST .../stacks/{id}/commands/check` valida sem criar run (spec [0009](docs/specs/0009-run-de-comando.md)).

- **Sem shell.** Cada linha roda direto, em ordem, e para na primeira que falhar. Só é aceito o engine do stack (`terraform` ou `tofu`) ou, em stack Terragrunt, só `terragrunt`, que é quem monta o state de cada unit. Em ambos os casos, com uma lista fechada de subcomandos: `state list/show`, `output`, `show`, `validate`, `providers`, `version`, `workspace list/show` só leem; `state mv/rm/replace-provider`, `import`, `taint`, `untaint` e `force-unlock` mudam o state. Pipes, `;`, `&&`, `$( )`, redirecionamentos, `ssh`, `curl` e qualquer outro programa são recusados, assim como `apply`, `destroy`, `plan`, `state pull/push` e `-json` em `output` e `show`. Para chegar a uma unit: `terragrunt --working-dir units/vpc state list`, ou todas com `terragrunt run --all -- state list`.
- **Acesso ao state.** `read` (padrão) roda na fase plan, com o token que lê e trava mas não grava, sem aprovação. `write` roda na fase apply e segue a aprovação do stack: quando ela é exigida, o run espera aprovação ao chegar a vez dele, antes de qualquer checkout.
- **Onde roda.** Só agents que aceitam: o driver Kubernetes, ou o local com `FIRSTLUME_ALLOW_LOCAL_COMMANDS=true` (só para desenvolvimento). Agents de versões anteriores não pegam esses runs. No Kubernetes, os pods têm o rótulo `firstlume.io/run-type: command` e uma NetworkPolicy de saída (`agent.executor.commandEgress`, ligada por padrão) só libera DNS, a API do First Lume, HTTPS para a internet pública e o que estiver em `extraEgress`. SSH, telnet e o resto da rede privada ficam bloqueados. Vault e Git em endereço privado precisam entrar em `extraEgress`. A política só vale se o plugin de rede do cluster a aplica (Calico, Cilium e outros).
- O código do repositório ainda roda, como num plan (o provider durante um `import`, hooks do Terragrunt). Ele passa por revisão no Git; o que é digitado no diálogo, não, e por isso tem a lista.

### Gatilhos do Git

Um push na branch de um stack pode disparar o run sozinho. Por enquanto só com o Gitea. Gerar o segredo e instalar o webhook ainda são passos manuais, até o marco G2 ([spec 0006](docs/specs/0006-gatilhos-git.md)).

1. Gere o segredo do webhook. Ele só aparece nesta resposta, junto com a URL e os eventos:

   ```bash
   curl -X POST -H "Authorization: Bearer $TOKEN" https://firstlume.example.com/api/v1/orgs/{org}/git/webhook-secret
   ```

2. No repositório do Gitea, crie um webhook do tipo `gitea` com essa URL e esse segredo, `content_type` `json` e os eventos `push`, `pull_request` e `pull_request_sync`. O Gitea recusa endereços privados por padrão: numa instalação interna, ajuste `[webhook] ALLOWED_HOST_LIST` no `app.ini`.
3. Ligue o gatilho, herdado como as outras configurações, em **On push** nas configurações da organização, de uma pasta ou do stack (na API, `"git_on_push"` no `PUT .../settings`):

   | Valor | Um push que afeta o stack... |
   |---|---|
   | `off` (padrão) | não faz nada |
   | `plan` | cria um run `plan`, que nunca aplica |
   | `apply` | cria um run `apply`, que espera aprovação se `require_approval` estiver ligado e aplica direto se não estiver |

   Ligar não dispara nada: vale a partir do próximo push.

**Quem dá push executa código.** Com o gatilho ligado, quem escreve na branch roda código com as credenciais do stack já no plan, antes de qualquer aprovação. Use proteção de branch.

**O que conta como afetado.** Um stack é afetado quando muda algum arquivo dentro do `path` dele. Módulos fora do `path` só contam se forem declarados em `.firstlume/config.yaml`, com caminhos relativos à raiz do repositório:

```yaml
triggers:
  paths:              # afeta também quando estes mudam
    - modules/**
    - root.hcl
  ignore:             # não conta quando só estes mudaram
    - "**/*.md"
```

Force push, branch nova, falha ao comparar os commits e um `config.yaml` inválido fazem todos os stacks da branch contarem como afetados, porque rodar a mais termina em `no_changes` e rodar a menos deixaria mudança sem aplicar.

**Um push novo substitui o que ficou velho.** Runs de pushes anteriores que estão na fila são cancelados, e os que aguardam aprovação são descartados com "superseded by <commit>". Quem está executando termina, e runs manuais nunca são tocados.

**Por que não rodou?** A aba Runs do stack mostra a última decisão, e a página **Git events** (na seção Git connection das configurações da organização, ou `GET /api/v1/orgs/{org}/git/events?stack={id}`) mostra cada push recebido e a decisão por stack: `run_created`, `not_affected` (com os arquivos considerados), `trigger_off`, `other_branch` ou `duplicate`. Os eventos ficam guardados por 30 dias.

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
- **Exportar.** O botão **Export state…** do stack baixa o state atual em claro: o stack inteiro em `.tar.gz`, no formato que a importação lê, ou uma unit em `.tfstate` (`GET /api/v1/orgs/{org}/stacks/{id}/state/export`, com `?unit=<chave>` para uma unit). O arquivo tem segredos em texto puro, e cada exportação fica registrada.
- **Um stack com recursos no state só é excluído junto com o state.** O diálogo de excluir oferece exportar antes e pede o nome do stack (pela API, `DELETE .../stacks/{id}` com `{"delete_state": true, "confirm": "<nome>"}`). Sem isso, rode um destroy ou exclua o state antes: `DELETE .../stacks/{id}/state`, com `{"confirm": "<nome do stack>"}` no corpo. Essa exclusão é recusada com um run ativo ou uma unit travada. A infraestrutura continua de pé, e o state excluído fica guardado por 30 dias.

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

Pela tela, o novo stack pergunta se a infraestrutura já existe, e todo stack tem o botão **Import state…**. Ali o envio aceita vários arquivos (`tar.gz` e `.tfstate` soltos, em qualquer organização de pastas) e o Firstlume deduz onde cada state vai, nesta ordem: lineage que já está numa unit do stack, caminho no arquivo, recursos do state comparados com o código da pasta e, entre pastas com o mesmo código, os identificadores do state comparados com os valores que só uma delas tem. O que ele não decidir fica para você escolher, e nada é gravado antes da confirmação. A dedução não é prova: o plan depois da importação deve dar "No changes".

Pela API, são dois passos:

```bash
# Diz onde cada state vai. Não grava nada e não devolve valores de atributos.
curl -X POST -H "Authorization: Bearer $ADMIN" "$API/api/v1/orgs/$ORG/stacks/$STACK/state/identify" \
  -F file=@old-states.tar.gz -F file=@vault.tfstate
# Importa, dizendo onde cada um vai. "source" é o nome que a identificação deu.
curl -X POST -H "Authorization: Bearer $ADMIN" "$API/api/v1/orgs/$ORG/stacks/$STACK/state/import" \
  -F file=@old-states.tar.gz -F file=@vault.tfstate \
  -F 'placement=[{"source":"ci/net/terraform.tfstate","unit":"net"},{"source":"vault.tfstate","unit":"vault"}]'
```

Uma unit sem state ganha a versão 1. Uma unit que já tem state aparece como `same`, `newer`, `older` ou `conflict` (outro lineage) e só muda se for marcada com `-F replace=<unit>` (uma vez por unit). Nesse caso ganha uma versão nova, e o histórico fica. Se o state importado tiver o mesmo lineage e um serial que não passa do atual, o serial sobe, para que um plano salvo antes não seja aplicado em cima dele. Uma unit travada não pode receber importação.

#### State no bucket da organização

Por padrão, o state de todas as organizações fica no storage da instalação (Postgres ou o bucket do Helm). Uma organização pode guardar o dela num bucket S3 próprio ([spec 0005](docs/specs/0005-storage-de-state-por-organizacao.md)):

```bash
curl -X PUT -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" "$API/api/v1/orgs/$ORG/state-storage" -d '{
  "kind": "s3", "endpoint": "https://s3.example.com", "bucket": "infra", "region": "us-east-1",
  "folder": "firstlume", "access_key_id": "...", "secret_access_key": "..."
}'
```

- O Firstlume testa o bucket antes de salvar (grava, lê e apaga um objeto) e diz o que falhou. `POST .../state-storage/check` testa sem salvar; sem corpo, testa o atual.
- Ele usa só a pasta `folder` (padrão `firstlume`) e grava nela o `.firstlume`, que diz de qual organização ela é. Uma pasta de outra organização é recusada. O resto do bucket não é tocado, nem pela limpeza.
- Dentro da pasta, os nomes do Firstlume: `<pastas>/<stack>/<unit>/v<número>-<id>.tfstate.enc`. Renomear não move nada: as versões novas vão para o caminho novo.
- **Cifrado por padrão.** Com `"encrypt": false` e `"plaintext_acknowledged": true`, as versões novas vão como `.tfstate`, o JSON do Terraform, legível por `mc` ou `aws s3`, e por qualquer um que leia o bucket. O storage da instalação é sempre cifrado.
- Trocar de storage (ou de modo) vale para as gravações seguintes; as versões antigas continuam lidas de onde estão. `{"kind": "installation"}` volta ao da instalação. O `GET` mostra o atual e os anteriores que ainda têm versões.
- A chave secreta nunca volta pela API; enviada vazia, mantém a guardada.
- Endpoint `http://` só com `controlPlane.state.allowHTTPOrgStorage: true` no Helm, para um storage dentro do cluster.
- Se o bucket cair, o state responde 503 e o run falha no Init com o motivo; o servidor e as outras organizações seguem.

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
- **Fontes:** um literal; `{var: NOME}`, que lê uma variável `env` ou `terraform` das configurações; ou `{vault: caminho, field: campo}`, um campo de um segredo do Vault (veja abaixo).
- **Por pastas:** o curinga só vale nas pastas do `path`, não no nome do arquivo, e não casa com pastas ocultas (`.terragrunt-cache`). Um padrão que não casa com nenhuma pasta falha o run, para um erro de digitação não passar despercebido.
- **`${stack_dir}`:** o caminho absoluto da pasta do stack no run, em qualquer texto da declaração. Serve para apontar um arquivo gerado a partir de código que roda dentro do `.terragrunt-cache`, como a chave SSH: `ssh_private_key_path: "${stack_dir}/ssh/id_ed25519"`. `$${` escreve um `${` literal.
- **Quando:** os arquivos são escritos depois do checkout e antes do `init`, no passo **Prepare**, em toda fase. O apply recebe os mesmos valores do plan.
- **Segurança:**
  - um arquivo que usa uma variável sensível é sensível: sai com permissão `0600` e o conteúdo é mascarado no log, linha a linha;
  - o Firstlume não sobrescreve um arquivo que já existe no repositório, não escreve o mesmo arquivo duas vezes e não segue link simbólico para fora da pasta do stack. Nesses casos o run falha;
  - o `path` não pode ter `..` nem apontar para `.git` ou `.firstlume`.
- **O mesmo nome** em `env` ou `terraform` da declaração e nas configurações: vencem as configurações, e o log registra a troca, sem valores.
- `GET /api/v1/orgs/{org}/stacks/{id}/declaration` mostra o que o último commit declara, com as referências e sem valores.

#### Segredos do Vault

O Firstlume não guarda os segredos: quem lê o Vault é o executor, dentro do pod do run, e os valores não passam pelo servidor nem pelo banco. Para isso, o Firstlume emite para cada fase um **JWT** (ES256, válido por 15 minutos) que diz qual stack está rodando, e o executor o troca por um token do Vault. O token é revogado ao fim da fase.

1. **No Firstlume:**
   - a conexão da organização: `PUT /api/v1/orgs/{org}/vault` com `{"address": "https://vault.example.com:8200"}` (opcionais: `ca_cert`, `namespace`, `auth_path`, padrão `jwt`, e `audience`, padrão `vault`);
   - o papel, herdado como as outras configurações: `"vault_role": "infra-dev"` numa pasta ou no stack;
   - `FIRSTLUME_PUBLIC_URL` (no Helm, `controlPlane.publicURL`; o padrão é o host do ingress): é o `iss` dos JWTs.
2. **No Vault:** o método `jwt` confiando no Firstlume, com a chave pública ou com a descoberta:

   ```bash
   vault auth enable jwt
   vault write auth/jwt/config bound_issuer=https://firstlume.example.com \
     oidc_discovery_url=https://firstlume.example.com      # ou jwt_validation_pubkeys=@firstlume.pem
   vault policy write infra-dev - <<'EOF'
   path "secret/data/infra/dev/*" { capabilities = ["read"] }
   EOF
   vault write auth/jwt/role/infra-dev role_type=jwt user_claim=sub bound_audiences=vault \
     bound_claims_type=glob bound_claims='{"stack_path": "infra/dev/*"}' \
     token_policies=infra-dev token_ttl=15m
   ```

   A chave pública fica em `GET /api/v1/orgs/{org}/vault/public-key` (PEM) e em `/.well-known/jwks.json`.
3. **No repositório:** `{vault: secret/infra/dev/${name}, field: ip}` em qualquer valor da declaração. O caminho é o mesmo do `vault kv get` (KV v1 e v2 são detectados), e outros engines também funcionam.

O JWT traz `sub` (`stack:<id>`), `org`, `stack_id`, `stack_name`, `stack_path` (pastas e nome, como `infra/dev/machines`), `folder_ids`, `run_id`, `run_type` e `phase` (`plan`, `apply` ou `drift`). Com `bound_claims`, um stack de dev não lê segredos de prod, e um papel com `phase = apply` não serve para o plan. Prefira `folder_ids` a `stack_path` se renomear pastas puder mudar o acesso.

- **Tudo o que vem do Vault é sensível:** mascarado no log, e os arquivos saem com `0600`.
- **Plan e apply:** o apply relê o Vault e confere com o plan (por um hash com chave, sem guardar valores). Se um segredo mudou entre os dois, o apply falha com "the secret in <caminho>#<campo> changed since the plan: plan again".
- **Erros** dizem o item da declaração, o caminho e o campo, nunca o valor: sem conexão, sem papel, login recusado, permissão negada, segredo ou campo ausente, Vault fora do ar.
- **Testar sem rodar nada:** `POST /api/v1/orgs/{org}/stacks/{id}/vault/check` com `{"paths": ["secret/infra/dev/n01"]}` faz o login como o stack e diz, por caminho, se o papel pode ler, sem ler nenhum valor.

Para experimentar localmente: com o server de dev rodando, `make dev-vault` sobe um Vault em modo dev, confiando no server, com segredos em `secret/infra/{dev,prod}` e os papéis `dev` e `prod`. Ponha o stack `environments/machines` do Gitea de dev numa pasta `dev` com `vault_role: dev`.

#### Credenciais de registry

Para o código baixar charts e imagens de registries com login (Harbor, GHCR, Docker Hub) ou módulos e providers de um registry Terraform privado:

- **Nas configurações**, herdadas por host (o nível mais próximo vence, e hosts diferentes se somam):

  ```bash
  curl -s -H "$H" $O/settings | jq '.level | .registries = [
      {"host": "harbor.example.com", "username": "robot$infra", "password": "..."},
      {"host": "ghcr.io", "username": "bot", "password_vault": {"path": "secret/ci/ghcr", "field": "token"}},
      {"host": "tf.example.com", "kind": "terraform", "password": "..."}
    ]' | curl -s -X PUT -H "$H" $O/settings -d @-
  ```

  **O `PUT .../settings` substitui tudo o que o nível define, exceto `registries`.** O que não vier no corpo deixa de valer ali: as variáveis do nível são apagadas e `require_approval`, `drift_interval_minutes`, `confirm_destructive`, `vault_role` e `git_on_push` voltam a ser herdados. Por isso o exemplo parte do `level` devolvido pelo `GET`, em vez de mandar só `registries`. Nele, as variáveis sensíveis vêm sem valor e, mandadas assim, continuam com o valor guardado.

  A senha nunca é devolvida. Mandada vazia, a guardada continua. A lista enviada substitui a do nível. Sem o campo `registries`, as credenciais do nível ficam como estão; `[]` remove todas.
- **No repositório**, em `registries` do `config.yaml`, com as mesmas fontes da declaração (`{var: ...}`, `{vault: ...}`). Para o mesmo host, vencem as configurações.

No run, o Firstlume escreve o `config.json` do Docker no HOME da fase, aponta `DOCKER_CONFIG` e `HELM_REGISTRY_CONFIG` para ele (o provider `helm` e o próprio `helm` leem daí) e exporta `TF_TOKEN_<host>` para registries do tipo `terraform`. Senhas e o `usuário:senha` em base64 são mascarados no log. O apply usa as credenciais do plan.

Para experimentar localmente: `make dev-registry` sobe um registry com login em `localhost:5001`, com o chart `oci://localhost:5001/charts/hello`. O stack `environments/charts` do Gitea de dev usa esse chart.

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
F=$(curl -s -H "$H" $O/folders -d '{"name": "infra"}' | jq -r .id)

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

Agent adicional em outro cluster (crie o token com `POST /api/v1/orgs/{org}/agents`; o agent atende só essa organização):

```bash
helm install firstlume-agent deploy/helm/firstlume -n firstlume --create-namespace \
  --set controlPlane.enabled=false \
  --set agent.name=cluster-b \
  --set agent.apiUrl=https://firstlume.empresa.com \
  --set agent.token=fla_...
```

A imagem é publicada pelo GitHub Actions em `ghcr.io/bsteigleder/firstlume` (`main`, `sha-<commit>` e, nas tags `vX.Y.Z`, `X.Y.Z`), para amd64 e arm64. Se o pacote for privado, crie um Secret de pull e passe `--set imagePullSecrets[0].name=<secret>`: o server, o agent e os Jobs dos runs (pela ServiceAccount do executor) passam a usá-lo. Num cluster com registry interno, prefira um proxy-cache dele apontando para o GHCR, com a credencial num lugar só.

**Onde os runs rodam.** Os Jobs dos runs aceitam node selector, tolerations (o taint fica no node; aqui se diz quais o run tolera), afinidade de node, afinidade e antiafinidade de pod, com os mesmos campos de um pod. Use quando só alguns nodes alcançam as máquinas que o código configura. Dá para definir em três lugares, e a regra é a da herança, campo a campo: **stack → pasta → organização → agent → instalação**. Um campo num nível substitui o mesmo campo dos seguintes, sem somar, e um campo vazio cancela o herdado.

- **Na instalação:** `agent.executor.nodeSelector`, `tolerations` e `affinity` no Helm. Valem para todos os runs daquele agent.
- **No agent:** ao criar ou editar em Settings › Agents, ou em `POST /api/v1/orgs/{org}/agents` e `PUT /api/v1/orgs/{org}/agents/{id}/placement`. Vale a partir da próxima fase, sem reiniciar.
- **Na organização, na pasta e no stack:** em Variables & settings, ou no campo `placement` de `PUT .../settings`. É o que manda um stack para uma zona: qualquer agent pode pegar qualquer run, então dois agents com zonas diferentes não separam stacks. Não dá para definir em `.firstlume/config.yaml`, porque quem envia commit não escolhe onde o código roda.

O que é definido no First Lume só vale em quem aceita: `agent.executor.allowPlacement`, **desligado por padrão**, porque uma toleration pode colocar o run num node reservado, inclusive no control plane. Desligado, o agent só pega runs sem colocação, e nenhum run se ele mesmo tiver uma; o run fica na fila, com o motivo na página. Com vários agents que aceitam, os rótulos valem no cluster de quem pegar o run, então ligue a opção só no agent do cluster certo.

A colocação de organização, pasta e stack fica gravada no run quando ele é criado, e o apply usa a mesma do plan. Mudar a configuração não mexe em runs já criados. Num stack com `run_all`, é um Job só, e todas as units rodam no mesmo lugar.

Se nenhum node aceitar o run por `agent.executor.unschedulableTimeout` (5 min por padrão), o Job é removido e o run falha com a mensagem do scheduler. Mantenha esse valor abaixo de `controlPlane.heartbeatTimeout`, ou o heartbeat vence, com uma mensagem que não diz o motivo. Uma regra obrigatória (`required`) de antiafinidade pode falhar um run só porque os nodes estão ocupados; `preferred` só distribui e nunca bloqueia. Os pods dos runs têm o rótulo `firstlume.io/stack-id`, para uma regra poder citar um stack.

Para código que fala com a API do próprio cluster (providers `kubernetes`/`helm` sem kubeconfig), ligue `agent.executor.automountServiceAccountToken` e dê permissões à ServiceAccount do executor com `agent.executor.serviceAccount.clusterRoles`. Elas valem para todos os runs daquele agent: para permissões diferentes, use outro agent.

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

- **Login pelo Gitea (OIDC) ou token admin, com um papel só por organização.** Quem está num grupo de `auth.oidc.adminGroups` vê todas as organizações; os outros só veem aquelas para as quais foram convidados, e todo membro é administrador da organização. Papéis mais finos e aprovadores por pessoa ou grupo entram depois.
- **Um run em execução pode ser parado, mas não morto na hora**: um apply nunca é morto depois da interrupção, para não perder state, e não há botão de "parar agora".
- **As ferramentas são baixadas a cada run** (releases oficiais, com checksum SHA256 verificado). O executor precisa de acesso de saída à internet, ou de um mirror (ainda não configurável).
- **Licença do Terraform:** as versões 1.6 em diante são BSL. Para oferecer a plataforma comercialmente, use `engine: opentofu` ou Terraform ≤ 1.5.7.

## Roadmap

1. ✅ Runs de plan/apply, logs, aprovação configurável, drift detection agendado (só reporta), UI web, agent com Kubernetes e chart Helm
2. State backend HTTP próprio, por unit, com lock, versões e criptografia, no Postgres ou no Garage/S3 (✅ marcos S1 e S2), importação de state existente e modo "só state" para quem continua usando o CI atual (✅ marco S3); aba State na UI
3. Terragrunt nativo: descoberta de units, grafo de dependências e um run por unit; onde os runs executam, com afinidade, antiafinidade e tolerations na instalação, no agent, na pasta e no stack (✅ spec 0007)
4. Arquivos e variáveis declarados no repositório, variáveis do tipo arquivo segredos do Vault com JWT emitido pelo Firstlume por fase, sem o Firstlume virar um segundo cofre, e credenciais de registry (✅ marcos F1 a F3 da spec 0003)
5. Usuários, grupos e SSO; aprovadores por pessoa ou grupo; trilha de auditoria
6. Integração Git (Gitea/Forgejo, GitHub, GitLab): plan em PR, commit status, apply no merge e gatilho por impacto
7. Observabilidade: export via OpenTelemetry (métricas, eventos, logs e traces) e notificações (webhook, Slack, Rocket.Chat)
8. Hooks por fase, tarefas agendadas, políticas (OPA), backup/restore e alta disponibilidade

Para pensar no futuro: custos (estimativa no plan e acompanhamento por ambiente).

## Licença

Copyright © 2026 Bernardo Steigleder. Todos os direitos reservados.

O código está público apenas para consulta. Nenhuma licença de uso, cópia, modificação ou distribuição é concedida.
