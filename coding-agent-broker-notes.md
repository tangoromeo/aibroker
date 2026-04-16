ниже некая концепция сервиса, для общего понимания класса задачи, которую планируется реализовать.
## Вводная
Корпоративный сервис для разработки (coding agents) с возможностью безопасной передачи проприетарного кода в внешние сервисы. Проще говоря - брокер/оркестратор для этой задачи.
Основные фичи
1. Приоритетное использование локальных coding моделей
2. Умение самостоятельно поймать момент когда применимость локальной модели ограничена 
3. Выделение проблемных участков кода для эскалации
4. Матчинг выделенного кода по security screening правилам
5. Оптимизация взаимодействия с внешними сервисами 
6. Прием результатов от внешних сервисов и security screening
7. Использование полученных результатов в основном пайплайне

Первая фаза - реализация прозрачного прокси для протоколов ACP и MCP, который позволит проксировать подключение локальных агентов, например плагина kilo, к кодинг моделям в внутреннем контуре.
Прокси должен гарантировать корректную работу нескольких клиентов одновременно без потери состояния между запросами.
Прокси дожен содержать точки расширения, через которые будет реализовываться оркестрация дальнейших задач по списку.

проанализируй и предложи варианты архитектуры.
Язык разработки предположительно go, если против - предлагай варианты.

# Broker для coding-agent эскалации: рабочий конспект

## Вводная
Корпоративный сервис для разработки (coding agents) с возможностью безопасной передачи проприетарного кода в внешние сервисы 
Основные фичи
1. Приоритетное использование локальных coding моделей
2. Умение самостоятельно поймать момент когда применимость локальной модели ограничена 
3. Выделение проблемных участков кода для эскалации
4. Матчинг выделенного кода по security screening правилам
5. Оптимищзация взаимодействия с внешними сервисами 
6. Прием результатов от внешних сервисов и security screening
7. Использование полученных результатов в основном пайплайне



## 1. Задача

Нужен брокер, который прячет от пользователя многоконтурную схему работы coding-агента.

Для разработчика в IDE существует один агент. Внутри платформы брокер сам решает:

- достаточно ли локальной coding-модели
- нужна ли эскалация во внешний контур
- какой контекст можно вынести наружу
- как этот контекст сократить и санитизировать
- как провалидировать ответ и вернуть результат в основной пайплайн

---

## 2. Базовая архитектура

### Внешний интерфейс
IDE / plugin общается с одним внутренним endpoint.

Снаружи это единый агентный API. Внутри это не proxy, а decision point.

### Внутренние роли брокера
Брокер должен:

- принимать агентный запрос
- держать state сессии и шага
- запускать local-first execution
- принимать решение об эскалации
- оркестрировать tools
- вызывать policy layer
- формировать контекстный пакет
- вызывать screening / sanitization
- роутить в модели
- валидировать ответ
- возвращать унифицированный результат

### Важная граница
API-фасад вторичен. Главная ценность не в endpoint'ах, а в runtime-логике:

- policy-aware routing
- context shaping
- escalation decisioning
- validation and reintegration

---

## 3. Роль n8n

`n8n` подходит как оркестратор вокруг системы, но не как ядро интерактивного agent runtime.

### Где подходит
- approval workflows
- аудит
- уведомления
- интеграции с Jira / GitLab / Slack / SIEM
- фоновые и асинхронные процессы
- exception handling
- ручные эскалации

### Где не подходит
- hot path из IDE
- stateful interactive loop
- тонкий контроль tool/model шагов
- policy-heavy decision runtime
- AST-aware extraction / sanitization
- низколатентный streaming loop

### Вывод
Правильная схема:

`IDE -> broker/core runtime -> models/tools/policies`

А `n8n` используется рядом:

`broker -> n8n` для approval, аудита и вторичных workflow.

---

## 4. Что такое policy

Policy здесь не просто набор запретов. Это машина принятия решений о допустимом маршруте обработки.

Policy отвечает на вопросы:

- можно ли решить задачу локально
- можно ли эскалировать наружу
- в каком виде можно вынести контекст
- какой объём контекста допустим
- какой провайдер допустим
- какие safeguards обязательны
- нужен ли approval
- какие post-check обязательны

### Policy input
Типовой вход policy engine:

- `task_type`
- `project_classification`
- `artifact_type`
- `artifact_sensitivity`
- `contains_secrets`
- `scope_size`
- `requires_repo_wide_context`
- `local_attempt_status`
- `user_role`
- `provider_constraints`

### Policy output
Типовой выход:

- `decision = allow | deny | allow_with_constraints | approval_required`
- `route = local_only | external_summary | external_snippet | human_gate`
- `allowed_providers`
- `max_scope`
- `required_transformations`
- `required_validations`
- `reason_codes`

### Слои policy
1. **Hard rules**
   - secrets found -> deny raw external
   - auth / crypto / anti-fraud -> deny или approval
   - restricted repo -> local only

2. **Contextual rules**
   - isolated test generation -> external allowed with constraints
   - architecture/security reasoning -> usually external denied
   - small refactor in utility module -> maybe external allowed

