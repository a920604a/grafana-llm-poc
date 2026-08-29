package dashboard

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/acme/ai-assistant/pkg/chat"
	"github.com/google/uuid"
)

var (
	ErrNotFound = errors.New("draft not found")
	ErrConflict = errors.New("draft conflict")
)

type Operation string

const (
	CreateDashboard Operation = "create_dashboard"
	ModifyDashboard Operation = "modify_dashboard"
)

type Command struct {
	Operation      Operation `json:"operation"`
	Instruction    string    `json:"instruction"`
	FolderUID      string    `json:"folderUid"`
	DatasourceUIDs []string  `json:"datasourceUids"`
	DashboardUID   string    `json:"dashboardUid,omitempty"`
	TargetPanelID  *int64    `json:"targetPanelId,omitempty"`
}

type JobDescriptor struct {
	RequestID string    `json:"requestId"`
	SessionID string    `json:"sessionId"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Draft struct {
	ID                 string         `json:"draftId"`
	Revision           int            `json:"revision"`
	Operation          Operation      `json:"operation"`
	Title              string         `json:"title"`
	FolderUID          string         `json:"folderUid"`
	TargetDashboardUID string         `json:"targetDashboardUid,omitempty"`
	Dashboard          map[string]any `json:"dashboard"`
	Warnings           []Warning      `json:"warnings"`
	ExpiresAt          time.Time      `json:"expiresAt"`
	Applied            bool           `json:"applied"`
	Applying           bool           `json:"-"`
	ApplyResult        map[string]any `json:"applyResult,omitempty"`
}

type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type job struct {
	descriptor JobDescriptor
	owner      chat.Principal
	command    Command
	claimed    bool
	cancel     context.CancelFunc
}

type Registry struct {
	mu          sync.Mutex
	jobs        map[string]*job
	drafts      map[string]*Draft
	draftOwners map[string]chat.Principal
	ttl         time.Duration
}

func NewRegistry(ttl time.Duration) *Registry {
	return &Registry{jobs: make(map[string]*job), drafts: make(map[string]*Draft), draftOwners: make(map[string]chat.Principal), ttl: ttl}
}

func (r *Registry) Create(owner chat.Principal, command Command) JobDescriptor {
	now := time.Now().UTC()
	d := JobDescriptor{RequestID: uuid.NewString(), SessionID: uuid.NewString(), ExpiresAt: now.Add(r.ttl)}
	r.mu.Lock()
	r.jobs[d.RequestID] = &job{descriptor: d, owner: owner, command: command}
	r.mu.Unlock()
	return d
}

func (r *Registry) Authorize(owner chat.Principal, requestID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[requestID]
	if j == nil || !j.owner.Equal(owner) || time.Now().After(j.descriptor.ExpiresAt) {
		return ErrNotFound
	}
	return nil
}

func (r *Registry) Claim(owner chat.Principal, requestID string, cancel context.CancelFunc) (JobDescriptor, Command, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.jobs[requestID]
	if j == nil || !j.owner.Equal(owner) || time.Now().After(j.descriptor.ExpiresAt) {
		return JobDescriptor{}, Command{}, ErrNotFound
	}
	if j.claimed {
		return JobDescriptor{}, Command{}, ErrConflict
	}
	j.claimed, j.cancel = true, cancel
	return j.descriptor, j.command, nil
}

func (r *Registry) Store(owner chat.Principal, command Command, dashboard map[string]any) Draft {
	id := uuid.NewString()
	title, _ := dashboard["title"].(string)
	draft := Draft{ID: id, Revision: 1, Operation: command.Operation, Title: title, FolderUID: command.FolderUID, TargetDashboardUID: command.DashboardUID,
		Dashboard: dashboard, Warnings: []Warning{{Code: "QUERY_NOT_EXECUTED", Message: "此 POC 尚未自動執行查詢驗證。"}}, ExpiresAt: time.Now().UTC().Add(r.ttl)}
	r.mu.Lock()
	r.drafts[id], r.draftOwners[id] = &draft, owner
	r.mu.Unlock()
	return draft
}

func (r *Registry) Get(owner chat.Principal, id string) (Draft, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.drafts[id]
	if !ok || !r.draftOwners[id].Equal(owner) || time.Now().After(d.ExpiresAt) {
		return Draft{}, ErrNotFound
	}
	return *d, nil
}

func (r *Registry) BeginApply(owner chat.Principal, id string, revision int) (Draft, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.drafts[id]
	if !ok || !r.draftOwners[id].Equal(owner) || time.Now().After(d.ExpiresAt) {
		return Draft{}, false, ErrNotFound
	}
	if d.Revision != revision || d.Applying {
		return Draft{}, false, ErrConflict
	}
	if d.Applied {
		return *d, true, nil
	}
	d.Applying = true
	return *d, false, nil
}

func (r *Registry) FinishApply(owner chat.Principal, id string, result map[string]any, applyErr error) (Draft, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.drafts[id]
	if !ok || !r.draftOwners[id].Equal(owner) {
		return Draft{}, ErrNotFound
	}
	d.Applying = false
	if applyErr == nil {
		d.Applied, d.ApplyResult = true, result
	}
	return *d, nil
}

func (r *Registry) Dispose() {
	r.mu.Lock()
	cancels := []context.CancelFunc{}
	for _, j := range r.jobs {
		if j.cancel != nil {
			cancels = append(cancels, j.cancel)
		}
	}
	r.jobs = map[string]*job{}
	r.drafts = map[string]*Draft{}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}
