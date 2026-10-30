# Prompt: base de ideias para o Firstlume a partir da pipeline atual (Woodpecker)

> Cole o texto abaixo (a partir de "Contexto") numa sessão do Claude Code aberta no repositório que tem a pipeline do Woodpecker.

---

## Contexto

Estou construindo o **Firstlume**, uma plataforma de automação de Infraestrutura como Código, no estilo do env0, Spacelift e Terraform Cloud. Primeiro ela vai ser usada na empresa onde trabalho, para substituir o jeito como gerenciamos infraestrutura hoje. Depois quero vendê-la no mercado.

Este repositório é como fazemos isso **hoje**: pipelines do **Woodpecker CI** que rodam Terraform/Terragrunt. Quero usar o que existe aqui como base real para decidir o que o Firstlume precisa ter para ser a melhor alternativa de gerenciamento de infraestrutura.

### O que o Firstlume é (estado atual)

- **Stack:** backend em Go e UI em React + TypeScript, num monorepo. É um binário único, com uma imagem só, instalado com **um único Helm chart** em qualquer cluster Kubernetes. Usa Postgres.
- **Engines:** Terraform, OpenTofu (por causa da licença BSL do Terraform ≥ 1.6) e Terragrunt. As versões são fixadas por template, baixadas com checksum SHA256 verificado.
- **Modelo de dados:**
  - **template:** repositório git, path, ref padrão, engine e versão, token git opcional;
  - **environment:** uma instância de um template, com ref própria, variáveis e flag de auto-apply;
  - **variables:** Terraform (`TF_VAR_*`) ou variáveis de ambiente. As sensíveis são criptografadas com AES-256-GCM e mascaradas nos logs;
  - **runs:** `plan`, `apply` ou `destroy`;
  - **agents.**
- **Ciclo de um run:**
  - `queued → planning → awaiting_approval → apply_queued → applying → applied`;
  - alternativas: `no_changes`, `planned`, `discarded`, `failed`, `canceled`.
  - O apply aplica **exatamente** o plano aprovado: mesmo commit SHA, com o arquivo de plano salvo criptografado.
  - Há **lock por environment**, com fila FIFO.
- **Execução:**
  - um **agent** em modelo *pull* (só conexões de saída) busca o trabalho e cria **um Job Kubernetes efêmero por fase** (plan, apply). Esse pod é endurecido: não roda como root, tem rootfs somente leitura e nunca é retentado;
  - as credenciais de nuvem vêm da identidade do pod (IRSA, Workload Identity);
  - o agent pode rodar em outro cluster ou rede e falar com o control plane central.
- **Logs ao vivo** via SSE, e aprovação/descarte pela UI ou pela API.
- **Autenticação:** por enquanto, só um token admin.
- **Roadmap já planejado:**
  - fase 2: state backend HTTP próprio, com lock e versionamento;
  - fase 3: GitHub App, com plan automático em PR, comentário com o plano e apply no merge;
  - fase 4: drift detection agendado;
  - fase 5: usuários, SSO e RBAC, credenciais OIDC, políticas OPA e custos.

## Tarefa

### 1. Entenda como a infraestrutura é gerenciada hoje

Leia o repositório:
- pipelines do Woodpecker (`.woodpecker.yml`, `.woodpecker.yaml`, `.woodpecker/*`);
- código Terraform/Terragrunt;
- scripts, Makefiles, Dockerfiles e documentação.

Mapeie:
- **gatilhos:** push, PR, tag, cron, manual/promote, e os filtros de branch e path;
- **etapas:** fmt, validate, lint (tflint, checkov, tfsec/trivy), plan, apply, testes;
- **ferramentas:** imagens, versões e como são fixadas;
- **state:** backends, locking, um state por ambiente/região/conta;
- **ambientes:** como são separados (dev, stg, prd), contas e projetos de nuvem, promoção entre ambientes;
- **aprovação:** quem aprova, como o plano é revisado antes do apply e se o apply usa o mesmo plano revisado;
- **segredos e credenciais:** de onde vêm (secrets do Woodpecker, Vault, OIDC, chaves estáticas). **Nunca copie valores de segredos**, só os nomes e o mecanismo;
- **módulos:** registry privado, refs git, dependências entre stacks (`dependency` do Terragrunt, `run-all`);
- **observabilidade:** onde ficam os logs dos runs, histórico, notificações (Slack etc.) e auditoria;
- **drift, custos e políticas,** se existir algo.

