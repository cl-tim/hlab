package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/output"
	printerconfig "github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/sshx"
	"github.com/cl-tim/hlab/internal/tlsmgr"
)

type tlsTarget struct {
	Ref        string
	Target     sshx.Target
	KnownHosts string
}
type tlsResult struct {
	Target string `json:"target" yaml:"target"`
	Action string `json:"action" yaml:"action"`
	Detail string `json:"detail" yaml:"detail"`
	Error  string `json:"error,omitempty" yaml:"error,omitempty"`
}

func newTLSCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{Use: "tls", Short: "Provision a private CA, device HTTPS, and system trust", Long: "Manage the private CA and device-local HTTPS listeners declared in tls:.\nCA and server private keys remain on their machines. Renewals run there,\nwithout this workstation. Preview commands are read-only; writes require --apply."}
	var caApply bool
	ca := &cobra.Command{Use: "ca", Short: "Provision the CA and pin its public root over verified SSH", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.tlsConfig()
		if err != nil {
			return err
		}
		h, ok := a.cfg.Inventory.Host(c.Authority)
		if !ok || !h.Managed {
			return fmt.Errorf("CA authority %q is not a managed host", c.Authority)
		}
		t := tlsTarget{"host:" + h.Name, targetFor(h), a.cfg.Defaults.KnownHostsFile}
		if !caApply {
			return a.renderTLS([]tlsResult{{t.Ref, "provision CA", c.DirectoryURL(), ""}}, false)
		}
		if a.insecureHostKey {
			return fmt.Errorf("TLS provisioning requires verified SSH host keys; remove --insecure-host-key")
		}
		cl, err := a.tlsDial(cmd.Context(), t)
		if err != nil {
			return err
		}
		defer cl.Close()
		// Check an existing pin before changing anything. Loss of CA state must
		// be repaired from backup, rather than silently creating a second CA.
		prior, e := os.ReadFile(a.tlsRootPath(c))
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		r, err := tlsExec(cmd.Context(), cl, "if test -f "+tlsmgr.RootPath+"; then cat "+tlsmgr.RootPath+"; fi")
		if err != nil {
			return err
		}
		if len(prior) > 0 {
			if strings.TrimSpace(r) == "" {
				return fmt.Errorf("pinned CA exists but remote CA state is missing; restore /var/lib/hlab-tls from backup")
			}
			if _, err = tlsmgr.Pin(a.tlsRootPath(c), []byte(r)); err != nil {
				return err
			}
		}
		if err = a.provisionTLS(cmd.Context(), cl, c, t.Ref, nil); err != nil {
			return err
		}
		var raw string
		for i := 0; i < 20; i++ {
			raw, err = tlsExec(cmd.Context(), cl, "cat "+tlsmgr.RootPath)
			if err == nil {
				break
			}
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		if err != nil {
			return fmt.Errorf("read public CA root: %w", err)
		}
		fp, err := tlsmgr.Pin(a.tlsRootPath(c), []byte(raw))
		if err != nil {
			return err
		}
		script, err := tlsmgr.TrustScript([]byte(raw))
		if err != nil {
			return err
		}
		if _, err = tlsExec(cmd.Context(), cl, script); err != nil {
			return fmt.Errorf("CA provisioned but authority trust failed: %w", err)
		}
		return a.renderTLS([]tlsResult{{t.Ref, "CA ready", c.DirectoryURL() + "; SHA256 " + fp, ""}}, false)
	}}
	ca.Flags().BoolVar(&caApply, "apply", false, "provision the CA and pin its public root")
	cmd.AddCommand(ca)
	cmd.AddCommand(newTLSDNSCmd(a))
	cmd.AddCommand(newTLSSignCmd(a))
	for _, action := range []string{"apply", "trust"} {
		action := action
		var apply, printers, local bool
		sub := &cobra.Command{Use: action + " [selector]", Short: map[string]string{"apply": "Provision device-local HTTPS and automatic certificate renewal", "trust": "Install the pinned CA into enrolled machines' system trust stores"}[action], Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.tlsConfig()
			if err != nil {
				return err
			}
			if local {
				if action != "trust" || printers || len(args) > 0 {
					return fmt.Errorf("--local is only valid with tls trust and no selector or --printers")
				}
				if !apply {
					return a.renderTLS([]tlsResult{{"local", "install trust", a.tlsRootPath(c), ""}}, false)
				}
				raw, err := a.tlsRoot(c)
				if err != nil {
					return err
				}
				if err = installLocalTrust(cmd.Context(), a.tlsRootPath(c), raw); err != nil {
					return err
				}
				return a.renderTLS([]tlsResult{{"local", "trusted", a.tlsRootPath(c), ""}}, false)
			}
			targets, err := a.tlsTargets(args, printers)
			if err != nil {
				return err
			}
			var results []tlsResult
			var raw []byte
			if apply {
				raw, err = a.tlsRoot(c)
				if err != nil {
					return err
				}
				if a.insecureHostKey {
					return fmt.Errorf("TLS provisioning requires verified SSH host keys")
				}
			}
			failed := false
			for _, t := range targets {
				detail := "system CA trust store"
				if action == "apply" {
					var names []string
					for _, s := range c.Services {
						if s.Target == t.Ref {
							names = append(names, s.URLs()...)
						}
					}
					if len(names) == 0 {
						continue
					}
					detail = strings.Join(names, ", ")
				}
				r := tlsResult{t.Ref, action, detail, ""}
				if apply {
					ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
					cl, e := a.tlsDial(ctx, t)
					if e == nil {
						script, _ := tlsmgr.TrustScript(raw)
						_, e = tlsExec(ctx, cl, script)
						if e == nil && action == "apply" {
							e = a.provisionTLS(ctx, cl, c, t.Ref, raw)
						}
						cl.Close()
					}
					cancel()
					if e != nil {
						r.Error = e.Error()
						failed = true
					} else {
						r.Action += " complete"
					}
					fmt.Fprintf(a.errOut, "%s: %s\n", t.Ref, orTLSResult(r))
				}
				results = append(results, r)
			}
			if len(results) == 0 {
				return fmt.Errorf("no configured TLS services matched the selection")
			}
			return a.renderTLS(results, failed)
		}}
		sub.Flags().BoolVar(&apply, "apply", false, "perform the changes; otherwise preview only")
		sub.Flags().BoolVar(&printers, "printers", false, "resolve the selector against enrolled printers instead of managed hosts")
		if action == "trust" {
			sub.Flags().BoolVar(&local, "local", false, "install into this workstation's trust store (Windows: current user)")
		}
		cmd.AddCommand(sub)
	}
	cmd.AddCommand(&cobra.Command{Use: "status", Short: "Verify every declared HTTPS endpoint against the pinned root", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.tlsConfig()
		if err != nil {
			return err
		}
		raw, err := a.tlsRoot(c)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(raw)
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		var results []tlsResult
		failed := false
		urls := []struct{ target, address string }{{"host:" + c.Authority, strings.TrimSuffix(c.DirectoryURL(), "/acme/hlab/directory") + "/healthz"}}
		for _, s := range c.Services {
			for _, u := range s.URLs() {
				urls = append(urls, struct{ target, address string }{s.Target, u})
			}
		}
		for _, u := range urls {
			r := tlsResult{u.target, "verify", u.address, ""}
			req, _ := http.NewRequestWithContext(cmd.Context(), http.MethodGet, u.address, nil)
			resp, e := client.Do(req)
			if e != nil {
				r.Error = e.Error()
				failed = true
			} else {
				resp.Body.Close()
				r.Detail += fmt.Sprintf(" HTTP %d; expires %s", resp.StatusCode, resp.TLS.PeerCertificates[0].NotAfter.UTC().Format(time.RFC3339))
				if resp.StatusCode >= 400 && resp.StatusCode != 401 && resp.StatusCode != 403 {
					r.Error = "upstream failed"
					failed = true
				}
			}
			results = append(results, r)
		}
		return a.renderTLS(results, failed)
	}})
	var destination string
	export := &cobra.Command{Use: "export", Short: "Export the public CA certificate for unmanaged clients", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		c, err := a.tlsConfig()
		if err != nil {
			return err
		}
		raw, err := a.tlsRoot(c)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = f.Write(raw)
		e := f.Close()
		if err == nil {
			err = e
		}
		return err
	}}
	export.Flags().StringVar(&destination, "out", "", "destination for the public PEM root (must not exist)")
	_ = export.MarkFlagRequired("out")
	cmd.AddCommand(export)
	return cmd
}

