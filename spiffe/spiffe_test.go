package spiffe

import "testing"

func TestTrustDomainID(t *testing.T) {
	td := TrustDomainID()
	if td.IsZero() {
		t.Fatal("trust domain should parse to a non-zero value")
	}
	if got := td.Name(); got != "sentiae.io" {
		t.Fatalf("trust domain name = %q, want sentiae.io", got)
	}
}

func TestServiceID(t *testing.T) {
	id := ServiceID("foundry")
	if want := "spiffe://sentiae.io/svc/foundry"; id.String() != want {
		t.Fatalf("ServiceID = %q, want %q", id.String(), want)
	}
	if id.Path() != "/svc/foundry" {
		t.Fatalf("ServiceID path = %q, want /svc/foundry", id.Path())
	}
	if !id.MemberOf(TrustDomainID()) {
		t.Fatal("ServiceID should be a member of the trust domain")
	}
}
