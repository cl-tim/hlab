// Package sshtest provides an in-process SSH server for tests, so the SSH code
// paths can be exercised end to end without a real machine.
package sshtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Response is what the server replies with for an exec request.
type Response struct {
	Stdout string
	Stderr string
	Exit   uint32
}

// Handler maps an incoming command to the response the server should send.
type Handler func(command string) Response

// Server is a running in-process SSH server.
type Server struct {
	Addr string
	Host string
	Port int
	// HostKey is the key the server presents, for tests that record it.
	HostKey ssh.PublicKey

	mu       sync.Mutex
	commands []string
}

// Commands returns every command the server was asked to run.
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// Start runs an SSH server on a random localhost port and registers cleanup.
// Any public key is accepted: authentication policy is not what these tests
// are checking.
func Start(t *testing.T, handler Handler) *Server {
	t.Helper()
	return StartWithKeys(t, handler, func(ssh.PublicKey) bool { return true })
}

// StartWithKeys is Start with a public-key policy, for testing what happens
// when a machine refuses a key.
func StartWithKeys(t *testing.T, handler Handler, accept func(ssh.PublicKey) bool) *Server {
	t.Helper()

	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !accept(key) {
				return nil, fmt.Errorf("key refused")
			}
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	srv := &Server{Addr: ln.Addr().String(), Host: host, Port: port, HostKey: hostSigner.PublicKey()}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go srv.handleConn(conn, cfg, handler)
		}
	}()

	return srv
}

func (s *Server) handleConn(conn net.Conn, cfg *ssh.ServerConfig, handler Handler) {
	defer func() { _ = conn.Close() }()

	sshConn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sshConn.Close() }()

	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only sessions supported")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, requests, handler)
	}
}

func (s *Server) handleSession(ch ssh.Channel, requests <-chan *ssh.Request, handler Handler) {
	defer func() { _ = ch.Close() }()

	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}

		var payload struct{ Command string }
		if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
			_ = req.Reply(false, nil)
			return
		}
		_ = req.Reply(true, nil)

		s.mu.Lock()
		s.commands = append(s.commands, payload.Command)
		s.mu.Unlock()

		resp := handler(payload.Command)
		_, _ = io.WriteString(ch, resp.Stdout)
		_, _ = io.WriteString(ch.Stderr(), resp.Stderr)
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{resp.Exit}))
		return
	}
}

// WriteKey writes a throwaway ed25519 private key and returns its path, for
// use as a Target's IdentityFile.
func WriteKey(t *testing.T) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "id_ed25519")
	body := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

// ClosedPort binds and immediately releases a port, so callers have an address
// where nothing is listening.
func ClosedPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}
