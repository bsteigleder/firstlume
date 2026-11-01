# Spec 0001: organizações, pastas, stacks, herança e Gitea

- **Status:** aprovada em 2026-10-30, com o desenho das telas em https://claude.ai/artifact/Gvhm5NtcncxEjh7a9ug1CK. **Emenda do login (R7.11 a R7.14, Marco 3a/3b, D6 a D8) aprovada em 2026-10-31.**
- **Data:** 2026-10-30
- **Substitui:** o modelo atual de *templates* e *environments*

## 1. Contexto

Hoje o Firstlume tem três limitações estruturais:

- **Organização única,** fixada por variável de ambiente.
- **Template preso ao repositório:** cada template aponta para um repositório e um caminho.
- **Environment solto:** cada environment é uma instância de um template, sem nenhuma organização acima dele.

Isso não serve para como a infraestrutura é organizada de verdade:

- **A hierarquia é da empresa, não do repositório.** Um time quer organizar a infra em pastas como `Ambientes/prod`, `Ambientes/homologação` ou `Clientes/X/rede`, e isso não espelha a estrutura dos repositórios.
- **Um repositório pode ter várias infraestruturas.** Exemplos: `environments/prod` e `environments/edge` na mesma branch. Cada uma precisa ser gerenciada separadamente, com state, lock, runs e configurações próprios.
- **Configurações se repetem.** O mesmo segredo, a mesma regra de aprovação e o mesmo intervalo de drift valem para dezenas de infraestruturas. Precisam ser definidos uma vez num nível alto e sobrescritos onde for preciso.
- **O código vive num Git.** O Gitea é o primeiro alvo. O usuário deve conectar a conta do Git uma vez e ver todos os repositórios, sem cadastrar URL por URL. O login pode ser feito pelo próprio Git.

**Objetivo desta spec:** substituir templates e environments por **organizações → pastas → stacks**, com **herança de configurações** do nível mais alto para o mais baixo. Também cobre a conexão com o **Gitea**, a descoberta automática das pastas **`.firstlume`** e o **login pelo Gitea**.

## 2. Glossário

| Termo | Significado |
|---|---|
| **Organização** | Topo da hierarquia. Agrupa pastas, stacks, conexão Git, agents, membros, configurações e segredos. Uma instalação tem várias. |
| **Pasta** | Nó livre da árvore dentro de uma organização. O nome é escolhido por quem cria. Pode conter pastas e stacks. Tem até 10 níveis. |
| **Stack** | Uma infraestrutura gerenciada (o que hoje é "environment"). Fica numa pasta ou na raiz da organização. Aponta para um repositório, uma branch e uma pasta `.firstlume`. Tem state, lock, runs e configurações próprios. |
| **Conexão Git** | Ligação de uma organização com uma conta do Git (no Gitea, uma organização ou um usuário). Dá acesso a todos os repositórios dessa conta. Por enquanto há uma por organização. |
| **`.firstlume`** | Pasta dentro de um repositório que marca a raiz de uma infraestrutura e guarda a configuração dela (`config.yaml`). |
| **Nível** | Organização, cada pasta ou o stack. É onde uma configuração pode ser definida. |
| **Herança** | Regra que decide qual valor de uma configuração vale para um stack: o definido no nível mais próximo dele. |
| **Run** | Execução de plan, apply, destroy ou drift de um stack. Funciona como hoje. |

## 3. Escopo

**Dentro:**
- várias organizações;
- árvore de pastas;
- stacks;
- herança de variáveis, segredos e configurações;
- conexão Gitea;
- descoberta de `.firstlume`;
- `config.yaml`;
- login pelo Gitea com sessão;
- agents por organização;
- nova API e nova UI de navegação;
- ambiente de desenvolvimento com Gitea local.

**Fora (próximas specs):**
- aprovadores por pessoa ou grupo (o modelo desta spec já prevê que sejam herdados);
- várias conexões Git por organização;
- GitHub e GitLab;
- plan automático em PR ou push (webhooks);
- state backend próprio;
- Terragrunt nativo (units);
- Vault como fonte de segredos;
- mover pastas e stacks de lugar;
- OpenTelemetry;
- custos.

## 4. Regras do modelo

### 4.1 Organizações

- **R1.1** Uma instalação tem N organizações. Cada uma tem **nome** (exibição) e **slug** (identificador na URL: minúsculas, números e hífen, de 2 a 40 caracteres, único na instalação).
- **R1.2** Todo dado (pastas, stacks, runs, agents, variáveis, conexão) pertence a exatamente uma organização. Nenhuma consulta ou ação cruza organizações.
- **R1.3** O usuário só vê as organizações das quais é membro. O token admin da instalação vê todas.
- **R1.4** A organização é o **nível mais alto** de configuração e a **última** a ser consultada na herança (seção 4.6).

### 4.2 Pastas

- **R2.1** Uma pasta tem nome livre, de 1 a 100 caracteres. Não pode conter `/` nem ser só espaços. O nome é único entre irmãos (mesmo pai), ignorando maiúsculas e minúsculas.
- **R2.2** Uma pasta fica na raiz da organização ou dentro de outra pasta. A **profundidade máxima é 10**: uma pasta na raiz tem nível 1, e não pode existir pasta no nível 11.
- **R2.3** Pastas são opcionais. Uma organização pode ter todos os stacks na raiz.
- **R2.4** Uma pasta pode ser renomeada. Mover pastas fica fora desta spec.
- **R2.5** Uma pasta só pode ser excluída se estiver vazia, sem subpastas nem stacks.
- **R2.6** Toda pasta é um nível de configuração (seção 4.6).

### 4.3 Stacks

- **R3.1** Um stack tem nome livre (mesmas regras da pasta), único entre os stacks da mesma pasta, ou da raiz se não tiver pasta.
- **R3.2** Um stack aponta para:
  - **repositório:** `dono/nome`, dentro da conta da conexão Git;
  - **branch:** padrão é a branch principal do repositório;
  - **caminho:** a pasta do repositório que **contém** o `.firstlume`, por exemplo `environments/prod`. Na raiz do repositório o caminho é `.`.
- **R3.3** Não podem existir dois stacks na mesma organização com o mesmo **repositório + branch + caminho**. Eles gerenciariam a mesma infraestrutura com states diferentes. A tentativa é recusada com mensagem clara.
- **R3.4** Vários stacks podem vir do mesmo repositório e da mesma branch, com caminhos diferentes. Cada um tem **state, lock, runs, variáveis e configurações próprios**. Um run de um stack nunca executa outro stack junto.
- **R3.5** O lock e a fila de runs são por stack, exatamente como hoje são por environment.
- **R3.6** Um stack só pode ser excluído sem run ativo, como hoje. Excluir não destrói infraestrutura.
- **R3.7** Repositório, branch e caminho de um stack podem ser editados. A UI avisa que mudar o caminho muda qual infraestrutura o stack gerencia.