func (a *app) tlsConfig() (tlsmgr.Config, error) {
	if a.cfg == nil || a.cfg.TLS == nil {
		return tlsmgr.Config{}, fmt.Errorf("no tls: configuration; see https://cl-tim.github.io/hlab/linux/tls/")
	}
	c := a.cfg.TLS.Defaults()
	if err := c.Validate(); err != nil {
		return c, err
	}
	if h, ok := a.cfg.Inventory.Host(c.Authority); !ok || !h.Managed {
		return c, fmt.Errorf("TLS authority %q is not a managed host", c.Authority)
	}
	var printers *printerconfig.Config
	for _, s := range c.Services {
		kind, name, _ := strings.Cut(s.Target, ":")
		if kind == "host" {
			if h, ok := a.cfg.Inventory.Host(name); !ok || !h.Managed {
				return c, fmt.Errorf("TLS service %s refers to unmanaged or unknown host %q", s.Name, name)
			}
		} else {
			if printers == nil {
				var err error
				printers, err = printerconfig.Load(a.cfg.Path)
				if err != nil {
					return c, err
				}
			}
			found := false
			for _, p := range printers.Inventory.Printers() {
				if p.Name == name {
					found = true
				}
			}
			if !found {
				return c, fmt.Errorf("TLS service %s refers to unknown printer %q", s.Name, name)
			}
		}
	}
	return c, nil
}

