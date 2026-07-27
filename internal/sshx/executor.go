package sshx

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Result is the outcome of running one command against one host.
type Result struct {
	Host     string `json:"host" yaml:"host"`
	Command  string `json:"command" yaml:"command"`
	Stdout   string `json:"stdout" yaml:"stdout"`
	Stderr   string `json:"stderr" yaml:"stderr"`
	ExitCode int    `json:"exitCode" yaml:"exitCode"`
	// Duration is the wall time for this host. It is not serialised directly
	// because time.Duration encodes as nanoseconds; DurationMs is emitted
	// instead and is populated by the executor before returning.
	Duration   time.Duration `json:"-" yaml:"-"`
	DurationMs int64         `json:"durationMs" yaml:"durationMs"`
	// Err is set when the host could not be reached or the command could not
	// be started. A command that ran and exited non-zero is a successful
	// execution with a non-zero ExitCode, not an Err.
	Err error `json:"-" yaml:"-"`
	// Error is the string form of Err, for structured output.
	Error string `json:"error,omitempty" yaml:"error,omitempty"`
}

// OK reports whether the host was reachable and the command exited zero.
func (r Result) OK() bool { return r.Err == nil && r.ExitCode == 0 }

// Executor fans a command out across many hosts with bounded concurrency.
type Executor struct {
	dialer      *Dialer
	concurrency int
}

// NewExecutor returns an Executor that runs at most concurrency connections at
// a time. A concurrency of zero or less means "one at a time".
func NewExecutor(d *Dialer, concurrency int) *Executor {
	if concurrency < 1 {
		concurrency = 1
	}
	return &Executor{dialer: d, concurrency: concurrency}
}

// Run executes command on every target and returns one Result per target,
// sorted by host name. It does not return an error for individual host
// failures; inspect the Results. Cancelling ctx aborts in-flight work.
func (e *Executor) Run(ctx context.Context, targets []Target, command string) []Result {
	results := make([]Result, len(targets))

	sem := make(chan struct{}, e.concurrency)
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		go func(idx int, target Target) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = Result{Host: target.Name, Command: command, Err: ctx.Err(), ExitCode: -1}
				return
			}

			results[idx] = e.runOne(ctx, target, command)
		}(i, t)
	}
	wg.Wait()

	return finalize(results)
}

// finalize fills in the serialisable mirror fields and sorts by host name so
// output is stable regardless of completion order.
func finalize(results []Result) []Result {
	for i := range results {
		if results[i].Err != nil {
			results[i].Error = results[i].Err.Error()
		}
		results[i].DurationMs = results[i].Duration.Milliseconds()
	}
	sort.Slice(results, func(a, b int) bool { return results[a].Host < results[b].Host })
	return results
}

// runOne dials a single host, runs the command, and captures its streams.
func (e *Executor) runOne(ctx context.Context, t Target, command string) Result {
	start := time.Now()
	res := Result{Host: t.Name, Command: command}

	client, err := e.dialer.Dial(ctx, t)
	if err != nil {
		res.Err = err
		res.ExitCode = -1
		res.Duration = time.Since(start)
		return res
	}
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		res.Err = err
		res.ExitCode = -1
		res.Duration = time.Since(start)
		return res
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	// Close the session if the context is cancelled mid-run, which unblocks
	// session.Run below.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGTERM)
			_ = session.Close()
		case <-done:
		}
	}()

	runErr := session.Run(command)

	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	res.Duration = time.Since(start)

	switch {
	case runErr == nil:
		res.ExitCode = 0
	default:
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			// The command ran to completion and reported a non-zero status.
			res.ExitCode = exitErr.ExitStatus()
		} else {
			res.Err = runErr
			res.ExitCode = -1
		}
	}

	return res
}

// Ping opens a connection to every target and immediately closes it, which
// verifies reachability, authentication and host key trust without running a
// remote command.
func (e *Executor) Ping(ctx context.Context, targets []Target) []Result {
	results := make([]Result, len(targets))

	sem := make(chan struct{}, e.concurrency)
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		go func(idx int, target Target) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[idx] = Result{Host: target.Name, Err: ctx.Err(), ExitCode: -1}
				return
			}

			start := time.Now()
			r := Result{Host: target.Name}
			client, err := e.dialer.Dial(ctx, target)
			if err != nil {
				r.Err = err
				r.ExitCode = -1
			} else {
				_ = client.Close()
			}
			r.Duration = time.Since(start)
			results[idx] = r
		}(i, t)
	}
	wg.Wait()

	return finalize(results)
}