### 4.4 A pasta `.firstlume` e o `config.yaml`

- **R4.1** Uma infraestrutura é marcada no repositório por uma pasta `.firstlume` com o arquivo `config.yaml`. A pasta que **contém** o `.firstlume` é a raiz onde o Terraform/Terragrunt roda.

  ```
  environments/
  ├── prod/
  │   ├── .firstlume/config.yaml   → stack com caminho environments/prod
  │   └── main.tf ...
  └── edge/
      ├── .firstlume/config.yaml   → stack com caminho environments/edge
      └── main.tf ...
  ```

- **R4.2** Formato do `config.yaml`, versão 1:

  ```yaml
  version: 1                  # obrigatório; hoje só 1
  engine: terraform           # obrigatório: terraform | opentofu
  engine_version: 1.15.8      # obrigatório: versão exata (x.y.z)
  terragrunt_version: 0.77.0  # opcional: se presente, roda via Terragrunt
  ```

  - Campos desconhecidos são **erro**, para pegar erros de digitação.
  - As regras de versão são as mesmas de hoje: versão exata, download com checksum.

- **R4.3** A configuração é lida **do commit que está sendo executado**. Trocar a versão do Terraform é um commit, e o apply sempre usa a mesma configuração do plan, porque usa o mesmo commit.
- **R4.4** Se o `config.yaml` estiver ausente ou inválido no commit, o run falha na fase de plan com uma mensagem que diz o arquivo e o erro. Exemplo: `environments/prod/.firstlume/config.yaml: engine_version must be an exact version like 1.9.8`.
- **R4.5** O Firstlume guarda no stack uma cópia da última configuração lida, só para exibição (engine e versão). Ela não é usada para executar.
- **R4.5a** **Terragrunt `run --all`**: com `run_all: true` (exige `terragrunt_version`), o stack roda `terragrunt run --all` a partir da pasta que contém o `.firstlume`, cobrindo todas as units abaixo dela, como os pipelines de hoje:

  ```yaml
  version: 1
  engine: terraform
  engine_version: 1.15.8
  terragrunt_version: 1.2.1
  run_all: true
  ```

  - **Plan:** salva o plano de cada unit (`--out-dir`) e o JSON de cada unit (`--json-out-dir`). Os planos de todas as units viajam juntos, num único artefato criptografado, até o apply.
  - **Apply:** aplica exatamente esses planos, unit por unit.
  - **Revisão:** a lista de mudanças (seção 4.11) indica a **unit** de cada recurso.
  - **Limite desta versão:** o run é um só para todas as units. A visão por unit, com runs separados, sequência e desenho da arquitetura, é uma spec futura (seção 11).
- **R4.6** A pasta `.firstlume` é reservada para o Firstlume. O `config.yaml` vai ganhar, numa spec futura, **gatilhos** (push, PR, merge em branch, caminhos que disparam) e **steps** (ganchos antes e depois das fases). Essa spec seguirá três regras:
  1. O repositório define *como* e *quando* rodar. Segredos, aprovação e permissões ficam no Firstlume (seção 4.6). O repositório pode **apertar** essas regras, por exemplo exigir aprovação, mas nunca **afrouxar**.
  2. Steps são ganchos em volta das fases fixas, não uma pipeline livre. A sequência `plan → aprovação → apply desse plan` continua garantida pelo Firstlume.
  3. Em runs de PR, gatilhos e steps vêm da branch de destino, não do PR, para que um PR não consiga alterar steps e ler segredos.

  Até lá, campos que não são da versão 1 são erro (R4.2). Quando a spec chegar, ela sobe `version` e mantém a versão 1 válida.

### 4.5 Conexão Git (Gitea)

- **R5.1** Cada organização tem **no máximo uma** conexão Git nesta spec. O modelo de dados já permite várias.
- **R5.2** Uma conexão Gitea guarda:
  - a **URL do Gitea**, que nesta spec é a mesma configurada na instalação para login;
  - a **conta**: o nome de uma organização ou de um usuário do Gitea;
  - um **token de serviço**: token de acesso de uma conta de bot do Gitea, com leitura dos repositórios. Fica criptografado como os segredos, nunca é devolvido pela API e nunca aparece em log.
- **R5.3** O token de serviço é **separado do login**. Ele é usado para listar repositórios, procurar `.firstlume`, ler `config.yaml` e clonar nos runs, inclusive nos agendados. Assim nada para quando alguém sai da empresa.
- **R5.4** Ao salvar a conexão, o Firstlume valida na hora que o token funciona e enxerga a conta. Se falhar, recusa com o motivo, por exemplo "token inválido" ou "conta não encontrada".
- **R5.5** **Listar repositórios:** traz todos os repositórios da conta (paginado), com nome, branch padrão e se é privado.
- **R5.6** **Descobrir `.firstlume`:** dado repositório e branch, lista todas as pastas `.firstlume` da árvore daquela branch **sem clonar** (pela API do Gitea). Para cada uma, devolve:
  - o caminho do stack (a pasta que a contém);
  - se o `config.yaml` é válido, e a engine e a versão, ou o erro;
  - se já existe stack na organização com esse repositório, branch e caminho (R3.3), e qual.
- **R5.7** Repositórios grandes: a descoberta precisa funcionar com pelo menos 20.000 arquivos. Se o Gitea truncar a árvore, o Firstlume pagina. Se ainda assim não conseguir, avisa em vez de mostrar uma lista incompleta como se fosse completa.
- **R5.8** No clone, o executor usa o token de serviço, como hoje usa o token do template.

### 4.6 Herança de configurações

Esta é a regra central da spec.

- **R6.1** **Níveis**, do mais próximo ao mais distante de um stack:

  ```
  stack → pasta onde está → pasta pai → ... → pasta de nível 1 → organização
  ```

