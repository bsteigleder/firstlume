# Spec 0009: rodar um comando no repositório do stack

- **Status:** telas e spec aprovadas em 2026-11-07 (quadros revistos no mesmo dia para a lista de comandos permitidos). Implementada; NetworkPolicy testada num kind com Calico em 2026-11-07; CAC10 testado no kind com Gitea no cluster em 2026-11-07
- **Data:** 2026-11-07
- **Depende de:** spec 0002 (backend http, tokens por fase, R5.2 e R5.3), spec 0003 (arquivos, Vault, registries), spec 0008 (parar um run)
- **Telas:** quadros 68, 69 e 70 do canvas

## 1. Contexto

Plan e apply cobrem o dia a dia, mas há tarefas que hoje só se fazem fora do Firstlume, na máquina de alguém, com credenciais copiadas: mover e remover recursos do state, importar, soltar uma trava presa, ler outputs. Fora do Firstlume essas tarefas não têm registro, não passam por aprovação e mexem num state que o Firstlume guarda.

**Objetivo:** um novo tipo de run que roda comandos do Terraform, OpenTofu ou Terragrunt no diretório do stack, com o mesmo preparo de um plan (checkout, arquivos, Vault, registries, ferramentas, init) e o state do stack já configurado. O log é o resultado.

**Decisões do usuário (2026-11-07):**

- Cada run escolhe se pode mudar o state: **Read only** (padrão) ou **May change the state**.
- O comando roda na raiz do stack, também em stacks Terragrunt com várias units.
- **Segurança:** o comando não pode acessar outras máquinas (ssh, telnet e afins) nem destruir a máquina onde roda. Por isso **não há shell**: cada linha roda direto, e só uma lista fechada de programas e subcomandos é aceita (seção 3). Isso substitui o "shell livre" escolhido antes no mesmo dia. Scripts do repositório ficam de fora.
- A rede dos pods de run de comando é limitada por uma NetworkPolicy de saída (seção 4). Plan e apply ficam como hoje.

## 2. Regras do run

### Criar

- **R1** Novo tipo de run `command`. O pedido de criação (`POST /stacks/{id}/runs`) leva `type: "command"`, `command` (texto, obrigatório, até 4 KiB, uma ou mais linhas) e `state_access`: `read` (padrão) ou `write`. Os outros tipos recusam `command` e `state_access` (422, como as outras validações desse endpoint).
- **R2** O servidor valida o comando pela seção 3 ao criar o run e recusa (422) com uma mensagem que diz a linha e o motivo. Nada é guardado nem enfileirado quando a validação falha.
- **R3** O commit é fixado quando o run é criado, quando dá: um SHA completo vale como está, e um branch é resolvido pela conexão Git da organização (só para repositório da conta da conexão). Sem conexão, o checkout resolve, como nos outros runs, e a aprovação mostra o branch.
- **R4** Quem pode iniciar um run no stack pode iniciar um run de comando. Um run de comando nunca nasce de gatilho do Git, de agendamento de drift ou de plano de PR.
- **R5** O run guarda o comando inteiro e o mostra na página. Na lista do stack, na pasta e na gaveta de runs ativos, o nome é "Command" com a primeira linha, cortada.
- **R6** O comando é guardado como foi digitado. O Firstlume não procura segredos nele. O texto de ajuda diz para não colar segredos.

### Fila e aprovação

- **R7** Um run de comando entra na fila do stack como qualquer run: só executa quando nenhum outro run do stack está ativo, e nenhum outro começa enquanto ele está ativo.
- **R8** `state_access: read` não pede aprovação. Fluxo: `queued` → `planning` (fase plan) → fim.
- **R9** `state_access: write` segue a configuração de aprovação herdada do stack (a mesma do apply):
  - sem aprovação: `queued` → `applying` (fase apply) → fim;
  - com aprovação: `queued` → `awaiting_approval` quando chega a vez dele na fila (já na criação, se o stack está livre; senão, quando um agent que aceita comandos pede trabalho) → aprovado → `apply_queued` → `applying` → fim. Discard funciona como hoje. Nada roda antes da aprovação, nem o checkout.
