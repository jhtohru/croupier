# ARCHITECTURE

Decisões técnicas do croupier. Preenchido incrementalmente conforme o [TODO.md](TODO.md) avança — cada seção referencia a fase que a define.

## Visão geral

```
                         HTTP (internal/httpapi)
provider/serviço interno ───────────────┐
                                         ▼
                              internal/app (casos de uso)
                                 WalletCreator, WagerSubmitter,
                                 WalletReconciler, PendingReferenceResolver
                                         │
                    ┌────────────────────┼────────────────────┐
                    ▼                    ▼                    ▼
              internal/wallet      internal/wager        internal/outbox
              (Wallet, Ledger)   (WagerTransaction)      (Entry PENDING/
                    │                    │                PUBLISHED)
                    └──────────┬─────────┘                    │
                                ▼                              ▼
                        internal/postgres (pgx)         internal/sqs.Publisher
                        1 transação: estado +                  │
                        saldo + ledger + outbox                ▼
                                                        wallet-events.fifo
provider ──── SQS (wager-transactions.fifo) ──▶ internal/sqs.Consumer
                                                (internal/inbox dedup)
                                                        │
                                                        ▼
                                          mesmo internal/app.WagerSubmitter
                                          usado pela rota HTTP
```

Duas portas de entrada (HTTP e SQS) convergem no mesmo caso de uso (`app.WagerSubmitter.Submit`) — garantias de idempotência/isolamento por provider idênticas nos dois caminhos, por construção, não por coincidência (ver "Mensageria (SQS)" abaixo). Toda mutação de estado (wallet, ledger, wager transaction) e o registro do evento de domínio correspondente (`outbox`) commitam juntos, na mesma transação Postgres — a publicação de fato na fila (`internal/sqs.Publisher`, via `OutboxWorker`) é sempre um passo **posterior**, assíncrono, nunca parte da transação que gerou o evento (ver "Publisher + OutboxWorker" abaixo, e a entrada correspondente em "Falhas desqualificantes"). `cmd/croupier` é o único ponto do projeto que conhece tipo concreto de infraestrutura e monta esse grafo inteiro via Uber Fx — ver "Composição (Uber Fx) e ciclo de vida".

## Representação de dinheiro (Money)

`Money` é um value object imutável (`internal/money`), com dois campos privados: `currency` (código ISO 4217) e `amount` (`int64`, em unidade mínima da moeda — centavos, no caso de BRL).

**Por que `int64` em vez de uma biblioteca decimal.** O domínio trabalha com escala fixa de 2 casas decimais e apenas operações de soma, subtração, negação e comparação — nunca multiplicação ou divisão fracionária. Nesse cenário, `int64` em unidades mínimas é exato por construção (aritmética inteira, sem arredondamento), não introduz dependência externa, e é o padrão usado por processadores de pagamento reais (ex.: a API da Stripe representa valores como inteiro na menor unidade da moeda). Bibliotecas de precisão arbitrária (`shopspring/decimal`, `cockroachdb/apd`) foram consideradas e descartadas: elas não conhecem a regra "escala fixa em 2 casas" — essa validação teria que ser reimplementada por cima da biblioteca do mesmo jeito que já é feita sobre `int64`, sem ganho real dado o escopo do desafio. Dinheiro nunca passa por `float32`/`float64` em nenhum ponto do código.

**Money é simétrico quanto ao sinal.** O tipo aceita e representa valores negativos livremente — é matematicamente coerente que `Money` seja negativo (ex.: a `difference` de uma reconciliação, ou um delta de ledger). A regra "um valor de aposta não pode ser negativo" **não** é responsabilidade de `Money`; é uma regra de `WagerTransaction`, validada por tipo de operação (BET/WIN/REFUND/ROLLBACK exigem `!amount.IsNegative()`; LOSS exige `amount.Equal(Zero(currency))`). Isso mantém `Money` livre de conhecimento sobre um contexto de negócio específico, reutilizável em qualquer situação que precise representar uma quantia monetária, positiva ou não.

**Validação de moeda.** `validateCurrency` usa `golang.org/x/text/currency.ParseISO`, que valida contra a tabela real da ISO 4217 (mantida junto ao próprio Go, derivada do CLDR da Unicode) — não é só uma checagem estrutural de "3 letras maiúsculas". Isso evita manter uma lista própria de ~180 códigos e lida corretamente com códigos especiais reservados (ex.: `XTS`, `XXX`).

**Formato de string do amount.** Regex `^-?(0|[1-9]\d*)\.\d{2}$`: exige exatamente 2 casas decimais, rejeita zeros à esquerda desnecessários, e rejeita implicitamente qualquer coisa que não seja dígitos/ponto/sinal — o que já cobre `NaN`, `Infinity` e notação científica sem precisar de regra especial pra cada um. Sinal negativo é opcional.

Exceção deliberada: a string `"-0.00"` é rejeitada mesmo batendo na regex. "Zero negativo" não é um valor diferente de zero, mas seria uma segunda forma de escrever a mesma quantia — quebraria a exigência de uma representação canônica única por valor, necessária para o hash determinístico do payload usado na idempotência (`{providerId}:{externalTransactionId}` + hash canônico de campos de negócio). `"+0.00"` já é rejeitado de graça, já que a regex não aceita `+` em lugar nenhum.

**Dois construtores, dois pontos de confiança diferentes.**
- `New(currency, amountStr string) (Money, error)`: parseia uma string externa (não confiável) — HTTP, SQS, JSON. É o ponto estrito de validação.
- `FromMinorUnits(currency string, amount int64) (Money, error)`: reconstrói `Money` a partir de um dado já confiável (ex.: uma linha escaneada do Postgres via `pgx`, coluna `BIGINT`), sem parsing de string. Necessário porque os campos de `Money` são privados — nenhum outro pacote consegue montar a struct diretamente — e porque nesse caso o dado já chega como `int64`, não como string formatada.
- `Zero(currency)` é um atalho para `FromMinorUnits(currency, 0)`.

**Overflow.** `Add`, `Subtract` e `Negate` verificam overflow de `int64` explicitamente e retornam `ErrOverflow`, aproveitando o fato de que Go define (não é UB, ao contrário de C) o comportamento de wraparound em overflow de inteiro assinado — a checagem compara o resultado contra os operandos pra detectar o wraparound. `Subtract(n)` é implementado como `Add(Negate(n))`, reaproveitando a checagem de `Add`. `Negate` trata `math.MinInt64` como caso especial, já que negar esse valor estouraria o `int64`.

**Serialização JSON.** Formato de objeto, não string concatenada: `{"currency": "BRL", "amount": "25.00"}` — `amount` é string, não número JSON, pra não arriscar perda de precisão em parsers que decodificam número JSON como `float64`. A conversão `int64 → string` (`amountString`) usa `strconv.FormatInt` seguido de manipulação de string (inserir o ponto decimal, extrair o sinal) em vez de divisão/módulo — a abordagem aritmética (`amount/100`, `amount%100`) produz resultado incorreto pra valores negativos (os dois já vêm negativos, gerando `"-123.-45"` em vez de `"-123.45"`) e não teria como lidar com `math.MinInt64` sem estourar ao tentar obter o valor absoluto.

**Decisões descartadas por YAGNI.** `Money.String()` foi removido — nenhum consumidor real dependia dele, e `fmt` já imprime campos não exportados via reflection quando necessário para debug/teste. Um método `Sign()` (retornando -1/0/1) não foi criado — `IsNegative() bool` cobre a única necessidade concreta identificada até agora (validação de sinal em `WagerTransaction`).

**Erros sentinela:** `ErrInvalidAmount`, `ErrInvalidCurrency` (adjetivo + substantivo, espelham as mensagens `"invalid amount"`/`"invalid currency"`), `ErrCurrencyMismatch` (substantivo + substantivo, espelha `"currency mismatch"` — ordem diferente porque "mismatch" é substantivo, sem forma adjetiva natural equivalente), `ErrOverflow`.

## Wallet e invariantes de saldo

`Wallet` (`internal/wallet`) é o aggregate root identificado por `(playerId, currency)` — não existe um campo `currency` redundante na struct; a moeda é sempre `balance.Currency()`. Campos privados (`id`, `playerId`, `version`, `balance`, `createdAt`, `updatedAt`), expostos só por getters — a única forma de mutar o saldo é através dos métodos `Credit`/`Debit` do próprio agregado.

**O invariante de saldo não-negativo mora no agregado, não no caso de uso.** Isso é uma escolha deliberadamente diferente da que fizemos pra `Money`: lá, "não pode ser negativo" era uma regra de um contexto de negócio específico (`WagerTransaction`), porque `Money` é usado em vários contextos onde negativo é legítimo (ex.: `difference` de reconciliação). Aqui não há contexto nenhum em que uma `Wallet` válida possa ter saldo negativo — é o que define uma `Wallet` válida, sempre. Por isso a checagem fica dentro de `Credit`/`Debit`/`New`, tornando um saldo negativo estruturalmente impossível de existir em memória, em vez de depender de todo caso de uso lembrar de checar antes de mutar.