- **R6.2** **Regra de resolução:** para cada configuração, vale o valor do **primeiro nível que a define**, subindo a partir do stack. A organização é consultada por último.
- **R6.3** O que é herdado:

  | Configuração | Chave de identidade | Observação |
  |---|---|---|
  | Variáveis Terraform (`TF_VAR_*`) | categoria + nome | Inclui as sensíveis (segredos) |
  | Variáveis de ambiente | categoria + nome | Inclui as sensíveis (segredos) |
  | Exigir aprovação antes do apply | — | Três estados: herdar, sim, não |
  | Intervalo de drift detection | — | Herdar, desligado ou um intervalo |
  | Confirmar cada exclusão ou recriação | — | Três estados: herdar, sim, não. Padrão **não** (R11.5) |

  Estas configurações das próximas specs seguirão a mesma regra: aprovadores, agent ou pool, fonte Vault, hooks e notificações.

- **R6.4** **Variáveis:** a identidade é **categoria + nome**. `TF_VAR_region` e a variável de ambiente `region` são coisas diferentes. Uma pasta que define `region` (Terraform) sobrescreve a `region` (Terraform) da organização só para os stacks abaixo dela.
- **R6.5** **Segredos** são variáveis com `sensitive: true` e seguem a mesma regra. O valor de um segredo nunca é devolvido pela API em nenhum nível. A tela mostra de onde ele vem, mas não o valor.
- **R6.6** O mascaramento nos logs vale para o valor **efetivo** de toda variável sensível que chega ao run.
- **R6.7** **Padrões quando nenhum nível define:**
  - aprovação **exigida**, porque é o padrão seguro;
  - drift **desligado**;
  - nenhuma variável.
- **R6.8** Nesta spec não há "remover uma variável herdada" num nível de baixo. Dá para sobrescrever o valor, mas não apagar. Se for preciso, entra numa spec futura.
- **R6.9** **A herança é avaliada no momento do run:**
  - o **plan** usa as variáveis efetivas na hora em que começa;
  - o **apply** usa as **mesmas** variáveis do plan (snapshot guardado criptografado no run), mesmo que alguém mude uma pasta entre o plan e o apply. Assim o apply executa exatamente o que foi aprovado;
  - a **regra de aprovação** é lida quando o plan termina, como hoje;
  - o **drift** é avaliado pelo agendador.
- **R6.10** **Transparência:** em qualquer nível, a UI mostra:
  - o que está definido **neste** nível, editável;
  - o que vem **herdado**, somente leitura, com o nome do nível de origem e um link para ele;
  - o que **este nível sobrescreve**, com a marca "sobrescreve <origem>".
- **R6.11** **Exemplo:**

  ```
  Organização Acme           TF_VAR_region=us-east-1, segredo DB_PASSWORD=a, aprovação=sim
  └── Ambientes              (nada)
      ├── prod               segredo DB_PASSWORD=b, drift=a cada 6h
      │   └── stack cluster   (nada)          → region=us-east-1 (Acme), DB_PASSWORD=b (prod), aprovação=sim (Acme), drift=6h (prod)
      └── dev                aprovação=não
          └── stack dev01    TF_VAR_region=sa-east-1
                                             → region=sa-east-1 (stack), DB_PASSWORD=a (Acme), aprovação=não (dev), drift=off (padrão)
  ```

### 4.7 Usuários, login e membros

- **R7.1** O login é pelo **Gitea**, que por sua vez pode autenticar os usuários num provedor de identidade (IdP) como Casdoor ou Keycloak. A cadeia fica Firstlume → Gitea → IdP. O fluxo é OAuth2/OIDC (*authorization code* com PKCE e `state`):
  1. O usuário clica em "Entrar com Gitea".
  2. Ele se autentica no Gitea.
  3. O Gitea devolve o usuário ao Firstlume, já logado.
- **R7.2** O Firstlume cria ou atualiza o usuário: id no provedor, login, nome, e-mail, avatar e grupos (organizações e times do Gitea). Os grupos são relidos a cada login. Eles são gravados já nesta spec, mas só serão usados quando houver mapa de papéis (R7.10).
- **R7.2a** O login é implementado como **cliente OIDC genérico**, configurado por URL do emissor, client ID e client secret. O Gitea é o primeiro provedor. Trocar para o **IdP direto** (por exemplo Casdoor ou Keycloak), ou outro provedor OIDC, deve ser só configuração, sem mudar código.
- **R7.3** **Sessão:**
  - cookie `HttpOnly`, `SameSite=Lax` e `Secure` quando o acesso é por HTTPS;
  - validade de 12 horas;
  - guardada no servidor (hash), o que permite revogar;
  - "Sair" encerra a sessão no servidor.
- **R7.4** **Proteção contra CSRF:** toda requisição que altera dados com cookie de sessão exige `Origin` igual à URL pública do Firstlume e `Content-Type: application/json`. A exceção é `multipart/form-data`, usado pelo import de state; o `Origin` já barra um formulário de outro site. Sem `Origin`, a requisição é recusada.
- **R7.5** O **token admin da instalação** continua existindo, para automação, bootstrap e emergência. Ele tem acesso a todas as organizações. A tela de login mantém a opção "usar token admin", que pode ser desligada por configuração.
- **R7.5a** **Configuração na instalação:** o login pelo Gitea é configurado nos values do Helm: URL do Gitea, client ID, e client secret num Secret. É isso que permite o primeiro login, antes de existir qualquer organização.
- **R7.6** **Acesso (decidido):** as organizações do Firstlume são **criadas do zero** e **não** espelham as organizações do Gitea.
  - Quem cria uma organização vira administrador dela.
  - Outras pessoas entram por **convite pelo usuário do Gitea**, feito por um administrador. O convidado ganha acesso no próximo login.
  - Remover um membro tira o acesso imediatamente, e as sessões dele naquela organização deixam de valer.
- **R7.7** **Primeiro acesso:** quem faz login e não pertence a nenhuma organização cai na tela "Bem-vindo, vamos criar a sua primeira organização", com nome e endereço. Depois de criar, segue para os próximos passos: conectar uma conta do Gitea, conferir o agent e criar o primeiro stack. Qualquer usuário logado pode criar organizações.
- **R7.8** **Papéis nesta spec: só administrador.** Todo usuário com acesso a uma organização é **administrador** dela e pode fazer tudo nela: pastas, stacks, variáveis e segredos em qualquer nível, runs, aprovação, conexão Git e agents. O token admin da instalação também é administrador de todas as organizações. **Não há** distinção entre quem pode rodar e quem pode aprovar nesta versão.
- **R7.10** **Mapa de papéis (próxima spec, fora desta):** os perfis definidos no **provedor de identidade** (IdP) vão decidir:
  - quem pode entrar;
  - quem é usuário normal e quem é administrador;
  - quem pode codar e rodar pipelines, mas **não** aprovar;
  - quem aprova e em quais pastas.

  Os perfis do IdP não chegam ao Firstlume pelo login via Gitea, porque o Gitea não repassa os papéis do IdP. Por isso essa spec deve avaliar o login **direto no IdP** por OIDC, o que a R7.2a já permite, e mapear os grupos ou papéis do IdP para papéis do Firstlume. Os papéis entram por nível e são herdados conforme a seção 4.6.