- **R10** Aprovar um run de comando não pede a confirmação de recursos destrutivos (`confirm_destructive`), porque não há plano. Quem aprova lê o comando inteiro.

### Executar

- **R11** O executor faz os passos do plan até o Init (Checkout, Prepare, Init) e depois o passo **Command**. Em stack Terragrunt com `run --all` o Firstlume não roda o próprio init, como hoje no plan.
- **R12** O executor valida o comando de novo pela seção 3 antes de rodá-lo (defesa em profundidade: um run alterado no banco não passa).
- **R13** Cada linha vira uma lista de argumentos e é executada direto (`exec`), **sem shell**, no diretório do stack. O ambiente é o do Init: `HOME` do run, variáveis do stack, arquivos e valores do Vault, credenciais de registry, token do Git e o backend http do state.
- **R14** As linhas rodam em ordem e param na primeira que sair com código diferente de 0. O log mostra cada linha, com prefixo `$`, antes de rodá-la. A entrada padrão é vazia e o comando recebe `TF_INPUT=0`: não há terminal.
- **R15** A saída passa pelo mascaramento de segredos de sempre.
- **R16** O token de state segue a fase (spec 0002 R5.2): `read` usa o da fase plan, que lê e trava mas não grava; `write` usa o da fase apply. As travas são soltas ao sair da fase (R5.3).
- **R17** O tempo limite é o de qualquer fase (`agent.executor.activeDeadline`).

### Terminar

- **R18** Novo status final `succeeded`: todas as linhas saíram com 0. Senão termina `failed` e o run guarda `exit_code` e a linha que falhou. Um passo que falha antes do comando (checkout, Vault, init) termina `failed` sem `exit_code`, como hoje.
- **R19** Num run `write`, o fim diz se o state mudou: a leitura do run lista as versões do state que ele gravou (`state_writes`, das versões gravadas com o `run_id` dele). A página mostra "The state changed" com link para a versão, ou "The state didn't change".
- **R20** Num run `read` em que o backend recusou uma gravação mesmo assim, o executor acrescenta ao log e ao erro: "The state is read only in this run, so the backend refused a write."
- **R21** Parar segue a spec 0008: `read` para como um plan (sem confirmação, mata depois de 2 minutos); `write` para como um apply (com confirmação, nunca mata). Parado, o run termina `canceled`. Depois da parada nenhuma linha nova começa: a que está rodando recebe a interrupção, como um Ctrl+C (em `read` é morta se não sair em 2 minutos; em `write`, nunca), e o log diz se ela foi interrompida ou terminou e em que linha o comando parou. Se todas as linhas terminaram antes de a parada chegar ao executor, o run termina `succeeded`, como um apply que terminou fica `applied`, e a página diz que a parada chegou depois da última linha.
- **R22** "Run again" abre o diálogo preenchido com o mesmo comando, acesso e branch ou commit. Os últimos comandos distintos do stack (até 5) aparecem no diálogo. Clicar só preenche, nunca roda.

## 3. O que um comando pode ter

A validação é uma só, num pacote Go usado pelo servidor (R2) e pelo executor (R12).