func (a *app) tlsRootPath(c tlsmgr.Config) string {
	key := sha256.Sum256([]byte(c.Authority + "\x00" + c.DirectoryURL()))
	return filepath.Join(config.Home(), "pki", hex.EncodeToString(key[:12]), "root.crt")
}

func (a *app) tlsRoot(c tlsmgr.Config) ([]byte, error) {
	raw, err := os.ReadFile(a.tlsRootPath(c))
	if err != nil {
		return nil, fmt.Errorf("load pinned CA (run hlab tls ca --apply first): %w", err)
	}
	_, _, err = tlsmgr.Root(raw)
	return raw, err
}

func (a *app) tlsTargets(args []string, printers bool) ([]tlsTarget, error) {
	var out []tlsTarget
	if !printers {
		hosts, err := a.managedHosts(args)
		if err != nil {
			return nil, err
		}
		for _, h := range hosts {
			out = append(out, tlsTarget{"host:" + h.Name, targetFor(h), a.cfg.Defaults.KnownHostsFile})
		}
		return out, nil
	}
	cfg, err := printerconfig.Load(a.cfg.Path)
	if err != nil {
		return nil, err
	}
	expr := "all"
	if len(args) > 0 {
		expr = args[0]
	}
	ps, err := cfg.Inventory.Select(expr)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		out = append(out, tlsTarget{"printer:" + p.Name, sshx.Target{Name: p.Name, Address: p.Address, Port: p.Port, User: p.User, IdentityFile: p.IdentityFile}, cfg.Defaults.KnownHostsFile})
	}
	return out, nil
}

func (a *app) tlsDial(ctx context.Context, t tlsTarget) (*ssh.Client, error) {
	d, err := sshx.NewDialer(sshx.Options{KnownHostsFile: t.KnownHosts, ConnectTimeout: a.resolveConnectTimeout()})
	if err != nil {
		return nil, err
	}
	return d.Dial(ctx, t.Target)
}

func tlsExec(ctx context.Context, cl *ssh.Client, script string) (string, error) {
	id, err := sshx.Exec(ctx, cl, "id -u", nil)
	if err != nil {
		return "", err
	}
	command := "sudo -n sh -s"
	if strings.TrimSpace(id.Stdout) == "0" {
		command = "sh -s"
	}
	r, err := sshx.Exec(ctx, cl, command, strings.NewReader(script))
	if err != nil {
		return "", err
	}
	if !r.OK() {
		return "", fmt.Errorf("remote command exited %d: %s", r.ExitCode, strings.TrimSpace(r.Stderr))
	}
	return r.Stdout, nil
}