- **R7.9** Runs, aprovações e alterações de configuração registram **quem** fez, pelo login do Gitea ou como `admin-token`. Isso substitui o `admin` fixo de hoje.

#### Emenda de 2026-10-31: quem pode entrar e como instalar

Vem da primeira instalação real: lá o CI já faz login pelo Gitea e só aceita quem é membro de uma organização do Gitea. O Firstlume deve funcionar do mesmo jeito, para que a empresa tenha uma regra só.

- **R7.11** **Quem pode entrar (lista de grupos):** a instalação pode exigir que o usuário pertença a pelo menos um grupo de uma lista (`allowedGroups`).
  - Os grupos vêm do claim `groups` do token OIDC. No Gitea, com o scope `groups`, esse claim traz as organizações (`acme`) e os times (`acme:infra`), e a lista aceita os dois formatos.
  - O nome do claim é configurável (`groupsClaim`), para servir a outro provedor (R7.2a).
  - **Lista vazia:** entra qualquer pessoa que o provedor autenticar, como na R7.1.
  - **Fora da lista:** o login é recusado, e a tela explica que é preciso ser membro de um dos grupos, sem listar quais. Nenhum usuário é criado.
  - **Lista configurada, mas o token não traz o claim:** o login é recusado como "fora da lista" (falha fechada). O Gitea omite o claim de quem não pertence a nenhuma organização, então esse é o caso comum. O servidor registra no log que o claim não veio e lembra do scope `groups`, para o caso de ninguém conseguir entrar.
  - **Quando é verificada:** a cada login. Quem sai do grupo no Gitea perde a entrada no próximo login, e a sessão aberta vale até expirar (12 h, R7.3). Revogar na hora é por "remover membro" (R7.6).
  - A lista decide só **quem entra**. Ela não dá acesso a nenhuma organização do Firstlume: isso continua sendo convite (R7.6) ou criar a própria (R7.7).
- **R7.12** **Provedor OIDC na instalação:** valores do Helm e variáveis de ambiente.

  | Valor do Helm (em `controlPlane.`) | Variável | Exemplo / padrão |
  |---|---|---|
  | `publicUrl` | `FIRSTLUME_PUBLIC_URL` | `https://firstlume.example.com`; obrigatório com OIDC |
  | `auth.oidc.enabled` | (liga as abaixo) | `false` |
  | `auth.oidc.providerName` | `FIRSTLUME_OIDC_PROVIDER_NAME` | `Gitea`, texto do botão "Entrar com …" |
  | `auth.oidc.issuer` | `FIRSTLUME_OIDC_ISSUER` | `https://gitea.example.com`, o `issuer` do discovery. O Gitea responde sem a barra final; o servidor aceita a barra diferente |
  | `auth.oidc.clientId` | `FIRSTLUME_OIDC_CLIENT_ID` | |
  | `auth.oidc.existingSecret` + `clientSecretKey` | `FIRSTLUME_OIDC_CLIENT_SECRET` | Secret existente, key `client-secret` |
  | `auth.oidc.scopes` | `FIRSTLUME_OIDC_SCOPES` | `openid profile email groups` |
  | `auth.oidc.groupsClaim` | `FIRSTLUME_OIDC_GROUPS_CLAIM` | `groups` |
  | `auth.oidc.allowedGroups` | `FIRSTLUME_OIDC_ALLOWED_GROUPS` | lista separada por vírgula; vazia = todos |
  | `auth.adminTokenLogin` | `FIRSTLUME_ADMIN_TOKEN_LOGIN` | `true` (R7.5) |

  - O endereço de retorno é sempre `<publicUrl>/auth/callback`. É o que se cadastra no app OAuth do provedor, e o servidor recusa subir com OIDC ligado e sem `publicUrl`.
  - O client secret só entra por Secret existente, nunca em texto nos values. Assim ele pode vir de um cofre (Vault, via operador) e o ArgoCD não guarda segredo.
  - Na subida, o servidor lê o `/.well-known/openid-configuration` do emissor. Se falhar, ele **sobe mesmo assim**, com o botão do provedor desligado e o token admin funcionando, e tenta de novo a cada minuto. Um provedor fora do ar não pode derrubar o Firstlume.
- **R7.13** **App OAuth no Gitea:** é um app confidencial, criado por um admin do Gitea (ou por organização). O guia de instalação documenta o passo a passo e o retorno a cadastrar. O `make dev-gitea` cria esse app sozinho (seção 8).
- **R7.14** **Tela de login:**
  - mostra "Entrar com <providerName>" quando o OIDC está ligado e o emissor respondeu;
  - mostra "Usar token admin" quando `adminTokenLogin` está ligado;
  - sem nenhum dos dois, explica que o login não está configurado e como configurar;
  - um erro do provedor (login recusado pela R7.11, `state` inválido, provedor fora) volta para a tela de login com uma mensagem clara, nunca uma página em branco ou um JSON.

### 4.8 Agents

- **R8.1** Agents pertencem a uma organização, como hoje, e só recebem runs dela.
- **R8.2** O agent de bootstrap (`FIRSTLUME_BOOTSTRAP_AGENT_TOKEN`) pertence à organização de bootstrap (`FIRSTLUME_ORGANIZATION`, padrão `default`), que continua sendo criada na subida para facilitar o desenvolvimento.

### 4.9 Runs e execução

- **R9.1** O ciclo de vida, os tipos (plan, apply, destroy e drift), o lock por stack, o plano criptografado, a aprovação e o drift funcionam como hoje.
- **R9.2** O `ExecSpec` passa a ser montado com:
  - URL de clone: conexão + repositório;
  - token de serviço;
  - branch ou commit;
  - caminho do stack;
  - variáveis efetivas (R6.9).

  A engine e as versões saem do `config.yaml` no commit (R4.3).
- **R9.3** O executor, depois do checkout, lê e valida `<caminho>/.firstlume/config.yaml` antes de instalar ferramentas.

### 4.10 Runs em andamento sempre à vista

