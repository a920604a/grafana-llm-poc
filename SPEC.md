# Grafana AI 助理 POC — 程式開發規格暨技術設計

| 欄位 | 值 |
|---|---|
| 狀態 | 已提案，可進入實作 |
| 範圍 | POC v1 |
| 最後檢視日期 | 2026-08-28 |
| 主要外掛 ID | `acme-aiassistant-app`（建立骨架時可替換；替換後全專案一致） |
| 主要技術 | Grafana OSS、React/TypeScript、Go、Grafana Live、Ollama、`mcp-grafana` |
| 部署模式 | Docker Compose、純地端執行 |

> 本文件是實作契約。實作者可以調整官方骨架自動產生的次要檔名，但不得改變本文的邊界、協定、安全原則與驗收條件，除非先記錄架構決策紀錄（ADR）。文件以繁體中文說明；程式識別字、API 欄位、套件名稱與業界標準名稱保留英文。

## 0. 架構決策摘要

POC v1 應建立一個由官方 `@grafana/create-plugin` 產生、附帶 Go 後端元件的 **Grafana App Plugin**。不建立獨立資料來源外掛、不建立巢狀資料來源外掛，也不建立自製 WebSocket 伺服器。

整套 POC 以 Docker Compose 純地端部署。執行期間所有 prompt、模型回應、Grafana 資料、dashboard 草稿與 MCP tool calls 都留在本機 Docker 網路；不呼叫 Grafana Cloud、OpenAI hosted API 或其他 SaaS。唯一對 host 開放的服務是綁定 `127.0.0.1:3000` 的 Grafana UI。

命令與事件採二段式流程：

1. React 以 App Plugin Resource API 建立 chat request；backend 驗證、配置 server-side request/session state，回傳 `202 Accepted`、`requestId` 與 Grafana Live channel address。
2. React 訂閱該 plugin-scoped Grafana Live channel；Grafana 呼叫 plugin 的 `SubscribeStream` / `RunStream`，backend 才開始 OpenAI-compatible streaming request，並以 `start`、`token`、`complete`、`error` event 回傳。

除一般對話外，v1 的主要成果之一是將自然語言轉換為 **Grafana dashboard 或 panel 草稿**。此功能採「產生草稿 → 前端預覽與差異檢視 → 使用者明確確認 → 後端套用」四階段流程。模型不得直接、不經確認地修改 Grafana。

`mcp-grafana` 是後端存取 Grafana dashboard、datasource 與 folder 能力的介面。它以官方 Go 安裝命令安裝到自訂 Grafana 開發映像中，並由 App Plugin 後端以 STDIO 啟動與管理；瀏覽器不直接連線 MCP：

```bash
GOBIN=/usr/local/bin go install github.com/grafana/mcp-grafana/cmd/mcp-grafana@<固定版本>
```

開發者在本機也可使用官方文件的命令：

```bash
GOBIN="$HOME/go/bin" go install github.com/grafana/mcp-grafana/cmd/mcp-grafana@latest
```

正式可重現建置不得使用浮動的 `@latest`，實作 Phase 0 必須選定並固定已測試版本。

這個 handshake 可避免把 prompt 放進 WebSocket subscribe data，也避免 HTTP 接受後、瀏覽器尚未訂閱就遺失 token。WebSocket 只承擔 subscription/event delivery；建立與取消 request 仍是 HTTP command。

### 必須注意的 Grafana 版本風險

截至 2026-08-28，Grafana 官方 repository 有一個仍開啟的 Grafana 12.4+ plugin-scoped Live subscription regression 回報。回報顯示相同 plugin 在 12.3.3 正常、12.4.3 的 `LiveChannelScope.Plugin` subscription 可能未送出。因此：

- POC baseline 固定 `GRAFANA_VERSION=12.3.3`，先完成核心驗證。
- 不使用 datasource scope 冒充 app stream，也不因此增加 nested data source。
- CI/E2E 增加可覆寫 `GRAFANA_VERSION` 的 compatibility run。
- 升級至 12.4+ 前，必須以最小 Live smoke test 驗證 plugin-scoped subscription；issue 修復後再更新 pin。
- 不將 HTTP SSE 當成 v1 fallback，因為 Definition of Done 明確要求 Grafana Live；若產品方向允許 SSE，需另立 ADR。

相關官方資料見「References」。

---

## 1. 需求理解

### 1.1 目標

建立一個可由 `docker compose up` 啟動的 Grafana OSS AI Assistant POC，驗證以下 vertical slice：

```text
Grafana App React UI
    -- HTTP command --> Go backend component
    -- internal HTTP/SSE --> OpenAI-compatible self-hosted LLM (v1: Ollama)
    -- Grafana Live events --> React UI
```

POC 的架構價值不只在「能聊天」，而在證明：

- browser 僅信任 Grafana boundary，不接觸 LLM/DB endpoint 或 credential；
- command 與 streaming event transport 分離；
- provider-specific behavior 被隔離在 backend adapter；
- 日後可更換 Docker Model Runner、vLLM、LocalAI 或 OpenAI-compatible service，而 frontend contract 不變；
- 日後可增加受控的 SQL/data tools，而不是讓模型直接持有 DB connection。

### 1.2 功能需求

Frontend 必須：

- 提供 Grafana sidebar/app page entry。
- 顯示 model label、conversation、input、Send、Cancel。
- 維護 page-lifetime local conversation state。
- 透過 Resource API 建立/取消 chat request。
- 使用 `getGrafanaLiveSrv()` 訂閱 backend 回傳的 channel address。
- 增量組合 token，顯示 loading、streaming、complete、error 與 Live connection state。
- 不出現 LLM base URL、API key 或 DB credential。
- 提供兩種清楚的操作模式：`一般對話` 與 `建立/修改 Dashboard`。
- 在 Dashboard 模式讓使用者選擇動作：建立新 dashboard、在既有 dashboard 新增 panel、修改既有 panel。
- 顯示結構化草稿預覽，而不是只顯示模型文字：標題、folder、datasource、panel 清單、panel type、query、grid 位置與可能的警告。
- 對修改既有 dashboard 顯示摘要或差異，包括新增、修改、刪除的 panel；v1 預設不允許刪除。
- 在使用者按下「確認建立」或「確認套用」前，不得觸發 Grafana 寫入工具。
- 套用成功後顯示 dashboard UID、URL 與「在 Grafana 開啟」按鈕；套用失敗時保留草稿供修正或重試。

Backend 必須：

- 只接受已通過 Grafana authentication boundary 的 Resource/Live requests。
- 驗證 payload、限制 body/message 大小、角色與 message 數量。
- 產生 server-side `requestId`；管理 `sessionId`、ownership、idempotency 與短期 request lifecycle。
- 經 `LLMProvider` 呼叫 OpenAI-compatible `/chat/completions`，consume SSE stream。
- 經 Grafana Live `StreamSender` 發送事件。
- 支援 timeout、Live disconnect cancellation 與 explicit HTTP cancellation。
- 將 terminal state 收斂成 exactly one of `complete` or `error`（transport 中斷時可能無法送達）。
- 輸出結構化 logging fields。
- 管理 `mcp-grafana` 子行程與 MCP client lifecycle，並限制可呼叫的工具白名單。
- 透過 MCP 讀取 datasource、folder、既有 dashboard/panel context。
- 要求 LLM 產出符合內部 `DashboardDraft` schema 的結構化結果，不能直接把任意模型文字交給 Grafana API。
- 對草稿執行 schema、datasource UID、query、panel type、grid、覆寫範圍與大小驗證。
- 只有獨立的 HTTP apply command 可以呼叫 MCP `update_dashboard`；chat streaming 流程不能直接寫入 Grafana。

Environment 必須：

- Compose 至少有 `grafana`、`ollama`。
- Grafana 僅暴露 host port `3000`。
- Ollama 只加入 internal Compose network，不設定 host `ports`。
- 提供可重複的 model pull/init 方法，使 README 的 fresh-start 步驟明確。
- 自訂 Grafana 開發映像需包含固定版本的 `mcp-grafana` binary；不另外公開 MCP network port。

### 1.3 非功能需求

Security：

- secrets 僅存在 environment、Grafana secure configuration 或 secret manager；不進 frontend bundle、Git 或 logs。
- backend bind request state to Grafana org/user identity；他人不能訂閱或取消 request。
- prompt、response content、API key 預設不寫 log。
- 僅允許固定的 server-configured LLM base URL/model；client 不得 override endpoint。
- MCP credential、Grafana service account token 與 MCP process environment 僅存在 server-side。
- 產生草稿與套用草稿必須分成兩個 command；LLM 無權自行發出 apply command。
- MCP client 採 exact tool allowlist；即使 `mcp-grafana` 提供其他工具，application 也不得任意轉交給模型。
- Dashboard 寫入帳號採最小權限；POC 至少限制於指定 folder，不使用全域 Admin 權限作為目標設計。

Reliability：

- request 有 pending-subscription TTL 與 total LLM timeout。
- cancellation 使用 `context.Context` 傳遞到 outbound HTTP request。
- request registry concurrency-safe，terminal cleanup 有 TTL。
- duplicate command 不得造成 duplicate inference。

Observability：

- structured logs 至少包含 `request_id`, `session_id`, `model`, `status`, `latency_ms`, `error_code`。
- 易於實作時記錄 `time_to_first_token_ms`, `total_duration_ms`, `token_event_count`。
- 使用 Grafana plugin SDK logger；不加入大型 telemetry stack。

Maintainability/testability：

- React component、transport service、stream reducer 分離。
- HTTP handler、request coordinator、LLM provider、OpenAI SSE decoder 分離。
- 使用小 interface 與 constructor injection；避免 container/framework/service locator。
- pure parsing/reducer logic 有 unit tests；critical flow 有 integration/E2E tests。

### 1.4 不在本版範圍

POC v1 明確不做：

- LangChain、LangGraph、通用 agent framework、無上限 autonomous tool loop。允許本規格定義的固定、可稽核 MCP 呼叫流程。
- PostgreSQL/MySQL connection 或 SQL generation/execution。
- vector DB、RAG、embedding、persistent chat history。
- Kafka、Redis、distributed request/session registry。
- Kubernetes、HA/multi-replica correctness。
- 自製 WebSocket server、socket protocol 或 browser-to-LLM connection。
- authentication system；沿用 Grafana authentication/session。
- production plugin signing/publishing、multi-tenant SaaS hardening。
- rich Markdown/code execution、file upload、vision、任意 function calling。僅允許後端白名單中的 Grafana MCP 操作。
- token accounting/billing、content moderation、prompt caching。
- automatic resume/replay of a partially delivered stream。

---

## 2. 假設

以下都是待驗證假設，不是既知事實：

