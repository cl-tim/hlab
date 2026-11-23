// Package tlsmgr provisions a private Caddy CA, device-local HTTPS listeners,
// and pinned public trust anchors. CA and leaf private keys never leave servers.
package tlsmgr

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const CaddyVersion = "2.11.9"

// Config is deliberately opt-in. Targets refer to a managed host or an enrolled
// printer, never to credentials duplicated in the TLS configuration.
type Config struct {
	Authority     string    `yaml:"authority" json:"authority"`
	AuthorityDNS  string    `yaml:"authorityDNS" json:"authorityDNS"`
	AuthorityPort int       `yaml:"authorityPort,omitempty" json:"authorityPort"`
	Name          string    `yaml:"name,omitempty" json:"name"`
	TrustOnEnroll bool      `yaml:"trustOnEnroll,omitempty" json:"trustOnEnroll"`
	Resolvers     []string  `yaml:"resolvers,omitempty" json:"resolvers,omitempty"`
	ExternalNames []string  `yaml:"externalNames,omitempty" json:"externalNames,omitempty"`
	Services      []Service `yaml:"services" json:"services"`
}

type Service struct {
	Name     string   `yaml:"name" json:"name"`
	Target   string   `yaml:"target" json:"target"`
	Names    []string `yaml:"names" json:"names"`
	Port     int      `yaml:"port,omitempty" json:"port"`
	Upstream string   `yaml:"upstream" json:"upstream"`
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validDNS(s string) bool {
	if len(s) > 253 || !strings.Contains(s, ".") || net.ParseIP(s) != nil {
		return false
	}
	for _, p := range strings.Split(s, ".") {
		if !dnsLabel.MatchString(p) {
			return false
		}
	}
	return true
}

func (c Config) Defaults() Config {
	c.Services = append([]Service(nil), c.Services...)
	if c.AuthorityPort == 0 {
		c.AuthorityPort = 8443
	}
	if c.Name == "" {
		c.Name = "hlab Internal CA"
	}
	for i := range c.Services {
		if c.Services[i].Port == 0 {
			c.Services[i].Port = 443
		}
	}
	return c
}

func (c Config) Validate() error {
	c = c.Defaults()
	if c.Authority == "" || strings.ContainsAny(c.Authority, "\r\n\t ") {
		return fmt.Errorf("tls.authority must name one managed host")
	}
	if !validDNS(c.AuthorityDNS) {
		return fmt.Errorf("tls.authorityDNS must be a lowercase fully-qualified DNS name")
	}
	if c.AuthorityPort < 1024 || c.AuthorityPort > 65535 {
		return fmt.Errorf("tls.authorityPort must be between 1024 and 65535")
	}
	if len(c.Name) > 120 || strings.ContainsAny(c.Name, "\r\n{}\"\\") {
		return fmt.Errorf("invalid TLS authority name")
	}
	for _, r := range c.Resolvers {
		if net.ParseIP(r) == nil {
			return fmt.Errorf("TLS resolver must be an IP address: %q", r)
		}
	}
	for _, n := range c.ExternalNames {
		if !validDNS(n) && net.ParseIP(n) == nil {
			return fmt.Errorf("invalid external certificate name %q", n)
		}
	}
	seen := map[string]bool{}
	listeners := map[string]string{}
	names := map[string]string{c.AuthorityDNS: "host:" + c.Authority}
	for _, s := range c.Services {
		if !dnsLabel.MatchString(s.Name) || seen[s.Name] {
			return fmt.Errorf("TLS service name %q is invalid or duplicated", s.Name)
		}
		seen[s.Name] = true
		kind, id, ok := strings.Cut(s.Target, ":")
		if !ok || (kind != "host" && kind != "printer") || id == "" || strings.ContainsAny(id, "\n\r\t ") {
			return fmt.Errorf("%s: target must be host:<name> or printer:<name>", s.Name)
		}
		if s.Port < 1 || s.Port > 65535 || s.Port == 80 {
			return fmt.Errorf("%s: invalid HTTPS port (port 80 is preserved)", s.Name)
		}
		if s.Target == "host:"+c.Authority && s.Port == c.AuthorityPort {
			return fmt.Errorf("%s: port is reserved for the CA", s.Name)
		}
		if len(s.Names) == 0 {
			return fmt.Errorf("%s: at least one DNS name is required", s.Name)
		}
		for _, n := range s.Names {
			if !validDNS(n) {
				return fmt.Errorf("%s: invalid DNS name %q", s.Name, n)
			}
			key := s.Target + ":" + strconv.Itoa(s.Port) + ":" + n
			if listeners[key] != "" {
				return fmt.Errorf("duplicate HTTPS listener for %s", n)
			}
			listeners[key] = s.Name
			if target := names[n]; target != "" && target != s.Target {
				return fmt.Errorf("%s is assigned to two different devices", n)
			}
			names[n] = s.Target
		}
		u, err := url.Parse(s.Upstream)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("%s: upstream must be an HTTP(S) origin", s.Name)
		}
		if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
			return fmt.Errorf("%s: upstream must be loopback so HTTPS terminates on the service's device", s.Name)
		}
		if p := u.Port(); p != "" {
			v, e := strconv.Atoi(p)
			if e != nil || v < 1 || v > 65535 {
				return fmt.Errorf("%s: invalid upstream port", s.Name)
			}
		}
		p := u.Port()
		if p == "" {
			if u.Scheme == "https" {
				p = "443"
			} else {
				p = "80"
			}
		}
		if p == strconv.Itoa(s.Port) {
			return fmt.Errorf("%s: proxy would loop back to its own listener", s.Name)
		}
	}
	return nil
}