**Dois erros de saldo distintos, de propósito:**
- `ErrNegativeInitialBalance` (em `New`): saldo inicial inválido na criação da wallet — checagem estrutural de entrada.
- `ErrInsufficientBalance` (em `Debit`): o resultado de um débito específico deixaria o saldo negativo — resultado de uma operação de negócio.

Mantê-los separados (em vez de reaproveitar um erro genérico) é necessário porque a Fase 3 exige failure codes distintos pra "BET com saldo insuficiente" vs. "reversão que excede saldo" — a distinção semântica já nasce aqui, no nível da `Wallet`.

**`Credit`/`Debit` exigem `amount` estritamente positivo** (`ErrNonPositiveAmount`, rejeita zero e negativo). Dois motivos: (1) a direção do movimento é decidida por qual método é chamado, não pelo sinal do valor — aceitar negativo em `Credit` abriria uma forma de debitar sem passar pela checagem de saldo suficiente de `Debit`; (2) protege o invariante "version só incrementa quando o saldo muda" — um valor zero seria um "movimento" que não movimenta nada, e não deveria conseguir incrementar a versão.

**Version incrementa só em mutação bem-sucedida.** `Credit`/`Debit` só executam `w.version++` depois que a operação de saldo (`Add`/`Subtract`) é confirmada sem erro — qualquer falha (moeda incompatível, overflow, saldo insuficiente, amount não-positivo) retorna antes de tocar em `balance` ou `version`. `version` existe também como preparação pra a estratégia de concorrência otimista que será decidida na Fase 5.

**`FromPersistence` não revalida os dados.** Ao contrário de `New`, que é a porta de entrada para dado não confiável, `FromPersistence` assume que o dado já passou pela escrita (e, portanto, pelas constraints do schema) — reconstituir e revalidar de novo seria redundante. Essa decisão cria uma dependência explícita com a Fase 6: a tabela `wallets` precisa de `CHECK (balance >= 0)`, `CHECK (version >= 1)` e `NOT NULL` em `id`/`player_id`/`created_at`/`updated_at`, senão nenhuma camada garante esses invariantes pra dado lido de volta do banco.

## WagerTransaction, estados e tipos

`Transaction` (`internal/wager`) modela a operação externa. `Kind` (`OPENING`/`BET`/`WIN`/`LOSS`/`REFUND`/`ROLLBACK`) e `TxStatus` (`PENDING`/`PENDING_REFERENCE`/`PROCESSED`/`REJECTED`/`FAILED`) são tipos string — mesmo idiom de `net/http.MethodGet`, evita tabela de mapeamento int↔string pra (de)serializar contra o wire format.

**Máquina de estados**: `PENDING` é sempre o estado inicial. `PENDING_REFERENCE` só é alcançável a partir de `PENDING`, e não é reentrante (`MarkPendingReference()` chamado de `PENDING_REFERENCE` retorna `ErrInvalidTransition`) — uma transação que já está esperando referência não "reprocessa do zero", ela resolve direto pra um estado terminal quando a referência aparece ou o TTL estoura. Os quatro métodos `Mark*` (`MarkProcessed`, `MarkRejected`, `MarkPendingReference`, `MarkFailed`) protegem essas transições dentro do próprio agregado — mesmo princípio usado em `Wallet.Credit`/`Debit`: o tipo garante que um estado inválido é estruturalmente impossível de alcançar, em vez de depender do caso de uso lembrar de checar antes de mutar.

**Construção via `NewTransactionInput`** (struct de entrada, não parâmetros posicionais) — `NewTransaction` valida em duas camadas: (1) campos estruturais obrigatórios (`providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId` não podem ser vazios/nulos) e (2) regras específicas por `Kind` via `switch`: `BET`/`WIN`/`REFUND`/`ROLLBACK`/`OPENING` exigem amount positivo, `LOSS` exige amount exatamente zero, `BET` nunca aceita referência, `REFUND`/`ROLLBACK` sempre exigem referência.

**Validação de sinal do amount mora aqui, não em `Money`** — decisão deliberada: `Money` é um value object simétrico e livre de contexto de negócio (aceita negativo, usado também em diffs de reconciliação); "este valor não pode ser negativo" é uma regra específica de `WagerTransaction`, então vive na camada que sabe o porquê.

**`OPENING` não é rejeitada aqui** — correção importante de uma decisão inicial errada: `NewTransaction` **precisa** conseguir construir uma transação `OPENING` (é como a abertura de wallet com saldo inicial cria seu registro), então rejeitar `KindOpening` dentro do construtor tornaria essa feature impossível. A regra "rejeitar OPENING vinda de HTTP/SQS" pertence ao caso de uso de submissão externa (Fase 5), que deve recusar `kind == KindOpening` **antes** de chamar `NewTransaction` — o construtor de domínio fica genérico e utilizável por qualquer chamador legítimo, interno ou não.

**`IdempotencyKey()` e `PayloadHash()` são métodos computados, não campos armazenados** — os dois são 100% deriváveis de campos que já existem na struct (`providerID`+`externalTransactionID` pra chave; os campos de negócio pro hash), então guardá-los separadamente só criaria risco de dessincronia. `PayloadHash()` serializa uma struct fixa (ordem de campos determinística, sem precisar de lib de canonicalização) com os campos de negócio, **excluindo** `providerID`/`externalTransactionID` (eles são a própria chave de idempotência, não conteúdo a comparar) e usa SHA-256; retorna `[32]byte`, comparável direto com `==`.

**Duas referências, mesma forma**: `referenceExternalTransactionID *string` (o que o provider mandou) e `referenceTransactionID *uuid.UUID` (o ID interno resolvido, via `ResolveReference`) — ambos ponteiro, deliberadamente, porque representam o mesmo conceito de opcionalidade em dois momentos do ciclo de vida; tratar um como ponteiro e o outro com sentinela de valor zero criaria uma assimetria pior do que a repetição de padrão. `ResolveReference` é idempotente (mesmo id de novo não é erro) mas rejeita um id diferente do já resolvido (`ErrReferenceMismatch`) — essa checagem específica não tem equivalente barato de constraint de banco (uma `CHECK` não compara com o valor anterior da linha sem trigger), então fica no domínio por necessidade, não por excesso de cautela.

## Ledger (WalletLedgerEntry)

`LedgerEntry` (`internal/wallet`) é totalmente imutável: nenhum método muta o struct depois de construído — "append-only, sem edição/remoção" se garante estruturalmente (não existe API pra isso), não por convenção. Só tem `createdAt`, sem `updatedAt` — diferente de `Wallet`/`Transaction`, nunca é atualizado.

`Direction` (`DEBIT`/`CREDIT`) é tipo string, mesmo idiom de `Kind`/`TxStatus`/`FailureCode`.

**`NewLedgerEntry` valida `balanceAfter`, não calcula.** Decisão deliberada: o caso de uso (Fase 5) vai ter tanto `balanceBefore` quanto `balanceAfter` em mãos, os dois observados diretamente da `Wallet` real (`before := wallet.Balance()`, chama `Credit`/`Debit`, `after := wallet.Balance()`) — que é a fonte de verdade da aritmética de saldo. Se o construtor só recebesse `balanceBefore` e recalculasse `balanceAfter` internamente, um bug no caso de uso (capturar o snapshot da wallet errada, ou na ordem errada) passaria batido — o `LedgerEntry` produziria um resultado "consistente" internamente, mas errado em relação ao que a `Wallet` de fato fez. Receber os dois valores e validar `balanceAfter == balanceBefore ± amount` (via `Money.Add`/`Subtract`/`Equal`, reaproveitados, sem aritmética nova) é uma checagem real sobre a contabilidade do chamador, não só uma repetição da fórmula.

**Defesa em profundidade replicada**: `amount` estritamente positivo e `balanceBefore`/`balanceAfter` não-negativos são checados aqui de novo, mesmo já validados em `Wallet.Credit`/`Debit` — mesmo princípio de não confiar cegamente no chamador que já aplicamos lá.

**Unicidade `(walletId, transactionId)` não é responsabilidade deste tipo** — uma única construção em memória não tem visibilidade de outras entries já persistidas; fica pra constraint de schema na Fase 6 (`UNIQUE(wallet_id, transaction_id)`).

## Reversões: REFUND e ROLLBACK

Ambas exigem referência obrigatória (`ErrMissingReference` se ausente) e revertem uma transação que precisa estar `PROCESSED` — não dá pra reverter algo ainda `PENDING`, `REJECTED` ou `FAILED`.