1. POC 使用單一 Grafana organization，仍保留 org/user ownership checks。
2. 開發者可以安裝 Docker、Node LTS、npm 10+、Go 與 Mage。
3. 開發機有足夠 CPU/RAM 執行選定的小型 Ollama model；GPU 不是必要條件。
4. 預設模型暫定 `qwen2.5:3b`；實作前可依硬體改成其他已存在且 license 可接受的 model。
5. Grafana container 能將環境變數傳給其啟動的 backend plugin process；implementation spike 必須驗證 `LLM_*` 可見性，但不得 log secret value。
6. POC 每次只有低量 concurrent chats；in-memory registry 可接受。
7. App backend instance 不會在單次 request 中途被刻意 restart；restart 後 in-flight request loss 是已知 POC 限制。
8. Conversation history 由 frontend 每次完整送出；server 不持久化對話。
9. `content` 僅為 plain text；UI 可以安全地以 React text node 呈現，不使用 raw HTML。
10. OpenAI-compatible providers 對 Chat Completions SSE 的核心欄位相容，但並非完整 OpenAI API 等價；provider compatibility 需要 contract test。
11. Docker Model Runner 未放進 v1 Compose；未來可用另一個純地端 Compose profile 與 base URL/model 設定切換。
12. POC baseline 使用 Grafana OSS 12.3.3 是可以接受的暫時 pin，並會在 Live regression 修復後重新評估。
13. App plugin 在 development mode 可載入 unsigned build；production signing 不在本版。
14. 使用者 refresh 後不需要恢復 in-flight stream；UI 應明確顯示該請求已中斷。
15. 一個 chat request 只允許一個 active Live consumer；多 tab replay 不在本版。
16. 使用者願意在寫入 Grafana 前檢視並確認 dashboard/panel 草稿；POC 不提供全自動無人審核模式。
17. POC 會使用一個 server-side Grafana service account 供 `mcp-grafana` 操作；此帳號可限制在測試 folder。若 Phase 0 無法自動 provision token，README 會要求一次性手動建立。
18. `mcp-grafana` 的 `update_dashboard` 會建立或更新完整 dashboard；panel-only 修改因此必須先讀取既有 dashboard、產生完整但受限的新版草稿，再整體送回。
19. 第一版的 dashboard preview 是結構化摘要與 JSON/diff 預覽，不假設未儲存 dashboard 能直接由 Grafana 原生 UI render。
20. 「純地端」指 runtime 不需連外；第一次建置/準備仍可能需要下載 container images、Go modules、npm packages 與 Ollama model。完全 air-gapped 環境需預先匯入這些 artifacts。

---

## 3. 架構

### 3.1 元件與責任

| 元件 | 責任 | 明確禁止事項 |
|---|---|---|
| `AssistantPage` | Layout、input/message rendering、UI state | 直接 fetch Ollama、解析 SSE |
| frontend `chatApi` | Resource API command calls | 保存 secrets、決定 provider endpoint |
| frontend `liveChat` | 訂閱 Grafana Live、將 wire event 轉為 typed event | 發送 prompt command |
| frontend reducer | deterministic conversation/request state transitions | network I/O |
| `DashboardDraftPreview` | 顯示 dashboard/panel 草稿、query、警告與差異；收集確認 | 自行呼叫 MCP 或 Grafana write API |
| frontend `dashboardApi` | 建立草稿、套用草稿的 Resource API command | 接受/保存 MCP credential |
| Go Resource handlers | auth context、HTTP method/body validation、create/cancel response | 直接組 UI state |
| `ChatCoordinator` | request registry、ownership、idempotency、state machine、cancel/cleanup | provider-specific JSON parsing |
| `StreamHandler` | authorize subscribe、claim request、run one stream、send protocol events | 接受 prompt command via WebSocket |
| `LLMProvider` | provider-neutral streaming contract | Grafana Live knowledge |
| `OpenAICompatibleProvider` | outbound HTTP、SSE decode、OpenAI-compatible mapping | Grafana user/session knowledge |
| `DashboardDraftService` | 組合 MCP context、要求結構化輸出、驗證草稿、建立 diff | 未經確認套用草稿 |
| `MCPClient` | 管理 `mcp-grafana` STDIO、列出/呼叫白名單工具 | 將全部 MCP tools 暴露給 LLM |
| `mcp-grafana` 子行程 | 以官方工具存取 Grafana datasource/dashboard API | 對 browser 開 port |
| Grafana Live | authenticated WS connection、channel subscription、event delivery | command semantics、history persistence |
| Ollama | local inference and OpenAI-compatible SSE | public browser access |

### 3.2 架構圖

```text
Untrusted / user-controlled                     Trusted server-side

┌──────────────────────── Browser ────────────────────────┐
│ Grafana-authenticated App Plugin page                   │
│                                                        │
│  AssistantPage ──> chatApi/dashboardApi ─ HTTP commands ┼──────┐
│       │                                                │      │
│       ├── DashboardDraftPreview + Confirm/Apply         │      │
│       └── liveChat ── Grafana-managed WebSocket ────────┼───┐  │
└────────────────────────────────────────────────────────┘   │  │
                                                             │  │
┌──────────────────────── Grafana OSS ────────────────────────▼──▼─┐
│ Authentication / organization boundary                           │
│ Resource proxy                    Grafana Live PUB/SUB            │
│      │                                  │                         │
│      ▼                                  ▼                         │
│ ┌────────────── App backend plugin process (Go) ───────────────┐ │
│ │ ResourceHandler ──> ChatCoordinator <── StreamHandler        │ │
│ │       │                 │                 │                   │ │
│ │       └─> DashboardDraftService ──> MCPClient ──┐             │ │
│ │                 │                               │ STDIO       │ │
│ │                 └──> LLMProvider ──┤ StreamSender│            │ │
│ │                                    └─ events     ▼            │ │
│ │                                            mcp-grafana        │ │
│ └─────────────────────────────────┼─────────────────────────────┘ │
│                                      ▲            │ Grafana HTTP  │
│                                      └────────────┘ API            │
└───────────────────────────────────┼───────────────────────────────┘
                                    │ internal Compose network
                                    │ HTTP + SSE
                           ┌────────▼─────────┐
                           │ Ollama :11434    │
                           │ /v1/chat/...     │
                           └──────────────────┘

Future server-side extension (not implemented):
Chat/Application service ──> ToolProvider ──> policy-enforced DataProvider
                                             └─> read-only PostgreSQL/MySQL
```

### 3.3 邊界

- Presentation boundary：React 只依賴 typed frontend transport contracts。
- Grafana boundary：所有 browser command/stream 都經 Grafana session 與 plugin routing。
- Application boundary：`ChatCoordinator` 擁有 request lifecycle，不依賴 HTTP、SSE JSON 或 React types。
- Provider boundary：`LLMProvider` 只暴露 normalized messages/chunks/errors。
- Future tool boundary：`ToolProvider` 不等於 raw DB client；未來 DataProvider 必須內建 allowlist/policy。
- MCP boundary：`mcp-grafana` 是 server-side Grafana capability adapter；browser 與 LLM 都不能直接選擇任意 tool。Application service 決定工具、參數與呼叫時機。

### 3.4 一般對話命令流程

```text
1. User clicks Send.
2. Frontend creates opaque clientRequestId (idempotency only).
3. POST /api/plugins/<plugin-id>/resources/v1/chat
4. Backend validates authenticated identity + payload.
5. Backend creates requestId/sessionId and Pending record with TTL.
6. Backend returns 202 + Live channel address.
7. Frontend subscribes to returned address.
8. Live calls SubscribeStream; backend checks owner and pending/active state.
9. Live calls RunStream; coordinator atomically claims request exactly once.
10. Backend emits start, calls provider, emits token*, then complete or error.
```

The POST does **not** start inference immediately. It prepares an authorized, one-shot server-side command. The Live subscription activates the prepared command. This preserves HTTP-as-command while eliminating subscription race/token loss.

### 3.5 事件與資料流程

```text
LLM SSE bytes
  -> OpenAICompatibleProvider decoder
  -> normalized Chunk{Content, FinishReason, Usage?}
  -> StreamHandler protocol mapper
  -> backend.StreamSender JSON frame
  -> Grafana Live plugin-scoped channel
  -> Grafana-managed /api/live/ws
  -> frontend typed decoder/reducer
  -> assistant message text
```

No raw upstream SSE frame is exposed to the frontend.

### 3.6 請求狀態機

```text
                 pending TTL
created/pending  -----------> expired
      │ Live RunStream claim
      ▼
   streaming  ---- cancel/disconnect/timeout/provider failure ---> failed/cancelled
      │ upstream EOF with valid terminal condition
      ▼
   completed
```

Rules：

- transitions are mutex-protected/atomic and monotonic；terminal state cannot return to active。
- only `pending` can be claimed；second `RunStream` receives an `ALREADY_CONSUMED` terminal error。
- terminal records retained briefly (default 5 minutes) for idempotency/status/cancel responses, then removed。
- app `Dispose()` cancels every active request and stops cleanup goroutine/ticker。

### 3.7 Dashboard／Panel 產生與套用流程

```text
1. 使用者選擇「建立 Dashboard」或指定既有 dashboard/panel，輸入自然語言。
2. POST /v1/dashboard-drafts 建立草稿工作。
3. 後端透過 MCP 讀取白名單 context：datasource、folder、既有 dashboard。
4. 後端把受限 context 與需求送給 LLM，要求輸出 DashboardDraft JSON。
5. 後端解析並驗證草稿；以 Grafana Live 串流進度與 draft event。
6. 前端顯示摘要、panel/query 清單、警告與既有 dashboard diff。
7. 使用者可繼續以自然語言修訂草稿；每次產生新的 draft revision。
8. 使用者按下「確認建立／套用」。
9. POST /v1/dashboard-drafts/{draftId}/apply，帶目前 revision 與 confirm=true。
10. 後端重新檢查 ownership、TTL、revision、基準 dashboard version 與 policy。
11. 後端以固定 MCP tool `update_dashboard` 套用，不讓模型參與此步驟。
12. HTTP 回傳 dashboard UID/URL/version；前端提供開啟連結。
```

重要限制：LLM 只產生草稿；apply handler 才能寫入。即使模型輸出內容宣稱「已建立 dashboard」，前端也只能把它視為文字，不能視為成功狀態。

### 3.8 信任邊界與資料分類

| 資料 | 來源 | 信任處理 |
|---|---|---|
| prompt/history | browser | untrusted; validate size/count/role; do not log content |
| `clientRequestId` | browser | validate UUID/length; idempotency hint only |
| `requestId` | backend | UUID v4; still authorize ownership |
| Grafana user/org | Grafana backend context | authoritative identity for ownership |
| model | server config | return as display metadata; client cannot override in v1 |
| LLM stream | provider | untrusted text; render as text, cap total bytes |
| API key | environment/secure config | secret; never serialize to frontend/log |
| DashboardDraft | LLM | 不可信；必須 schema/policy/reference validation |
| apply confirmation | authenticated browser command | 必須綁定 owner、draft revision 與短 TTL |
| MCP tool result | `mcp-grafana`/Grafana | server-side data；仍需限制大小並避免完整內容進 log |

---

## 4. Grafana 外掛設計

### 4.1 選型決策

Use：

