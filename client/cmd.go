package client

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// CmdEndpoint is a persistent command session over a single tunnel.
//
// Protocol:
//   - Each Write is a complete command line (argv, space-separated).
//   - The process is spawned; its stdout+stderr stream out via Read.
//   - When the process exits, the endpoint goes idle and Read blocks
//     until the next command arrives.
//   - Read returns EOF only after Close.
//
// There is no stdin forwarding: once a command is running, further Writes
// are rejected until it exits.
type CmdEndpoint struct {
	mu      sync.Mutex
	stdout  *io.PipeReader // current process output; nil when idle
	cmd     *exec.Cmd      // current process; nil when idle
	pending bytes.Buffer   // status/error text queued for the remote side
	signal  chan struct{}  // buffered(1): wakes a blocked Read
	done    chan struct{}  // closed by Close
}

func NewCmdEndpoint() *CmdEndpoint {
	return &CmdEndpoint{
		signal: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// wake unblocks Read. Non-blocking: a pending wake is already enough.
func (c *CmdEndpoint) wake() {
	select {
	case c.signal <- struct{}{}:
	default:
	}
}

// Write parses p as a command line and spawns it. If a process is already
// running, the write is dropped with a queued error (the tunnel stays open).
// Spawn errors are also queued rather than returned, for the same reason.
func (c *CmdEndpoint) Write(p []byte) (int, error) {
	parts := strings.Fields(strings.TrimSpace(string(p)))
	if len(parts) == 0 {
		return len(p), nil
	}

	c.mu.Lock()
	busy := c.cmd != nil
	c.mu.Unlock()

	if busy {
		c.queueErr(fmt.Errorf("busy: command already running"))
		return len(p), nil
	}

	cmd := exec.Command(parts[0], parts[1:]...)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		c.queueErr(fmt.Errorf("start %q: %w", parts[0], err))
		return len(p), nil
	}

	c.mu.Lock()
	c.cmd = cmd
	c.stdout = pr
	c.mu.Unlock()
	c.wake()

	// Close the pipe writer when the process exits so Read sees EOF.
	go func() {
		_ = cmd.Wait()
		pw.Close()
	}()

	return len(p), nil
}

// Read drains queued messages, then streams the running process's output.
// On process exit, resets to idle and waits for the next command.
// Returns EOF only after Close.
func (c *CmdEndpoint) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		cur := c.stdout
		n, _ := c.pending.Read(p) // 0 on empty buffer
		c.mu.Unlock()

		if n > 0 {
			return n, nil
		}

		if cur == nil {
			select {
			case <-c.signal:
				continue
			case <-c.done:
				return 0, io.EOF
			}
		}

		n, err := cur.Read(p)
		if n > 0 {
			return n, nil
		}
		if err != nil {
			// Process exited — reset and wait for the next command.
			c.reset(cur)
			continue
		}
	}
}

// Close kills any running process and unblocks a waiting Read.
func (c *CmdEndpoint) Close() error {
	select {
	case <-c.done:
		return nil // already closed
	default:
		close(c.done)
	}

	c.mu.Lock()
	cmd := c.cmd
	stdout := c.stdout
	c.cmd = nil
	c.stdout = nil
	c.mu.Unlock()

	if cmd != nil {
		_ = cmd.Process.Kill() // Wait() goroutine closes pw
	}
	if stdout != nil {
		stdout.Close() // unblocks any pending Read
	}
	return nil
}

// reset clears cmd/stdout, but only if stdout still matches what the caller
// observed. Idempotent across the natural exit path.
func (c *CmdEndpoint) reset(want *io.PipeReader) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdout != want {
		return
	}
	c.stdout.Close()
	c.stdout = nil
	c.cmd = nil
}

// queueErr puts a remote-visible error message into pending and wakes Read.
func (c *CmdEndpoint) queueErr(err error) {
	c.mu.Lock()
	fmt.Fprintf(&c.pending, "error: %v\r\n", err)
	c.mu.Unlock()
	c.wake()
}
