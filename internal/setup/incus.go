package setup

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
	"github.com/lxc/incus/v6/shared/cliconfig"
	localtls "github.com/lxc/incus/v6/shared/tls"
)

const incusDefaultHTTPSAddress = ":8443"

type IncusStatus struct {
	Reachable bool
}

type IncusManager interface {
	ConfiguredServerStatus(ctx context.Context) (IncusStatus, error)
	HasRemote(ctx context.Context, remoteName string) (bool, error)
	AddRemote(ctx context.Context, remoteName, address, token string) error
	VerifyRemote(ctx context.Context, remoteName string) error
	SwitchRemote(ctx context.Context, remoteName string) error
	BootstrapServer(ctx context.Context, socketPath, bridgeName string) error
	CreateTrustToken(ctx context.Context, socketPath, clientName string) (string, error)
}

type realIncusManager struct{}

func newIncusManager() IncusManager {
	return realIncusManager{}
}

type remoteTrustClient interface {
	GetServer() (*api.Server, string, error)
	CreateCertificate(api.CertificatesPost) error
}

func BootstrapLocalLinuxServer(ctx context.Context) error {
	return newIncusManager().BootstrapServer(ctx, "", "")
}

func (realIncusManager) ConfiguredServerStatus(_ context.Context) (IncusStatus, error) {
	conf, _, err := loadIncusConfig()
	if err != nil {
		return IncusStatus{}, err
	}

	remoteName := conf.DefaultRemote
	if remoteName == "" {
		return IncusStatus{}, nil
	}

	if runtime.GOOS != "linux" && remoteName == "local" {
		return IncusStatus{}, nil
	}

	server, err := conf.GetInstanceServer(remoteName)
	if err != nil {
		return IncusStatus{}, nil
	}

	if _, _, err := server.GetServer(); err != nil {
		return IncusStatus{}, nil
	}

	return IncusStatus{Reachable: true}, nil
}

func (realIncusManager) HasRemote(_ context.Context, remoteName string) (bool, error) {
	conf, _, err := loadIncusConfig()
	if err != nil {
		return false, err
	}

	_, exists := conf.Remotes[remoteName]
	return exists, nil
}

func (realIncusManager) AddRemote(_ context.Context, remoteName, address, token string) error {
	conf, configPath, err := loadIncusConfig()
	if err != nil {
		return err
	}

	if _, exists := conf.Remotes[remoteName]; exists {
		return fmt.Errorf("Incus remote %q already exists", remoteName)
	}

	if err := ensureIncusConfigDir(conf); err != nil {
		return err
	}

	if !conf.HasClientCertificate() {
		if err := conf.GenerateClientCertificate(); err != nil {
			return fmt.Errorf("generating the Incus client certificate: %w", err)
		}
	}

	tokenInfo, err := localtls.CertificateTokenDecode(token)
	if err != nil {
		return fmt.Errorf("decoding the Incus trust token: %w", err)
	}

	conf.Remotes[remoteName] = cliconfig.Remote{
		Addr:     normalizeRemoteAddress(address),
		AuthType: api.AuthenticationMethodTLS,
		Protocol: "incus",
	}

	certificate, err := localtls.GetRemoteCertificate(conf.Remotes[remoteName].Addr, conf.UserAgent)
	if err != nil {
		return fmt.Errorf("retrieving the remote Incus certificate: %w", err)
	}

	if tokenInfo.Fingerprint != localtls.CertFingerprint(certificate) {
		return fmt.Errorf("the Incus certificate fingerprint for %q did not match the trust token", conf.Remotes[remoteName].Addr)
	}

	if err := saveRemoteCertificate(conf, remoteName, certificate); err != nil {
		return err
	}

	server, err := conf.GetInstanceServer(remoteName)
	if err != nil {
		return fmt.Errorf("connecting to the Incus remote %q: %w", remoteName, err)
	}

	if err := ensureRemoteTrusted(server, token); err != nil {
		return err
	}

	if err := saveIncusConfig(conf, configPath); err != nil {
		return err
	}

	return nil
}

func ensureRemoteTrusted(server remoteTrustClient, token string) error {
	serverInfo, _, err := server.GetServer()
	if err != nil {
		return fmt.Errorf("querying the Incus remote before trusting the local client: %w", err)
	}

	if serverInfo.Auth == "trusted" {
		return nil
	}

	if err := server.CreateCertificate(api.CertificatesPost{
		CertificatePut: api.CertificatePut{Type: api.CertificateTypeClient},
		TrustToken:     token,
	}); err != nil {
		return fmt.Errorf("trusting the local client with the remote Incus server: %w", err)
	}

	serverInfo, _, err = server.GetServer()
	if err != nil {
		return fmt.Errorf("querying the Incus remote after trusting the local client: %w", err)
	}

	if serverInfo.Auth != "trusted" {
		return errors.New("the Incus server does not trust the local client after applying the trust token")
	}

	return nil
}