- **S1** Sem shell, os caracteres `|`, `;`, `&`, `$`, `` ` ``, `<`, `>`, `(`, `)`, `{`, `}`, `*`, `?`, `~` e `!` fora de aspas recusam a linha. Aspas simples e duplas agrupam um argumento e não expandem nada. Uma linha que termina em `\` continua na seguinte. Linhas vazias e linhas que começam com `#` são ignoradas.
- **S2** O primeiro argumento precisa ser o engine do stack (`terraform` ou `tofu`, o que o `.firstlume/config.yaml` diz) ou, quando o stack usa Terragrunt, só `terragrunt`: num stack Terragrunt o engine sozinho não acha o backend da unit (o Terragrunt o gera no cache) e trabalharia num state local vazio, então um `import` "daria certo" e se perderia (achado no teste de ponta a ponta de 2026-11-07). Nenhum caminho (`/usr/bin/terraform`, `./x`) e nenhum outro programa: `ssh`, `scp`, `telnet`, `nc`, `curl`, `wget`, `rm`, `sh`, `python`, `git` e todo o resto são recusados.
- **S3** Subcomandos permitidos, e o acesso ao state que cada um exige:

  | Subcomando | Acesso |
  |---|---|
  | `state list`, `state show` | read |
  | `output`, `show` | read |
  | `validate`, `providers`, `version`, `workspace list`, `workspace show` | read |
  | `state mv`, `state rm`, `state replace-provider` | write |
  | `import`, `taint`, `untaint`, `force-unlock` | write |

  Tudo o mais é recusado, em especial `apply`, `destroy`, `plan`, `refresh`, `console`, `init` (o Firstlume já roda), `state pull` e `state push`, `workspace new`/`select`/`delete`, `login`, `logout`, `test`, `get`. Pedir um subcomando de `write` num run `read` é recusado na criação, com a frase "Choose “May change the state” to run <subcomando>".
- **S4** Opções recusadas em qualquer subcomando: `-state`, `-state-out`, `-backup`, `-lock=false`, `-plugin-dir`, `-config`. `-json` e `-raw` são recusados em `output` e `show`, porque mostram valores sensíveis que o mascaramento não conhece; sem eles, o Terraform mostra `(sensitive value)`.
- **S5** `-chdir=<dir>` (Terraform/OpenTofu) e `--working-dir <dir>` (Terragrunt) só aceitam caminho relativo que, resolvido, fica dentro do diretório do stack. É o jeito de chegar a uma unit, já que não há `cd`.
- **S6** Terragrunt: `terragrunt <subcomando permitido>` e `terragrunt run [--all] -- <subcomando permitido>`, com as mesmas regras de S3 a S5 para o que vem depois de `--`. Outras opções globais do Terragrunt ficam de fora até alguém precisar.
- **S7** Fica dito na tela e na documentação: o código do repositório ainda roda (providers no `import`, hooks do `terragrunt.hcl`), como num plan. Esse código passa por revisão no Git; o que o usuário digita no diálogo, não.

## 4. Onde o comando roda

- **E1** Só agents que declaram aceitar pegam run de comando (`accepts_commands` no pedido de trabalho): o driver Kubernetes sempre; o local só com `FIRSTLUME_ALLOW_LOCAL_COMMANDS=true`, para desenvolvimento. Agents de versões anteriores não declaram e nunca pegam um. Sem agent que aceite, o run fica na fila, como um run cuja colocação nenhum agent aceita.
- **E2** O pod do executor continua como hoje: sem root, sem escalada de privilégio, root filesystem só leitura, todas as capabilities removidas, seccomp `RuntimeDefault` e sem token da ServiceAccount por padrão. Destruir a máquina, no pior caso, destrói esse pod descartável.
- **E3** Os pods de run de comando levam o rótulo `firstlume.io/run-type: command`. O chart ganha `agent.executor.commandEgress` (`enabled: true` e `publicHTTPS: true` por padrão), que cria uma NetworkPolicy de saída para esses pods:
  - libera DNS (53/UDP e TCP) e a API do Firstlume;
  - libera HTTPS (443) para a internet pública, exceto as faixas privadas (10/8, 172.16/12, 192.168/16, 100.64/10, 169.254/16), para providers, registries e APIs de nuvem;
  - acrescenta as regras de `agent.executor.commandEgress.extraEgress` (regras de egress da NetworkPolicy, como estão), para Vault, Git e APIs internas (LXD, por exemplo) na rede privada;
  - bloqueia todo o resto, inclusive SSH (22) e telnet (23) para qualquer destino e qualquer acesso à rede interna fora da lista.

  Vault e Git em IP privado precisam estar em `extraEgress`, senão o run de comando falha no Checkout ou no Prepare. O `NOTES.txt` do chart lembra isso.