`Transaction.ValidateReference(ref Transaction)` valida a compatibilidade: mesmo `providerId` (crítico — sem isso, uma referência poderia resolver contra a transação de **outro provider** só porque o `externalTransactionId` coincidiu, já que esse campo só é único dentro do escopo de um provider), `externalTransactionId` batendo com a string de referência, mesmo `playerId`/`walletId`/`roundId`, e mesmo `amount` (a comparação usa `Money.Equal`, que já cobre `currency` implicitamente). `gameId` foi deliberadamente **excluído** da checagem — o enunciado lista só "provider/player/wallet/currency/round" como dimensões obrigatórias; a suposição é que uma rodada já pertence a um único jogo, então `roundId` batendo já implica `gameId` batendo.

**Interação REFUND + ROLLBACK**: `ValidateReference` só garante que uma referência é *estruturalmente* válida (aponta pra uma transação processada e compatível) — não decide se essa é a *primeira* reversão daquele tipo sobre essa referência. "Impedir reversão duplicada do mesmo tipo sobre a mesma referência" exige consultar outras transações já persistidas (uma query — "já existe um REFUND processado apontando pra este BET?"), então essa parte da regra fica pro caso de uso na Fase 5, que tem acesso ao repositório. O modelo de domínio aqui não impede, por si só, um REFUND e um ROLLBACK apontando pra mesma referência original — a decisão de permitir ou não essa combinação (e em que ordem) é responsabilidade do caso de uso, a documentar quando a Fase 5 definir isso.

**Failure code distinto**: `FailureCode` é um tipo string próprio (mesmo idiom de `Kind`/`TxStatus`), preparado para códigos como "saldo insuficiente em BET" vs. "reversão excede saldo disponível" serem valores distintos — os códigos específicos ainda não foram enumerados, ficam pra Fase 5 quando a integração com `Wallet` definir exatamente quais falhas de negócio existem.

## Idempotência

`app.WagerSubmitter.Submit` (compartilhado entre HTTP e SQS — mesmo `SubmitWagerTransactionInput`) resolve idempotência antes de qualquer regra de negócio: constrói a transação candidata via `wager.NewTransaction` (o que já valida forma/sinal) e busca uma existente por `(providerId, externalTransactionId)`.

- **Mesma chave, mesmo conteúdo** (`PayloadHash()` bate): replay idempotente. Devolve a transação já persistida com `idempotentReplay: true` e o **saldo observado no processamento original**, não o atual — via `WalletRepository.FindLedgerEntryByTransactionID`, lendo `BalanceAfter()` daquela entrada específica do ledger, não `Wallet.Balance()` (que pode já ter mudado por outras operações). Testado explicitamente: a suíte move o saldo entre a submissão original e o replay e confirma que o valor devolvido é o antigo.
- **Mesma chave, conteúdo diferente**: `ErrIdempotencyConflict`.
- Kinds sem movimento de saldo (`LOSS`, ou qualquer transação que terminou `PENDING_REFERENCE`/`REJECTED`) não têm `LedgerEntry` — nesse caso o replay cai de volta pro saldo atual da wallet (é a melhor resposta disponível, já que não existe um snapshot histórico pra essas).
- A validação "header `Idempotency-Key` bate com `providerId:externalTransactionId` do corpo" fica na camada HTTP (Fase 7) — `Submit` deriva a chave diretamente dos campos do domínio, não recebe um header separado pra comparar.

**A checagem inicial (`FindByProviderAndExternalID` antes de processar) é check-then-act, deliberadamente não atômica com o processamento — e isso só é seguro por causa do que acontece quando duas goroutines perdem essa corrida ao mesmo tempo**: até a Fase 12, nada garantia isso de verdade. Sob concorrência real (50 submissões idênticas simultâneas, `TestWagerSubmitterConcurrentDuplicateSubmissions`), várias goroutines passam pela checagem antes de qualquer uma commitar — só uma vence a inserção em `wager_transactions` (protegida pela constraint `wager_transactions_provider_external_unique`, já existente desde a Fase 6); as outras recebiam, antes da correção, o erro cru do Postgres (`23505`) em vez de um replay. Corrigido em duas pontas: `WagerRepository.Save` traduz essa violação pra `app.ErrWagerTransactionAlreadyExists` (mesmo padrão de `wallets_player_currency_unique` → `app.ErrWalletAlreadyExists`), e `Submit` captura esse erro e tenta de novo como um replay comum contra a linha que venceu — quem perde a corrida de inserção nunca vê um erro, só o mesmo resultado que veria se tivesse chegado um instante depois.

## Referências pendentes (PENDING_REFERENCE)

`REFUND`/`ROLLBACK` resolvem a referência buscando por `(providerId, referenceExternalTransactionId)` antes de aplicar qualquer movimento:

- **Não encontrada** → `Transaction.MarkPendingReference()`, salva, devolve status `PENDING_REFERENCE` sem tocar na wallet. Evento `WagerTransactionPendingReference` publicado.
- **Encontrada, mas inválida** (`ValidateReference` reprova — campos não batem ou tipo incompatível) → `MarkRejected(FailureCodeInvalidReference)`.
- **Encontrada e válida** → `ResolveReference`, depois checagem de reversão duplicada (`WagerRepository.FindReversal`, busca uma `REFUND`/`ROLLBACK` `PROCESSED` já apontando pra essa mesma referência) — se já existe, `MarkRejected(FailureCodeDuplicateReversal)`.

**Worker de retry (`app.PendingReferenceResolver`)**: revisita periodicamente transações em `PENDING_REFERENCE` — não depende só de alguém submeter a referência que faltava. `ResolveDue` busca (via `WagerRepository.FindDuePendingReferences`) as transações cujo agendamento de retry já venceu e, pra cada uma, **reentra em `WagerSubmitter.process`** — resolver uma referência pendente é exatamente a mesma decisão de uma submissão nova, só que partindo de `PENDING_REFERENCE` em vez de `PENDING` (por isso `Transaction.MarkPendingReference()` é idempotente a partir de `PENDING_REFERENCE`: o worker chama esse caminho de novo a cada tentativa sem sucesso).

- **Ainda não encontrada**: `process` re-marca `PENDING_REFERENCE` (idempotente), mas `parkPendingReference` só reemite o evento/regrava a linha na *primeira* vez que a transação entra nesse estado — uma tentativa que não muda nada não tem o que anunciar de novo, e reemitir o mesmo evento a cada ciclo de retry só faria spam no outbox. O agendamento do próximo retry (`attempts`/`next_retry_at`, colunas `pending_reference_attempts`/`pending_reference_next_retry_at` da migration `000007`) é responsabilidade só do `PendingReferenceResolver`, não do domínio — não é invariante de `wager.Transaction`, é metadado operacional do worker.
- **Backoff exponencial**: `base * 2^attempts`, `attempts` contado à parte do agregado (não persistido em `wager.Transaction`).
- **Desistência**: `attempts >= maxAttempts` OU `now - CreatedAt() >= ttl` (o que vier primeiro) → `MarkRejected(FailureCodeReferenceNotFound)`, via o mesmo `WagerSubmitter.reject` usado pelos outros caminhos de rejeição.
- `maxAttempts`/`ttl`/`backoffBase` são parâmetros do construtor, não constantes fixas — os valores reais de produção e a cadência de chamada de `ResolveDue` (timer/ticker) ficam pra Fase 10 (`cmd/croupier`/Fx), que ainda não existe.

## Estratégia de concorrência

**Lock pessimista** (`SELECT ... FOR UPDATE`), não otimista com retry nem update condicional atômico.

`internal/postgres.WalletRepository.FindByID` emite `SELECT ... FOR UPDATE` quando chamado dentro de uma transação aberta por `TxManager.WithinTx` (detectado via `context.Context` — uma convenção de pacote: `WithinTx` guarda o `pgx.Tx` ativo no contexto, e todo método de repositório verifica se existe um antes de decidir se usa o pool direto ou a transação). Fora de `WithinTx` (leituras puras como `WalletGetter.Get`, `WalletReconciler.Reconcile`, `WalletLedgerLister.List`), nenhum lock é tomado — só as operações que efetivamente vão mutar a wallet abrem transação.

**Por que pessimista, e não otimista com retry**: a alternativa exigiria reestruturar `WagerSubmitter.process` (e potencialmente `WalletCreator.Create`) com um loop de retry em cima de código já escrito e testado contra fakes — risco desnecessário sob prazo apertado. Lock pessimista, ao contrário, é uma mudança inteiramente contida na camada de repositório mais um ajuste estrutural único: mover a leitura da wallet pra dentro da transação em `WagerSubmitter.process` (antes ela lia fora, mutava em memória, e só a escrita final acontecia dentro de `WithinTx` — não protegia nada contra corrida, já que duas goroutines podiam ler o mesmo saldo antes de qualquer uma escrever).