- **R10.1** A UI mostra, em todas as telas, um indicador com quantas runs da organização estão **ativas**: na fila, planejando, esperando aprovação, com apply na fila ou aplicando.
- **R10.2** O indicador abre a **fila de runs ativas**. Para cada run aparecem:
  - stack e caminho de pastas;
  - tipo e status;
  - tempo decorrido;
  - quem disparou;
  - autor e mensagem do commit.

  Um clique leva direto para os logs ao vivo da run, sem precisar navegar até o stack. A fila funciona com 1 ou com dezenas de runs ao mesmo tempo.
- **R10.3** Runs **esperando aprovação** ficam destacadas, porque dependem de alguém agir.
- **R10.4** Cada run guarda o **autor do commit** (nome e e-mail) e a **primeira linha da mensagem**, lidos pelo executor no checkout.
- **R10.6** O commit de uma run tem **link para o commit no Git**, que abre a página do commit no Gitea (ou no GitHub/GitLab). O link aparece na run, no histórico do stack e na fila. Ele é montado a partir do repositório:
  - `https://host/dono/repo(.git)` → `https://host/dono/repo/commit/<sha>`;
  - `git@host:dono/repo.git` → `https://host/dono/repo/commit/<sha>`;
  - `file://` ou formatos desconhecidos ficam sem link.

  No Marco 2, a conexão Gitea fornece a URL web diretamente.
- **R10.5** **As minhas primeiro** (*Marco 3*, porque precisa saber quem está logado): na fila, primeiro vêm as runs disparadas pelo usuário ou cujo commit é dele. O e-mail do commit é comparado com o e-mail do usuário no Gitea. Depois vêm as dos outros. Dentro de cada grupo, a ordem é: esperando aprovação, depois as mais recentes.

### 4.11 Revisão do plano: o que vai mudar, recurso por recurso

O log do Terraform continua disponível, mas **não é a forma principal de revisar**. Numa pipeline comum, deleções e recriações se perdem no meio do texto, e o resumo `Plan: X to add, Y to change, Z to destroy` conta uma recriação como 1 add + 1 destroy, sem dizer que é a mesma coisa sendo recriada.

- **R11.1** No plan, o executor guarda o plano estruturado (`terraform show -json`), criptografado como o arquivo de plano. A partir dele, o Firstlume monta a lista de mudanças por recurso:
  - endereço;
  - tipo;
  - nome;
  - ação: **criar**, **atualizar no lugar**, **recriar** (deletar e criar) ou **deletar**;
  - atributos que mudam (antes → depois);
  - na recriação, quais atributos a forçam (`replace_paths`).

  Leituras de data sources e recursos sem mudança ficam de fora.
- **R11.2** Valores marcados como sensíveis no plano (`before_sensitive` e `after_sensitive`), e os valores das variáveis sensíveis efetivas, aparecem como `(sensitive)`. Nunca vão para a API nem para a UI.
- **R11.3** A tela da run mostra a lista agrupada nesta ordem, com contagem por grupo:
  1. **Vai ser deletado**
  2. **Vai ser recriado**
  3. **Vai ser atualizado**
  4. **Vai ser criado**

  Recriação tem cor e texto próprios ("deleta e cria de novo"), com o atributo que força a recriação. O tipo aparece com nome legível quando conhecido, por exemplo "Database (RDS instance)" para `aws_db_instance` e "Subnet" para `aws_subnet`, e com o tipo técnico quando não.
- **R11.4** O resumo da run passa a contar **recriações separadas** de criações e deleções: `1 delete · 1 replace · 2 update · 2 create`.
- **R11.5** **Confirmar cada exclusão ou recriação** é uma configuração herdada (seção 4.6), **desligada por padrão**. Quando ligada:
  - cada recurso que vai ser deletado ou recriado ganha uma caixa "Estou ciente de que X vai ser deletado" (ou "recriado");
  - o botão de aprovar só libera com todas marcadas;
  - **o servidor também confere:** a aprovação envia os endereços confirmados, e é recusada (409) se faltar algum dos deletados ou recriados daquele plano;
  - os endereços confirmados ficam registrados na run, junto com quem aprovou.
- **R11.7** **Trilha de passos da run**:
  - a run é dividida em **passos**, agrupados por fase:
    - **Plan:** na fila → checkout → init → plan;
    - **Aprovação;**
    - **Apply:** apply → salvar state (quando o state backend existir);
  - o executor informa ao servidor o início e o fim de cada passo, e cada passo guarda status (pendente, rodando, feito, falhou, pulado), duração e **o próprio trecho de log**;
  - a UI mostra a trilha no topo da run, e clicar num passo filtra os logs para ele;
  - os **ganchos** do `.firstlume` (antes do plan, depois do apply etc., spec futura) aparecem como passos próprios, marcados como "hook". A trilha mostra os passos que a run realmente tem, sem uma lista fixa.
- **R11.8** **Configuração usada pela run**: a run guarda e mostra o que foi carregado para ela:
  - o `config.yaml` do commit, com o caminho e um link para o arquivo no Git naquele commit;
  - as configurações efetivas (aprovação, confirmação de destrutivos, drift) com a origem de cada uma;
  - os nomes das variáveis efetivas, com a origem. Segredos aparecem como `••••••`.

  Mudar uma pasta depois não altera o que a run mostra, porque ela reflete o momento em que rodou.

  As três partes (`config.yaml`, configurações e variáveis) começam **recolhidas**. Cada configuração e cada variável tem um botão **"Edit in <nível>"** que leva às settings do nível de onde ela vem, para alterar ali.
- **R11.6** Fica para depois: **sugestões** sobre o plano. Exemplo: "recriar esta subnet também afeta o que está dentro dela; considere `create_before_destroy`".

## 5. API (v1, revisada)

As rotas ficam aninhadas na organização, pelo slug. Autenticação: sessão (cookie) ou `Authorization: Bearer <token admin>`.