- **E4** A NetworkPolicy só vale se o CNI do cluster a aplica (Calico, Cilium e outros). O `NOTES.txt` avisa isso. O aviso "Egress limited by Helm" na página do agent ficou para depois: o agent ainda não informa essa opção ao servidor.
- **E5** Plan e apply não ganham NetworkPolicy nesta spec.

## 5. Tela

- **Diálogo de run (quadro 68):** quinta opção "Run a command". A caixa do comando mostra o que é aceito e valida enquanto se digita (o mesmo pacote, compilado para o navegador ou chamado pela API). O botão diz "Start run", ou "Ask for approval" quando o acesso é `write` e o stack exige aprovação.
- **O que é aceito (quadro 70):** lista dos subcomandos, exemplos de recusas com a mensagem de cada uma, e a nota da rede.
- **Lista de runs (quadro 68):** "Command · <primeira linha>" com etiqueta "Read only" ou "Changes state"; status "Succeeded" ou "Failed · exit N".
- **Página do run (quadro 69):** o comando inteiro no topo; meta com quem iniciou e aprovou, commit e acesso ao state. Passos Checkout, Prepare, Init e Command. Aguardando aprovação: "Discard" e "Approve and run". Rodando: "Stop command". Fim: código, tempo e versão nova do state; na falha, a linha que falhou e as últimas linhas do log primeiro.
- O status `planning`/`applying` de um run de comando aparece como "Running".

## 6. Dados e API

- Migração **00020**: `runs.command text`, `runs.state_access text` (`read`/`write`, nulo nos outros tipos), `runs.exit_code int`, `runs.failed_line int`. Novo valor `command` no tipo e `succeeded` no status, onde houver checagem.
- `ExecSpec` ganha `Command` e `StateAccess`. `CompleteRequest` ganha `ExitCode` e `FailedLine`. `Assignment` ganha `Type`, para o rótulo do pod. O endpoint de commit do executor aceita a fase apply de um run de comando.
- A saída dos comandos passa sem os códigos de cor do terminal (o usuário não escreve `-no-color`).
- `POST /stacks/{id}/commands/check` valida um comando sem criar run e devolve a linha e o motivo, para o diálogo.
- Na fila (R7, R9): um run `queued` de comando `write` que precisa de aprovação, quando chega a vez dele, vai para `awaiting_approval` em vez de ser entregue a um agent. Um `write` sem aprovação é entregue direto na fase apply.

## 7. Critérios de aceite