**Serialização é por linha, não global**: `FOR UPDATE` trava só a linha da wallet específica sendo lida — duas requisições concorrentes contra **wallets diferentes** continuam paralelas, sem nenhum lock compartilhado entre elas. Provado diretamente contra o primitivo de lock (Fase 12, `TestWalletRepositoryFindByIDLocksPerRowNotGlobally`): segura o lock da wallet A aberto de propósito e confirma que uma leitura concorrente da wallet B nunca bloqueia atrás dele — sem depender de heurística de tempo.

**Verificado contra Postgres real**, não só por leitura de código: o cenário obrigatório do desafio (wallet com 100.00 BRL recebendo duas apostas concorrentes de 80.00) foi rodado com goroutines de verdade contra um Postgres real via `docker-compose.yml` — resultado consistente em 10 execuções seguidas: uma `PROCESSED`, uma `REJECTED` (`FailureCodeInsufficientBalance`), saldo final 20.00 BRL, exatamente um `LedgerEntry`. Teste em `internal/postgres/integration_test.go` (`TestWagerSubmitterConcurrentBets`, atrás de `//go:build integration`).

**Limitação conhecida, documentada aqui**: `WalletCreator.Create` não abre transação em volta da checagem de duplicidade (`FindByPlayerAndCurrency`) — a proteção real contra duas criações concorrentes da mesma `(playerId, currency)` vem da constraint `UNIQUE (player_id, currency)` do schema, capturada em `WalletRepository.Save` (código Postgres `23505`) e traduzida pra `app.ErrWalletAlreadyExists`. Isso é suficiente pra correção (nenhuma wallet duplicada é possível), mas significa que a checagem prévia é só uma otimização de UX (erro mais específico antes de tentar o insert), não a garantia em si.

## Inbox / Outbox

Esboço de domínio (Fase 4) — mecânica real de fila/worker (SQS, publicação, agendamento) fica pra Fase 8.

**`Inbox`** (`internal/inbox`) — dedup no nível do consumer SQS, chave `(consumerName, messageId)`. Guarda `payloadHash` (detectar se o mesmo `messageId` reaparece com conteúdo diferente) e `completedAt *time.Time` (nulo = ainda não concluído; ponteiro em vez de um `bool` separado, mesmo raciocínio de evitar dois campos representando o mesmo fato). `MarkCompleted()` tem só uma transição válida e retorna `ErrAlreadyCompleted` se chamado de novo — diferente do `Outbox.MarkPublished` (ver abaixo), aqui não há um cenário documentado de múltiplas instâncias disputando a mesma mensagem (a unicidade `(consumerName, messageId)` no schema já deveria impedir isso na inserção), então uma segunda chamada é tratada como bug, não como corrida esperada.

Decisão que ficou em aberto até a Fase 8, resolvida agora: o enunciado lista "receipt" como campo do Inbox, mas não ficou claro se é o `ReceiptHandle` do SQS ou um recibo de domínio genérico. Ficou de fora do modelo de propósito — `ReceiptHandle` é dado efêmero, válido só durante a janela de visibilidade de uma entrega específica (uma redelivery chega com handle novo), não sobrevive nem faz sentido persistir; `internal/sqs.Consumer` usa o handle da entrega atual (`msg.ReceiptHandle`, do SDK) diretamente pra deletar da fila após o commit, sem passar pelo domínio `Inbox`.

**`Outbox`** (`internal/outbox`) — `Entry` com `id` (eventId estável, preservado entre republicações), `aggregateType`/`aggregateId`, `eventType`, `payload` (`[]byte`, snapshot JSON imutável — o outbox não precisa entender a estrutura interna do evento), `occurredAt`, `status` (`PENDING`/`PUBLISHED`), `retryCount`, `nextSendAt`.

Só dois estados, sem um "FAILED" permanente: diferente de `WagerTransaction` (que tem falhas de negócio legítimas e terminais), uma falha de publicação de evento é sempre problema de infraestrutura transitório — o objetivo é sempre publicar eventualmente, nunca desistir. `MarkPublished()` é **idempotente** (chamar de novo já publicado não é erro) — decisão deliberadamente diferente do `Inbox`, porque aqui existe um cenário documentado de múltiplos workers publicadores disputando a mesma entrada (cenário obrigatório de teste); tratar a segunda confirmação como erro seria punir exatamente o caso que o sistema precisa tolerar. `ScheduleRetry(next time.Time)` recebe o próximo horário já calculado pelo chamador — a fórmula de backoff (exponencial, jitter, etc.) é decisão operacional da Fase 8, não do modelo de domínio; `Entry` só registra o agendamento, não decide o algoritmo.

Nenhum dos dois modela coordenação entre múltiplas instâncias de worker (lease, `claimedUntil`, `SELECT ... FOR UPDATE SKIP LOCKED`) — ficou mesmo fora do tipo de domínio, como planejado; resolvido na Fase 8 inteiramente no repositório (`OutboxRepository.FindDueForUpdate`), sem `Entry` precisar saber que existe coordenação nenhuma — ver "Mensageria (SQS)" abaixo.

## Persistência (PostgreSQL)

Migrations em `internal/postgres/migrations`, uma tabela por migration, geridas por `golang-migrate` (pares `.up.sql`/`.down.sql`). Cinco tabelas: `wallets`, `wallet_ledger_entries`, `wager_transactions`, `inbox`, `outbox` — mapeiam 1:1 pros tipos de domínio já implementados.

**Constraints replicam os invariantes de domínio, não os substituem.** Mesmo raciocínio usado em todo o projeto (ex.: a decisão de `Wallet.FromPersistence` não revalidar, condicionada a essas constraints existirem): o domínio protege o invariante em memória, antes de qualquer escrita; o schema é o backstop contra escrita concorrente, bug, ou acesso direto ao banco.

- `CHECK (balance >= 0)`, `CHECK (version >= 1)` em `wallets`.
- `CHECK` de consistência em `wallet_ledger_entries`: `balance_after = balance_before ± amount` conforme `direction` — mesma fórmula que `wallet.NewLedgerEntry` já valida em Go, replicada no schema.
- **Imutabilidade do ledger não é só convenção da aplicação**: um trigger `BEFORE UPDATE OR DELETE` em `wallet_ledger_entries` levanta exceção pra qualquer tentativa de alterar ou apagar uma entrada já escrita. Testado de verdade (não só lido): `UPDATE`/`DELETE` são rejeitados pelo Postgres, não só evitados pelo código Go.
- `CHECK` em `wager_transactions` exigindo `provider_id`/`external_transaction_id`/`round_id`/`game_id` presentes pra todo `kind` exceto `OPENING` — replica a validação de `NewTransaction` (`ErrInvalidInput`).

**Decisão de schema pra `OPENING`**: `provider_id`/`external_transaction_id`/`round_id`/`game_id` são `NULL`-áveis (não `NOT NULL`), porque `OPENING` genuinamente não tem esses campos (ver `wager.NewOpeningTransaction`). Isso importa pra `UNIQUE (provider_id, external_transaction_id)`: em SQL padrão, cada `NULL` é tratado como distinto de qualquer outro valor, incluindo outro `NULL` — então múltiplas linhas `OPENING` com esses campos nulos coexistem sem colidir na constraint de unicidade. Verificado com duas inserções `OPENING` de teste antes de aceitar essa decisão como correta.

**O que foi verificado de verdade, não só lido no SQL**: subi um Postgres real via `docker-compose.yml`, apliquei as migrations (`up`), testei cada constraint acima com inserções que deveriam falhar e inserções que deveriam passar, reverti tudo (`down -all`) e reapliquei (`up`) pra confirmar que o ciclo completo funciona antes de considerar essa fase pronta.

**Migrations `000006`/`000007`, adicionadas depois, na revisão**: `000006` adiciona um índice em `wallet_ledger_entries.transaction_id` (o único índice existente ali é liderado por `wallet_id`, então não serve pra `FindLedgerEntryByTransactionID`, que filtra só por `transaction_id` — usado em todo replay idempotente). `000007` adiciona `pending_reference_attempts`/`pending_reference_next_retry_at` em `wager_transactions`, metadado operacional do `PendingReferenceResolver` (ver "Referências pendentes"), não invariante de domínio. Migrations já aplicadas nunca são editadas — regra seguida à risca aqui: mesmo sendo mudanças pequenas na mesma tabela de uma migration anterior, cada uma virou um arquivo novo.

## API HTTP (internal/httpapi)

**Roteamento**: `net/http` puro — `ServeMux` do Go 1.22+ já resolve método+path com wildcards (`"POST /wallets/{walletId}/reconciliation"`, `r.PathValue("walletId")`), sem justificar um framework de roteamento como dependência nova. Mesmo raciocínio já aplicado em `internal/postgres`: SQL/stdlib explícito em vez de abstração de terceiros quando a stdlib já resolve.

