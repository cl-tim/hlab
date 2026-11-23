package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/cl-tim/hlab/internal/tlsmgr"
	"github.com/spf13/cobra"
)

func newTLSSignCmd(a *app) *cobra.Command {
	var request, destination string
	var days int
	var apply bool
	cmd := &cobra.Command{Use: "sign", Short: "Sign an appliance CSR without transferring its private key", Long: "Sign a CSR for identities explicitly listed in tls.externalNames.\nThe device generates and retains its key. The returned PEM contains the server\ncertificate followed by the intermediate; use tls export for the root.\nManual appliance certificates require renewal before expiry (maximum 90 days).", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		c, err := a.tlsConfig()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(request)
		if err != nil {
			return err
		}
		csr, err := c.CheckCSR(raw)
		if err != nil {
			return err
		}
		script, err := c.SignScript(raw, days)
		if err != nil {
			return err
		}
		var names = append([]string{}, csr.DNSNames...)
		for _, ip := range csr.IPAddresses {
			names = append(names, ip.String())
		}
		if !apply {
			return a.renderTLS([]tlsResult{{"appliance", "sign CSR", fmt.Sprintf("%s; %d days; output %s", strings.Join(names, ", "), days, destination), ""}}, false)
		}
		if a.insecureHostKey {
			return fmt.Errorf("CSR signing requires verified SSH host keys")
		}
		if _, e := os.Stat(destination); !os.IsNotExist(e) {
			return fmt.Errorf("output must not exist: %s", destination)
		}
		root, err := a.tlsRoot(c)
		if err != nil {
			return err
		}
		h, _ := a.cfg.Inventory.Host(c.Authority)
		cl, err := a.tlsDial(cmd.Context(), tlsTarget{"host:" + h.Name, targetFor(h), a.cfg.Defaults.KnownHostsFile})
		if err != nil {
			return err
		}
		defer cl.Close()
		remote, err := tlsExec(cmd.Context(), cl, "cat "+tlsmgr.RootPath)
		if err != nil {
			return err
		}
		if _, err = tlsmgr.Pin(a.tlsRootPath(c), []byte(remote)); err != nil {
			return err
		}
		chain, err := tlsExec(cmd.Context(), cl, script)
		if err != nil {
			return err
		}
		if err = tlsmgr.VerifyIssued([]byte(chain), csr, root); err != nil {
			return err
		}
		f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(chain)
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		return a.renderTLS([]tlsResult{{"appliance", "signed", destination, ""}}, false)
	}}
	cmd.Flags().StringVar(&request, "csr", "", "PEM CSR generated on the appliance")
	cmd.Flags().StringVar(&destination, "out", "", "new PEM file for the certificate and intermediate chain")
	cmd.Flags().IntVar(&days, "days", 90, "validity in days (1–90)")
	cmd.Flags().BoolVar(&apply, "apply", false, "issue the certificate and write the output file")
	_ = cmd.MarkFlagRequired("csr")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}