- **App Plugin**：yes，提供 sidebar/app page 與 integrated UX。
- **Backend Plugin capability**：yes，作為同一 App Plugin 的 Go backend component（`plugin.json.backend=true` + executable）。「Backend plugin」不是另一個獨立 UI-less plugin type；它是 app 的 backend capability。
- **Data Source Plugin**：no for v1。
- **Nested Plugin**：no for v1。

### 4.2 為何選擇帶後端的 App Plugin

Grafana 官方文件明確支援在 App Plugin 加 backend component，並建議以 `CallResource` + Go `httpadapter`/`http.ServeMux` 建立 custom endpoint。Grafana core 亦支援 `plugin` scope 的 Live stream，namespace 為 plugin ID。這正好覆蓋此 POC 的 page、server-side integration 與 streaming。

建議從官方 scaffold：

```text
npx @grafana/create-plugin@latest
  type: app
  backend part: yes
```

Scaffold 完成後，保留官方 build/config conventions；將根目錄視為 plugin project，而不是再包一層 `grafana-ai-app/`。

### 4.3 為何目前不加入資料來源／巢狀外掛

Data source plugin 的主要語意是 Grafana QueryData、Explore/dashboard query、data source instance configuration、alerting 等。這版沒有 query editor、data frame 或 Explore integration。加入 nested data source 會造成：

- 第二個 plugin ID / instance / provisioning lifecycle；
- 不必要的 QueryData frontend/backend surface；
- streaming channel namespace 與 app command ownership 更複雜；
- 容易誤把 chat workflow 建模成 metric query。

未來只有在下列需求出現時才新增 nested data source：

- DB/query provider 要直接出現在 Explore/dashboard query editor；
- 需要 Grafana data frames、query caching、alerting 或 datasource instance lifecycle；
- 要將完整 datasource 與 App 一起 distribution。

若未來 DB 僅是 assistant 的受控 tool，仍應留在 App backend，不必成為 data source。

### 4.4 串流實作選擇

Frontend：

```ts
getGrafanaLiveSrv().getStream({
  scope: LiveChannelScope.Plugin,
  namespace: PLUGIN_ID,
  path: `chat/v1/requests/${requestId}`,
});
```

Backend App instance implements：

- `backend.CallResourceHandler`
- `backend.SubscribeStreamHandler`
- `backend.RunStreamHandler`
- `backend.PublishStreamHandler`（always deny browser publish）
- `instancemgmt.Instance`/`Dispose()` as required by current scaffold SDK

`SubscribeStream` authorization：

- path must exactly match `chat/v1/requests/<uuid>`；
- request must exist；
- Grafana org/user must match owner；
- status must be subscribable；
- otherwise NotFound or PermissionDenied（對 cross-user request 優先 NotFound，避免 enumeration）。

`PublishStream` always returns permission denied because browser must not publish commands/events。

### 4.5 取捨

| 選擇 | 優點 | 成本／限制 |
|---|---|---|
| one backend-enabled App | simplest deployment and ownership | future Explore integration needs nested datasource later |
| in-memory coordinator | small, testable POC | no HA/restart recovery |
| subscription activates inference | no pre-subscription token loss | no inference until Live works; pending TTL required |
| plugin-scoped Live | semantically correct app transport | affected by current 12.4.x regression; version pin needed |
| full history per POST | stateless conversation storage | payload grows; apply limits |

---

## 5. API 契約

All resource responses use `Content-Type: application/json`. Unknown fields may be ignored for forward compatibility. Required fields must be validated. Frontend calls through Grafana runtime, never constructs an absolute Grafana host URL.

Base path：

```text
/api/plugins/acme-aiassistant-app/resources/v1
```

### 5.1 建立對話命令

```http
POST /api/plugins/acme-aiassistant-app/resources/v1/chat
Content-Type: application/json
```

Request：

```json
{
  "clientRequestId": "92e70419-0958-43f5-bb55-9cb0b82ddbb9",
  "sessionId": "optional-server-issued-session-uuid",
  "messages": [
    { "role": "user", "content": "Explain this alert." }
  ]
}
```

Rules：

- `clientRequestId`: required UUID v4, generated by frontend, used for retry idempotency per owner。
- `sessionId`: optional UUID；absent means backend creates one。Frontend must not invent a server session ID。
- `messages`: required, 1..50；last role must be `user`。
- supported role: `system`, `user`, `assistant`；maximum one system message and it must be first。
- each content: non-empty after validation, max 16 KiB UTF-8 bytes；total request body max 256 KiB。
- v1 model is server-selected；request must not accept `baseUrl`, `apiKey`, arbitrary `model`, tools, raw SQL or provider options。

Success：

```http
HTTP/1.1 202 Accepted
```

```json
{
  "requestId": "d00c3f2d-8141-4d3d-a488-cbbb2b794edd",
  "sessionId": "7d886e12-f674-4b59-8870-93aad4843207",
  "model": "qwen2.5:3b",
  "channel": {
    "scope": "plugin",
    "namespace": "acme-aiassistant-app",
    "path": "chat/v1/requests/d00c3f2d-8141-4d3d-a488-cbbb2b794edd"
  },
  "expiresAt": "2026-08-28T12:00:30Z"
}
```

Retry with same `clientRequestId`, same owner, identical normalized payload returns the original `202` response。Same key with different payload returns `409 IDEMPOTENCY_CONFLICT`。

### 5.2 取消對話命令

```http
DELETE /api/plugins/acme-aiassistant-app/resources/v1/chat/{requestId}
```

Behavior：

- owner + pending：mark cancelled；later subscription emits/returns cancelled terminal state。
- owner + streaming：call request cancel function；provider request context terminates。
- owner + terminal：idempotently return current state。
- missing/non-owner：`404`。

Response：

```json
{
  "requestId": "d00c3f2d-8141-4d3d-a488-cbbb2b794edd",
  "status": "cancelled"
}
```

Use `200` for known request and idempotent repeated cancel；do not require `204`。

### 5.3 選用健康檢查端點

```http
GET /api/plugins/acme-aiassistant-app/resources/v1/health
```

POC endpoint may return plugin configuration readiness without exposing URL/key：

```json
{
  "status": "ok",
  "provider": "openai-compatible",
  "model": "qwen2.5:3b"
}
```

It should perform a short `/models` probe only when explicitly requested; startup must not fail solely because Ollama is still warming up。

### 5.4 錯誤格式

```json
{
  "error": {
    "code": "VALIDATION_ERROR",
    "message": "messages must contain at least one user message",
    "requestId": "optional-if-created"
  }
}
```

HTTP mapping：

| HTTP 狀態 | 錯誤代碼 | 用途 |
|---|---|---|
| 400 | `VALIDATION_ERROR` | malformed JSON/invalid field |
| 401/403 | Grafana boundary / `FORBIDDEN` | unauthenticated/insufficient access |
| 404 | `REQUEST_NOT_FOUND` | unknown or non-owned request |
| 409 | `IDEMPOTENCY_CONFLICT`, `REQUEST_ALREADY_CONSUMED` | state conflict |
| 413 | `PAYLOAD_TOO_LARGE` | body/content limit |
| 429 | `TOO_MANY_REQUESTS` | per-user/global active cap |
| 500 | `INTERNAL_ERROR` | unexpected server failure |
| 503 | `LLM_UNAVAILABLE` | optional synchronous health-only failures |

Do not return raw upstream body, internal URL, stack trace or secret。

### 5.5 Grafana Live 頻道命名慣例

Logical address：

```text
scope     = plugin
namespace = acme-aiassistant-app
path      = chat/v1/requests/<requestId>
```

Resulting debug channel resembles：

```text
plugin/acme-aiassistant-app/chat/v1/requests/<requestId>
```

Grafana may add an organization/namespace prefix on the WebSocket wire；frontend/backend must use runtime/SDK address types and must not manually construct that prefix。

Why this naming：

- `plugin` scope routes to the App backend stream handlers。
- plugin ID is the natural namespace and authorization boundary。
- versioned path allows protocol evolution。
- request ID creates one lightweight ephemeral channel per generation。
- all characters comply with Grafana Live character restrictions and total length stays below 160。

Do not include `sessionId`, user ID, email, prompt, model or secret in channel path。

### 5.6 串流事件格式

Common envelope：

```ts
type ChatStreamEvent =
  | StartEvent
  | TokenEvent
  | DraftEvent
  | CompleteEvent
  | ErrorEvent;

interface BaseEvent {
  version: 1;
  requestId: string;
  sessionId: string;
  sequence: number;
  type: 'start' | 'token' | 'draft' | 'complete' | 'error';
  timestamp: string; // RFC 3339 UTC
}
```

`sequence` starts at 0 and increments by one per successfully sent event. Frontend uses it for duplicate/gap detection, not replay。

Start：

```json
{
  "version": 1,
  "requestId": "d00c3f2d-8141-4d3d-a488-cbbb2b794edd",
  "sessionId": "7d886e12-f674-4b59-8870-93aad4843207",
  "sequence": 0,
  "type": "start",
  "timestamp": "2026-08-28T12:00:01.123Z",
  "model": "qwen2.5:3b"
}
```

Token（`content` is a text delta，不保證是 tokenizer 的一個 token）：

```json
{
  "version": 1,
  "requestId": "d00c3f2d-8141-4d3d-a488-cbbb2b794edd",
  "sessionId": "7d886e12-f674-4b59-8870-93aad4843207",
  "sequence": 1,
  "type": "token",
  "timestamp": "2026-08-28T12:00:01.450Z",
  "content": "hello"
}
```

Draft（只有 dashboard/panel 草稿工作會送出；`draft` 是已由後端驗證的結構）：

```json
{
  "version": 1,
  "requestId": "d00c3f2d-8141-4d3d-a488-cbbb2b794edd",
  "sessionId": "7d886e12-f674-4b59-8870-93aad4843207",
  "sequence": 18,
  "type": "draft",
  "timestamp": "2026-08-28T12:00:03.500Z",
  "draftId": "1b1afb58-a791-471e-a015-f1a21f76cf98",
  "revision": 1,
  "kind": "dashboard",
  "summary": "建立 Kubernetes 叢集總覽，共 4 個 panels",
  "warningCount": 1
}
```

Complete：

```json
{
  "version": 1,
  "requestId": "d00c3f2d-8141-4d3d-a488-cbbb2b794edd",
  "sessionId": "7d886e12-f674-4b59-8870-93aad4843207",
  "sequence": 42,
  "type": "complete",
  "timestamp": "2026-08-28T12:00:04.120Z",
  "finishReason": "stop",
  "usage": {
    "promptTokens": 120,
    "completionTokens": 41,
    "totalTokens": 161
  }
}
```

`usage` optional，因 provider 可能不回傳。

Error：

```json
{
  "version": 1,
  "requestId": "d00c3f2d-8141-4d3d-a488-cbbb2b794edd",
  "sessionId": "7d886e12-f674-4b59-8870-93aad4843207",
  "sequence": 8,
  "type": "error",
  "timestamp": "2026-08-28T12:00:10.000Z",
  "error": {
    "code": "LLM_TIMEOUT",
    "message": "The model did not finish before the timeout.",
    "retryable": true
  }
}
```