func (realIncusManager) VerifyRemote(_ context.Context, remoteName string) error {
	conf, _, err := loadIncusConfig()
	if err != nil {
		return err
	}

	server, err := conf.GetInstanceServer(remoteName)
	if err != nil {
		return fmt.Errorf("connecting to the Incus remote %q: %w", remoteName, err)
	}

	if _, _, err := server.GetServer(); err != nil {
		return fmt.Errorf("querying the Incus remote %q: %w", remoteName, err)
	}

	return nil
}

func (realIncusManager) SwitchRemote(_ context.Context, remoteName string) error {
	conf, configPath, err := loadIncusConfig()
	if err != nil {
		return err
	}

	if _, exists := conf.Remotes[remoteName]; !exists {
		return fmt.Errorf("Incus remote %q does not exist", remoteName)
	}

	conf.DefaultRemote = remoteName
	return saveIncusConfig(conf, configPath)
}

func (realIncusManager) BootstrapServer(ctx context.Context, socketPath, bridgeName string) error {
	server, err := connectIncusUnix(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("connecting to the Incus daemon: %w", err)
	}

	serverInfo, serverETag, err := server.GetServer()
	if err != nil {
		return fmt.Errorf("loading the Incus server configuration: %w", err)
	}

	serverPut := serverInfo.Writable()
	if serverPut.Config == nil {
		serverPut.Config = map[string]string{}
	}

	if serverPut.Config["core.https_address"] != incusDefaultHTTPSAddress {
		serverPut.Config["core.https_address"] = incusDefaultHTTPSAddress
		if err := server.UpdateServer(serverPut, serverETag); err != nil {
			return fmt.Errorf("configuring the Incus API listener: %w", err)
		}
	}

	poolName, err := ensureDefaultStoragePool(server)
	if err != nil {
		return err
	}

	managedNetworks, err := getManagedNetworks(server)
	if err != nil {
		return err
	}

	profile, profileETag, err := server.GetProfile("default")
	if err != nil {
		if !api.StatusErrorCheck(err, http.StatusNotFound) {
			return fmt.Errorf("loading the default Incus profile: %w", err)
		}

		if err := server.CreateProfile(api.ProfilesPost{Name: "default"}); err != nil {
			return fmt.Errorf("creating the default Incus profile: %w", err)
		}

		profile, profileETag, err = server.GetProfile("default")
		if err != nil {
			return fmt.Errorf("reloading the default Incus profile: %w", err)
		}
	}

	profileChanged := false
	if profile.Devices == nil {
		profile.Devices = map[string]map[string]string{}
	}

	if !profileHasRootDisk(profile) {
		profile.Devices["root"] = map[string]string{
			"type": "disk",
			"path": "/",
			"pool": poolName,
		}
		profileChanged = true
	}

	if !profileHasNIC(profile) && len(managedNetworks) == 0 {
		if bridgeName == "" {
			bridgeName = findAvailableBridgeName("/sys/class/net")
		}

		if err := ensureManagedBridge(server, bridgeName); err != nil {
			return err
		}

		profile.Devices["eth0"] = map[string]string{
			"type":    "nic",
			"network": bridgeName,
			"name":    "eth0",
		}
		profileChanged = true
	}

	if profileChanged {
		if err := server.UpdateProfile("default", profile.Writable(), profileETag); err != nil {
			return fmt.Errorf("updating the default Incus profile: %w", err)
		}
	}

	return nil
}

func (realIncusManager) CreateTrustToken(ctx context.Context, socketPath, clientName string) (string, error) {
	server, err := connectIncusUnix(ctx, socketPath)
	if err != nil {
		return "", fmt.Errorf("connecting to the Incus daemon: %w", err)
	}

	op, err := server.CreateCertificateToken(api.CertificatesPost{
		CertificatePut: api.CertificatePut{
			Name: clientName,
			Type: api.CertificateTypeClient,
		},
		Token: true,
	})
	if err != nil {
		return "", fmt.Errorf("creating the Incus trust token: %w", err)
	}

	token, err := operationCertificateToken(op)
	if err != nil {
		return "", fmt.Errorf("reading the Incus trust token: %w", err)
	}

	return token, nil
}

func loadIncusConfig() (*cliconfig.Config, string, error) {
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		return nil, "", fmt.Errorf("loading the Incus client configuration: %w", err)
	}

	configPath := filepath.Join(conf.ConfigDir, "config.yml")
	return conf, configPath, nil
}

func saveIncusConfig(conf *cliconfig.Config, configPath string) error {
	if err := ensureIncusConfigDir(conf); err != nil {
		return err
	}

	if err := conf.SaveConfig(configPath); err != nil {
		return fmt.Errorf("saving the Incus client configuration: %w", err)
	}

	return nil
}

