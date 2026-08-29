你是一位 Principal Software Engineer / Software Architect。

我要建立一個 POC，請先分析需求並產出完整的 **Coding Specification / Technical Design Spec**，暫時不要開始大量實作。

# Project Goal

建立一個：

* Grafana OSS
* Grafana App Plugin（React）
* Grafana Backend Plugin（Go）
* Grafana Live / WebSocket streaming
* Self-hosted LLM

  * 第一版使用 Ollama
  * 未來可切換 Docker Model Runner
* 未來可以擴充 PostgreSQL / MySQL 等 DB integration

的 AI Assistant POC。

核心驗證目標：

```text
Grafana React App
       │
       │ command
       ▼
Grafana Plugin Backend
       │
       ▼
Self-host LLM
       │
       │ streaming response
       ▼
Grafana Live
       │
       │ WebSocket
       ▼
React UI
```

---

# Design Principles

請優先遵守以下原則：

1. React frontend 不直接連 Ollama。
2. React frontend 不直接連 DB。
3. Credentials / DB connections / LLM connections 都必須存在 server-side。
4. HTTP / Resource API 負責 Command。
5. Grafana Live / WebSocket 負責 Event / Streaming。
6. 不自行建立另一套 WebSocket Server，優先使用 Grafana Live。
7. LLM provider 不要與 Ollama 強耦合。
8. Ollama / Docker Model Runner 應透過 abstraction 隔離。
9. 優先使用 OpenAI-compatible API abstraction。
10. POC 保持簡單，不使用不必要的 framework。

不要加入：

* LangChain
* LangGraph
* Kafka
* Redis
* Vector DB
* Kubernetes
* 自製 WebSocket Server
* Agent framework

除非能明確證明目前 POC 必須使用。

---

# Target Architecture

希望架構接近：

```text
Grafana OSS
│
├── AI App Plugin
│   │
│   └── React UI
│
├── Backend / Nested Data Source Plugin
│   │
│   ├── Resource API
│   ├── Streaming
│   └── LLM Client
│
├── Grafana Live
│
└────────────────────────────
               │
               ▼
        LLM Provider
               │
        ┌──────┴──────┐
        │             │
      Ollama      Docker Model Runner
```

未來：

```text
Plugin Backend
      │
      ├── LLM Provider
      │
      └── Tool / Data Provider
                │
                ▼
          PostgreSQL / MySQL
```

---

# POC Scope

第一版只需要：

## Grafana

* Grafana OSS Docker
* Plugin development environment
* App Plugin 可以從 Grafana UI 開啟

## Frontend

React App Plugin 提供：

```text
AI Assistant

Model: [model]

Conversation

User: ...
AI: streaming...

[ input                     ]
[ Send ]
```

需要：

* Chat input
* Message list
* Streaming response
* Loading / error state
* Connection state

Frontend responsibilities：

* UI
* local conversation state
* send command
* subscribe Grafana Live
* render stream

Frontend 不應知道 Ollama URL。

---

# Backend

使用 Grafana backend plugin / Go。

Backend responsibilities：

* 接收 chat command
* 建立 request/session ID
* 呼叫 LLM
* consume streaming response
* 將 chunk publish 到 Grafana Live
* timeout
* cancellation
* error handling
* logging

需要明確區分：

```text
Command Flow
React
  → HTTP / Resource API
  → Backend
```

以及：

```text
Event Flow
Backend
  → Grafana Live
  → WebSocket
  → React
```

不要讓 WebSocket 同時承擔 command API。

---

# Streaming Protocol

請設計簡單 event schema，例如：

```json
{
  "requestId": "uuid",
  "type": "token",
  "content": "hello"
}
```

event type 至少考慮：

```text
start
token
complete
error
```

例如：

```json
{
  "requestId": "...",
  "type": "complete"
}
```

Grafana Live channel 應該與 request/session 對應。

請分析適合的 channel naming convention。

---

# LLM Abstraction

請不要在 business logic 到處直接呼叫 Ollama。

設計：

```go
type LLMProvider interface {
    ChatStream(...)
}
```

實際 implementation 第一版：

```text
OpenAICompatibleProvider
```

Configuration：

```text
LLM_BASE_URL
LLM_MODEL
LLM_API_KEY
```

Ollama example：

```text
http://ollama:11434/v1
```

未來應能切換：

```text
Docker Model Runner
vLLM
LocalAI
OpenAI
```

而不需要修改 frontend。

---

# Docker

第一版希望使用 Docker Compose。

至少包含：

```text
grafana
ollama
```

Networking：

```text
Browser
   │
   ▼
Grafana :3000

Grafana Backend
   │
   │ internal docker network
   ▼
Ollama :11434
```

不要 expose Ollama 到 public network，除非 development 必須。

---

# Repository Structure

請評估並改善類似：