Allowed stream error codes：

- `LLM_UNAVAILABLE`
- `LLM_TIMEOUT`
- `LLM_PROTOCOL_ERROR`
- `STREAM_LIMIT_EXCEEDED`
- `REQUEST_CANCELLED`
- `REQUEST_EXPIRED`
- `ALREADY_CONSUMED`
- `INTERNAL_ERROR`

### 5.7 前端串流行為

- Create an empty assistant message after HTTP `202` and before subscription。
- `start`: mark streaming/connected and model label。
- `token`: append delta only when sequence is expected；ignore exact duplicate sequence；gap => fail request with `STREAM_GAP` UI error and unsubscribe。
- `complete`: terminal success，unsubscribe。
- `error`: terminal failure，preserve partial content and show error beneath it，unsubscribe。
- Live observable error/complete without terminal event: preserve partial content and show `STREAM_INTERRUPTED`。
- unsubscribe on component unmount；best-effort DELETE only if request is still pending/streaming。
- `draft`: 將 `draftId`/`revision` 綁定到目前 assistant turn，另以 Resource API 取得完整草稿；不得從 token 文字解析 dashboard JSON。

### 5.8 建立 Dashboard／Panel 草稿

```http
POST /api/plugins/acme-aiassistant-app/resources/v1/dashboard-drafts
Content-Type: application/json
```

建立新 dashboard：

```json
{
  "clientRequestId": "92e70419-0958-43f5-bb55-9cb0b82ddbb9",
  "operation": "create_dashboard",
  "instruction": "建立 Kubernetes 叢集 CPU、記憶體、Pod 數量與錯誤率總覽",
  "folderUid": "ai-poc",
  "datasourceUids": ["prometheus-main"]
}
```

新增或修改 panel：

```json
{
  "clientRequestId": "92e70419-0958-43f5-bb55-9cb0b82ddbb9",
  "operation": "modify_dashboard",
  "instruction": "新增最近一小時 HTTP 5xx rate 的 time series panel",
  "dashboardUid": "service-overview",
  "targetPanelId": null,
  "datasourceUids": ["prometheus-main"]
}
```

成功回傳 `202`，格式沿用 chat command descriptor，另加 `jobKind: "dashboard_draft"`。生成進度與摘要走 Grafana Live。

規則：

- `instruction` 最大 8 KiB；不得包含 credential。
- `operation` 只允許 `create_dashboard`、`modify_dashboard`。
- 修改時 `dashboardUid` 必填；如指定 `targetPanelId`，只能修改該 panel。
- datasource/folder 必須由 MCP 讀取並驗證存在，不能只相信 client UID。
- v1 草稿最多 12 panels、每個 panel 最多 4 targets；預設不允許刪除既有 panel、library panel、alert rule 或 dashboard permission。

### 5.9 取得草稿

```http
GET /api/plugins/acme-aiassistant-app/resources/v1/dashboard-drafts/{draftId}
```

回傳後端驗證後的資料：

```json
{
  "draftId": "1b1afb58-a791-471e-a015-f1a21f76cf98",
  "revision": 1,
  "operation": "create_dashboard",
  "title": "Kubernetes Cluster Overview",
  "folderUid": "ai-poc",
  "dashboard": { "title": "...", "panels": [] },
  "summary": {
    "addedPanels": 4,
    "modifiedPanels": 0,
    "removedPanels": 0
  },
  "warnings": [
    { "code": "QUERY_NOT_EXECUTED", "message": "此 POC 尚未自動執行查詢驗證。" }
  ],
  "expiresAt": "2026-08-28T12:10:00Z"
}
```

只有 owner 可以讀取。Response 大小需設上限；不回傳 MCP credential 或未裁切的 tool transcript。

### 5.10 套用草稿

```http
POST /api/plugins/acme-aiassistant-app/resources/v1/dashboard-drafts/{draftId}/apply
Content-Type: application/json
```

```json
{
  "revision": 1,
  "confirm": true
}
```

後端必須重新驗證：owner、未過期、revision、尚未套用、目標 dashboard version 未改變、folder/datasource policy。成功後僅由 application code 呼叫 MCP `update_dashboard`：

```json
{
  "draftId": "1b1afb58-a791-471e-a015-f1a21f76cf98",
  "status": "applied",
  "dashboardUid": "k8s-cluster-overview",
  "dashboardUrl": "/d/k8s-cluster-overview/kubernetes-cluster-overview",
  "version": 1
}
```

同一 `draftId`/`revision` 的重複 apply 必須 idempotent。既有 dashboard version 不一致回傳 `409 DASHBOARD_CHANGED`，要求重新產生 diff，不得直接 overwrite。

---

## 6. LLM Provider 設計

### 6.1 領域型別與介面

Keep the interface consumer-driven and small：

```go
package llm

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role
	Content string
}

type ChatRequest struct {
	Model    string
	Messages []Message
}

type Chunk struct {
	Content      string
	FinishReason string
	Usage        *Usage
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type ChunkHandler func(Chunk) error

type Provider interface {
	ChatStream(ctx context.Context, req ChatRequest, onChunk ChunkHandler) error
}
```

Design notes：

- callback keeps backpressure/error propagation simple；no goroutine/channel required in provider。
- provider must call callback synchronously in stream order。
- provider returns only after upstream complete, callback failure or context cancellation。
- empty content with finish/usage metadata is valid。
- do not expose `http.Response`, SSE frame, OpenAI SDK type or Ollama type。
- interface does not include tools/embeddings/models because v1 does not need them。

### 6.2 `OpenAICompatibleProvider`

Implementation uses Go standard `net/http` and a small internal SSE decoder；do not require official OpenAI SDK unless the implementation spike proves it materially reduces code without hiding cancellation/stream parsing。

Outbound request：

```http
POST {LLM_BASE_URL}/chat/completions
Authorization: Bearer {LLM_API_KEY}   # omit if empty
Content-Type: application/json
Accept: text/event-stream
```

```json
{
  "model": "qwen2.5:3b",
  "messages": [{"role":"user","content":"..."}],
  "stream": true,
  "stream_options": {"include_usage": true}
}
```

Compatibility rules：

- normalize `LLM_BASE_URL` by removing one trailing slash；append `/chat/completions` exactly once。
- accept SSE `data: {json}` frames and terminal `data: [DONE]`。
- extract all `choices[*].delta.content` conservatively；v1 expects one choice but parser must not panic on empty arrays。
- non-2xx: read a capped response body (max 32 KiB), map status, do not return raw body to UI。
- scanner/frame buffer must be explicitly sized (e.g. max 1 MiB frame)；default `bufio.Scanner` limit is insufficient as an implicit contract。
- total emitted content cap default 2 MiB；cancel and return `ErrStreamLimitExceeded` after cap。
- reject invalid content type only when it prevents parsing；some compatible servers may omit exact MIME parameters。
- outbound HTTP transport reused across requests；set connection/TLS/header timeouts plus request context total timeout。

### 6.3 設定

Required/optional environment variables：

| 環境變數 | 必填 | 預設值 | 說明 |
|---|---:|---|---|
| `LLM_BASE_URL` | yes | `http://ollama:11434/v1` in Compose | server-side only |
| `LLM_MODEL` | yes | `qwen2.5:3b` | server-selected |
| `LLM_API_KEY` | no for Ollama | empty | secret; Docker Model Runner may ignore it |
| `LLM_REQUEST_TIMEOUT` | no | `120s` | total generation deadline |
| `LLM_CONNECT_TIMEOUT` | no | `5s` | TCP/connect budget |
| `CHAT_PENDING_TTL` | no | `30s` | time to subscribe after POST |
| `CHAT_TERMINAL_TTL` | no | `5m` | idempotency/status retention |
| `CHAT_MAX_ACTIVE_PER_USER` | no | `2` | simple abuse/resource control |
| `CHAT_MAX_ACTIVE_GLOBAL` | no | `16` | POC protection |
| `CHAT_MAX_RESPONSE_BYTES` | no | `2097152` | normalized text cap |

Fail fast in `NewApp` for missing/invalid base URL/model/duration；do not make network availability a startup prerequisite。

Production evolution：move configurable secrets to App Plugin secure JSON settings when admin-configurable multi-environment deployment is required。The provider constructor still receives a typed `Config`。

### 6.4 切換 Provider

No frontend change is permitted。Only configuration and, if compatibility differs, provider adapter change：

| Provider | Example base URL | Model example |
|---|---|---|
| Ollama | `http://ollama:11434/v1` | `qwen2.5:3b` |
| Docker Model Runner | `http://model-runner.docker.internal:12434/engines/v1` | `ai/qwen2.5-coder` |
| vLLM / LocalAI | deployment-specific `/v1` | provider-specific |
| 其他地端 OpenAI-compatible service | 地端 deployment 的 `/v1` | provider-specific |

Docker Model Runner may need Compose `extra_hosts: model-runner.docker.internal:host-gateway` on Docker Engine；that belongs to a future profile/README, not v1 default。

### 6.5 錯誤正規化

Provider package exposes typed/sentinel categories：

- `ErrUnavailable` for connect refused, DNS, 502/503/504。
- `ErrUnauthorized` for 401/403（UI still gets generic configuration error）。
- `ErrRateLimited` for 429。
- `ErrProtocol` for malformed/truncated incompatible SSE。
- `ErrResponseTooLarge`。
- `context.DeadlineExceeded` and `context.Canceled` preserved for coordinator mapping。

Do not string-match errors in HTTP/stream handlers；use `errors.Is/As`。

### 6.6 未來 Tool／Data Provider 擴充點

Do not implement these interfaces now。A future ADR may introduce a small application-facing interface such as：

```go
type ToolProvider interface {
	Definitions(ctx context.Context, principal Principal) []ToolDefinition
	Invoke(ctx context.Context, principal Principal, call ToolCall) (ToolResult, error)
}
```

DB implementation must sit behind a policy-enforcing DataProvider and must guarantee：

- server-side credentials and per-environment connection config；
- read-only DB account；
- operation allowlist, not merely string prefix checking；
- parsed single `SELECT` statement only；no multi-statement, DDL/DML, dangerous functions or unrestricted schemas；
- parameterized values where applicable；
- timeout, row/byte limit, cancellation；
- audit log with user/request/tool/query fingerprint, not secrets；
- human/application policy decides execution；LLM output alone never authorizes SQL。

---

## 7. Grafana MCP 設計

### 7.1 角色與邊界

`mcp-grafana` 用來取代本專案自行維護大量 Grafana HTTP API client。POC 僅使用它完成：

- 列出與解析 datasource。
- 搜尋／讀取既有 dashboard。
- 建立或更新 dashboard。
- 視需要產生 Grafana deeplink。

MCP 不是 agent framework。Application service 仍掌握控制流程；模型不取得一份「所有 tools 清單」後自行無限迴圈呼叫。允許的 v1 工具白名單：

