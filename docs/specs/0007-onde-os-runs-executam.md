# Spec 0007: onde os runs executam (afinidade, antiafinidade e taints)

- **Status:** aprovada em 2026-11-04, com as propostas da seção 5. Implementada
- **Data:** 2026-11-04
- **Depende de:** spec 0001 (herança de configurações, mover stack)
- **Altera:** README ("Onde os runs rodam"), roadmap (item "agents com labels por rede/zona")
- **Telas:** quadros 63 a 65 do canvas, aprovados em 2026-11-04

## 1. Contexto

Os Jobs de um run já aceitam `nodeSelector`, `tolerations` e `affinity`, mas só em `agent.executor.*` no Helm. Isso vale para todos os runs daquele agent. Para mandar stacks diferentes para zonas diferentes, hoje é preciso instalar um agent por zona.

**Objetivo:** dizer em quais nodes os runs executam, em três lugares:

1. **na instalação**, nos values do Helm (já existe, e passa a aparecer na tela);
2. **no agent**, ao criar ou editar pela UI;
3. **na organização, na pasta e no stack**, com a herança de sempre, para um agent atender várias zonas.

A regra é uma só, campo a campo: stack → pasta → organização → agent → instalação. O nível mais próximo vence.

Taint fica no node, e o Firstlume não mexe em node. O que esta spec configura é a **toleration** no pod, que deixa o run entrar num node com taint.

Nada muda para os stacks existentes até alguém definir uma colocação e o operador do agent permitir (R3).

### 1.1 Como é hoje (conferido no código)

| Fato | Onde | Consequência |
|---|---|---|
| Job que nunca sai de `Pending` só falha pelo heartbeat, depois de 10 min, com "executor stopped reporting" | `FailStaleRuns` em `internal/store/runs.go` | O erro não diz que o problema é a colocação (R4) |
| O Role do agent só tem `jobs` e `secrets` | `deploy/helm/firstlume/templates/agent.yaml` | Ler o motivo do scheduler pede `pods`: `get`, `list` |
| O pod só tem rótulos de agent, run e fase | `BuildJob` em `internal/agent/kubernetes.go` | Antiafinidade entre stacks precisa de um rótulo do stack (R2.5) |
| `ParseScheduling` já lê os campos com tipos do Kubernetes e recusa campo desconhecido | `internal/agent/kubernetes.go` | A validação reaproveita esses tipos (R1.5) |

## 2. Fora de escopo

- **Colocação por unit.** Um stack com `run --all` é um Job só, e a colocação vale para todas as units. Separar por unit depende da pipeline por unit.
- **Escolher o agent de um stack.** Com agents em clusters diferentes, qualquer um que aceite colocação pode pegar o run (R3.4).
- **Prever quais nodes servem.** Mostrar "casa com 3 de 7 nodes" pediria dar ao agent leitura de nodes e um canal novo com o servidor.
- **`topologySpreadConstraints`, `priorityClassName`, recursos por stack.**
- **Declarar em `.firstlume/config.yaml`.** Quem envia commit não escolhe onde o código roda (R1.6).

## 3. Requisitos

### R1. A configuração

- **R1.1** Nova configuração herdada `placement`, com cinco campos: `node_selector`, `tolerations`, `node_affinity`, `pod_affinity` e `pod_anti_affinity`. Cada um usa o formato do campo correspondente no Kubernetes.
- **R1.2** **Cada campo herda sozinho**, pela regra de sempre: o nível mais próximo vence. A organização pode definir a antiafinidade, e a pasta, a zona.
- **R1.3** Um campo ausente herda. Um campo vazio (`{}` ou `[]`) cancela o herdado.
- **R1.4** Depois da organização vêm o agent (R5) e, por último, a instalação (`agent.executor.*` no Helm). Um campo com valor num nível substitui o dos níveis seguintes, sem somar.
- **R1.5** O servidor valida ao salvar: tipos do Kubernetes, campo desconhecido é erro, operadores e efeitos válidos, chaves de rótulo válidas, `topologyKey` presente, no máximo 16 KB. O erro aponta o campo. O que só o API server detecta volta como falha do run, com a mensagem dele.
- **R1.6** Só se configura pelas telas e pela API de configurações, nunca pelo repositório.
- **R1.7** Mover um stack lista a mudança de cada campo na prévia (R3.8 da spec 0001).

### R2. O run