func (a *app) provisionTLS(ctx context.Context, cl *ssh.Client, c tlsmgr.Config, target string, root []byte) error {
	script, err := c.Preflight(target)
	if err != nil {
		return err
	}
	facts, err := tlsExec(ctx, cl, script)
	if err != nil {
		return fmt.Errorf("HTTPS preflight: %w", err)
	}
	lines := strings.Fields(facts)
	if len(lines) < 2 {
		return fmt.Errorf("missing machine architecture")
	}
	arch, err := tlsmgr.Architecture(lines[len(lines)-2], lines[len(lines)-1])
	if err != nil {
		return err
	}
	version, _ := tlsExec(ctx, cl, "if test -x /usr/local/lib/hlab/caddy; then /usr/local/lib/hlab/caddy version; fi")
	if !strings.HasPrefix(version, "v"+tlsmgr.CaddyVersion+" ") {
		fmt.Fprintf(a.errOut, "%s: installing checksum-verified Caddy %s (%s)\n", target, tlsmgr.CaddyVersion, arch)
		binary, err := tlsmgr.Download(ctx, filepath.Join(config.Home(), "cache", "tls"), arch)
		if err != nil {
			return err
		}
		h := sha256.Sum256(binary)
		command := tlsmgr.BinaryCommand(hex.EncodeToString(h[:]))
		id, err := sshx.Exec(ctx, cl, "id -u", nil)
		if err != nil {
			return err
		}
		if strings.TrimSpace(id.Stdout) == "0" {
			command = strings.TrimPrefix(command, "sudo -n ")
		}
		r, err := sshx.Exec(ctx, cl, command, bytes.NewReader(binary))
		if err != nil {
			return err
		}
		if !r.OK() {
			return fmt.Errorf("install Caddy: %s", r.Stderr)
		}
	}
	rendered, err := c.Render(target)
	if err != nil {
		return err
	}
	script, err = tlsmgr.InstallScript(rendered, root, c.Resolvers)
	if err != nil {
		return err
	}
	_, err = tlsExec(ctx, cl, script)
	if err != nil {
		return err
	}
	if len(root) > 0 {
		if err := waitForDeviceTLS(ctx, c, target, root); err != nil {
			return fmt.Errorf("HTTPS listener configured, but certificate verification failed: %w", err)
		}
	}
	return nil
}

func waitForDeviceTLS(ctx context.Context, c tlsmgr.Config, target string, root []byte) error {
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(root)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var urls []string
	for _, s := range c.Services {
		if s.Target == target {
			urls = append(urls, s.URLs()...)
		}
	}
	for _, address := range urls {
		address = strings.TrimPrefix(strings.TrimSuffix(address, "/"), "https://")
		if !strings.Contains(address, ":") {
			address += ":443"
		}
		var last error
		for {
			d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}
			conn, err := d.DialContext(ctx, "tcp", address)
			if err == nil {
				conn.Close()
				break
			}
			last = err
			select {
			case <-ctx.Done():
				return fmt.Errorf("%s: %w", address, last)
			case <-time.After(time.Second):
			}
		}
	}
	return nil
}

func orTLSResult(r tlsResult) string {
	if r.Error != "" {
		return r.Error
	}
	return r.Action
}
func (a *app) renderTLS(results []tlsResult, failed bool) error {
	sort.SliceStable(results, func(i, j int) bool { return results[i].Target < results[j].Target })
	tbl := output.Table{Headers: []string{"TARGET", "ACTION", "DETAIL", "ERROR"}}
	for _, r := range results {
		tbl.AddRow(r.Target, r.Action, r.Detail, r.Error)
	}
	if err := output.Write(a.out, a.format, results, tbl); err != nil {
		return err
	}
	if failed {
		return &ExitError{Code: 1}
	}
	return nil
}

func installLocalTrust(ctx context.Context, path string, raw []byte) error {
	if _, _, err := tlsmgr.Root(raw); err != nil {
		return err
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.CommandContext(ctx, "certutil", "-user", "-addstore", "Root", path)
	case "linux":
		script, err := tlsmgr.TrustScript(raw)
		if err != nil {
			return err
		}
		command = exec.CommandContext(ctx, "sudo", "-n", "sh", "-s")
		command.Stdin = strings.NewReader(script)
	default:
		return fmt.Errorf("local trust installation is supported on Windows and Linux; use tls export for this OS")
	}
	if out, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("install local CA trust: %w: %s", err, out)
	}
	return nil
}

// Automatically trust the pinned CA after enrolling a managed Linux host. It
// does not create the CA or accept a new root as a side effect of enrollment.
func (a *app) trustEnrolledHost(ctx context.Context, name string) error {
	cfg, err := config.Load(a.configWritePath())
	if err != nil {
		return err
	}
	if cfg.TLS == nil || !cfg.TLS.TrustOnEnroll {
		return nil
	}
	c := cfg.TLS.Defaults()
	if err = c.Validate(); err != nil {
		return err
	}
	raw, err := a.tlsRoot(c)
	if err != nil {
		return err
	}
	script, err := tlsmgr.TrustScript(raw)
	if err != nil {
		return err
	}
	h, ok := cfg.Inventory.Host(name)
	if !ok || !h.Managed {
		return fmt.Errorf("newly enrolled host is missing")
	}
	t := tlsTarget{"host:" + name, targetFor(h), cfg.Defaults.KnownHostsFile}
	cl, err := a.tlsDial(ctx, t)
	if err != nil {
		return err
	}
	defer cl.Close()
	_, err = tlsExec(ctx, cl, script)
	return err
}