```text
list_datasources
get_datasource
search_dashboards
get_dashboard_by_uid
update_dashboard        # 僅 apply handler 可呼叫
generate_deeplink       # 選用，亦可由已知 UID 建相對 URL
```

若實際固定版本的工具名稱不同，Phase 0 需以 `tools/list` 產物修正本表與 contract tests，不得猜測工具名稱。

### 7.2 安裝方式

官方原始碼安裝命令為：

```bash
GOBIN="$HOME/go/bin" go install github.com/grafana/mcp-grafana/cmd/mcp-grafana@latest
```

POC 的 Docker 建置採 multi-stage Dockerfile，以相同 `go install` 方式將 binary 放入 Grafana 開發映像；版本固定：

```dockerfile
ARG MCP_GRAFANA_VERSION=<phase-0-selected-tag>
RUN GOBIN=/out go install github.com/grafana/mcp-grafana/cmd/mcp-grafana@${MCP_GRAFANA_VERSION}
```

不得在每次 container 啟動時下載或 compile，也不得在可重現建置中使用 `@latest`。

### 7.3 Transport 與 lifecycle

第一版使用預設 **STDIO transport**：

```text
Grafana App backend process
  └─ child process: mcp-grafana -t stdio
       └─ HTTP -> http://grafana:3000
```

理由：

- 不增加第三個公開 port 或另一個 HTTP trust boundary。
- 符合「簡單使用 go install binary」的目標。
- STDIO 是官方預設 local transport。
- caller authentication 與 TLS 對同一 container 內的 parent/child 不必要。

App instance 建立時啟動一個持久 MCP 子行程，不得每個 tool call 都重新啟動。`Dispose()` 必須關閉 stdin、cancel context、等待後再強制終止逾時子行程。子行程意外退出時：目前 request 回傳 `MCP_UNAVAILABLE`；可做一次受控重啟，但不建立複雜 supervisor。

不得將 MCP stdout 當 log；stdout 是 JSON-RPC protocol。stderr 經 plugin logger 收斂，且不得包含 token。

### 7.4 認證與授權

`mcp-grafana` 連回 Grafana 所用設定：

```text
GRAFANA_URL=http://127.0.0.1:3000
GRAFANA_SERVICE_ACCOUNT_TOKEN=<server-side secret>
GRAFANA_ORG_ID=1                    # POC 可固定；仍需記錄限制
```

官方已將 `GRAFANA_API_KEY` 標示為 deprecated，本專案不得使用。若同 container 內 `127.0.0.1:3000` 無法在啟動時連線，使用 `http://grafana:3000` 並由 readiness/retry 處理。

Service account 最低權限目標：

- datasource context：`datasources:read`，範圍限允許的 datasource。
- dashboard read：`dashboards:read`。
- create/update：`dashboards:create`、`dashboards:write`。
- folder scope：只允許 POC folder，例如 `folders:uid:ai-poc`。

POC 不以 Admin 作為最終設定。若開發期暫用 Editor，README 必須標示其權限較寬且僅限本機。

注意：MCP 使用 service account 時，Grafana 寫入稽核上的執行者是 service account，不是瀏覽器使用者。因此 App backend 仍必須在自己的 audit log 記錄發出確認的 Grafana user/org、`draft_id`、revision 與結果。未來若要完整 per-user authorization/attribution，需另立 ADR 評估 on-behalf-of credential 或受信 gateway。

### 7.5 工具呼叫政策

- `MCPClient` 在啟動時呼叫 `tools/list` 並確認必要工具存在；缺少 write tool 不阻止一般 chat，但 UI 將 apply 標成 unavailable。
- Application code 以常數白名單選 tool；client request 與 LLM output 不能提供 tool name。
- 產生草稿階段只允許 read tools。
- `update_dashboard` 只存在 `ApplyDraft` code path，且參數來自 server-stored、重新驗證的草稿。
- 不啟用／不呼叫 raw `grafana_api_request`、alerting、incident、admin 或 raw-SQL query tools。
- 不將 MCP service account token傳給 LLM context。
- tool result 需設 byte/depth/time limits；超限回傳 typed error。

`mcp-grafana --disable-write` 會移除 `update_dashboard`。因本 POC 確實需要經確認後寫入，所以 process 不能全域使用該旗標；安全性由 service account scope、application exact allowlist 與 apply confirmation 三層共同提供。

### 7.6 DashboardDraft 領域模型

內部草稿至少包含：

```go
type DashboardDraft struct {
	ID                   string
	Revision             int
	Owner                Principal
	Operation            DraftOperation
	FolderUID            string
	TargetDashboardUID   string
	BaseDashboardVersion int
	Dashboard            json.RawMessage
	Summary              DraftSummary
	Warnings             []DraftWarning
	CreatedAt             time.Time
	ExpiresAt             time.Time
	AppliedAt             *time.Time
}
```

草稿 TTL 預設 10 分鐘，存在 concurrency-safe in-memory registry。LLM 產物不得直接填入 ownership、ID、revision、base version、timestamps；這些欄位全由 server 建立。

草稿驗證至少包含：

- JSON 可解析，且只有單一 dashboard object。
- title 非空；UID 由 server/Grafana 決定，新建時不接受模型指定任意 UID。
- folder 在 allowlist。
- panel ID 唯一；grid position 為非負、寬度 1..24、高度有合理上限且不重疊或可正規化。
- panel type 位於 POC allowlist，例如 `timeseries`, `stat`, `gauge`, `table`, `logs`。
- datasource UID 必須存在且在 request/政策允許集合。
- query target 數量、字串長度與 dashboard 總大小受限。
- 修改既有 dashboard 時保留未知頂層欄位、variables、annotations、permissions-related metadata；v1 不允許刪除 panel。
- 對 `targetPanelId` 修改，其他 panels 必須深度相等（忽略允許由 Grafana 改寫的 version/id 欄位）。

### 7.7 LLM 產生策略

不要實作通用 agent loop。固定流程：

1. Backend 依 operation 決定要呼叫哪些 MCP read tools。
2. 將裁切後的 datasource schema、現有 dashboard 與使用者 instruction 組成 prompt。
3. 呼叫 `LLMProvider`，要求只輸出符合版本化 schema 的 JSON。
4. 累積 JSON 至上限後 parse/validate。
5. 驗證失敗可進行最多一次 repair request，帶結構化 validation errors；仍失敗即回 `DRAFT_VALIDATION_FAILED`。
6. 儲存通過驗證的 draft，發送 `draft` event。

這是一個固定最多兩次 LLM 呼叫的 workflow，不是 autonomous agent。

---

## 8. 前端設計

### 8.1 頁面資訊架構

單一 `AssistantPage`，但明確分為模式選擇、對話內容與操作區：

```text
┌──────────────────────────────────────────────────────────┐
│ AI 助理                       模型：qwen2.5:3b  ● 已連線 │
├──────────────────────────────────────────────────────────┤
│ 模式：[一般對話] [建立／修改 Dashboard]                 │
│                                                          │
│ User: 建立 Kubernetes CPU 與記憶體總覽                   │
│ AI: 正在讀取 Prometheus datasource...                    │
│ AI: 已產生草稿                                            │
│ ┌──────────── Dashboard 草稿 ──────────────────────────┐ │
│ │ Kubernetes Cluster Overview                           │ │
│ │ Folder: AI POC  Datasource: Prometheus                │ │
│ │ + CPU Usage      timeseries   sum(rate(...))          │ │
│ │ + Memory Usage   timeseries   ...                     │ │
│ │ ⚠ 查詢尚未實際執行驗證                                │ │
│ │ [檢視 JSON] [要求修改] [確認建立]                     │ │
│ └───────────────────────────────────────────────────────┘ │
│                                                          │
│ [輸入需求                                      ] [送出]  │
└──────────────────────────────────────────────────────────┘
```

### 8.2 前端狀態模型

頂層狀態至少包含：

```ts
interface AssistantState {
  mode: 'chat' | 'dashboard';
  messages: ConversationMessage[];
  activeRequest?: ActiveRequest;
  draft?: DashboardDraftView;
  connection: 'idle' | 'connecting' | 'streaming' | 'disconnected' | 'error';
  apply: 'idle' | 'confirming' | 'applying' | 'applied' | 'error';
}
```

`ConversationMessage` 與 `DashboardDraftView` 分開；不可把未驗證 JSON 塞入 assistant markdown 再解析。Reducer 事件至少含：

```text
SEND_REQUESTED
COMMAND_ACCEPTED
STREAM_STARTED
TOKEN_RECEIVED
DRAFT_RECEIVED
STREAM_COMPLETED
STREAM_FAILED
APPLY_REQUESTED
APPLY_SUCCEEDED
APPLY_FAILED
CANCELLED
```

### 8.3 Dashboard 操作體驗

- 建立 dashboard：選 folder、datasource，再輸入自然語言；folder/datasource options 由 server API 回傳，不由 browser 直接呼叫 MCP。
- 新增 panel：先搜尋/選 dashboard，預設 targetPanelId 為空，代表新增。
- 修改 panel：選 dashboard 後列出 panels，再指定一個 panel；v1 不支援一次任意重寫多個既有 panels。
- Preview 預設顯示人可讀摘要；JSON 與 diff 放在可展開區，避免要求一般使用者閱讀完整 dashboard JSON。
- Apply 必須開啟確認對話框，列出目標 folder/dashboard、panel 變更數、警告與「此操作會修改 Grafana」提示。
- 有 blocking validation error 時禁用 apply；warning 可由使用者確認後繼續。
- Apply 成功後清楚顯示 server 回傳結果，不用模型文字推測成功。
- Apply 中按鈕 disabled，重複 click 不產生第二次寫入。

### 8.4 前端邊界

前端只連：

```text
/api/plugins/<plugin-id>/resources/...
Grafana runtime 管理的 /api/live/ws
```

前端不得知道或持有：

```text
http://ollama:11434
mcp-grafana command/path
Grafana service account token
LLM API key
任意 DB credential
```

所有 MCP list/search/get/update 都由 Go backend 執行。這與 Grafana 官方提供可在 React 使用 MCP client 的方案不同，是本 POC 主動採取的較嚴格 server-side boundary，以符合 credential 與寫入控制原則。

### 8.5 元件測試重點

- 模式切換不遺失已完成對話，但 active request 時不可切換造成 orphan request。
- draft event 只觸發 server fetch，不直接信任 event 中的完整 payload。
- diff 正確標示 added/modified/removed；removed 在 v1 是 blocking error。
- confirm dialog 關閉不呼叫 apply。
- revision mismatch/409 顯示「Dashboard 已變更，請重新產生草稿」。
- Apply 成功 link 僅接受 Grafana 相對 URL，避免 open redirect。
- 所有文字以 React escaping render；JSON viewer 不啟用 HTML execution。

---

## 9. Repository 結構

Use the official scaffold root directly：