- **R2.1** A colocação de organização, pasta e stack é gravada no run **quando ele é criado**, por qualquer origem: manual, drift e push.
- **R2.2** O apply usa a colocação gravada, como já faz com as variáveis. O que foi aprovado roda no lugar em que foi planejado, mesmo que a configuração mude depois. Os campos que vêm do agent ou da instalação são os do agent que pegar a fase.
- **R2.3** Mudar a configuração não mexe em runs já criados. Um run na fila com a colocação errada precisa ser cancelado e criado de novo.
- **R2.4** A página do run mostra o que a última fase usou e de que nível veio cada campo, inclusive o agent e a instalação. Isso é gravado quando o agent pega a fase, porque o agent pode mudar depois. Antes disso, mostra a colocação gravada no run.
- **R2.5** O pod ganha o rótulo `firstlume.io/stack-id`, para uma regra de antiafinidade poder citar um stack. A colocação não altera nenhum outro campo do pod, nem a ServiceAccount.

### R3. Quem permite

Hoje todo membro é admin da organização, e o agent pertence a quem opera o cluster. Uma toleration pode colocar o run num node reservado, inclusive no control plane. Por isso, quem opera o cluster decide.

- **R3.1** Nova opção `agent.executor.allowPlacement`, **`false` por padrão**.
- **R3.2** O agent informa essa opção ao pedir trabalho. Com `false`, ele só recebe runs sem colocação gravada. O driver local nunca aceita. A colocação da instalação vale sempre, porque foi quem opera o cluster que a definiu.
- **R3.3** Um run com colocação e nenhum agent que aceite **fica na fila**, com o motivo na página. A organização vê em cada agent se ele aceita, e a tela de configurações avisa ao salvar se nenhum aceita.
- **R3.4** Com vários agents que aceitam, qualquer um pega o run, e os rótulos valem no cluster dele. Num cluster sem esses rótulos, o run falha por R4. Para evitar isso, ligue `allowPlacement` só no agent do cluster certo. **Dois agents com zonas diferentes na UI não separam stacks:** quem separa é a colocação da pasta ou do stack.

### R4. Quando nenhum node serve

- **R4.1** O agent acompanha os pods dos seus Jobs. Com `PodScheduled=False` e motivo `Unschedulable` por mais de `agent.executor.unschedulableTimeout` (**5 min** por padrão), ele apaga o Job e falha a fase, com a mensagem do scheduler.
- **R4.2** O erro do run mostra a mensagem como veio, por exemplo `0/7 nodes are available: 3 node(s) had untolerated taint...`, e a colocação usada.
- **R4.3** O `unschedulableTimeout` precisa ser menor que `controlPlane.heartbeatTimeout` (10 min por padrão). Se não for, o heartbeat vence, com a mensagem antiga. O README avisa.
- **R4.4** O Role do agent ganha `pods`: `get`, `list`, no namespace dos runs.
- **R4.5** Valem também as regras obrigatórias de antiafinidade: um run pode falhar só porque os nodes estão ocupados por outros runs. A tela avisa e sugere `preferred` (quadro 63).

### R5. Na instalação e no agent

- **R5.1** **Instalação:** `agent.executor.nodeSelector`, `tolerations` e `affinity` continuam como estão, e o `values.yaml` ganha `allowPlacement` e `unschedulableTimeout`.
- **R5.2** O agent informa ao servidor, ao pedir trabalho, o que veio da instalação e se aceita colocação. A tela mostra esses valores só para leitura, com a origem "installation".
- **R5.3** **Criar agent pela UI:** o formulário ganha uma parte opcional com os cinco campos, no mesmo editor das configurações. `POST /orgs/{org}/agents` aceita `placement`.
- **R5.4** **Editar:** cada agent pode ter a colocação alterada depois, inclusive o que a instalação cria (`PUT /orgs/{org}/agents/{id}/placement`). A mudança vale para as próximas fases.
- **R5.5** Com colocação definida na UI, o comando de Helm mostrado junto com o token já inclui `--set agent.executor.allowPlacement=true`.
- **R5.6** **Um agent com colocação na UI que não aceita colocação não pega nenhum run**, e a tela diz por quê. Melhor parado do que rodando no lugar errado.
- **R5.7** A validação é a de R1.5.

## 4. Critérios de aceite

Os testes usam Postgres real. Os de colocação usam um cluster real com nodes rotulados e com taint (kind serve).

### Marco C1: configuração, run e agent

