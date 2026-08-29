package grafanamcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrUnavailable = errors.New("mcp-grafana unavailable")
	ErrToolDenied  = errors.New("MCP tool is not allowed")
)

var allowedTools = map[string]struct{}{
	"list_datasources":      {},
	"get_datasource_by_uid": {},
	"search_dashboards":     {},
	"get_dashboard_by_uid":  {},
	"update_dashboard":      {},
	"generate_deeplink":     {},
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

type Client struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	pending map[int64]chan response
	nextID  atomic.Int64
	done    chan struct{}
	err     error
}

func Start(ctx context.Context, binary string) (*Client, error) {
	if binary == "" {
		binary = "mcp-grafana"
	}
	cmd := exec.CommandContext(ctx, binary, "-t", "stdio", "--disable-alerting", "--disable-oncall", "--disable-incident", "--disable-admin", "--disable-sift")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdin: %v", ErrUnavailable, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: stdout: %v", ErrUnavailable, err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%w: start: %v", ErrUnavailable, err)
	}
	client := &Client{cmd: cmd, stdin: stdin, pending: make(map[int64]chan response), done: make(chan struct{})}
	go client.readLoop(stdout)
	go client.waitLoop()
	initCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var initialized json.RawMessage
	err = client.call(initCtx, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "acme-aiassistant-app", "version": "1.0.0"},
	}, &initialized)
	if err != nil {
		client.Close()
		return nil, err
	}
	if err := client.notify("notifications/initialized", map[string]any{}); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) ListTools(ctx context.Context) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.call(ctx, "tools/list", map[string]any{}, &result)
	return result, err
}

func (c *Client) CallTool(ctx context.Context, name string, arguments any) (json.RawMessage, error) {
	if _, ok := allowedTools[name]; !ok {
		return nil, fmt.Errorf("%w: %s", ErrToolDenied, name)
	}
	var result json.RawMessage
	err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result)
	return result, err
}

func (c *Client) call(ctx context.Context, method string, params any, destination any) error {
	id := c.nextID.Add(1)
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	c.pending[id] = ch
	c.mu.Unlock()
	request := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	if err := c.write(request); err != nil {
		c.removePending(id)
		return err
	}
	select {
	case <-ctx.Done():
		c.removePending(id)
		return ctx.Err()
	case <-c.done:
		return ErrUnavailable
	case resp := <-ch:
		if resp.Error != nil {
			return fmt.Errorf("MCP %s failed (%d): %s", method, resp.Error.Code, resp.Error.Message)
		}
		if destination != nil {
			if err := json.Unmarshal(resp.Result, destination); err != nil {
				return fmt.Errorf("decode MCP response: %w", err)
			}
		}
		return nil
	}
}

func (c *Client) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) write(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("%w: write: %v", ErrUnavailable, err)
	}
	return nil
}

func (c *Client) readLoop(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var resp response
		if json.Unmarshal(scanner.Bytes(), &resp) != nil || resp.ID == 0 {
			continue
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	c.fail(scanner.Err())
}

func (c *Client) waitLoop() { c.fail(c.cmd.Wait()) }

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		if err == nil {
			err = io.EOF
		}
		c.err = err
		close(c.done)
	}
	c.pending = make(map[int64]chan response)
	c.mu.Unlock()
}

func (c *Client) removePending(id int64) { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }

func (c *Client) Close() {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}