**Interfaces definidas pelo consumidor**: `Deps` (em `server.go`) não recebe `*app.WalletCreator` etc. diretamente — recebe interfaces locais e não-exportadas (`walletCreator`, `wagerSubmitter`, ...) com só o método que cada handler de fato chama. Cada tipo concreto de `internal/app` já satisfaz a interface correspondente sem nenhuma mudança do lado de `app` — é só um ponto de acoplamento a menos. Isso é o que permite testar cada handler com um *stub* de uma linha (`internal/httpapi/stubs_test.go`), sem precisar de repositório fake nem Postgres real, testando só forma de JSON/status HTTP/mapeamento de erro — a lógica de caso de uso já tem sua própria suíte em `internal/app`.

**Mapeamento de erro → status** (`errors.go`): três categorias explícitas, tudo mais vira `500` genérico com o erro de verdade só logado no servidor, nunca devolvido ao cliente — mesmo raciocínio de não vazar detalhe interno que já rege o que entra em log (Fase 11 adiante formaliza isso pra log; aqui é o equivalente pra resposta HTTP).
- Não encontrado (`app.ErrWalletNotFound`, `app.ErrWagerTransactionNotFound`, `app.ErrLedgerEntryNotFound`) → `404`
- Conflito (`app.ErrWalletAlreadyExists`, `app.ErrIdempotencyConflict`) → `409`
- Validação de input alcançável por JSON bem-formado mas semanticamente inválido (`wallet.ErrNegativeInitialBalance`, `wager.ErrInvalidKind`, `money.ErrInvalidCurrency`, etc. — lista fechada em `validationErrors`, não todo sentinel dos três pacotes) → `400`

**`money.Money` é reusada direto como campo de DTO**, não reimplementada — já tem `MarshalJSON`/`UnmarshalJSON` no formato `{"amount":"...","currency":"..."}` desde a Fase 1, então um `Money` malformado no corpo já vira erro de decode (`400`) antes mesmo de chegar no caso de uso.

**`Idempotency-Key`**: quando presente, é conferido contra `providerId:externalTransactionId` do corpo (`400` se não bater). Isso é só uma checagem de consistência client-facing — o mecanismo de idempotência em si já é inteiramente do corpo, via `WagerSubmitter.Submit` (Fase 5); a ausência do header não abre brecha nenhuma, só perde essa checagem extra.

**Fechado na Fase 9** (ver "Autenticação e Autorização" abaixo): `requireAuth`/`requireInternalRole` (`internal/httpapi/auth.go`) gateiam toda rota exceto `/health/*`, e `providerId` nas rotas de wagering vem de `claimsFromContext(r.Context()).ProviderID` — do token, nunca de corpo/path informado pelo cliente. `submitWagerTransactionRequest` nem tem campo `providerId` mais. Exatamente como previsto aqui: nenhum handler de negócio mudou de estrutura, só a origem do valor.

**Verificado contra Postgres real, não só com stubs**: `internal/httpapi/integration_test.go` (`//go:build integration`) sobe um `*Server` com repositórios Postgres de verdade e roda um fluxo completo por HTTP — cria wallet, submete BET, replay idempotente (confirma que não duplica saldo nem ledger), submete WIN, consulta por id interno e por `(providerId, externalTransactionId)`, lista ledger, concilia — tudo por cima da API HTTP real, não chamando `internal/app` direto. IdP real (pra fechar a lacuna acima) fica pra depois da Fase 9.

## Mensageria (SQS)

**Duas filas, dois sentidos, nomes decididos aqui (não especificados no enunciado original)**:
- `wager-transactions.fifo` (+ `wager-transactions-dlq.fifo`) — **entrada**: providers (ou o harness de teste do desafio) publicam submissões de wager transaction aqui, no mesmo formato do corpo de `POST /wagering/transactions`. `internal/sqs.Consumer` consome.
- `wallet-events.fifo` — **saída**: os eventos de domínio que já existiam desde a Fase 5 (`WagerTransactionProcessed`, `WagerTransactionRejected`, `WagerTransactionPendingReference`, `WalletBalanceChanged`) via `internal/app/events.go`, publicados pelo `OutboxWorker`.

Ambas provisionadas automaticamente por `deploy/localstack/init-queues.sh`, montado como hook `ready.d` do próprio container LocalStack (roda uma vez, no start; o healthcheck do serviço só fica "healthy" depois do script terminar). `wager-transactions.fifo` tem `RedrivePolicy` com `maxReceiveCount=5` apontando pra `wager-transactions-dlq.fifo`.

### Consumer (entrada)

`internal/sqs.Consumer.Run` faz long-poll (`ReceiveMessage`, `WaitTimeSeconds` configurável) num loop que respeita `ctx` — sai assim que `ctx` é cancelado, mas deixa uma mensagem que já começou a processar terminar antes de checar `ctx` de novo (não aborta no meio). Cada mensagem passa por `handle`:

1. Calcula o hash do payload (`sha256`), busca `(consumerName, messageId)` no `InboxRepository`.
2. **Não existe ainda** → cria a entrada (`inbox.New` + `Save`, ainda não completa) e segue pra 4.
3. **Existe, hash diferente** → erro permanente (mesmo `messageId`, conteúdo diferente — não deveria acontecer nunca; não apaga a mensagem, deixa o `maxReceiveCount` levar pra DLQ).
4. **Existe, já completa** → é exatamente o cenário obrigatório "interrompido depois do commit, antes de remover da fila": a mensagem foi redelivered porque a remoção anterior falhou ou nunca aconteceu, mas o trabalho já está feito. Não reprocessa, só confirma (deleta) e segue.
5. **Existe, não completa** (mesmo hash) → uma tentativa anterior morreu entre processar e marcar completo. Reprocessa — **isso é seguro mesmo se o processamento anterior na verdade tiver terminado**, porque `WagerSubmitter.Submit` (passo seguinte) já é idempotente por `providerId:externalTransactionId` desde a Fase 5. O `Inbox` aqui é uma camada de dedup mais barata (evita reabrir uma transação inteira quando dá pra responder só com um `SELECT`), não o mecanismo que garante correção — essa garantia é do `Submit`.
6. Chama `WagerSubmitter.Submit` com o mesmo `SubmitWagerTransactionInput` que a rota HTTP usa — os dois caminhos de ingestão têm garantias idênticas por construção, não por coincidência.
7. Sucesso → marca o `Inbox` completo, salva.

A mensagem só é removida da fila (`DeleteMessage`) depois que `handle` retorna sem erro — ou seja, depois que `Submit` já commitou no Postgres. Qualquer erro em qualquer ponto do caminho significa: não deleta, deixa o `visibility timeout` da fila expirar e redelivered — sem retry/backoff próprio na aplicação, de propósito (ver TODO.md, Fase 8: reinventar isso por cima do que o SQS já garante seria complexidade sem ganho).

### Publisher + OutboxWorker (saída)

`app.OutboxWorker.RunOnce` (chamado sob demanda por quem for orquestrar a cadência — Fase 10 — mesmo padrão do `PendingReferenceResolver.ResolveDue`) faz tudo dentro de uma `TxManager.WithinTx`:

1. `OutboxRepository.FindDueForUpdate` — `SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1` na entrada `PENDING` mais antiga já due.
2. Publica via `internal/sqs.Publisher.Publish` — envelope `{eventId, aggregateType, aggregateId, eventType, occurredAt, data}`, `MessageGroupId` = `aggregateId` (ordena eventos do mesmo agregado entre si, paraleliza entre agregados diferentes), `MessageDeduplicationId` = `eventId` (o próprio `id` da linha, nunca muda entre tentativas — é isso que dá "republicação preservando eventId": um consumidor lendo o corpo da mensagem reconhece a mesma entrega lógica mesmo depois de uma falha e reenvio).
3. Sucesso → `MarkPublished` (idempotente); falha → `ScheduleRetry` com backoff exponencial (mesma função `backoffDelay` do `PendingReferenceResolver`, reaproveitada).

**"Múltiplos publishers" e "recovery de trabalho abandonado" vêm inteiramente do lock da consulta, não de uma coluna de lease**: o `FOR UPDATE SKIP LOCKED` só existe enquanto a transação que o pegou está aberta. Dois workers concorrentes nunca pegam a mesma linha (`SKIP LOCKED` faz o segundo pular pra próxima). Se um worker morre no meio (depois de publicar, antes de commitar o `MarkPublished`), a conexão cai, o lock some, e a linha volta a ficar disponível pro próximo worker — que republica (com o mesmo `eventId`, ponto anterior). **Verificado contra Postgres real**, não só por leitura do SQL: duas goroutines disputando duas entradas `PENDING` concorrentemente nunca pegam a mesma (`TestOutboxRepositoryFindDueForUpdateSkipsLockedRows`).

### O que foi verificado de verdade