func ensureIncusConfigDir(conf *cliconfig.Config) error {
	if conf.ConfigDir == "" {
		return errors.New("could not determine the Incus configuration directory")
	}

	if err := os.MkdirAll(conf.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("creating the Incus configuration directory: %w", err)
	}

	return nil
}

func saveRemoteCertificate(conf *cliconfig.Config, remoteName string, certificate *x509.Certificate) error {
	serverCertPath := conf.ServerCertPath(remoteName)
	if err := os.MkdirAll(filepath.Dir(serverCertPath), 0o750); err != nil {
		return fmt.Errorf("creating the Incus server certificate directory: %w", err)
	}

	file, err := os.Create(serverCertPath)
	if err != nil {
		return fmt.Errorf("creating the Incus server certificate for %q: %w", remoteName, err)
	}

	if err := pem.Encode(file, &pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}); err != nil {
		file.Close()
		return fmt.Errorf("encoding the Incus server certificate for %q: %w", remoteName, err)
	}

	if err := file.Close(); err != nil {
		return fmt.Errorf("closing the Incus server certificate for %q: %w", remoteName, err)
	}

	return nil
}

func normalizeRemoteAddress(address string) string {
	address = strings.TrimSpace(address)
	if strings.Contains(address, "://") {
		return address
	}

	return "https://" + address
}

func connectIncusUnix(ctx context.Context, socketPath string) (incus.InstanceServer, error) {
	deadline := time.Now().Add(30 * time.Second)
	if deadlineFromContext, ok := ctx.Deadline(); ok && deadlineFromContext.Before(deadline) {
		deadline = deadlineFromContext
	}

	var lastErr error
	for {
		server, err := incus.ConnectIncusUnix(socketPath, nil)
		if err == nil {
			return server, nil
		}

		lastErr = err
		if time.Now().After(deadline) {
			return nil, lastErr
		}

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func ensureDefaultStoragePool(server incus.InstanceServer) (string, error) {
	storagePools, err := server.GetStoragePoolNames()
	if err != nil {
		return "", fmt.Errorf("loading existing Incus storage pools: %w", err)
	}

	if len(storagePools) == 0 {
		if err := server.CreateStoragePool(api.StoragePoolsPost{
			Name:   "default",
			Driver: "dir",
		}); err != nil {
			return "", fmt.Errorf("creating the default Incus storage pool: %w", err)
		}

		return "default", nil
	}

	for _, name := range storagePools {
		if name == "default" {
			return name, nil
		}
	}

	return storagePools[0], nil
}

func getManagedNetworks(server incus.InstanceServer) ([]api.Network, error) {
	networks, err := server.GetNetworks()
	if err != nil {
		return nil, fmt.Errorf("loading existing Incus networks: %w", err)
	}

	managed := make([]api.Network, 0, len(networks))
	for _, network := range networks {
		if network.Managed {
			managed = append(managed, network)
		}
	}

	return managed, nil
}

func ensureManagedBridge(server incus.InstanceServer, bridgeName string) error {
	network, _, err := server.GetNetwork(bridgeName)
	if err == nil {
		if network.Managed {
			return nil
		}

		return fmt.Errorf("a non-managed network named %q already exists", bridgeName)
	}

	if !api.StatusErrorCheck(err, http.StatusNotFound) {
		return fmt.Errorf("loading the Incus network %q: %w", bridgeName, err)
	}

	if err := server.CreateNetwork(api.NetworksPost{
		Name: bridgeName,
		Type: "bridge",
	}); err != nil {
		return fmt.Errorf("creating the Incus bridge %q: %w", bridgeName, err)
	}

	return nil
}

func profileHasRootDisk(profile *api.Profile) bool {
	for _, device := range profile.Devices {
		if device["type"] == "disk" && device["path"] == "/" {
			return true
		}
	}

	return false
}

func profileHasNIC(profile *api.Profile) bool {
	for _, device := range profile.Devices {
		if device["type"] == "nic" {
			return true
		}
	}

	return false
}

func findAvailableBridgeName(basePath string) string {
	for index := 0; ; index++ {
		name := fmt.Sprintf("incusbr%d", index)
		if _, err := os.Stat(filepath.Join(basePath, name)); errors.Is(err, os.ErrNotExist) {
			return name
		}
	}
}

func operationCertificateToken(op incus.Operation) (string, error) {
	operation := op.Get()
	token, err := operation.ToCertificateAddToken()
	if err == nil {
		return token.String(), nil
	}

	if refreshErr := op.Refresh(); refreshErr != nil {
		return "", err
	}

	operation = op.Get()
	token, err = operation.ToCertificateAddToken()
	if err != nil {
		return "", err
	}

	return token.String(), nil
}