```
GET    /auth/login                 → redireciona ao Gitea
GET    /auth/callback              → volta do Gitea, cria a sessão
POST   /auth/logout
GET    /api/v1/me                  → usuário, organizações e papéis

GET    /api/v1/orgs                POST /api/v1/orgs
GET    /api/v1/orgs/{org}          PATCH, DELETE (vazia)

GET    /api/v1/orgs/{org}/tree                       → pastas e stacks (para navegação)
POST   /api/v1/orgs/{org}/folders                    {name, parent_id|null}
PATCH  /api/v1/orgs/{org}/folders/{id}               {name}
DELETE /api/v1/orgs/{org}/folders/{id}               (só vazia)

GET    /api/v1/orgs/{org}/settings                   → configurações e variáveis do nível org
PUT    /api/v1/orgs/{org}/settings
GET    /api/v1/orgs/{org}/folders/{id}/settings      → as deste nível + efetivas com origem
PUT    /api/v1/orgs/{org}/folders/{id}/settings
GET    /api/v1/orgs/{org}/stacks/{id}/settings
PUT    /api/v1/orgs/{org}/stacks/{id}/settings

GET    /api/v1/orgs/{org}/git/connection             PUT, DELETE
GET    /api/v1/orgs/{org}/git/repos                  → repositórios da conta
GET    /api/v1/orgs/{org}/git/repos/{owner}/{repo}/branches
GET    /api/v1/orgs/{org}/git/repos/{owner}/{repo}/discover?ref=main   → .firstlume encontrados

POST   /api/v1/orgs/{org}/stacks                     {folder_id|null, name, repository, ref, path}
GET    /api/v1/orgs/{org}/stacks/{id}                PATCH, DELETE
GET    /api/v1/orgs/{org}/stacks/{id}/runs           POST
GET    /api/v1/orgs/{org}/runs/{id}                  + approve, discard, cancel, logs (como hoje)
GET    /api/v1/orgs/{org}/runs?active=true           → runs ativas da organização, com stack e caminho de pastas
GET    /api/v1/orgs/{org}/runs/{id}/changes          → mudanças do plano, por recurso (R11)
POST   /api/v1/orgs/{org}/runs/{id}/approve          {acknowledged: ["aws_db_instance.legacy", ...]} quando R11.5 está ligado

GET    /api/v1/orgs/{org}/agents                     POST, DELETE
```

Somem as rotas `/api/v1/templates` e `/api/v1/environments`. As APIs do agent (`/agent/v1`) e do executor (`/exec/v1`) não mudam de forma.

## 6. UI

- **Login:** botão "Entrar com Gitea", e "Usar token admin" se estiver habilitado.
- **Seletor de organização** no topo do menu, com a opção "Criar organização".
- **Navegação:**
  - árvore de pastas e stacks no menu lateral, expansível;
  - página de **pasta** com breadcrumb (`Acme / Ambientes / prod`), mostrando subpastas e stacks, cada stack com status do último run e do drift;
  - botões **Nova pasta** e **Novo stack**.
- **Assistente "Novo stack"**, dentro da pasta atual:
  1. **Repositório:** lista da conta, com busca.
  2. **Branch:** já vem com a branch padrão.
  3. **Escolher `.firstlume`:** mostra os encontrados, com caminho, engine e versão, e marca os inválidos com o erro e os já usados com o stack que os usa.
  4. **Nome:** sugerido a partir da pasta do caminho, como `prod`.
  5. Criar.

  Se nenhum `.firstlume` for encontrado, a tela explica como criar um e mostra o exemplo do `config.yaml`.
- **Página do stack:** a página de environment de hoje, com runs, drift, aprovação e variáveis. As configurações e variáveis seguem a R6.10 e mostram herdadas, locais e sobrescritas.
- **Configurações da pasta e da organização:** a mesma tela de variáveis e configurações, para aquele nível.
- **Configurações da organização:** conexão Git (URL, conta, token, botão "testar"), agents e membros (somente leitura nesta spec, vindos do Gitea).

## 7. Migração do modelo atual

- O Firstlume ainda não tem dados de produção. A migration `00003` **remove** `templates`, `environments`, `environment_variables`, `runs` e `run_log_chunks` e as tabelas ligadas a eles, e cria o modelo novo. **Os dados de teste locais se perdem**, e isso é aceito.
- A organização de bootstrap e o agent de bootstrap continuam sendo criados na subida.

## 8. Ambiente de desenvolvimento

- **Gitea local:** o `docker-compose` ganha um Gitea com versão fixa.
- **`make dev-gitea`** prepara tudo de forma idempotente:
  - cria um usuário admin e uma conta de bot;
  - cria a organização `acme`;
  - cria o app OAuth do Firstlume, com callback em `http://127.0.0.1:8787/auth/callback`;
  - cria o token de serviço;
  - publica um repositório de exemplo `acme/infra` com `environments/prod`, `environments/edge` e `environments/dev`, cada um com `.firstlume/config.yaml` e um `terraform_data`;
  - acrescenta ao `.env` as variáveis necessárias.
- **`deploy/dev/demo.sh`** é atualizado para o fluxo novo: criar pasta, conectar o Gitea, descobrir, criar stack e rodar.
- **Helm:** novos valores para a URL pública do Firstlume e para o provedor OIDC, listados na R7.12. O login pelo Gitea é opcional: sem ele, só o token admin.
- **`make dev-gitea`** também cria um time `acme:infra` e um usuário de fora da `acme`, para testar a R7.11 à mão.

## 9. Entregas e critérios de aceite

A spec é entregue em **três marcos**. Cada marco termina funcionando, testado e com commit. Os critérios são verificáveis. O formato é "dado / quando / então", em resumo.

### Marco 1: organizações, pastas, stacks e herança (sem Gitea ainda)

Neste marco a autenticação ainda é só o token admin. Os stacks usam uma conexão **Git genérica**, com URL do repositório e token opcional, e o caminho é digitado sem descoberta. Isso mantém tudo funcionando enquanto o Gitea não chega, e continua útil para qualquer Git.

- [ ] **CA1.1** Dá para criar duas organizações. Pastas, stacks, runs e agents de uma **não aparecem** na outra, nem pela API nem pela UI. (Teste de integração com duas orgs.)
- [ ] **CA1.2** Dá para criar pastas até o **nível 10**. Criar no nível 11 retorna 422 com "profundidade máxima é 10".
- [ ] **CA1.3** Nomes de pasta e de stack repetidos no mesmo pai são recusados (409), inclusive com diferença só de maiúsculas. Em pais diferentes, são aceitos.
- [ ] **CA1.4** Excluir uma pasta com conteúdo é recusado (409). Uma pasta vazia é excluída.
- [ ] **CA1.5** Um stack pode ficar na raiz da organização ou em qualquer pasta.
- [ ] **CA1.6** Dois stacks com o mesmo repositório + branch + caminho são recusados (409, mensagem cita o stack existente). O mesmo repositório e a mesma branch com caminhos diferentes são aceitos.
- [ ] **CA1.7** Dois stacks do mesmo repositório rodam com **locks independentes**: um run ativo no stack A não segura a fila do stack B. (Teste de integração.)
- [ ] **CA1.8** **Herança:** com o cenário do exemplo R6.11, as variáveis que chegam ao executor de `cluster` e `dev01` são exatamente as da tabela, assim como a aprovação e o drift efetivos. (Teste de integração que compara o `ExecSpec`.)
- [ ] **CA1.9** **Herança na tela:** na página de `cluster`, `DB_PASSWORD` aparece como herdada de `prod`, sem valor, e `region` como herdada de `Acme`. Em `prod`, `DB_PASSWORD` aparece como "sobrescreve Acme".
- [ ] **CA1.10** **Snapshot entre plan e apply:** um run para em `awaiting_approval`, alguém muda uma variável herdada na organização, e depois aprova. O apply recebe os valores do plan, não os novos. (Teste de integração.)
- [ ] **CA1.11** Sem nenhuma definição, a aprovação é exigida e o drift fica desligado.
- [ ] **CA1.12** **Segredos:** nenhum valor sensível, em qualquer nível, aparece em resposta da API, log do servidor ou log do run. No log do run aparece `***`. (Teste que procura o valor em todas as respostas e logs.)
- [ ] **CA1.13** O `config.yaml` é lido do commit:
  - um commit com `engine_version` diferente faz o run usar a versão nova;
  - um `config.yaml` ausente ou inválido falha o plan com o caminho do arquivo e o erro.