`internal/sqs/integration_test.go` (`//go:build integration`) roda contra LocalStack real, não mock: `TestConsumerConsumesRealSQSMessage` publica uma mensagem crua (como um provider faria) e confirma que o saldo da wallet muda; `TestPublisherAndOutboxWorkerOverRealSQS` cria uma wallet com saldo inicial (gera outbox de verdade), drena com `OutboxWorker` real publicando num `Publisher` real, e lê de volta da fila real conferindo `eventId`/`eventType`.

**Decisão de infraestrutura de teste que vale registrar**: os testes de integração deste pacote criam uma fila FIFO efêmera própria por execução (`CreateQueue`/`DeleteQueue` no `t.Cleanup`), em vez de reusar as filas de produção provisionadas pelo `init-queues.sh`. Motivo encontrado por observação direta, não suposição: compartilhar uma fila entre muitas execuções ao longo de uma sessão de testes deixou o LocalStack pouco confiável (mensagens novas às vezes nunca ficavam visíveis pra `ReceiveMessage`); a correção óbvia — `PurgeQueue` antes de cada execução — piorou o problema em vez de resolver, porque `PurgeQueue` é assíncrono mesmo na AWS real ("a deleção tipicamente completa em até 60 segundos", pela própria documentação da API), e um purge seguido imediatamente de um `SendMessage`+`ReceiveMessage` no mesmo processo reproduzia a falha de forma consistente. Fila efêmera dedicada não tem histórico nenhum pra purgar, e evita a classe inteira de problema.

**Redelivery contra SQS real** (não só unit test com fake) verificado na Fase 12 — `TestConsumerRedeliveryAfterCommitBeforeDelete`, ver "Instruções de teste" → "Simulando falhas". **DLQ ainda não verificada de ponta a ponta**: forçar uma mensagem a falhar as 5 tentativas (`maxReceiveCount`) e observar ela cair de fato na `wager-transactions-dlq.fifo` é o único item desta seção que ficou pra trás — exigiria fazer `Submit` falhar deterministicamente 5 vezes seguidas pra mesma mensagem (ex.: apontar temporariamente pra um Postgres fora do ar), o que não se encaixou no formato dos outros testes desta suíte sem introduzir infraestrutura só pra esse teste.

## Autenticação e Autorização

**IdP: Keycloak**, `client_credentials` — sem fluxo de usuário/senha, faz sentido pra service-to-service (providers e uso interno são serviços, não pessoas). Realm inteiro (`croupier`) provisionado via `--import-realm` de `deploy/keycloak/realm-export.json` — não um script customizado batendo na Admin API, o próprio Keycloak já resolve isso nativamente na subida do container.

**Três identidades de teste, todas com secret fixo (valor de exemplo, não segredo real)**: `provider-a`, `provider-b`, `internal-service`. Cada client de provider tem um protocol mapper `oidc-hardcoded-claim-mapper` que injeta `providerId` no token — **decisão deliberada de não depender de `azp`/`client_id`** (claims que o Keycloak já inclui por padrão em token de service account): um claim explícito, nomeado pelo próprio domínio do problema, deixa o contrato entre IdP e API auto-descritivo, sem exigir que quem lê o código do verificador saiba de antemão qual claim genérico do OIDC foi escolhido pra carregar essa informação.

**Autorização é por role de realm, não por client**: `internal-service` é uma role atribuída só ao service account do client `internal-service` — nunca aos clients de provider. Rotas de wallet (`internal/httpapi/server.go`) e o lookup interno de transação por id exigem essa role; rotas de wagering exigem só "qualquer token válido" (`requireAuth`), com `providerId` vindo do claim do token. Isso é o que faz "restringir wallet a uso interno" ser garantia de autorização (checagem de role), não coincidência de rota — um provider com token perfeitamente válido ainda toma `403` numa rota de wallet.

**Isolamento entre providers é enforcement no handler, não confiança no cliente**: `GET /providers/:providerId/wagering/transactions/:externalTransactionId` compara o `providerId` do path com o `providerId` do token — `403` se não bater. `POST /wagering/transactions` nem tem campo `providerId` no corpo (removido deliberadamente da Fase 7 pra cá) — o valor usado pra tudo (idempotência, isolamento, o registro em si) vem exclusivamente do token, então não existe um campo pra um provider mentir sobre a própria identidade.

**`internal/auth.Verifier`** usa `github.com/coreos/go-oidc/v3`: `oidc.NewProvider` faz discovery (`.well-known/openid-configuration`) e `Provider.Verifier` monta um verificador que busca e cacheia as chaves públicas (JWKS) automaticamente — nenhum código próprio de parsing de JWT ou fetch de chave. `SkipClientIDCheck: true` é necessário e correto aqui: esta API é um resource server que aceita tokens de **múltiplos** clients diferentes (`provider-a`, `provider-b`, `internal-service`, ...), então não existe um único `aud`/client id esperado pra checar contra — a identidade real vem do claim `providerId` e das roles do `realm_access`, não do `aud`.

**`internal/httpapi` só conhece uma interface mínima** (`tokenVerifier`, um método `Verify`), mesmo padrão de toda a camada HTTP — permite testar `requireAuth`/`requireInternalRole` com um stub que nunca fala com Keycloak (`internal/httpapi/auth_test.go`), reservando a verificação de assinatura/emissor/expiração de verdade pra suíte própria do `internal/auth` (`verifier_test.go`, `//go:build integration`, contra Keycloak real: token com `providerId` correto por client, secret errado rejeitado pelo próprio Keycloak, token malformado e assinatura adulterada rejeitados pelo `Verifier`).

**O que não foi testado com espera real**: expiração de token — `accessTokenLifespan` do realm é 300s, esperar isso de verdade deixaria a suíte lenta pra um ganho pequeno (o mecanismo de checagem de expiração do `go-oidc` é o mesmo caminho de código que já é exercitado pelos testes de assinatura inválida). Se algum dia isso importar mais (ex.: um bug específico na lógica de `exp`), dá pra encurtar `accessTokenLifespan` num realm de teste dedicado.

## Composição (Uber Fx) e ciclo de vida

**`cmd/croupier` é o único lugar do projeto que conhece tipo concreto de infraestrutura.** Todo outro pacote (`internal/app`, `internal/httpapi`, `internal/sqs`) só depende de interface — é aqui, e só aqui, que `*postgres.WalletRepository` vira o valor injetado onde `app.WalletRepository` é esperado, que `*auth.Verifier` vira `Deps.Auth`, etc. Migrations rodam **antes** de montar o `fx.App` (`postgres.ApplyMigrations`, síncrono, conexão `database/sql` própria, separada do `pgxpool.Pool` do resto da aplicação) — nada aceita tráfego contra um schema desatualizado. `ApplyMigrations` mora em `internal/postgres`, não em `cmd/croupier`, precisamente pra poder ser reusada por `internal/testdb` sem que este importasse `package main` (Go não permite importar `main`) — ver "Testes de integração" abaixo.

**Providers concretos vs. interface** (`cmd/croupier/providers.go`): `postgres.NewWalletRepository` retorna `*postgres.WalletRepository`, não `app.WalletRepository` — se eu desse `fx.Provide(postgres.NewWalletRepository)` direto, o Fx registraria o tipo concreto no grafo, e `app.NewWalletCreator` (que pede `app.WalletRepository` como parâmetro) nunca encontraria um valor compatível, porque o Fx casa por tipo exato, não por satisfação estrutural de interface. A correção são 5 funções pequenas (`provideWalletRepository`, `provideWagerRepository`, ...) cuja única função é declarar o tipo de retorno como a interface do `app`, não o ponteiro concreto — depois disso, `fx.Provide(app.NewWalletCreator)` funciona direto, sem precisar de `fx.Annotate`/`fx.As`.

**Workers de fundo são gente grande no grafo, não uma goroutine solta no `main`**: `PendingReferenceResolver`, `OutboxWorker` e o `sqs.Consumer` são todos registrados via `fx.Invoke` (`cmd/croupier/lifecycle.go`), cada um com seu próprio `fx.Hook` de `OnStart`/`OnStop`. `registerBackgroundLoop` é o padrão compartilhado pelos três: cria um `context.WithCancel` próprio no `OnStart`, roda a função de loop numa goroutine, e no `OnStop` cancela o contexto e **espera** (com timeout, `Config.ShutdownTimeout`) a goroutine realmente retornar antes de considerar o hook concluído — sem isso, `fx.App.Stop` retornaria antes de qualquer worker ter de fato parado, e o processo poderia morrer no meio de uma transação.

**Resolução de fila de SQS acontece uma vez, no provider, não a cada mensagem**: `resolveQueueURL` (uma chamada `GetQueueUrl`) roda dentro de `provideConsumer`/`provideOutboxPublisher`/`provideReadyChecker` — símples e correto pra 3 chamadas que só acontecem na subida do processo; não valeria a complexidade de um cache compartilhado no grafo do Fx pra isso.

## Graceful shutdown