- [x] **CAC1.1** Sem nenhuma colocação, o spec do Job é igual ao de antes, a não ser pelo rótulo do stack.
- [x] **CAC1.2** Pasta com `node_selector` e organização com `pod_anti_affinity`: o Job do stack recebe os dois, e o stack sobrescreve cada um sozinho.
- [x] **CAC1.3** Um campo vazio no stack cancela o herdado e o do Helm.
- [x] **CAC1.4** Campo desconhecido, operador inválido ou `topologyKey` ausente: `422`, com o nome do campo.
- [x] **CAC1.5** Mudar a configuração entre o plan e o apply: o apply sai com a colocação do plan.
- [x] **CAC1.6** Runs manual, de drift e de push gravam a colocação.
- [x] **CAC1.7** Com `allowPlacement: false`, o agent não recebe run com colocação e continua recebendo os outros. Com dois agents, só o que aceita pega.
- [x] **CAC1.8** Em um cluster real, um run com toleration e `node_selector` executa no node com taint, e sem a toleration não executa.
- [x] **CAC1.9** Antiafinidade pelo rótulo `firstlume.io/stack-id` separa os runs de dois stacks em nodes diferentes.
- [x] **CAC1.10** O `.firstlume/config.yaml` não consegue definir colocação.
- [x] **CAC1.11** A ordem de R1.4 vale campo a campo: instalação com `tolerations`, agent com `node_selector`, pasta com `pod_anti_affinity`: o Job recebe os três. Um campo no stack substitui o mesmo campo em todos os outros níveis.
- [x] **CAC1.12** Um agent criado com colocação pela API aplica essa colocação, e editar vale para a próxima fase, sem reiniciar o agent.
- [x] **CAC1.13** Um agent com colocação na UI e `allowPlacement: false` não recebe nenhum run. Ao ligar a opção, volta a receber.
- [x] **CAC1.14** A API de agents devolve o que veio da instalação e se o agent aceita colocação.

### Marco C2: nenhum node serve

- [x] **CAC2.1** Um `node_selector` que não casa com nada falha o run depois do tempo configurado, com a mensagem do scheduler, e o Job é apagado.
- [x] **CAC2.2** Um node que aparece antes do tempo deixa o run seguir normalmente.
- [x] **CAC2.3** Falha no apply por isso não aplica nada, e o run diz que o apply não começou. Coberto só por teste com cliente falso: o cenário no cluster real usou o plan.
- [x] **CAC2.4** Uma colocação que o API server recusa falha o run com a mensagem dele.

### Marco C3: telas (depois do desenho aprovado)

- [x] **CAC3.1** As telas seguem os quadros 63 a 65.
- [x] **CAC3.2** Run na fila sem agent que aceite mostra o motivo, e cancelar funciona.
- [x] **CAC3.3** A prévia de mover stack lista a mudança de colocação.
- [x] **CAC3.4** Criar um agent com colocação pela tela mostra o comando de Helm com `allowPlacement`, e o agent instalado com esse comando aplica a colocação. O comando foi visto na tela, e a aplicação, com um agent criado pela API e as mesmas variáveis de ambiente, sem `helm install`.

### Critérios gerais

- [x] **CGC1** `make vet`, os testes unitários, os de integração, o `tsc` e o `helm lint` passam.
- [x] **CGC2** README: a seção "Onde os runs rodam", com os três lugares e a ordem entre eles, `allowPlacement`, `unschedulableTimeout` e o aviso sobre tolerations. O `values.yaml` traz os mesmos exemplos.
- [x] **CGC3** Migração **00018** (conferir o número na `main` antes de criar).
- [x] **CGC4** Nada de dados reais da empresa no repositório público.

## 5. Decisões para aprovar

| # | Pergunta | Proposta |
|---|---|---|
| D1 | A herança é por campo ou da colocação inteira? | Por campo (R1.2). Permite antiafinidade na organização e zona na pasta |
| D2 | Precisa da permissão do operador no agent? | Sim, desligada por padrão (R3), porque hoje todo membro é admin |
| D3 | Run sem agent que aceite falha ou espera? | Espera, com o motivo na página (R3.3) |
| D4 | Tempo até falhar sem node | 5 min, configurável (R4.1). Quem usa autoscaler pode precisar de mais |
| D5 | Colocação na pasta e no stack continua, além de instalação e agent? | Sim. Só com instalação e agent, um run pode cair em qualquer agent da organização, e não dá para mandar um stack para uma zona |
| D6 | Agent com colocação na UI e sem permissão no Helm | Fica parado, com o motivo na tela (R5.6) |
