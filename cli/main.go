package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
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
		configureCertificateFile(path)
	}
	cmd.SetVersion(Version)
	cmd.SetCommit(Commit)
	cmd.Execute()
}

// Configure the default network clients before starting commands or workers.
func configureCertificateFile(path string) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	roots, err := readCertificateFile(path)
	if err != nil {
		// Proxy selection runs before dialing, including for direct connections.
		// Report the error only when a request needs TLS.
		proxy := func(req *http.Request) (*url.URL, error) {
			if req.URL.Scheme == "https" || req.URL.Scheme == "wss" {
				return nil, err
			}
			return http.ProxyFromEnvironment(req)
		}
		transport.Proxy = proxy
		websocket.DefaultDialer.Proxy = proxy
	} else {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.RootCAs = roots
		websocket.DefaultDialer.TLSClientConfig = &tls.Config{RootCAs: roots}
	}
	http.DefaultTransport = transport
}

func readCertificateFile(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SSL_CERT_FILE: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("SSL_CERT_FILE %q contains no PEM certificates", path)
	}
	return roots, nil
}
