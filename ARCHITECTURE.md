# ARCHITECTURE

Decisões técnicas do croupier. Preenchido incrementalmente conforme o [TODO.md](TODO.md) avança — cada seção referencia a fase que a define.

## Visão geral

_(a preencher — diagrama/descrição geral do fluxo provider → wager → wallet → ledger → outbox, ao final das Fases 1-5)_

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

_(Fase 2)_

## WagerTransaction, estados e tipos

_(Fase 3)_

## Ledger (WalletLedgerEntry)

_(Fase 3)_

## Reversões: REFUND e ROLLBACK

_(Fase 3 — documentar explicitamente a interação entre os dois)_

## Idempotência

_(Fase 5)_

## Referências pendentes (PENDING_REFERENCE)

_(Fase 5)_

## Estratégia de concorrência

_(Fase 5 — escolha entre lock pessimista, controle otimista com retry ou update condicional atômico, e justificativa)_

## Inbox / Outbox

_(Fase 4 esboço, detalhado na Fase 8)_

## Persistência (PostgreSQL)

_(Fase 6)_

## Mensageria (SQS)

_(Fase 8)_

## Autenticação e Autorização

_(Fase 9 — escolha do IdP, validação de credenciais, modelo de permissão)_

## Composição (Uber Fx) e ciclo de vida

_(Fase 10)_

## Graceful shutdown

_(Fase 10)_

## Observabilidade

_(Fase 11)_

## Limitações, interpretações e trabalho incompleto

_(Fase 13 — revisão final)_

## Instruções de teste

### Preparo do ambiente

_(Fase 13)_

### Testes de integração

_(Fase 13)_

### Simulando múltiplas instâncias

_(Fase 12)_

### Simulando falhas (kill, restart, interrupção)

_(Fase 12)_