```text
grafana-llm-poc/
├── .config/                         # generated Grafana plugin dev config
│   ├── docker-compose-base.yaml
│   └── ...
├── .github/
│   └── workflows/                   # scaffolded build/test checks, optional in POC
├── provisioning/
│   └── plugins/
│       └── acme-aiassistant-app.yaml
├── src/                             # React/TypeScript frontend
│   ├── components/
│   │   ├── ChatComposer.tsx
│   │   ├── MessageList.tsx
│   │   ├── MessageBubble.tsx
│   │   ├── ConnectionStatus.tsx
│   │   ├── AssistantModeSelector.tsx
│   │   ├── DashboardTargetPicker.tsx
│   │   ├── DashboardDraftPreview.tsx
│   │   ├── DashboardDiff.tsx
│   │   └── ApplyDraftButton.tsx
│   ├── pages/
│   │   └── AssistantPage.tsx
│   ├── services/
│   │   ├── chatApi.ts               # Resource API only
│   │   ├── dashboardApi.ts          # draft/read/apply commands
│   │   ├── liveChat.ts              # Grafana Live only
│   │   └── contracts.ts             # wire DTOs/type guards
│   ├── state/
│   │   ├── chatReducer.ts
│   │   └── types.ts
│   ├── utils/
│   │   └── pluginRoutes.ts
│   ├── img/
│   │   └── logo.svg
│   ├── module.ts
│   └── plugin.json
├── pkg/                             # Go backend
│   ├── main.go                      # app.Manage entrypoint only
│   ├── plugin/
│   │   ├── app.go                   # wiring/lifecycle
│   │   ├── config.go
│   │   ├── resources.go             # route registration + thin handlers
│   │   ├── resources_test.go
│   │   ├── stream.go                # Subscribe/Run/Publish handlers
│   │   └── stream_test.go
│   ├── chat/
│   │   ├── coordinator.go
│   │   ├── coordinator_test.go
│   │   ├── request.go
│   │   └── errors.go
│   ├── llm/
│   │   ├── provider.go              # domain interface/types
│   │   └── errors.go
│   └── llmopenai/
│       ├── provider.go
│       ├── sse.go
│       ├── provider_test.go
│       └── testdata/
│           ├── stream_ok.txt
│           └── stream_malformed.txt
│   ├── dashboard/
│   │   ├── draft.go
│   │   ├── service.go
│   │   ├── validator.go
│   │   ├── diff.go
│   │   └── service_test.go
│   └── grafanamcp/
│       ├── client.go                 # MCP client + exact tool allowlist
│       ├── process.go                # mcp-grafana STDIO lifecycle
│       ├── tools.go                  # typed read/update wrappers
│       └── client_test.go
├── tests/
│   └── assistant.spec.ts            # Grafana plugin E2E
├── docker-compose.yaml              # extend scaffold + Ollama
├── docker/
│   └── grafana/
│       └── Dockerfile               # install pinned mcp-grafana binary
├── scripts/
│   └── bootstrap-mcp-service-account.sh # idempotent local Grafana bootstrap
├── .env.example
├── .gitignore
├── Magefile.go
├── go.mod
├── go.sum
├── package.json
├── package-lock.json                # npm is official scaffold default
├── tsconfig.json
├── README.md
├── CHANGELOG.md
├── LICENSE
└── SPEC.md
```

Notes：

- 不建立 `src/datasource/` 或 nested plugin directory。
- `pkg/plugin.App` 只 wiring dependencies；避免 god object。
- frontend tests 可 colocate (`*.test.tsx`) 以符合 scaffold config。
- 如果最新版 scaffold 產生的 `.config`/script 名稱不同，保留 scaffold convention 並更新本 tree/README。
- repository 目前只有 `todo.md` 且未初始化 Git；scaffold phase 要先備份/保留需求，避免覆寫 `SPEC.md`。

---

## 10. Docker Compose 純地端架構

### 10.1 容器

`grafana`：

- image `grafana/grafana:12.3.3` through scaffold `GRAFANA_IMAGE=grafana` + `GRAFANA_VERSION` override。
- host port 僅綁 loopback：`127.0.0.1:3000:3000`；若日後要供 LAN 使用，必須明確調整 firewall/TLS/auth，不列入 POC 預設。
- mount built plugin `dist` through official scaffold convention。
- development mode / allow unsigned plugin only in local Compose。
- receives non-secret and secret `LLM_*` server-side env values。
- 使用 `docker/grafana/Dockerfile` 建立自訂 image，內含固定版本 `mcp-grafana`；MCP 使用 STDIO，不監聽 port。
- 關閉非必要對外分析、版本與 plugin catalog 檢查，使 runtime 可離線，例如 `GF_ANALYTICS_REPORTING_ENABLED=false`、`GF_ANALYTICS_CHECK_FOR_UPDATES=false`、`GF_PLUGINS_CHECK_FOR_UPDATES=false`；Phase 0 需依固定 Grafana 版本驗證實際設定名稱。

`ollama`：

- pin a tested Ollama image tag during implementation；do not use floating `latest` in final README。
- no host `ports`。
- internal expose/documentation port `11434` only。
- named volume `/root/.ollama` for downloaded models。
- healthcheck against local API if image contains appropriate tool；otherwise use an init/pull command with retry。

`grafana-init`（一次性 Compose service）：

- 等待 Grafana health endpoint ready。
- 使用僅存在本機 `.env` 的 bootstrap admin credential，以 Grafana API idempotently 建立 MCP service account、folder scope/role 與 token。
- 將 token 寫入只供 `grafana` 與 init service 使用的 named secret volume；不得印到 stdout。
- `mcp-grafana` 使用 `GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE`，官方行為會在每次 request 重新讀檔，因此不需因 token 建立/rotation 重啟。
- 如 Grafana OSS 固定版本不支援所需細粒度 RBAC，POC 可退化為 local Editor service account，但必須在 README 與 UI health 中標示 `broad_local_poc_permission=true`。

Optional `ollama-init` one-shot service：

- same image/network/volume as Ollama。
- waits for API then runs `ollama pull ${LLM_MODEL}`。
- Compose `grafana` may wait for Ollama health, but model download/warmup must still surface friendly UI unavailable/loading errors。
- Whether included by default depends on model download size; README must state first-run cost。

### 10.2 網路

```text
host/browser -- 127.0.0.1:3000 --> grafana
grafana ------ internal HTTP --> ollama:11434
grafana plugin -- STDIO --> mcp-grafana child process
mcp-grafana -- localhost HTTP --> Grafana API :3000
ollama ------- no published host port
```

Define a dedicated bridge network, e.g. `ai_internal` with `internal: true` only if that still permits required image startup/DNS behavior in target Docker environment。A Compose internal network blocks external access and can complicate model download；practical v1 recommendation：

- `grafana` joins default network plus internal AI network as needed by scaffold；
- `ollama` joins AI network；
- no host port is the primary browser isolation control；
- `ollama-init` needs outbound registry access for pull, so do not set the only network `internal: true` until validated。

Never rely on CORS as the security boundary。

Runtime 封閉性驗收：停止 host 網際網路後，已完成 image/model 準備的 Compose 仍可登入、對話、串流、產生草稿並套用 dashboard。不得有任何功能性 request 送往 public hostname。

### 10.3 Volume

| Volume | 用途 | 保存方式 |
|---|---|---|
| scaffold plugin mount | built frontend/backend into Grafana | bind mount/dev |
| `grafana-data` | Grafana DB/config state | named; optional reset instructions |
| `ollama-models` | model blobs | named; avoids repeated downloads |
| provisioning bind | auto-enable/configure app if supported by scaffold | source-controlled, no secrets |
| `mcp-local-secret` | bootstrap 產生的 Grafana service account token file | named volume；不進 Git、不掛載給 Ollama |

### 10.4 環境變數範例

`.env.example` contains only safe examples：

```dotenv
GRAFANA_VERSION=12.3.3
GRAFANA_IMAGE=grafana
GF_SECURITY_ADMIN_USER=admin
GF_SECURITY_ADMIN_PASSWORD=admin
GF_ANALYTICS_REPORTING_ENABLED=false
GF_ANALYTICS_CHECK_FOR_UPDATES=false
GF_PLUGINS_CHECK_FOR_UPDATES=false
LLM_BASE_URL=http://ollama:11434/v1
LLM_MODEL=qwen2.5:3b
LLM_API_KEY=
LLM_REQUEST_TIMEOUT=120s
CHAT_PENDING_TTL=30s
CHAT_TERMINAL_TTL=5m
MCP_GRAFANA_VERSION=<pinned-version>
GRAFANA_SERVICE_ACCOUNT_TOKEN=
GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE=/run/grafana-mcp/token
GRAFANA_ORG_ID=1
```

`README` must warn that default Grafana credentials are development-only。Do not commit a real `.env`。

### 10.5 啟動契約

Definition of Done says `docker compose up` starts services, not necessarily that a multi-GB model is instantly ready。To make the UX deterministic：

1. Compose starts Grafana/Ollama and optional init pull。
2. plugin loads even if Ollama is not ready。
3. Assistant page is accessible and reports provider unavailable/warming rather than crashing。
4. README provides `docker compose exec ollama ollama pull <model>` fallback and test prompt。

### 10.6 純地端與 air-gapped 準備

一般有網路的準備階段：

```text
docker compose build       # 下載 Grafana/Go/npm dependencies，編入 mcp-grafana
docker compose pull        # 取得 Ollama image
docker compose up -d
docker compose exec ollama ollama pull <model>
```

完全 air-gapped 環境不在 runtime 臨時下載。README 必須說明如何在有網路機器準備並搬移：

- `docker save` / `docker load` 自訂 Grafana image 與 Ollama image。
- 匯出／匯入 `ollama-models` volume，或使用 Ollama 支援的離線 model 準備方式。
- 使用 lockfile、Go module checksum 與固定 image digest。
- 啟動後以 network inspection 證明沒有 SaaS dependency。

---

## 11. 失敗模式

