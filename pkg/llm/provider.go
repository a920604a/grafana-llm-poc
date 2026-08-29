package llm

import "context"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model    string
	Messages []Message
}

type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

type Chunk struct {
	Content      string
	FinishReason string
	Usage        *Usage
}

type ChunkHandler func(Chunk) error

type Provider interface {
	ChatStream(ctx context.Context, req ChatRequest, onChunk ChunkHandler) error
}
