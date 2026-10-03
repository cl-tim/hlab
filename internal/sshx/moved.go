package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// DialMoved connects to a machine at an address other than the one it is
// recorded under, accepting it only if it presents a host key known_hosts
// already holds for the recorded address.
//
// That is the proof a DHCP move needs. A name resolving somewhere new says
// nothing about who is there — DNS is exactly what an attacker would poison —
// but a machine that holds the private half of the recorded host key is the
// machine that was recorded. Nothing is trusted that was not already trusted;
// the key is only allowed to vouch for a second address.
//
// On success the key is recorded under the new address as well, so ordinary
// verification works there from then on.
func (d *Dialer) DialMoved(ctx context.Context, t Target, newAddress string) (*ssh.Client, error) {
	if d.opts.InsecureIgnoreHostKey {
		return nil, errors.New("host key checking is off, so a moved machine cannot be proved to be the same one")
	}
	path := d.opts.KnownHostsFile
	known, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", path, err)
	}

	auths, err := authMethods(t)
	if err != nil {
		return nil, err
	}

	recorded := t.addr()
	var presented ssh.PublicKey
	vouch := func(_ string, remote net.Addr, key ssh.PublicKey) error {
		if err := known(recorded, remote, key); err != nil {
			var keyErr *knownhosts.KeyError
			if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
				return fmt.Errorf("no host key is recorded for %s, so nothing can prove the machine at %s is the same one",
					recorded, remote)
			}
			return err
		}
		presented = key
		return nil
	}

	moved := t
	moved.Address = newAddress
	client, err := d.dialWith(ctx, moved, auths, vouch)
	if err != nil {
		return nil, err
	}

	if err := appendKnownHost(path, moved.addr(), nil, presented); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