| 失敗情境 | 偵測方式 | POC 必須處理 | 延後項目 |
|---|---|---|---|
| Grafana Live disconnect before start | observable error / `RunStream` context cancel | preserve user msg, mark assistant interrupted, best-effort cancel; backend cancels upstream | replay/resume |
| Grafana Live disconnect mid-stream | context cancellation/send error | cancel provider; retain partial UI content + interrupted status | durable buffered replay |
| Ollama unavailable | connect/DNS/5xx typed error | send `start` if possible then `error: LLM_UNAVAILABLE`; UI retry affordance | circuit breaker |
| LLM total timeout | context deadline | cancel HTTP body, emit `LLM_TIMEOUT`, log duration | adaptive timeout |
| first token stalls | total timeout initially | UI remains loading and can cancel | separate TTFT timeout (optional later) |
| client refresh | unmount/disconnect | backend cancellation; new page has no restored conversation | persistence/resume |
| duplicate POST retry | same clientRequestId | return same request/channel, do not duplicate inference | distributed idempotency |
| duplicate subscription | atomic claim | one succeeds; others get `ALREADY_CONSUMED`/NotFound | multicast |
| partial/truncated upstream SSE | invalid EOF/no valid completion | emit `LLM_PROTOCOL_ERROR`; preserve already delivered content | provider-specific recovery |
| user cancellation | DELETE or disconnect | context cancel, terminal `REQUEST_CANCELLED` when channel alive, idempotent response | cross-device control |
| POST succeeds but no subscribe | pending TTL | expire and cleanup; no inference started | durable queue |
| malformed Live event in frontend | runtime type guard | stop request, show protocol error, log browser console without content | schema registry |
| response exceeds cap | byte counter | cancel, emit `STREAM_LIMIT_EXCEEDED` | dynamic quotas |
| plugin/Grafana restart | process loss | UI stream interrupts; requests lost | external registry/HA |
| Ollama model absent | upstream 4xx/404 body mapping | generic model configuration error; detailed server log without full body | auto-select model |
| slow consumer/send failure | sender error/context | cancel upstream to bound resources | buffering/backpressure service |
| 12.4.x Live regression | E2E never sees subscription/start | stay on pinned 12.3.3; compatibility test before upgrade | alternate SSE transport unless ADR |
| `mcp-grafana` 未安裝／啟動失敗 | process start/handshake error | 一般 chat 可用；dashboard 功能顯示 `MCP_UNAVAILABLE` | 複雜 supervisor |
| MCP tool 不存在／版本不相容 | startup `tools/list` | health 顯示 degraded；disable draft/apply | runtime tool discovery adaptation |
| MCP credential 缺少／過期 | Grafana 401/403 | 不洩漏 token；apply 失敗並指示管理員修復 | 自動 rotation UI |
| LLM 產生非法 dashboard JSON | parse/schema/policy error | 最多一次 repair；仍失敗送 `DRAFT_VALIDATION_FAILED` | 多輪 agent self-healing |
| 草稿過期 | TTL | 410/typed error，要求重新產生 | persistent drafts |
| dashboard 在預覽後被他人修改 | base version mismatch | 409 `DASHBOARD_CHANGED`，禁止 overwrite | automatic three-way merge |
| 重複 apply | idempotency state | 回傳第一次結果，不重複 update | distributed idempotency |
| MCP update 部分成功但 response lost | follow-up get by UID/idempotency lookup | 查證 dashboard state，再決定成功或 ambiguous | transaction coordinator |
| 使用者拒絕確認 | dialog cancel | 不做任何 write，保留草稿至 TTL | approval workflow |

### 11.1 必要清理行為

- Every provider response body is closed。
- Every active request has exactly one cancel function and owner。
- Terminal transition cancels timers and releases active counters。
- Cleanup ticker is stopped in `Dispose()`。
- `RunStream` returns promptly when context is done or sender fails。
- Sending a terminal event is best effort；state/logging must still become terminal if transport is gone。
- App `Dispose()` 必須關閉 MCP client/STDIO 子行程；MCP crash 不得留下 zombie process。

### 11.2 日誌契約

At request accepted, stream start, first token, terminal state log structured fields：

```text
request_id session_id grafana_org_id grafana_user_id model status
latency_ms time_to_first_token_ms total_duration_ms token_event_count error_code
draft_id draft_revision dashboard_uid mcp_tool apply_status
```

Rules：

- user email/name, prompt, response, API key and full upstream error body are excluded by default。
- preserve wrapped error server-side；user sees normalized message。
- cancellation caused by browser disconnect logs as informational status, not noisy server error。

---

## 12. 實作計畫

### Phase 0 — 相容性驗證與版本固定

目標：驗證目前官方骨架能建立帶後端的 App Plugin，且 plugin-scoped Live 可在選定的 Grafana OSS image 運作。

影響檔案：

- generated scaffold files
- `docker-compose.yaml`
- `.env.example`
- `README.md` compatibility note

驗收條件：

- official scaffold builds frontend/backend。
- Grafana OSS 12.3.3 loads and enables unsigned development plugin。
- minimal `plugin/<id>/chat/v1/smoke/<uuid>` stream sends one JSON event to frontend。
- same test is runnable with overridden `GRAFANA_VERSION` and documents 12.4.x result。

### Phase 1 — Grafana 外掛骨架與導覽

目標：建立官方 repository 結構與可見的 AI 助理頁面。

影響檔案：

- `src/plugin.json`, `src/module.ts`, `src/pages/AssistantPage.tsx`
- `pkg/main.go`, `pkg/plugin/app.go`
- scaffold config/build files

驗收條件：

- `npm run build` and Go/Mage backend build pass。
- Grafana registers plugin ID exactly once。
- app can be enabled and page appears in navigation/sidebar。
- page renders placeholder without console/runtime error。

### Phase 2 — React 對話 UI 與純狀態模型

目標：實作不耦合實際 transport 的對話 UI。

影響檔案：

- `src/components/*`
- `src/pages/AssistantPage.tsx`
- `src/state/*`
- colocated frontend tests

驗收條件：

- user can enter/send with button and keyboard behavior defined (`Enter` send, `Shift+Enter` newline)。
- empty input cannot send；input disabled or guarded during configured active request policy。
- reducer handles accepted/start/token/complete/error/interrupted/cancel states。
- partial response is retained on error。
- React renders content as text, not unsafe HTML。

### Phase 3 — 後端 Resource API 與協調器

目標：建立經驗證的 pending request 與取消端點。

影響檔案：

- `pkg/plugin/resources.go`, `config.go`
- `pkg/chat/*`
- handler/coordinator tests
- `src/services/chatApi.ts`, `contracts.ts`

驗收條件：

- valid POST returns specified `202` contract and server-generated IDs/channel。
- invalid/malformed/oversized requests return normalized 4xx。
- identity ownership enforced。
- same idempotency key/same payload returns same request；different payload returns 409。
- cancel is ownership-safe and idempotent。
- pending/terminal TTL cleanup tested with injected clock or short test durations。

### Phase 4 — OpenAI-compatible LLM 整合

目標：先由假的 OpenAI-compatible server、再由 Ollama，透過 `llm.Provider` 完成串流。

影響檔案：

- `pkg/llm/*`, `pkg/llmopenai/*`
- `pkg/plugin/app.go`, `config.go`
- provider fixtures/tests

驗收條件：

- provider parses multi-frame SSE and `[DONE]` in order。
- handles empty delta, usage, malformed JSON, non-2xx, oversized frame, cancellation and timeout。
- test server proves Authorization omitted/attached as configured。
- coordinator/business logic imports no Ollama-specific types/package。
- switching base URL/model needs no frontend change。

### Phase 5 — Grafana Live 協定

目標：將準備完成的 command 接到經授權、一次性的 plugin-scoped stream。

影響檔案：

- `pkg/plugin/stream.go` and tests
- `src/services/liveChat.ts`, `contracts.ts`
- `src/state/chatReducer.ts`

驗收條件：

- `SubscribeStream` validates path, ownership and state。
- `PublishStream` denies browser publishing。
- `RunStream` atomically claims request and emits schema-valid monotonic events。
- frontend subscribes using returned address and runtime API, not raw WS URL/channel prefix。
- duplicate/gap/transport error handling is tested。

### Phase 6 — Ollama 端到端串流

目標：在 Compose 完成真正的端到端 vertical slice。

影響檔案：

- `docker-compose.yaml`, `.env.example`, provisioning
- E2E test/manual smoke script as supported by scaffold
- integration wiring files

驗收條件：

- Ollama has no published host port (`docker compose port ollama 11434` returns none)。
- browser network shows command to Grafana and Grafana Live WS only；no `11434` request。
- prompt produces more than one incremental UI update when model/provider chunks permit。
- cancel interrupts generation and releases backend request。
- unavailable Ollama produces normalized UI error。

### Phase 7 — `mcp-grafana` 安裝與唯讀整合

目標：以固定版本 `go install` 將 MCP binary 編入純地端 Grafana image，建立可靠的 STDIO client 與唯讀工具 wrapper。

影響檔案：

- `docker/grafana/Dockerfile`, `docker-compose.yaml`
- `pkg/grafanamcp/*`
- `scripts/bootstrap-mcp-service-account.sh`
- `.env.example`

驗收條件：

- image build 顯示固定 `mcp-grafana` version，runtime 不下載 binary。
- backend 能啟動、handshake、list tools 並在 Dispose 正常關閉子行程。
- 只允許白名單 read tools；任意 tool name 被拒絕。
- 可列出 datasource、搜尋並取得 dashboard。
- MCP port 未公開，browser 看不到 MCP endpoint/credential。
- 斷網後（images/model 已準備）功能仍正常。

### Phase 8 — 自然語言 Dashboard／Panel 草稿

目標：讓使用者以自然語言建立受驗證、可預覽但尚未寫入的 dashboard/panel draft。

影響檔案：

- `pkg/dashboard/*`
- dashboard draft Resource handlers
- `src/components/Dashboard*`, `src/services/dashboardApi.ts`
- reducer/contracts/tests

驗收條件：

- 支援 create dashboard 與新增/修改單一 panel。
- 後端從 MCP 取得真實 datasource/target dashboard context。
- LLM 只輸出 versioned structured draft；invalid output 最多 repair 一次。
- schema/policy/datasource/diff validation pass 後才發 `draft` event。
- 前端顯示摘要、queries、warnings、JSON/diff；未確認前 MCP write call count 為 0。

### Phase 9 — 使用者確認與 MCP 套用

目標：經獨立 HTTP command、明確確認與 concurrency checks 安全建立/更新 dashboard。

影響檔案：

- apply Resource handler
- `pkg/dashboard/service.go`, draft registry
- `pkg/grafanamcp/tools.go`
- `DashboardDraftPreview`, `ApplyDraftButton`, confirmation dialog
- integration/E2E tests

驗收條件：

- `confirm=false`、過期、non-owner、revision mismatch、dashboard version mismatch 都不能 write。
- apply code path 固定呼叫 `update_dashboard`，不採用 LLM 提供的 tool name。
- 重複 apply idempotent。
- 成功回傳可信任的 dashboard UID/relative URL/version，UI 可開啟結果。
- audit log 可關聯 Grafana user、draft、MCP tool 與 dashboard，但不含 prompt/token。

### Phase 10 — 強化、測試與 README

目標：讓全新 clone 的重現流程與已知限制清楚可執行。

影響檔案：

- `README.md`, `CHANGELOG.md`
- unit/integration/E2E tests
- logging/error paths

驗收條件：

- README covers prerequisites, build, model pull, start, enable/open app, smoke test, cancel, logs, troubleshooting and reset volumes。
- Definition of Done checklist is executable/documented。
- Go tests pass with race detector where practical；frontend unit tests/build pass；E2E smoke passes on pinned Grafana。
- no secrets or prompt/response bodies appear in committed files/log assertions。
- known 12.4.x Live issue and upgrade test are documented。

### Phase 11 — 選用的未來地端 Provider 相容性（不屬於 v1 DoD）

目標：證明只改設定即可切換到純地端 Docker Model Runner。

影響檔案：

- README profile/example env, provider contract tests only

驗收條件：

- DMR OpenAI-compatible endpoint passes the same provider contract test。
- no frontend or chat coordinator source change。
- any DMR-specific network setup is isolated to Compose profile/docs。