- [ ] **CA1.14** O fluxo atual continua funcionando na UI nova: plan → aprovação → apply, drift agendado e logs ao vivo.
- [ ] **CA1.15** A migration `00003` roda num banco de dev existente sem erro, e a subida cria a org e o agent de bootstrap.
- [ ] **CA1.17** O SHA do commit na run e no histórico do stack é um link para `…/commit/<sha>` do repositório (R10.6). Um repositório `file://` mostra o SHA sem link.
- [ ] **CA1.16** Com runs ativas em três stacks de pastas diferentes, o indicador mostra 3 em qualquer tela. A fila lista as três com stack, caminho, status, quem disparou, autor e mensagem do commit, e um clique abre os logs ao vivo da run. Uma run esperando aprovação aparece destacada. Quando todas terminam, o indicador zera sem recarregar a página.

### Marco 1b: revisão do plano por recurso

- [ ] **CA1b.1** Um plano com uma criação, uma atualização, uma recriação e uma deleção aparece na tela da run em quatro grupos, nessa ordem de gravidade, com o resumo `1 delete · 1 replace · 1 update · 1 create`. A recriação mostra o atributo que a força.
- [ ] **CA1b.2** Um atributo sensível que muda aparece como `(sensitive)`. O valor não aparece em nenhuma resposta da API (teste que procura o valor).
- [ ] **CA1b.3** Com "confirmar cada exclusão ou recriação" ligado numa pasta, a aprovação de um stack abaixo dela:
  - é recusada (409) sem a lista de endereços, ou com a lista incompleta;
  - é aceita com todos os endereços confirmados, que ficam gravados na run.

  Num stack fora dessa pasta, com a configuração no padrão, aprova sem confirmação.
- [ ] **CA1b.4** Com a configuração ligada, o botão de aprovar na tela só habilita depois de todas as caixas marcadas.
- [ ] **CA1b.5** A aba **Logs** continua mostrando o log completo ao vivo.
- [ ] **CA1b.7** A tela da run mostra o `config.yaml` carregado (do commit da run) e as configurações e variáveis usadas, com origem. Alterar uma variável da pasta depois não muda o que essa run mostra. Nenhum valor sensível aparece.
- [ ] **CA1b.6** Uma run de apply mostra a trilha de passos na ordem real, com status e duração de cada um, atualizando ao vivo. Uma run que falha no init mostra checkout feito, init falhou e plan pulado. Clicar num passo mostra só o log dele.

### Marco 2: conexão Gitea e descoberta de `.firstlume`

- [ ] **CA2.1** Um admin conecta a organização a uma conta do Gitea com URL, conta e token. Um token errado ou uma conta inexistente é recusado com o motivo. O token nunca volta na API.
- [ ] **CA2.2** A lista de repositórios mostra **todos** os da conta, inclusive com mais de 50 (paginação), com branch padrão e se é privado.
- [ ] **CA2.3** No repositório de exemplo `acme/infra`, a descoberta na `main` mostra exatamente `environments/prod`, `environments/edge` e `environments/dev`, com engine e versão de cada um.
- [ ] **CA2.4** Um `.firstlume` sem `config.yaml`, ou com o arquivo inválido, aparece marcado como inválido, com o erro, e não pode ser escolhido.
- [ ] **CA2.5** Um `.firstlume` já usado por um stack aparece marcado com o nome desse stack, e não pode ser escolhido de novo.
- [ ] **CA2.6** Com um repositório gerado com 20.000 arquivos e um `.firstlume` no fundo da árvore, a descoberta o encontra. Se a árvore não puder ser lida inteira, a tela avisa em vez de mostrar uma lista incompleta.
- [ ] **CA2.7** O assistente "Novo stack" cria um stack em ≤ 5 cliques depois de abrir, a partir de uma pasta, num repositório com um `.firstlume`. O run desse stack clona um repositório **privado** com o token de serviço.
- [ ] **CA2.8** Dois stacks criados a partir de `environments/prod` e `environments/edge` do mesmo repositório aparecem em pastas diferentes e rodam separados (CA1.7 vale também aqui).
- [ ] **CA2.9** O token de serviço não aparece em logs do servidor, do agent nem do run, nem na linha de comando dos processos (`ps`).

### Marco 3: login pelo Gitea, sessões e membros

**Emenda de 2026-10-31:** o marco é entregue em duas partes, para que a primeira instalação possa trocar o token admin por login de pessoas o quanto antes.

- **Marco 3a, login:** CA3.1, CA3.2, CA3.3, CA3.4, CA3.6, CA3.8, CA3.9, CA3.11, CA3.12 e CA3.13, mais a tela de primeiro acesso (primeiro item da CA3.5). Até o 3b, todo usuário logado vê todas as organizações, como o token admin, e a tela avisa isso.
- **Marco 3b, membros:** o resto da CA3.5 (convite, usuário de fora, remover membro), a CA3.7 e a CA3.10.

