//go:build !fips && !fips3

package http

import (
	nethttp "net/http"

	ntlmssp "github.com/Azure/go-ntlmssp"
)

func ntlmTransport(roundTripper nethttp.RoundTripper) (nethttp.RoundTripper, error) {
	return ntlmssp.Negotiator{RoundTripper: roundTripper}, nil
}