3. **Risk scoring**
   - sensitivity score
   - IP exposure score
   - sanitization loss score
   - ambiguity score

### Практический принцип
Policy должен отвечать не на вопрос “можно ли вообще”, а на вопрос:

**какой execution path разрешён при данном типе задачи, уровне риска и форме контекста.**

---

## 5. DMN

DMN подходит для policy decision layer.

### Где уместен
DMN хорошо решает:

- допустимость эскалации
- форму внешнего контекста
- необходимость approval
- набор обязательных safeguards
- allowed route

### Где не уместен
DMN не должен делать:

- AST-анализ
- scope extraction
- secret scanning
- sanitization
- session runtime
- retries
- model routing по live метрикам
- streaming loop

### Правильное разделение
- **analyzers / preprocessors** извлекают признаки
- **DMN** принимает policy-решение
- **broker** исполняет

### Вывод
DMN уместен как declarative policy engine, но не как движок всего брокера.

---

## 6. Local-first и эскалация

### Базовый режим
По умолчанию все задачи идут в локальную coding-модель.

Внешний контур используется только при явном решении брокера.

### Важный принцип
Для пользователя не существует “локальной” и “внешней” модели.
Для него существует один агент в IDE.

Эскалация должна быть:

- скрыта в UX
- полностью аудируема внутри платформы

### Решение об эскалации
Эскалация должна включаться не потому, что “модель призналась, что не справляется”, а потому что брокер наблюдает:

- отсутствие прогресса
- деградацию траектории
- перерасход бюджета
- task mismatch

---

## 7. Циркумизация контекста

Тут ключевая задача не “найти истинно минимальный контекст”, а **сделать безопасное сокращение контекста с контролируемым риском недодать**.

Фраза “оставить только то, без чего задача неразрешима” плохая, потому что требует оракула.

Правильная постановка:

**сформировать достаточно узкий контекстный пакет, который с высокой вероятностью сохраняет решаемость задачи и не выносит лишнее наружу.**

### Типы контекста
1. целевой фрагмент  
2. структурный контекст  
3. исполнительный контекст  
4. доменный контекст  
5. репозиторный контекст  

Главная ошибка - сразу тащить пятый тип.

### Уровни контекста
Нормальная ступенчатая модель:

- `L0`: task-only
- `L1`: target snippet
- `L2`: snippet + signatures
- `L3`: local dependency envelope
- `L4`: execution envelope
- `L5`: module envelope
- `L6`: repo-wide context

Во внешний контур чаще всего допустимы только `L1-L4`.
`L6` почти всегда должен быть denied.

### Принцип расширения
Контекст не собирается одним большим комом.
Он расширяется по шагам.

- сначала минимальный пакет
- потом проверка достаточности
- если не хватает, расширение на один уровень
- если следующий уровень уже нарушает policy, эскалация запрещается

### Что такое context package
Нужен не сырой текстовый комбайн, а структурированный пакет, например:

- `task_summary`
- `task_type`
- `target_artifact`
- `selected_code`
- `related_signatures`
- `diagnostics`
- `constraints`
- `tests`
- `sanitization_notes`
- `context_level`

### Пересечение с policy
- **policy** задаёт допустимые границы
- **context shaper** наполняет их содержимым

---

## 8. Локальный движок для shaping контекста

Логичная идея: перед внешней эскалацией прогонять контекст через локальный движок.

Но не с ложной постановкой “найди истинно минимальный контекст”, а так:

- предложи наиболее узкий пакет с высокой вероятностью достаточности
- выдели критичные зависимости
- выдели полезные, но необязательные зависимости
- оцени риск потери смысла
- предложи следующий шаг расширения

### Что должен возвращать local shaper
- `core`
- `supporting`
- `discarded`
- `sufficiency_confidence`
- `missing_risk`
- `next_expansion_candidates`
- `recommended_representation`

### Смысл
Local model тут не решает задачу минимизации точно.
Она делает **эвристическую семантическую компрессию контекста**.

---

## 9. Cross-validation контекста

Один и тот же движок не должен:

- резать контекст
- сам же говорить, что всё ок
- сам же запускать внешнюю эскалацию

Нужен второй контур проверки.

### Вариант проверки
Лучше всего сочетание:

1. **Rule-based validator**
   - scope budget
   - policy constraints
   - unresolved symbols
   - наличие обязательных diagnostics/tests
   - запретные классы данных

2. **Semantic validator**
   - локальная модель как второй голос
   - проверяет, решаема ли задача по данному пакету
   - если нет, указывает минимально недостающие части

### Что возвращает validator
- `is_sufficient`
- `is_safe`
- `missing_items`
- `violations`
- `suggested_next_scope`
- `recommended_format`
- `confidence`

### Вывод
Контекст должен не просто вырезаться, а проходить через:

- bounded extraction
- local semantic shaping
- независимую валидацию достаточности и безопасности

---

## 10. Большая кодогенерация

Для массивной кодогенерации вопрос звучит не так:

“можно ли отдать задачу наружу?”

А так:

**какие части задачи можно вынести наружу и в каком представлении?**


### Что нужно 
Нужен процесс:

`task decomposition -> sensitivity tagging -> policy evaluation -> package generation`

### Уровни представления задачи
Policy для большой генерации должна работать по representation level:

- `L0`: нельзя наружу вообще
- `L1`: можно только functional spec
- `L2`: spec + public contracts
- `L3`: spec + sanitized examples
- `L4`: spec + limited existing code patterns
- `L5`: raw internal code

### Что обычно можно
- DTO / schema-driven code
- CRUD scaffolding
- serializers / deserializers
- tests
- migration boilerplate
- SDK wrappers
- UI boilerplate

### Что почти всегда нельзя
- auth / authz
- crypto
- anti-fraud
- billing / pricing
- security controls
- proprietary ranking / recommendation
- customer-specific logic
- core architecture glue

### Вывод
Для большой генерации нужен не просто RAG, а:

- analytical planner
- sensitivity partitioning
- representation control
- policy on decomposition tree

---

## 11. Признаки фейла low-tier / local модели

Главный вывод: нельзя полагаться на самоотчёт модели.
Нужно смотреть на **внешние наблюдаемые признаки**.

### Слабые признаки
Нежелательно использовать как основной сигнал:

- “модель выглядит неуверенной”
- “модель сказала, что застряла”
- “у модели плохое внутреннее распределение вариантов”

Это слабая инженерия.

### Что реально годится

#### A. Cost overrun
- `iteration_count > N`
- `tool_calls > M`
- `latency > budget`
- `token/context budget exceeded`
- `diff churn > threshold`

#### B. Trajectory degradation
- одна и та же failure signature повторяется `K` шагов
- нет улучшения objective metric `K` шагов подряд
- scope drift
- context growth without gain
- tool thrash

#### C. Contract / constraint violations
- нарушены acceptance criteria
- затронуты файлы вне разрешённого envelope
- broken invariants
- unresolved symbols after patch

#### D. Task mismatch
- класс задачи выше capability local tier
- нужен слишком широкий контекст
- требуется coordinated multi-file reasoning

### Правильная формулировка stuck state
Модель считается застрявшей, если:

**в течение K шагов нет измеримого прогресса по objective function при ненулевом расходе бюджета.**

### Типовой статус локального контура
- `progressing`
- `stalled`
- `degrading`
- `completed`
- `hard_failed`

### Вывод
Эскалация должна включаться не потому, что модель “призналась”, а когда брокер видит:

- стагнацию
- деградацию
- перерасход бюджета
- task mismatch

---

## 12. Основные требования к брокеру

### Функциональные
Брокер должен уметь:

- принимать запрос от IDE
- вести сессию и execution context
- нормализовывать task
- классифицировать задачу и риск
- исполнять local-first
- принимать escalation decision
- запускать context shaping
- вызывать tools
- вызывать policy engine
- вызывать validators
- роутить в модели
- валидировать ответ
- возвращать унифицированный результат
- логировать весь маршрут

### Нефункциональные
- низкая задержка на hot path
- stateful execution
- cancellation
- streaming
- retries
- graceful degradation to local-only
- full observability
- audit trail

### Security
- explicit policy gate
- data classification awareness
- scope minimization
- secret / DLP screening before and after external call
- provider policy enforcement
- explainable routing decisions
- auditable execution path

---

## 13. Рекомендуемая внутренняя декомпозиция брокера

- API facade
- session manager
- task normalizer
- classifier
- planning engine
- policy engine adapter
- context/scoping coordinator
- tool router
- model router
- execution engine
- validation pipeline
- response assembler
- audit/telemetry layer

---

## 14. Что фиксировать в design doc

### Минимальный набор тезисов
1. Брокер является единой точкой исполнения агентных запросов из IDE.  
2. Брокер реализует local-first execution.  
3. Внешняя эскалация всегда policy-gated.  
4. Контекст формируется как структурированный, минимизированный package.  
5. Контекст расширяется ступенчато, а не одним большим куском.  
6. Перед внешней эскалацией контекст проходит shaping и cross-validation.  
7. Большие генеративные задачи декомпозируются на внешние и внутренние подзадачи.  
8. Эскалация определяется по внешним наблюдаемым признакам стагнации, деградации и перерасхода бюджета.  
9. Пользователь видит один агент в IDE, а не два контура.  
10. `n8n` используется как окружающая orchestration layer, но не как ядро runtime.  

---

## 15. Самая короткая суть

### Policy
Policy отвечает:

- можно ли
- куда можно
- в каком виде можно
- с какими ограничениями можно

### Context shaping
Context shaping отвечает:

- какой минимизированный пакет с высокой вероятностью сохранит решаемость
- что можно отбросить
- что обязательно оставить
- как расширять контекст ступенчато

### Broker
Брокер отвечает:

- как исполнить задачу по этим правилам
- когда эскалировать
- как провалидировать ответ
- как вернуть один непрерывный UX в IDE