**Ordem de parada, de fora pra dentro**: `fx.App.Stop` roda os hooks `OnStop` na ordem **inversa** de registro (padrão do próprio Fx) — como `registerHTTPServer` é invocado antes de `registerConsumer`/`registerPendingReferenceResolver`/`registerOutboxWorker` em `main.go`, os três workers de fundo param **primeiro**, e o servidor HTTP (`http.Server.Shutdown`, que já drena requisição em andamento antes de fechar) para **por último**. Isso é deliberado: não faz sentido aceitar uma requisição HTTP nova enquanto os workers que processariam o trabalho dela já pararam, mas também não queremos que uma conexão HTTP em andamento seja cortada no meio só porque um worker de fundo, sem relação nenhuma com ela, ainda está terminando.

**`fx.App.Done()` já cuida de `SIGINT`/`SIGTERM`** — não há `signal.Notify` próprio em `main.go`; é o próprio Fx que registra o handler de sinal e fecha o canal que `Done()` devolve. `main` só bloqueia nele e, quando libera, chama `fxApp.Stop(ctx)` com um timeout (`Config.ShutdownTimeout + 5s`, uma folga deliberada sobre o timeout que cada hook individual já respeita).

**Verificado de verdade, não só lido no código**: subi o binário (`go run ./cmd/croupier` e, separadamente, containerizado via `docker compose up app`), confirmei os dois healthchecks reais (`/health/live`, `/health/ready` — Postgres **e** SQS), rodei o fluxo HTTP completo (criar wallet, submeter aposta, ver saldo mudar) com tokens reais do Keycloak contra o binário de verdade, e mandei `SIGTERM` — os logs de `fx.Hook OnStop` confirmam a ordem exata descrita acima (os 3 workers primeiro, HTTP por último), e o processo encerra sem pendência.

**Uma armadilha real de rede, encontrada rodando (não hipotética)**: o Keycloak em `start-dev` resolve o `iss` de cada token dinamicamente a partir do header `Host` da requisição (sem `KC_HOSTNAME` fixo). Se `app` estivesse na rede padrão do compose falando com Keycloak via `http://keycloak:8080`, os tokens que ele valida internamente teriam um `iss` diferente do que alguém obtém via `curl` de fora (`http://localhost:8080`) — todo token real seria rejeitado por descasamento de emissor, mesmo sendo perfeitamente válido. `network_mode: host` no serviço `app` (ver `docker-compose.yml`) resolve isso trivialmente: "localhost" dentro do container passa a ser literalmente o mesmo "localhost" que Postgres, LocalStack, Keycloak e qualquer `curl` de fora já usam — exatamente a configuração já testada funcionando via `go run` direto no host, só que containerizada. Alternativa descartada: fixar `KC_HOSTNAME=keycloak` no Keycloak resolveria a consistência dentro da rede do compose, mas quebraria toda a suíte de testes de integração deste projeto (`internal/auth`, `internal/httpapi`), que roda no host e obtém token via `localhost:8080` — trocaria um problema por outro.

## Observabilidade

_(Fase 11)_

## Limitações, interpretações e trabalho incompleto

Todo item da lista "Falhas desqualificantes" no TODO.md foi revisado nesta revisão final e nenhum se aplica — ver TODO.md → "Falhas desqualificantes" pra a lista e as seções deste documento referenciadas na Fase 12 pra onde cada um foi verificado contra infraestrutura real. O que segue aqui é o que ficou deliberadamente incompleto ou foi decidido por interpretação própria (enunciado original não especificava), não desqualificante, mas honesto de registrar:

**Observabilidade (Fase 11) não foi implementada além do mínimo que já existia por consequência de outras fases.** `slog` já é usado em pontos-chave (`cmd/croupier/lifecycle.go`, `internal/sqs/consumer.go`, `internal/httpapi/errors.go`) com campos estruturados (`error`, `name`, ...), mas: (1) o handler é o default do Go (texto, não JSON); (2) não há injeção sistemática de `correlationId`/`messageId`/`transactionId`/`walletId`/`providerId` em todo log relevante, só nos poucos pontos citados; (3) não existe nenhuma métrica (contadores de outcome, duplicata, retry, DLQ, conflito de concorrência, latência de outbox, divergência de reconciliação) nem tracing OpenTelemetry nem dashboard. Cortado deliberadamente sob pressão de prazo, priorizando (conforme a legenda do próprio TODO.md) os itens `[!]` da Fase 12 — que exigiram, inclusive, uma correção de bug real de concorrência (ver "Instruções de teste" → "Testes de integração") — sobre os itens `[~]`/`[o]` desta fase.

**DLQ não verificada de ponta a ponta.** Documentado também em "Mensageria (SQS)" → "O que foi verificado de verdade": a redrive policy (`maxReceiveCount=5` → `wager-transactions-dlq.fifo`) está provisionada e testada por leitura de configuração, mas nenhum teste força de fato 5 falhas consecutivas da mesma mensagem e observa ela cair na DLQ real. Exigiria uma forma determinística de fazer `Submit` falhar repetidamente pra uma mensagem específica (ex.: apontar temporariamente pra um Postgres fora do ar) que não se encaixou no formato das suítes existentes sem introduzir infraestrutura só pra esse teste.

**Expiração de token não testada com espera real** (`internal/auth`, Fase 9): `accessTokenLifespan` do realm é 300s; o mecanismo de checagem de `exp` é o mesmo caminho de código já exercitado pelos testes de assinatura/emissor inválidos, então o ganho de esperar 5 minutos de verdade numa suíte de teste não pareceu valer o custo de tempo. Se algum dia importar mais especificamente, um realm de teste dedicado com `accessTokenLifespan` curto resolveria sem impactar as demais suítes.

**Diferenciais opcionais não implementados** (`[o]`, Fase 13): double-entry bookkeeping completo (o ledger atual já é auditável e imutável — trigger de banco bloqueia `UPDATE`/`DELETE` — mas não modela contrapartida dupla de fato, só entradas de débito/crédito por wallet) e load testing com métricas p50/p95/p99. Nenhum dos dois é necessário pra nenhum requisito obrigatório do desafio.

**Interpretações registradas ao longo do projeto** (cada uma já documentada na seção correspondente, listadas aqui só pra consolidar): nomes de fila (`wager-transactions.fifo`, `wallet-events.fifo`) e o formato do envelope de evento (`{eventId, aggregateType, aggregateId, eventType, occurredAt, data}`) não especificados no enunciado original — ver "Mensageria (SQS)"; `providerId` extraído via protocol mapper dedicado no token em vez de reaproveitar `azp`/`client_id` — ver "Autenticação e Autorização"; "restringir wallet a uso interno" resolvido via role de realm, não por lista de client id — mesma seção; recovery de outbox abandonado resolvido inteiramente pelo lock da consulta (`FOR UPDATE SKIP LOCKED`), sem coluna de lease/`claimedUntil` — ver "Mensageria (SQS)" → "Publisher + OutboxWorker".

**Refatorações cosméticas de baixa prioridade não feitas** (`[o]`, Fase 1): `money_test.go` ainda não segue o padrão de teste de tabela adotado a partir da Fase 2, e ainda repete o literal `"BRL"` em vez de uma constante local — comportamento e cobertura de teste não são afetados, só estilo.

## Instruções de teste

### Preparo do ambiente

Passos operacionais (pré-requisitos, variáveis de ambiente, `docker compose up`, migrations, filas, Keycloak) já estão no README.md — "Pré-requisitos", "Variáveis de ambiente", "Subindo o ambiente local (Docker Compose)", "Migrations", "Inicialização das filas", "Autenticação (IdP / Keycloak)". Esta seção documenta só o **porquê** por trás de decisões de teste que não cabem no README; passo a passo de comando fica lá, pra não duplicar em dois lugares e arriscar os dois divergirem.

### Testes de integração

**Cada pacote com testes de integração contra Postgres (`internal/postgres`, `internal/httpapi`, `internal/sqs`, `cmd/croupier`) tem seu próprio banco de dados descartável, recriado do zero a cada execução** — nunca o `croupier` que o serviço `app` do compose usa. Um `TestMain` dedicado por pacote (`testmain_test.go`, arquivo próprio em vez de entrar arbitrariamente num dos arquivos de teste já existentes) chama `testdb.Postgres("croupier_test_<pacote>")` (`internal/testdb`): dropa (se existir), recria, aplica todas as migrations via `postgres.ApplyMigrations` e devolve a DSN, que o `TestMain` põe em `TEST_DATABASE_URL` antes de rodar os testes do pacote (`m.Run()`). Cada pacote usa um nome de banco distinto (`croupier_test_postgres`, `croupier_test_httpapi`, `croupier_test_sqs`, `croupier_test_cmdcroupier`) porque `go test ./...` roda os binários de pacotes diferentes em paralelo por padrão — bancos com nomes distintos evitam qualquer disputa entre eles sem precisar de coordenação.