```text
grafana-ai-poc/
│
├── docker-compose.yml
├── .env.example
├── README.md
│
├── provisioning/
│
└── grafana-ai-app/
    │
    ├── src/
    │   ├── pages/
    │   ├── components/
    │   ├── services/
    │   └── datasource/
    │
    └── pkg/
        ├── plugin/
        └── llm/
```

如果 Grafana Plugin 官方 scaffold 對 repository layout 有更合理的方式，以官方 convention 為優先。

---

# Future DB Integration

這版不要真正實作 DB Agent。

但 architecture 必須保留合理 extension point：

```text
ToolProvider
DataProvider
```

未來 DB integration 必須遵守：

* DB credential server-side only
* read-only account
* SELECT only
* query timeout
* row limit
* audit logging
* 不允許 LLM 任意 execute SQL

請避免為尚未存在的功能 over-engineering。

---

# Non-Functional Requirements

POC 至少考慮：

## Security

* Ollama 不直接暴露 browser
* DB credentials 不進 frontend
* 不把 secret 放進 repository
* Grafana authentication boundary
* server-side validation

## Reliability

* LLM timeout
* streaming interruption
* frontend disconnect
* cancellation
* Ollama unavailable

## Observability

至少 logging：

```text
request_id
session_id
model
latency
status
error
```

如果容易做到，可以考慮：

```text
time_to_first_token
total_duration
```

但不要因此增加大型 monitoring framework。

## Maintainability

Frontend / transport / LLM provider 必須分離。

---

# Definition of Done

POC 完成需要滿足：

1. `docker compose up` 可以啟動 Grafana 與 Ollama。
2. Grafana 可以載入自製 App Plugin。
3. Sidebar / App page 可以開啟 AI Assistant。
4. 使用者可以輸入 prompt。
5. React 透過 backend API 發送 command。
6. Backend 可以呼叫 Ollama。
7. Ollama response 使用 streaming。
8. Backend 將 chunks publish 到 Grafana Live。
9. React 透過 Grafana Live / WebSocket 接收並即時顯示。
10. React 完全不知道 Ollama endpoint。
11. Ollama implementation 可以被其他 OpenAI-compatible provider 替換。
12. LLM service unavailable 時 UI 可以顯示合理 error。
13. README 能說明如何啟動與測試。

---

# Required Output

先不要直接完整實作。

請依序輸出：

## 1. Requirement Understanding

說明你理解的：

* Goal
* Functional Requirements
* Non-functional Requirements
* Out of Scope

## 2. Assumptions

列出所有目前做出的假設。

不要把假設描述成已知事實。

## 3. Architecture

說明：

* Components
* Responsibilities
* Boundaries
* Data Flow
* Control Flow
* Trust Boundary

並提供 ASCII architecture diagram。

## 4. Grafana Plugin Design

明確判斷應採用：

* App Plugin
* Data Source Plugin
* Backend Plugin
* Nested Plugin

中的哪些能力。

說明原因與 trade-offs。

優先遵循目前 Grafana 官方推薦的 plugin architecture。

## 5. API Contract

定義：

```text
POST chat command
Grafana Live streaming events
```

包含 request / response / event schema。

## 6. LLM Provider Design

定義 interface、configuration 與 provider boundary。

## 7. Repository Structure

產出完整建議 tree。

## 8. Docker Architecture

說明：

* containers
* network
* volumes
* ports
* environment variables

## 9. Failure Modes

至少分析：

* Grafana Live disconnect
* Ollama unavailable
* LLM timeout
* client refresh
* duplicated request
* partial stream
* user cancellation

並說明 POC 階段哪些需要處理、哪些可以 postpone。

## 10. Implementation Plan

拆成小型 milestones，例如：

```text
Phase 1 - Grafana plugin scaffold
Phase 2 - React page
Phase 3 - Backend resource API
Phase 4 - Ollama integration
Phase 5 - Grafana Live
Phase 6 - End-to-end streaming
Phase 7 - error handling + README
```

每個 Phase 必須包含：

* objective
* files affected
* acceptance criteria

## 11. Final Coding Spec

最後產生：

```text
SPEC.md
```

內容必須足以讓另一個 coding agent 根據 SPEC.md 獨立完成 implementation。

---

# Engineering Rules

實作規劃時：

* Prefer composition over unnecessary inheritance
* Keep interfaces small
* Dependency injection only where useful
* KISS
* YAGNI
* Avoid premature abstraction
* Avoid god objects
* Avoid unnecessary design patterns
* Prefer idiomatic Go / TypeScript / React
* 明確處理 errors
* code 必須容易 test
* 不為 POC 建立 production-scale infrastructure

若發現我的 architecture assumption 有問題，請直接指出，不要為了符合需求而照單全收。

如果官方 Grafana API / Plugin SDK 的實際能力與上述設計衝突，以官方目前版本能力為準，並在 SPEC 中明確指出差異。

現在請先進行 architecture analysis，然後產生 Coding Specification；**不要開始大量 implementation code。**