- [x] **CAC1** Criar run `command` sem `command`, com `command` em outro tipo ou com `state_access` inválido é recusado (400).
- [x] **CAC2** Validação (S1 a S6), em testes de tabela: são recusados `ssh host`, `telnet host 23`, `nc -z host 22`, `curl x`, `rm -rf /`, `/usr/bin/terraform state list`, `terraform state list | grep x`, `terraform state list; ssh x`, `terraform output $(id)`, `terraform apply -auto-approve`, `terraform destroy`, `terraform state pull`, `terraform output -json`, `terraform -chdir=../../outro state list`, `terraform state list -state=/tmp/x`, `terragrunt run --all -- apply`; são aceitos `terraform state list`, `terraform state mv 'module.a.x["k"]' module.b.x`, `terraform -chdir=units/vpc state list`, `terragrunt run --all -- state list`, `tofu output` num stack OpenTofu.
- [x] **CAC3** `terraform state rm` com `state_access: read` é recusado na criação com a frase de S3.
- [x] **CAC4** O executor recusa um comando inválido mesmo que ele tenha chegado ao banco, e o run termina `failed` sem rodar nada.
- [x] **CAC5** `read`: roda na fase plan, sem aprovação, mesmo com o stack exigindo aprovação; `terraform state list` lista o state do Firstlume e termina `succeeded`.
- [x] **CAC6** `write` com aprovação: espera em `awaiting_approval` sem checkout; aprovado, `terraform state mv` termina `succeeded` e o run aponta para a versão nova do state. Sem aprovação exigida, roda direto.
- [x] **CAC7** Fila: com um run de comando ativo, um plan do mesmo stack espera, e o contrário também.
- [x] **CAC8** Várias linhas: a que falha interrompe as seguintes; o log mostra cada linha com `$` e o run guarda a linha que falhou.
- [x] **CAC9** Variáveis, arquivos e valores do Vault chegam ao comando, e os segredos saem mascarados.
- [x] **CAC10** Terragrunt `run --all`: `terragrunt run --all -- state list` lista as units; `terragrunt --working-dir <unit> state list` funciona numa unit; `terraform -chdir=<unit> ...` é recusado num stack Terragrunt (S2). (Testado em 2026-11-07 no kind com Calico, Gitea no cluster e Terragrunt 1.1.6: `run --all -- state list` listou `terraform_data.vpc` e `terraform_data.app`, `--working-dir net state list` e `--working-dir app run -- state list` listaram a sua unit, `run --all -- output` mostrou `cidr`. Antes da correção, `terraform -chdir=net state list` falhava com "No state file was found!".)
- [x] **CAC11** Parar: `read` sem confirmação; `write` com confirmação e sem matar; os dois terminam `canceled`. (Testado em 2026-11-07 no kind com 195 linhas: em `read` a linha 83 foi interrompida; em `write` a linha 123 terminou e a 124 não começou; os dois em cerca de 2 s. O teste achou e corrigiu: as linhas seguintes não eram barradas de forma explícita, um run que terminou todas as linhas antes de a parada chegar ficava `canceled`, e o log dizia "Exit code -1".)
- [x] **CAC12** Driver local recusa run de comando sem a opção de desenvolvimento.
- [x] **CAC13** Num cluster kind com Calico: com `commandEgress` ligado, o pod de comando não abre TCP 22 nem 23 em lugar nenhum nem alcança um serviço interno fora de `extraEgress`, e ainda baixa providers por HTTPS e fala com a API do Firstlume e o Vault; um pod de plan do mesmo agent não é afetado. (Testado em 2026-11-07 com kind v0.33 e Calico v3.29.1, com a política gerada pelo chart e pods com os rótulos do agent. O pod de comando resolveu DNS, abriu HTTPS em example.com e falou com um servidor falso com os rótulos e a porta `http` do chart. Não abriu `github.com:22`, nem as portas 22, 23 e 443 de outro pod, nem a API do Kubernetes, nem o node, nem 169.254.169.254, nem HTTP na porta 80. Com uma regra em `extraEgress` para aquele pod na 443, passou a abrir só a 443. O pod de plan abriu tudo isso. O Vault foi representado pelo pod da regra de `extraEgress`, e o agent e o servidor não foram instalados pelo chart.)
- [x] **CAC14** Tela: diálogo com validação ao digitar, lista e página como nos quadros 68 a 70, nos temas claro e escuro e no celular.
- [x] **CAC15** Ponta a ponta com agent local (com a opção de desenvolvimento) e Terraform 1.15.6: `state list` e `output` (sensível escondido), `state mv` com aprovação pela tela (versão nova do state), comandos que falham (`state mv` de algo que não existe, linha 2 de 3) e recusas (`ssh`, `state rm` sem acesso). `import` não foi rodado.

## 8. Fora desta spec

- Scripts do repositório e qualquer outro programa (pode voltar como scripts declarados no `.firstlume/config.yaml`, se fizer falta).
- Terminal interativo.
- Listas de comandos por organização ou pasta.
- NetworkPolicy para plan e apply.
- Escolher uma unit do Terragrunt no diálogo.