**Motivo, encontrado por um bug real, não hipotético**: antes desta mudança, todo teste de integração apontava pro mesmo banco `croupier` que o `docker compose up app` também usa. Isso causava dois problemas distintos, ambos observados de verdade nesta sessão:
1. Um `app` rodando ao mesmo tempo dos testes tem seu próprio `OutboxWorker` fazendo poll da tabela `outbox` na cadência dele — competindo de verdade por linhas com os testes de concorrência do outbox (`TestOutboxRepositoryFindDueForUpdateSkipsLockedRows`), causando falha intermitente sem nada errado no código sendo testado. A instrução anterior era "rode com `docker compose stop app`" — um requisito frágil, fácil de esquecer, que só escondia o problema em vez de eliminá-lo.
2. Sessões de teste sucessivas acumulavam entradas de outbox de sessões anteriores no mesmo banco compartilhado, exigindo um `drainOutboxBacklog` manual antes de testes como `TestOutboxRecoveryAfterAbandonedPublish` pra garantir que só as entradas daquela execução específica importassem.

Um banco novo por execução de pacote elimina os dois de raiz: não existe outro processo (nem o `app`, nem uma sessão de teste anterior) tocando o mesmo banco, então os testes podem rodar com o `app` **de pé**, sem passo manual nenhum — confirmado rodando as quatro suítes várias vezes com `docker compose up app` deliberadamente ligado, todas passando. Isso não elimina a necessidade de isolamento **entre** funções de teste do mesmo pacote (`drainOutboxBacklog` e afins continuam existindo e continuam necessários — várias `Test...` de um mesmo pacote ainda compartilham o único banco daquela execução), só o isolamento **entre execuções** de `go test` e contra o `app` de verdade.

**Por que um banco novo por execução de pacote, e não por função de teste individual** (transação por teste com rollback, ou schema novo por teste): algumas suítes (`TestOutboxRepositoryFindDueForUpdateSkipsLockedRows`, `TestConcurrentBetsAcrossMultipleAppInstances`) dependem de comportamento real de lock/transação entre goroutines concorrentes — envolver cada teste numa transação englobante quebraria exatamente esse comportamento (locks e `SKIP LOCKED` não fazem sentido dentro de uma transação que nunca commita de verdade). Recriar schema a cada função individual seria isolamento correto, mas lento demais pra valer a pena frente ao ganho.

**`TestMain` é por pacote/binário de teste, não compartilhável entre pacotes** — restrição do próprio `go test` (cada pacote com a tag `integration` compila num binário de teste próprio). Por isso a lógica de bootstrap do banco vive uma vez só, reusável, em `internal/testdb` (que importa `internal/postgres` — nunca o contrário), e cada pacote só tem um `testmain_test.go` fino chamando essa lógica com seu próprio nome de banco.

`internal/auth` não precisa de `TestMain`/banco de teste — seus testes de integração (`verifier_test.go`) só exercitam Keycloak, nunca Postgres.

### Simulando múltiplas instâncias

`TestConcurrentBetsAcrossMultipleAppInstances` (`internal/postgres`) é a versão automatizada: três `*app.WagerSubmitter` totalmente independentes (cada um com seu próprio `*pgxpool.Pool`) disputando a mesma wallet — exatamente o que três processos `cmd/croupier` separados teriam, sem nenhum estado compartilhado em processo. Pra simular de verdade com processos separados via Docker (não só em teste Go):
```sh
docker compose up -d --build --scale app=3 app
docker compose ps app   # três containers croupier-app-1/2/3, todos contra o mesmo Postgres
```
Não dá pra publicar todos na mesma porta do host (`APP_PORT` colidiria) — pra esse teste manual, deixe o compose escolher portas efêmeras (`docker compose port app <N> 8081` mostra qual) ou teste só a nível de Postgres/logs (cada instância loga seu próprio start/stop de forma independente, confirmando que não há coordenação nenhuma entre elas além do banco).

### Simulando falhas (kill, restart, interrupção)

**Reiniciar o app com trabalho pendente** — passos reproduzíveis, exatamente como verificado nesta sessão (ver TODO.md, Fase 12):
```sh
# 1. Cria uma wallet e submete um REFUND referenciando um BET que ainda não existe
INTERNAL_TOKEN=$(curl -s -X POST http://localhost:8080/realms/croupier/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=internal-service -d client_secret=internal-service-secret \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
PROVIDER_TOKEN=$(curl -s -X POST http://localhost:8080/realms/croupier/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-secret \
  | python3 -c 'import sys,json; print(json.load(sys.stdin)["access_token"])')
WALLET_ID=$(curl -s -X POST localhost:8081/wallets -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -d '{"playerId":"33333333-3333-3333-3333-333333333333","initialBalance":{"amount":"100.00","currency":"BRL"}}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])")
curl -s -X POST localhost:8081/wagering/transactions -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -d "{\"externalTransactionId\":\"refund-1\",\"playerId\":\"33333333-3333-3333-3333-333333333333\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-1\",\"gameId\":\"game-1\",\"kind\":\"REFUND\",\"amount\":{\"amount\":\"10.00\",\"currency\":\"BRL\"},\"referenceExternalTransactionId\":\"bet-1\"}"
# → status "PENDING_REFERENCE"

# 2. Mata o app
docker compose stop app

# 3. Confirma direto no Postgres que nada se perdeu
docker exec croupier-postgres-1 psql -U croupier -d croupier -c \
  "SELECT status, pending_reference_attempts FROM wager_transactions WHERE external_transaction_id = 'refund-1';"
# → ainda PENDING_REFERENCE, attempts preservado

# 4. Reinicia
docker compose up -d app

# 5. Submete o BET que faltava
curl -s -X POST localhost:8081/wagering/transactions -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -d "{\"externalTransactionId\":\"bet-1\",\"playerId\":\"33333333-3333-3333-3333-333333333333\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-1\",\"gameId\":\"game-1\",\"kind\":\"BET\",\"amount\":{\"amount\":\"10.00\",\"currency\":\"BRL\"}}"

# 6. Reenvia o mesmo BET — idempotência tem que ter sobrevivido ao restart
curl -s -X POST localhost:8081/wagering/transactions -H "Authorization: Bearer $PROVIDER_TOKEN" \
  -d "{\"externalTransactionId\":\"bet-1\",\"playerId\":\"33333333-3333-3333-3333-333333333333\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-1\",\"gameId\":\"game-1\",\"kind\":\"BET\",\"amount\":{\"amount\":\"10.00\",\"currency\":\"BRL\"}}"
# → "idempotentReplay": true, mesmo saldo de antes

# 7. Espera até 5s (intervalo padrão de poll) e confere que o REFUND resolveu sozinho
docker exec croupier-postgres-1 psql -U croupier -d croupier -c \
  "SELECT status FROM wager_transactions WHERE external_transaction_id = 'refund-1';"
# → PROCESSED, sem nenhuma chamada manual pro resolver

# 8. Reconciliação final
curl -s -X POST localhost:8081/wallets/$WALLET_ID/reconciliation -H "Authorization: Bearer $INTERNAL_TOKEN"
# → "consistent": true
```
Resultado real desta sessão: todos os 8 passos se comportaram exatamente como descrito — trabalho pendente sobrevive ao restart, o resolvedor de `PENDING_REFERENCE` do processo novo retoma sozinho sem intervenção, idempotência (checada via banco, não memória) sobrevive, saldo consistente no final.

**Interromper consumer após commit, antes de remover da fila** — `TestConsumerRedeliveryAfterCommitBeforeDelete` (`internal/sqs`) automatiza isso contra SQS real: processa a mensagem (commit incluso), força ela ficar visível de novo (`ChangeMessageVisibility` com timeout 0, em vez de esperar os 30s reais), confirma que a redelivery de verdade (uma segunda `ReceiveMessage`, não construída à mão) não reprocessa. Pra simular com o processo de verdade sendo morto no meio (não só o handler): publique uma mensagem, mate o container (`docker compose kill app`) bem depois do log de commit mas antes do log de delete (difícil de cronometrar de fora — na prática, `SIGKILL` a qualquer momento e reiniciar já basta, porque a mensagem só é deletada depois do commit, então kill a qualquer momento é seguro por construção, não só nessa janela estreita).

**Dois publishers disputando o mesmo outbox** — `TestOutboxRecoveryAfterAbandonedPublish` (`internal/sqs`) automatiza contra Postgres+SQS reais: publisher 1 publica de verdade e "crasha" (transação nunca commitada — o lock da linha, que é o que garante exclusão mútua entre publishers concorrentes, some sozinho), publisher 2 reclama a mesma linha `PENDING` e completa. Ver ARCHITECTURE.md → "Mensageria (SQS)" pra o raciocínio completo de por que isso não precisa de lease/`claimedUntil`.
