package setup

import (
	"errors"
	"testing"

	"github.com/lxc/incus/v6/shared/api"
)

func TestEnsureRemoteTrustedSkipsTrustWhenAlreadyTrusted(t *testing.T) {
	t.Parallel()

	server := &fakeRemoteTrustClient{
		serverResponses: []api.Server{
			{ServerUntrusted: api.ServerUntrusted{Auth: "trusted"}},
		},
	}

	if err := ensureRemoteTrusted(server, "unused-token"); err != nil {
		t.Fatalf("ensureRemoteTrusted returned an error: %v", err)
	}

	if server.createCalls != 0 {
		t.Fatalf("expected no certificate creation calls, got %d", server.createCalls)
	}
}

func TestEnsureRemoteTrustedUsesTokenWhenUntrusted(t *testing.T) {
	t.Parallel()

	server := &fakeRemoteTrustClient{
		serverResponses: []api.Server{
			{ServerUntrusted: api.ServerUntrusted{Auth: "untrusted"}},
			{ServerUntrusted: api.ServerUntrusted{Auth: "trusted"}},
		},
	}

	if err := ensureRemoteTrusted(server, "token-value"); err != nil {
		t.Fatalf("ensureRemoteTrusted returned an error: %v", err)
	}

	if server.createCalls != 1 {
		t.Fatalf("expected one certificate creation call, got %d", server.createCalls)
	}

	if server.lastRequest.TrustToken != "token-value" {
		t.Fatalf("expected trust token to be forwarded, got %q", server.lastRequest.TrustToken)
	}

	if server.lastRequest.Type != api.CertificateTypeClient {
		t.Fatalf("expected client certificate type, got %q", server.lastRequest.Type)
	}
}

func TestEnsureRemoteTrustedFailsWhenServerStaysUntrusted(t *testing.T) {
	t.Parallel()

	server := &fakeRemoteTrustClient{
		serverResponses: []api.Server{
			{ServerUntrusted: api.ServerUntrusted{Auth: "untrusted"}},
			{ServerUntrusted: api.ServerUntrusted{Auth: "untrusted"}},
		},
	}

	err := ensureRemoteTrusted(server, "token-value")
	if err == nil {
		t.Fatal("expected ensureRemoteTrusted to fail")
	}

	if err.Error() != "the Incus server does not trust the local client after applying the trust token" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureRemoteTrustedWrapsCreateCertificateError(t *testing.T) {
	t.Parallel()

	server := &fakeRemoteTrustClient{
		serverResponses: []api.Server{
			{ServerUntrusted: api.ServerUntrusted{Auth: "untrusted"}},
		},
		createErr: errors.New("boom"),
	}

	err := ensureRemoteTrusted(server, "token-value")
	if err == nil {
		t.Fatal("expected ensureRemoteTrusted to fail")
	}

	if got := err.Error(); got != "trusting the local client with the remote Incus server: boom" {
		t.Fatalf("unexpected error: %v", err)
	}
}

type fakeRemoteTrustClient struct {
	serverResponses []api.Server
	createErr       error
	createCalls     int
	lastRequest     api.CertificatesPost
}

func (f *fakeRemoteTrustClient) GetServer() (*api.Server, string, error) {
	if len(f.serverResponses) == 0 {
		return &api.Server{ServerUntrusted: api.ServerUntrusted{Auth: "untrusted"}}, "", nil
	}

	response := f.serverResponses[0]
	f.serverResponses = f.serverResponses[1:]
	return &response, "", nil
}

func (f *fakeRemoteTrustClient) CreateCertificate(req api.CertificatesPost) error {
	f.createCalls++
	f.lastRequest = req
	return f.createErr
}