- [ ] **CA3.1** "Entrar com Gitea" leva ao Gitea e volta ao Firstlume logado, mostrando nome e avatar.
- [ ] **CA3.2** Um `state` inválido ou reutilizado no callback é recusado. O fluxo usa PKCE.
- [ ] **CA3.3** O cookie de sessão é `HttpOnly` e `SameSite=Lax`, e é `Secure` atrás de HTTPS. "Sair" invalida a sessão no servidor: reutilizar o cookie antigo dá 401.
- [ ] **CA3.4** Uma requisição que altera dados com cookie de sessão e `Origin` de outro site é recusada (403).
- [ ] **CA3.5** Acesso:
  - um usuário sem organização, no primeiro login, cai na tela de criar a primeira organização e vira administrador dela;
  - um usuário convidado pelo login do Gitea vê a organização no próximo login;
  - um usuário de fora não vê a organização e recebe 404 nas rotas dela;
  - um membro removido perde o acesso na hora (403/404, mesmo com sessão aberta).
- [ ] **CA3.6** O login usa só configuração OIDC: emissor, client ID e client secret. Um teste com um provedor OIDC falso, diferente do Gitea, faz login sem mudança de código (R7.2a).
- [ ] **CA3.7** Runs e aprovações registram o login do Gitea de quem fez. Pelo token admin, registram `admin-token`.
- [ ] **CA3.8** O token admin continua funcionando para a API. A opção "usar token admin" na tela de login some quando desabilitada por configuração.
- [ ] **CA3.10** Na fila de runs ativas, as runs do usuário logado (disparadas por ele ou com commit do e-mail dele) aparecem antes das dos outros (R10.5).
- [ ] **CA3.9** O Helm instala com o login pelo Gitea configurado: URL pública, client ID e secret vindo de um Secret existente. `helm lint` e `helm template` passam com e sem Gitea.
- [ ] **CA3.11** Lista de grupos (R7.11), com o provedor OIDC falso:
  - com `allowedGroups: [acme]`, um membro de `acme` entra e um usuário só de `outra` é recusado com a mensagem da R7.11, sem criar usuário;
  - com `allowedGroups: [acme:infra]`, entra só quem está nesse time;
  - com a lista configurada e um token sem o claim `groups` (o Gitea omite o claim de quem não é de nenhuma organização), o login é recusado como "fora da lista" e o log diz que o claim faltou;
  - com a lista vazia, qualquer usuário autenticado entra.
- [ ] **CA3.12** Com o emissor fora do ar na subida, o servidor sobe, o `/readyz` responde 200, o token admin funciona e o botão do provedor fica desligado. Quando o emissor volta, o botão aparece sem reiniciar (R7.12).
- [ ] **CA3.13** O servidor não sobe com OIDC ligado e sem `publicUrl`, com uma mensagem que diz qual valor falta. O client secret não aparece em log nem em `helm template` (só a referência ao Secret).

### Critérios gerais, para todos os marcos

- [ ] **CG1** `make vet`, os testes unitários e os de integração (`make test-integration`) passam, e o `tsc` da UI não tem erros.
- [ ] **CG2** Cada regra R* com comportamento tem pelo menos um teste automatizado: unitário, de integração ou contra um Gitea falso via `httptest` (no Marco 2).
- [ ] **CG3** A imagem Docker builda, e `make dev-gitea && deploy/dev/demo.sh` funciona do zero numa máquina limpa (Marco 2 em diante).
- [ ] **CG4** README e roadmap atualizados. Esta spec passa a "implementada" com as decisões finais registradas.
- [ ] **CG5** Nada de dados reais da empresa no repositório público.

## 10. Decisões pendentes

| # | Pergunta | Proposta |
|---|---|---|
| D1 | Quem tem acesso a uma organização? | **Decidido:** organizações são criadas do zero; quem cria é admin; os outros entram por convite pelo usuário do Gitea (R7.6). Todos são administradores nesta versão (R7.8). |
| D2 | Quem pode criar organizações? | **Decidido:** qualquer usuário logado (R7.7). |
| D5 | Login pelo Gitea ou direto no IdP? | Gitea agora, como pedido. Na spec de papéis, avaliar o IdP direto, porque é lá que os perfis estão (R7.10). |
| D3 | Nome do item | **Stack**. O nome de cada um, e de cada pasta, é livre. |
| D4 | Uma pasta pode **remover** uma variável herdada? | Não nesta spec, só sobrescrever (R6.8). |
| D6 | Quem pode entrar quando há uma lista de grupos? | **Decidido (emenda):** só quem pertence a um grupo da lista, verificado a cada login (R7.11), como o CI da primeira instalação faz. |
| D7 | Alguém vira administrador da instalação pelo login, como o "admin" do CI? | **Decidido (emenda):** não nesta spec. O token admin já cobre emergência e automação, e todos são administradores das próprias organizações (R7.8). Fica para a spec de papéis. |
| D8 | Até o Marco 3b, o que um usuário logado vê? | **Decidido (emenda):** todas as organizações, como o token admin, com um aviso na tela. Com a R7.11 limitando quem entra, o risco fica no mesmo grupo de pessoas que hoje recebe o token admin. |

## 11. Próximas specs (ordem sugerida)

1. **Units do Terragrunt como pipelines separados** (substitui o `run --all` da R4.5a):
   - cada unit é uma "caixinha" com seu próprio plan, aprovação e apply;
   - as units rodam em sequência, respeitando as dependências;
   - um **desenho dinâmico da arquitetura** mostra todas as units e as ligações entre elas, montado a partir do grafo do Terragrunt.
1. **Runners com tamanho automático** (CPU e memória de cada Job):
   - **padrão:** toda fase começa com um tamanho padrão da instalação;
   - **medição:** o executor mede o pico real de memória e CPU de cada fase (cgroup do pod) e grava na run. A tela mostra "pediu X, usou Y";
   - **ajuste automático:** se a run usou muito menos (desperdício) ou chegou perto do limite, a próxima run do mesmo stack e fase pede o p95 das últimas runs + 30%, entre um mínimo e um máximo;
   - **falta de memória:** um **plan** morto por falta de memória (OOMKilled) é repetido uma vez com o dobro. Um **apply** nunca é repetido sozinho: falha e sugere aumentar;
   - **manual:** o usuário pode fixar o tamanho (Small, Medium, Large ou valores próprios). É uma configuração herdada (seção 4.6), e o valor fixado desliga o ajuste automático naquele nível;
   - **`run --all`:** o `--parallelism` do Terragrunt acompanha os CPUs do runner.
1. Mapa de papéis vindo do provedor de identidade (quem entra, usuário ou administrador, quem roda e quem aprova), com aprovadores por pessoa ou grupo herdados por nível.
2. State backend HTTP por stack, com lock, versões e importação.
3. Webhooks do Gitea: plan no push e no PR, commit status.
4. Terragrunt nativo (units) e agents com labels.
5. Vault como fonte de segredos, herdado por nível.
6. OpenTelemetry (MELT) e notificações.
