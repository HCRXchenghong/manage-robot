package main

import (
	"crypto/x509"
	"net/url"
	"testing"
)

func TestCertificateSPIFFERequiresExactSingleGatewayIdentity(t *testing.T) {
	good, _ := url.Parse("spiffe://robot-agent/vehicle/veh-1/gateway/gw-1")
	got, err := certificateSPIFFE(&x509.Certificate{URIs: []*url.URL{good}})
	if err != nil || got != good.String() {
		t.Fatalf("valid SPIFFE rejected: got=%q err=%v", got, err)
	}
	for _, cert := range []*x509.Certificate{
		{},
		{URIs: []*url.URL{good, good}},
		{URIs: []*url.URL{mustURL(t, "spiffe://other/vehicle/veh-1/gateway/gw-1")}},
		{URIs: []*url.URL{mustURL(t, "https://robot-agent/vehicle/veh-1/gateway/gw-1")}},
	} {
		if _, err := certificateSPIFFE(cert); err == nil {
			t.Fatalf("invalid certificate identity accepted: %+v", cert.URIs)
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
