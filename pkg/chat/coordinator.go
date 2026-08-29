package chat

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/acme/ai-assistant/pkg/llm"
	"github.com/google/uuid"
)

var (
	ErrNotFound            = errors.New("request not found")
	ErrAlreadyConsumed     = errors.New("request already consumed")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
)

type Principal struct {
	OrgID int64
	Login string
}

func (p Principal) Equal(other Principal) bool {
	return p.OrgID == other.OrgID && p.Login != "" && p.Login == other.Login
}

type Status string

const (
	StatusPending   Status = "pending"
	StatusStreaming Status = "streaming"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Command struct {
	ClientRequestID string
	SessionID       string
	Messages        []llm.Message
}

type Descriptor struct {
	RequestID string
	SessionID string
	Model     string
	ExpiresAt time.Time
}

type Execution struct {
	RequestID string
	SessionID string
	Model     string
	Messages  []llm.Message
}

type record struct {
	descriptor      Descriptor
	clientRequestID string
	owner           Principal
	messages        []llm.Message
	payloadHash     [32]byte
	status          Status
	cancel          context.CancelFunc
	terminalAt      time.Time
}

type Coordinator struct {
	mu          sync.Mutex
	requests    map[string]*record
	idempotency map[string]string
	model       string
	pendingTTL  time.Duration
	terminalTTL time.Duration
}

func NewCoordinator(model string, pendingTTL, terminalTTL time.Duration) *Coordinator {
	return &Coordinator{
		requests:    make(map[string]*record),
		idempotency: make(map[string]string),
		model:       model,
		pendingTTL:  pendingTTL,
		terminalTTL: terminalTTL,
	}
}

func (c *Coordinator) Create(owner Principal, command Command) (Descriptor, error) {
	payload, _ := json.Marshal(command)
	hash := sha256.Sum256(payload)
	key := ownerKey(owner) + ":" + command.ClientRequestID

	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked(time.Now())
	if requestID, ok := c.idempotency[key]; ok {
		existing := c.requests[requestID]
		if existing != nil && existing.payloadHash == hash {
			return existing.descriptor, nil
		}
		return Descriptor{}, ErrIdempotencyConflict
	}

	now := time.Now().UTC()
	sessionID := command.SessionID
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	descriptor := Descriptor{
		RequestID: uuid.NewString(),
		SessionID: sessionID,
		Model:     c.model,
		ExpiresAt: now.Add(c.pendingTTL),
	}
	c.requests[descriptor.RequestID] = &record{
		descriptor:      descriptor,
		clientRequestID: command.ClientRequestID,
		owner:           owner,
		messages:        append([]llm.Message(nil), command.Messages...),
		payloadHash:     hash,
		status:          StatusPending,
	}
	c.idempotency[key] = descriptor.RequestID
	return descriptor, nil
}

func (c *Coordinator) Authorize(owner Principal, requestID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := c.requests[requestID]
	if rec == nil || !rec.owner.Equal(owner) || time.Now().After(rec.descriptor.ExpiresAt) && rec.status == StatusPending {
		return ErrNotFound
	}
	return nil
}

func (c *Coordinator) Claim(owner Principal, requestID string, cancel context.CancelFunc) (Execution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := c.requests[requestID]
	if rec == nil || !rec.owner.Equal(owner) || time.Now().After(rec.descriptor.ExpiresAt) && rec.status == StatusPending {
		return Execution{}, ErrNotFound
	}
	if rec.status != StatusPending {
		return Execution{}, ErrAlreadyConsumed
	}
	rec.status = StatusStreaming
	rec.cancel = cancel
	return Execution{RequestID: requestID, SessionID: rec.descriptor.SessionID, Model: rec.descriptor.Model, Messages: append([]llm.Message(nil), rec.messages...)}, nil
}

func (c *Coordinator) Finish(requestID string, status Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rec := c.requests[requestID]; rec != nil {
		rec.status = status
		rec.cancel = nil
		rec.terminalAt = time.Now().UTC()
	}
}

func (c *Coordinator) Cancel(owner Principal, requestID string) (Status, error) {
	c.mu.Lock()
	rec := c.requests[requestID]
	if rec == nil || !rec.owner.Equal(owner) {
		c.mu.Unlock()
		return "", ErrNotFound
	}
	if rec.status == StatusCompleted || rec.status == StatusFailed || rec.status == StatusCancelled {
		status := rec.status
		c.mu.Unlock()
		return status, nil
	}
	rec.status = StatusCancelled
	rec.terminalAt = time.Now().UTC()
	cancel := rec.cancel
	rec.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return StatusCancelled, nil
}

func (c *Coordinator) Dispose() {
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.requests))
	for _, rec := range c.requests {
		if rec.cancel != nil {
			cancels = append(cancels, rec.cancel)
		}
	}
	c.requests = make(map[string]*record)
	c.idempotency = make(map[string]string)
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (c *Coordinator) cleanupLocked(now time.Time) {
	for id, rec := range c.requests {
		expiredPending := rec.status == StatusPending && now.After(rec.descriptor.ExpiresAt)
		expiredTerminal := !rec.terminalAt.IsZero() && now.Sub(rec.terminalAt) > c.terminalTTL
		if expiredPending || expiredTerminal {
			delete(c.idempotency, ownerKey(rec.owner)+":"+rec.clientRequestID)
			delete(c.requests, id)
		}
	}
}

func ownerKey(owner Principal) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(owner.Login+":"+strconv.FormatInt(owner.OrgID, 10))).String()
}
