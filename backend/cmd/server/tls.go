package main

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
)

// tlsClientWithCA builds an http.Client that trusts the provided CA bundle file.
func tlsClientWithCA(caPath string) (*http.Client, error) {
	ca, err := os.ReadFile(caPath)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}, nil
}