---

## 13. 最終程式開發規格

### 13.1 實作不變條件

An implementation is non-conforming if any of these are false：

1. Browser cannot learn or call `LLM_BASE_URL` from normal frontend code/bundle/config response。
2. All commands are Resource HTTP APIs；Live client publish is denied。
3. All streaming response events travel through Grafana Live in v1。
4. There is no additional WebSocket server/library listening port。
5. App Plugin and its Go backend are one plugin package；no v1 data source/nested plugin。
6. Provider-neutral application code imports only `pkg/llm` abstractions。
7. Outbound LLM request uses context cancellation and bounded stream parsing。
8. Request ownership includes Grafana organization and user identity。
9. Exactly one inference may claim a prepared request。
10. UI preserves partial text and shows an explicit terminal/interrupted status。
11. Ollama has no host-published port in default Compose。
12. No secret, prompt or generated content is logged by default。
13. Runtime 完全地端；除了 loopback Grafana port，不公開 Ollama 或 MCP port，也不依賴 SaaS。
14. LLM 只能產生 DashboardDraft，不能直接執行 MCP write tool。
15. `update_dashboard` 只有經 owner/revision/confirmation/version checks 的 apply handler 能呼叫。
16. 前端必須提供 dashboard/panel 草稿預覽、警告、確認與 apply 結果，不得只有聊天文字。

### 13.2 後端組裝

`NewApp` should perform explicit composition：

```text
load Config
  -> construct reusable HTTP client
  -> construct OpenAICompatibleProvider
  -> start pinned mcp-grafana STDIO process and MCP client
  -> construct DashboardDraftService(MCP read tools, provider, validator)
  -> construct ChatCoordinator(provider, limits, clock/logger)
  -> register resource ServeMux
  -> expose stream handlers on App
```

Avoid a general DI container。Tests replace provider/clock through narrow constructor parameters。

### 13.3 Coordinator record（概念）

```go
type RequestRecord struct {
	RequestID       string
	SessionID       string
	ClientRequestID string
	Owner           Principal // OrgID + stable UserID
	Messages        []llm.Message
	Model           string
	Status          Status
	CreatedAt       time.Time
	ExpiresAt       time.Time
	Cancel          context.CancelFunc // only while active
	PayloadHash     [32]byte            // idempotency conflict detection
}
```

Do not expose mutable record pointers outside coordinator lock/API。Never hold registry mutex during network calls or `StreamSender` sends。

Minimum coordinator methods：

```text
Create(principal, command) -> descriptor
AuthorizeSubscription(principal, requestID) -> decision
Claim(principal, requestID) -> immutable execution + completion callback
Cancel(principal, requestID) -> status
Dispose()
```

### 13.4 HTTP handler 規則

- route with Go standard `http.ServeMux` through SDK `httpadapter` as currently recommended。
- enforce method even if router pattern supports method matching。
- use `http.MaxBytesReader` or equivalent before decode。
- decode one JSON object and reject trailing non-whitespace JSON。
- set JSON/content-type/status before writing body。
- no panic on absent PluginContext/user；return unauthorized/internal safely。
- handlers call application methods and map errors；they do not own goroutines。

### 13.5 Stream handler 規則

- parse only canonical path shape and UUID。
- `RunStream` sends `start` before provider content。
- increment sequence only after a send succeeds；stop immediately on send error。
- callback returns sender error so provider stops reading。
- derive provider context from RunStream context plus configured timeout。
- disconnect cancellation wins over a late successful EOF。
- completion callback/`defer` always updates coordinator terminal state/counters。
- do not send raw Go error strings to user。

### 13.6 前端規則

- use Grafana UI components where reasonable；do not add another component framework。
- transport functions return typed domain results/observables；components do not know URLs beyond imported plugin route helper。
- validate runtime event shape because WebSocket payload is untrusted。
- keep one subscription handle per active request and always unsubscribe on terminal/unmount。
- Send order: optimistic user message -> POST -> assistant placeholder -> subscribe。On POST failure, mark user turn failed/retryable without creating a phantom active stream。
- Retry creates a new `clientRequestId` only when user intentionally requests a new inference；automatic HTTP transport retry reuses the same ID。
- model label comes from POST/health response, never hard-coded provider URL。
- connection state distinguishes `connecting`, `streaming`, `disconnected`, `error`；do not claim WebSocket is connected merely because POST succeeded。
- dashboard mode 必須使用 server-returned datasource/folder/dashboard options；不得由 browser 直接列舉 Grafana API。
- draft preview 與 assistant token bubble 是不同的 typed UI element。
- apply 一律透過獨立 Resource API；前端不呼叫 MCP、不組 `update_dashboard` payload。

### 13.7 測試矩陣

Backend unit：

- input validation boundary values；
- identity ownership and non-enumerating 404；
- coordinator transition/idempotency/concurrency；
- SSE split across reads, multiple data lines if supported, `[DONE]`, empty choice, malformed JSON；
- timeout/cancel/send callback error closes upstream；
- exactly one terminal state and no lock held during callback。

Frontend unit：

- reducer happy path and event ordering；
- duplicate sequence ignored, gap rejected；
- partial content retained on error/disconnect；
- request validation/button/loading/cancel states；
- API error envelope and unknown Live event handling。

Integration：

- `httptest.Server` emits delayed SSE chunks and observes cancellation；
- Resource POST + simulated RunStream share the same coordinator request；
- duplicate RunStream does not duplicate provider call。

E2E/manual：

- plugin navigation；
- incremental response through Live；
- cancel；
- Ollama unavailable；
- browser never requests Ollama；
- Grafana pinned-version Live smoke；
- optional latest Grafana compatibility run allowed to report known regression separately from baseline。
- 自然語言建立 dashboard 草稿、預覽、明確確認、apply、開啟結果。
- 修改單一 panel 時其他 panels 保持不變；dashboard concurrent change 回 409。
- 未確認、invalid draft、non-owner、expired draft 都不產生 MCP write call。
- 切斷網際網路後，已準備的純地端 Compose 仍完成對話與 dashboard apply。

### 13.8 完成定義追蹤

| 原始完成條件 | 驗證方式 |
|---|---|
| Compose starts Grafana/Ollama | fresh environment startup + service health/logs |
| plugin loaded/app page opens | Grafana registration log + E2E navigation |
| prompt input | UI E2E |
| command through backend | browser network + handler integration test |
| backend calls Ollama | provider integration/log fields |
| upstream streaming | delayed SSE integration + Ollama smoke |
| chunks through Grafana Live | Live E2E with multiple rendered updates |
| frontend knows no endpoint | bundle/source review + network assertion |
| provider replaceable | fake/DMR-compatible contract test, config-only switch |
| unavailable UI error | stop Ollama E2E/manual test |
| README reproducible | fresh-clone runbook review |
| 自然語言生成 dashboard/panel | draft E2E + preview + confirmed MCP apply |
| 純地端 Compose | runtime offline test + only loopback Grafana port published |

### 13.9 明確拒絕的替代方案

- Frontend -> Ollama：violates trust/credential/network boundary。
- Resource API response streaming/SSE as v1 primary：simpler but does not validate required Grafana Live architecture。
- WebSocket command messages：mixes command/event semantics and bypasses Resource validation/idempotency contract。
- Nested datasource solely for Live：unnecessary domain/lifecycle complexity；App plugin scope supports streaming conceptually。
- Shared session channel：cross-request interleaving, harder cancellation/security；request channel is simpler。
- Starting inference before Live subscription：can lose initial tokens and terminal error。
- Heavy LLM/agent framework：no orchestration/tool/RAG need in v1。
- Browser 直接使用 MCP client：會擴大 credential/write boundary；本 POC 固定走 Go backend。
- 模型直接呼叫 `update_dashboard`：缺少人員確認、revision 與 concurrency protection。
- 只讓模型輸出任意 dashboard JSON 文字：前端無法可靠判定/驗證/apply；必須使用 server-side typed draft。

---

## 14. 實作前待決問題

These do not block the spec structure, but Phase 0/README must resolve them：

1. Final organization/plugin ID (`acme-aiassistant-app` is a placeholder)。
2. Final small default Ollama model based on target developer hardware and license。
3. Exact pinned Ollama image digest/tag after smoke test。
4. Whether optional `ollama-init` auto-pulls model or README requires explicit pull；prefer init only if `docker compose up` UX remains clear。
5. Whether app provisioning can auto-enable the app reliably with the selected scaffold/Grafana version；otherwise README includes one UI enable step。
6. Whether Grafana 12.4+ plugin Live regression is fixed in the exact target release at implementation time；do not remove 12.3.3 baseline without test evidence。
7. Phase 0 選定的 `mcp-grafana` 固定版本，以及該版本 `tools/list` 的確切工具名稱與 schema。
8. Grafana OSS 12.3.3 可用的最小 service account/RBAC provisioning 方式；若退化為 Editor，需限制在本機 POC 並記錄風險。
9. v1 預設支援的 panel type 與 datasource type allowlist；建議先做 Prometheus + timeseries/stat/table。

---

## 15. 參考資料

Authoritative/current references reviewed for this design：

- [Grafana: Add a backend component to an app plugin](https://grafana.com/developers/plugin-tools/how-to-guides/app-plugins/add-backend-component)
- [Grafana: Add resource handler for app plugins](https://grafana.com/developers/plugin-tools/how-to-guides/app-plugins/add-resource-handler)
- [Grafana: Plugin types and usage](https://grafana.com/developers/plugin-tools/key-concepts/plugin-types-usage)
- [Grafana: Work with nested plugins](https://grafana.com/developers/plugin-tools/how-to-guides/app-plugins/work-with-nested-plugins)
- [Grafana: Plugin backend system](https://grafana.com/developers/plugin-tools/key-concepts/backend-plugins)
- [Grafana: Set up Grafana Live](https://grafana.com/docs/grafana/latest/setup-grafana/set-up-grafana-live/)
- [Grafana: Build a streaming data source plugin](https://grafana.com/developers/plugin-tools/tutorials/build-a-streaming-data-source-plugin)
- [Grafana official plugin examples](https://github.com/grafana/grafana-plugin-examples)
- [Grafana core Live plugin-scope routing](https://github.com/grafana/grafana/blob/main/pkg/services/live/live.go)
- [Open Grafana 12.4+ plugin-scoped Live regression report](https://github.com/grafana/grafana/issues/125801)
- [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility)
- [Docker Model Runner API / OpenAI compatibility](https://docs.docker.com/ai/model-runner/api-reference/)
- [Grafana 官方 mcp-grafana repository、安裝、transport、tools 與 RBAC](https://github.com/grafana/mcp-grafana)
- [Grafana 官方：在 App Plugin 使用 LLM 與 MCP](https://grafana.com/developers/plugin-tools/how-to-guides/app-plugins/use-llms-and-mcp)

The open GitHub issue is evidence of a current compatibility risk, not an official guarantee that every 12.4 deployment fails。Phase 0 must reproduce against the selected image。