func (c Config) DirectoryURL() string {
	c = c.Defaults()
	return "https://" + net.JoinHostPort(c.AuthorityDNS, strconv.Itoa(c.AuthorityPort)) + "/acme/hlab/directory"
}

func (s Service) URLs() []string {
	port := s.Port
	if port == 0 {
		port = 443
	}
	var out []string
	for _, n := range s.Names {
		if port == 443 {
			out = append(out, "https://"+n+"/")
		} else {
			out = append(out, "https://"+net.JoinHostPort(n, strconv.Itoa(port))+"/")
		}
	}
	return out
}

// Render gives each device only its listeners. Only the authority can issue;
// other devices receive the public root and enroll using TLS-ALPN-01 on 443.
func (c Config) Render(target string) (string, error) {
	c = c.Defaults()
	if err := c.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by hlab tls apply. Private keys stay in /var/lib/hlab-tls.\n# hlab target: %s\n{\n\tadmin unix//run/hlab-tls/admin.sock\n\tauto_https disable_redirects\n\tskip_install_trust\n\tgrace_period 10s\n", target)
	authority := target == "host:"+c.Authority
	if authority {
		fmt.Fprintf(&b, "\tpki {\n\t\tca hlab {\n\t\t\tname %q\n", c.Name)
		b.WriteString("\t\t}\n\t}\n")
	} else {
		fmt.Fprintf(&b, "\tacme_ca %s\n\tacme_ca_root /etc/hlab/tls/root.crt\n", c.DirectoryURL())
	}
	b.WriteString("}\n\n")
	if authority {
		fmt.Fprintf(&b, "https://%s:%d {\n\ttls {\n\t\tissuer internal {\n\t\t\tca hlab\n\t\t}\n\t}\n\tacme_server {\n\t\tca hlab\n\t\tchallenges tls-alpn-01\n", c.AuthorityDNS, c.AuthorityPort)
		var names []string
		for _, s := range c.Services {
			names = append(names, s.Names...)
		}
		for _, n := range c.ExternalNames {
			if net.ParseIP(n) == nil {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			names = []string{c.AuthorityDNS}
		}
		fmt.Fprintf(&b, "\t\tallow {\n\t\t\tdomains %s\n\t\t}\n\t}\n\trespond /healthz \"hlab CA\"\n}\n\n", strings.Join(names, " "))
	}
	for _, s := range c.Services {
		if s.Target != target {
			continue
		}
		var addresses []string
		for _, n := range s.Names {
			addresses = append(addresses, fmt.Sprintf("https://%s:%d", n, s.Port))
		}
		fmt.Fprintf(&b, "%s {\n", strings.Join(addresses, ", "))
		if authority {
			b.WriteString("\ttls {\n\t\tissuer internal {\n\t\t\tca hlab\n\t\t}\n\t}\n")
		} else {
			b.WriteString("\ttls {\n\t\tissuer acme {\n\t\t\tdisable_http_challenge\n\t\t}\n\t}\n")
		}
		fmt.Fprintf(&b, "\treverse_proxy %s\n}\n\n", s.Upstream)
	}
	return b.String(), nil
}
