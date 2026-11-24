package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"runtime"

	"cli.eigenflux.ai/cmd"
	"github.com/gorilla/websocket"
)

var (
	Version = "dev"
	Commit  = "unknown"
)

func main() {
	if path := os.Getenv("SSL_CERT_FILE"); runtime.GOOS == "darwin" && path != "" {
		if err := configureCertificateFile(path); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	cmd.SetVersion(Version)
	cmd.SetCommit(Commit)
	cmd.Execute()
}

// Configure the default network clients before starting commands or workers.
func configureCertificateFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read SSL_CERT_FILE: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return fmt.Errorf("SSL_CERT_FILE %q contains no PEM certificates", path)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.RootCAs = roots
	http.DefaultTransport = transport
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{RootCAs: roots}
	return nil
}
