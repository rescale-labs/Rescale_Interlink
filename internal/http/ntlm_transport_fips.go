//go:build fips || fips3

package http

import (
	"fmt"
	nethttp "net/http"
)

func ntlmTransport(_ nethttp.RoundTripper) (nethttp.RoundTripper, error) {
	return nil, fmt.Errorf("NTLM proxy mode is disabled in FIPS builds because it requires non-FIPS MD4/MD5 algorithms")
}