### 2. Identifique dores e riscos

Liste os problemas concretos do processo atual, cada um com **evidência**: arquivo e linha, ou trecho curto da configuração. Exemplos do que procurar:
- apply que roda um plano diferente do revisado;
- falta de lock entre pipelines concorrentes;
- credenciais estáticas de longa duração;
- copy-paste de pipeline entre ambientes;
- dificuldade de ver o plano no PR;
- ausência de drift detection;
- logs que somem;
- versões não fixadas.

Separe o que é **fato observado** do que é **suposição**.

### 3. Gere a base de dicas para o Firstlume

Para cada ideia, informe:
- **Problema / oportunidade:** de preferência ligado a uma dor do passo 2;
- **Proposta:** o que o Firstlume deveria fazer, de forma concreta (comportamento, não só o nome da feature);
- **Por que é diferencial:** compare com o que env0, Spacelift, Terraform Cloud/HCP, Atlantis, Terrakube, Digger e Scalr fazem ou deixam de fazer. Seja honesto quando for só paridade;
- **Esforço** (P/M/G) e **prioridade** (must-have para migrar / diferencial / futuro);
- **Fase do roadmap** em que se encaixa, ou "nova fase".

Cubra pelo menos:
- fluxo GitOps de PR;
- state;
- drift;
- segurança (OIDC, segredos, isolamento);
- governança (RBAC, aprovações, políticas, auditoria);
- Terragrunt e dependências entre stacks;
- experiência do desenvolvedor (UI, CLI, templates, self-service);
- custos;
- observabilidade e notificações;
- performance (cache de providers e módulos);
- operação self-hosted (Helm, upgrades, backup, HA);
- **integração com o Woodpecker**, para coexistir durante a migração (ex.: step/plugin do Woodpecker que dispara runs no Firstlume, webhooks);
- modelo de negócio e licenciamento, onde for relevante.

### 4. Plano de migração

Proponha como sair do Woodpecker para o Firstlume de forma incremental:
- o que migra primeiro;
- como os dois coexistem;
- como importar o state e o histórico existentes;
- critérios de sucesso.
- O que o Firstlume **precisa** ter antes de assumir o primeiro ambiente real.

### 5. Perguntas em aberto

Liste o que você não conseguiu responder só pelo código e que eu deveria levantar com o time.

## Regras

- **Somente leitura.** Não altere pipelines, não rode `terraform`/`terragrunt` (nem plan) e não faça commit, a não ser o arquivo final pedido abaixo.
- Não reproduza valores de segredos, tokens, IDs de conta sensíveis nem endpoints internos que não sejam necessários. Prefira descrever o mecanismo.
- Cite caminhos de arquivo (`caminho:linha`) para sustentar as conclusões.
- Se pesquisar concorrentes na web, cite a fonte e a data. Recursos mudam rápido.

## Entrega

Escreva tudo em português, num único arquivo `docs/firstlume-insights.md` neste repositório, com:

1. **Resumo executivo** (até 10 linhas): as 5 ideias mais importantes e o principal motivo para trocar o processo atual.
2. **Inventário** do processo atual (passo 1), com uma tabela por pipeline.
3. **Dores e riscos** (passo 2).
4. **Base de dicas** (passo 3), numa tabela-resumo ordenada por prioridade, seguida do detalhe de cada item.
5. **Plano de migração** (passo 4).
6. **Perguntas em aberto** (passo 5).

No final, me mostre o resumo executivo aqui na conversa.
