package setup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSetupLocalDarwinStartsColimaWhenServerIsMissing(t *testing.T) {
	t.Parallel()

	runner := newFakeRunner(map[string]bool{
		"brew":   true,
		"colima": true,
		"incus":  true,
	})
	runner.queue("colima start --activate false --port-forwarder none --mount none --runtime incus --cpu 4 --memory 8 --nested-virtualization --vm-type vz", fakeRunResult{})

	incus := &fakeIncusManager{
		statuses: []IncusStatus{
			{Reachable: false},
			{Reachable: true},
		},
	}

	service := &Service{
		prompt:       &fakePrompter{},
		runner:       runner,
		hostDetector: fakeHostDetector{host: Host{GOOS: "darwin", Hostname: "test-client"}},
		out:          io.Discard,
		scriptWriter: func() (string, func(), error) { return "/tmp/unused", func() {}, nil },
		incus:        incus,
	}

	if err := service.setupLocalDarwin(context.Background()); err != nil {
		t.Fatalf("setupLocalDarwin returned an error: %v", err)
	}

	if !runner.called("colima start --activate false --port-forwarder none --mount none --runtime incus --cpu 4 --memory 8 --nested-virtualization --vm-type vz") {
		t.Fatal("expected Colima start to be invoked")
	}
	if !runner.calledWithEnv("colima start --activate false --port-forwarder none --mount none --runtime incus --cpu 4 --memory 8 --nested-virtualization --vm-type vz", "COLIMA_PROFILE=capsule") {
		t.Fatal("expected Colima start to set COLIMA_PROFILE=capsule")
	}
	if !runner.calledWithEnvPrefix("colima start --activate false --port-forwarder none --mount none --runtime incus --cpu 4 --memory 8 --nested-virtualization --vm-type vz", "INCUS_CONF=") {
		t.Fatal("expected Colima start to set INCUS_CONF to Capsule's Incus profile")
	}
}

func TestSetupLocalDarwinSkipsColimaWhenServerIsReachable(t *testing.T) {
	t.Parallel()

	runner := newFakeRunner(map[string]bool{
		"brew":   true,
		"colima": true,
		"incus":  true,
	})
	incus := &fakeIncusManager{
		statuses: []IncusStatus{{Reachable: true}},
	}

	service := &Service{
		prompt:       &fakePrompter{},
		runner:       runner,
		hostDetector: fakeHostDetector{host: Host{GOOS: "darwin", Hostname: "test-client"}},
		out:          io.Discard,
		scriptWriter: func() (string, func(), error) { return "/tmp/unused", func() {}, nil },
		incus:        incus,
	}

	if err := service.setupLocalDarwin(context.Background()); err != nil {
		t.Fatalf("setupLocalDarwin returned an error: %v", err)
	}

	if runner.called("colima start --activate false --port-forwarder none --mount none --runtime incus --cpu 4 --memory 8 --nested-virtualization --vm-type vz") {
		t.Fatal("expected Colima start to be skipped")
	}
}

func TestRunRemoteInstallsServerAndAddsRemote(t *testing.T) {
	t.Parallel()

	prompt := &fakePrompter{
		selectChoices: []int{1},
		askAnswers: []string{
			"root@198.51.100.10",
		},
	}

	runner := newFakeRunner(map[string]bool{
		"incus": true,
		"scp":   true,
		"ssh":   true,
	})
	incus := &fakeIncusManager{
		remoteExists: map[string][]bool{
			"capsule": {false, false},
		},
		trustToken: "token-value",
	}
	tunnel := &fakeSocketTunnel{path: "/tmp/capsule-incus.sock"}
	tunnelOpener := &fakeSocketTunnelOpener{tunnel: tunnel}

	precheck := `if [ "$(id -u)" -eq 0 ] || sudo -n true >/dev/null 2>&1; then printf ok; else exit 1; fi`
	installSnippet := `chmod +x '/tmp/capsule-incus.abcd' && if [ "$(id -u)" -eq 0 ]; then '/tmp/capsule-incus.abcd' --mode='server'; else sudo '/tmp/capsule-incus.abcd' --mode='server'; fi; status=$?; rm -f '/tmp/capsule-incus.abcd'; exit $status`
	socketSnippet := `for path in /run/incus/unix.socket /var/lib/incus/unix.socket; do if [ -S "$path" ]; then printf '%s\n' "$path"; exit 0; fi; done; exit 1`
	bridgeSnippet := `index=0; while [ -e "/sys/class/net/incusbr${index}" ]; do index=$((index + 1)); done; printf 'incusbr%s\n' "$index"`

	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(precheck), fakeRunResult{stdout: "ok"})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 mktemp /tmp/capsule-incus.XXXXXX", fakeRunResult{stdout: "/tmp/capsule-incus.abcd\n"})
	runner.queue("scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new /tmp/capsule-script.sh root@198.51.100.10:/tmp/capsule-incus.abcd", fakeRunResult{})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(installSnippet), fakeRunResult{})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(socketSnippet), fakeRunResult{stdout: "/run/incus/unix.socket\n"})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(bridgeSnippet), fakeRunResult{stdout: "incusbr0\n"})

	service := &Service{
		prompt:       prompt,
		runner:       runner,
		hostDetector: fakeHostDetector{host: Host{GOOS: "darwin", Hostname: "test-client"}},
		out:          io.Discard,
		scriptWriter: func() (string, func(), error) { return "/tmp/capsule-script.sh", func() {}, nil },
		incus:        incus,
		tunnels:      tunnelOpener,
	}

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}

	if len(incus.addRemoteCalls) != 1 {
		t.Fatalf("expected one remote add call, got %d", len(incus.addRemoteCalls))
	}
	if incus.addRemoteCalls[0] != (fakeAddRemoteCall{
		remoteName: "capsule",
		address:    "https://198.51.100.10:8443",
		token:      "token-value",
	}) {
		t.Fatalf("unexpected remote add call: %+v", incus.addRemoteCalls[0])
	}
	if len(incus.verifyRemoteCalls) != 1 || incus.verifyRemoteCalls[0] != "capsule" {
		t.Fatalf("unexpected remote verify calls: %+v", incus.verifyRemoteCalls)
	}
	if len(incus.switchRemoteCalls) != 1 || incus.switchRemoteCalls[0] != "capsule" {
		t.Fatalf("unexpected remote switch calls: %+v", incus.switchRemoteCalls)
	}
	if len(incus.bootstrapCalls) != 1 || incus.bootstrapCalls[0] != (fakeBootstrapCall{
		socketPath: "/tmp/capsule-incus.sock",
		bridgeName: "incusbr0",
	}) {
		t.Fatalf("unexpected bootstrap calls: %+v", incus.bootstrapCalls)
	}
	if len(incus.trustTokenCalls) != 1 || incus.trustTokenCalls[0] != (fakeTrustTokenCall{
		socketPath: "/tmp/capsule-incus.sock",
		clientName: "test-client",
	}) {
		t.Fatalf("unexpected trust token calls: %+v", incus.trustTokenCalls)
	}
	if len(prompt.askQuestions) != 1 {
		t.Fatalf("expected 1 prompt, got %d", len(prompt.askQuestions))
	}
	if !strings.Contains(prompt.askQuestions[0], "root@203.0.113.10") {
		t.Fatalf("expected SSH target prompt to include an example, got %q", prompt.askQuestions[0])
	}
	if !tunnel.closed {
		t.Fatal("expected SSH tunnel to be closed")
	}
}

func TestRunRemotePromptsForRemoteNameWhenCapsuleExists(t *testing.T) {
	t.Parallel()

	prompt := &fakePrompter{
		selectChoices: []int{1},
		askAnswers: []string{
			"root@198.51.100.10",
			"lab",
		},
	}

	runner := newFakeRunner(map[string]bool{
		"incus": true,
		"scp":   true,
		"ssh":   true,
	})
	incus := &fakeIncusManager{
		remoteExists: map[string][]bool{
			"capsule": {true},
			"lab":     {false, false},
		},
		trustToken: "token-value",
	}
	tunnelOpener := &fakeSocketTunnelOpener{tunnel: &fakeSocketTunnel{path: "/tmp/capsule-incus.sock"}}

	precheck := `if [ "$(id -u)" -eq 0 ] || sudo -n true >/dev/null 2>&1; then printf ok; else exit 1; fi`
	installSnippet := `chmod +x '/tmp/capsule-incus.abcd' && if [ "$(id -u)" -eq 0 ]; then '/tmp/capsule-incus.abcd' --mode='server'; else sudo '/tmp/capsule-incus.abcd' --mode='server'; fi; status=$?; rm -f '/tmp/capsule-incus.abcd'; exit $status`
	socketSnippet := `for path in /run/incus/unix.socket /var/lib/incus/unix.socket; do if [ -S "$path" ]; then printf '%s\n' "$path"; exit 0; fi; done; exit 1`
	bridgeSnippet := `index=0; while [ -e "/sys/class/net/incusbr${index}" ]; do index=$((index + 1)); done; printf 'incusbr%s\n' "$index"`

	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(precheck), fakeRunResult{stdout: "ok"})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 mktemp /tmp/capsule-incus.XXXXXX", fakeRunResult{stdout: "/tmp/capsule-incus.abcd\n"})
	runner.queue("scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new /tmp/capsule-script.sh root@198.51.100.10:/tmp/capsule-incus.abcd", fakeRunResult{})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(installSnippet), fakeRunResult{})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(socketSnippet), fakeRunResult{stdout: "/run/incus/unix.socket\n"})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(bridgeSnippet), fakeRunResult{stdout: "incusbr0\n"})

	service := &Service{
		prompt:       prompt,
		runner:       runner,
		hostDetector: fakeHostDetector{host: Host{GOOS: "darwin", Hostname: "test-client"}},
		out:          io.Discard,
		scriptWriter: func() (string, func(), error) { return "/tmp/capsule-script.sh", func() {}, nil },
		incus:        incus,
		tunnels:      tunnelOpener,
	}

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}

	if len(incus.addRemoteCalls) != 1 || incus.addRemoteCalls[0].remoteName != "lab" {
		t.Fatalf("expected alternate remote add call, got %+v", incus.addRemoteCalls)
	}
	if prompt.askDefaults[1] != "capsule-198-51-100-10" {
		t.Fatalf("expected alternate remote name suggestion, got %q", prompt.askDefaults[1])
	}
}

func TestRunRemoteSuggestsNextAvailableRemoteNameWhenFallbackExists(t *testing.T) {
	t.Parallel()

	prompt := &fakePrompter{
		selectChoices: []int{1},
		askAnswers: []string{
			"root@198.51.100.10",
			"",
		},
	}

	runner := newFakeRunner(map[string]bool{
		"incus": true,
		"scp":   true,
		"ssh":   true,
	})
	incus := &fakeIncusManager{
		remoteExists: map[string][]bool{
			"capsule":                 {true},
			"capsule-198-51-100-10":   {true},
			"capsule-198-51-100-10-2": {false, false},
		},
		trustToken: "token-value",
	}
	tunnelOpener := &fakeSocketTunnelOpener{tunnel: &fakeSocketTunnel{path: "/tmp/capsule-incus.sock"}}

	precheck := `if [ "$(id -u)" -eq 0 ] || sudo -n true >/dev/null 2>&1; then printf ok; else exit 1; fi`
	installSnippet := `chmod +x '/tmp/capsule-incus.abcd' && if [ "$(id -u)" -eq 0 ]; then '/tmp/capsule-incus.abcd' --mode='server'; else sudo '/tmp/capsule-incus.abcd' --mode='server'; fi; status=$?; rm -f '/tmp/capsule-incus.abcd'; exit $status`
	socketSnippet := `for path in /run/incus/unix.socket /var/lib/incus/unix.socket; do if [ -S "$path" ]; then printf '%s\n' "$path"; exit 0; fi; done; exit 1`
	bridgeSnippet := `index=0; while [ -e "/sys/class/net/incusbr${index}" ]; do index=$((index + 1)); done; printf 'incusbr%s\n' "$index"`

	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(precheck), fakeRunResult{stdout: "ok"})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 mktemp /tmp/capsule-incus.XXXXXX", fakeRunResult{stdout: "/tmp/capsule-incus.abcd\n"})
	runner.queue("scp -o BatchMode=yes -o StrictHostKeyChecking=accept-new /tmp/capsule-script.sh root@198.51.100.10:/tmp/capsule-incus.abcd", fakeRunResult{})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(installSnippet), fakeRunResult{})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(socketSnippet), fakeRunResult{stdout: "/run/incus/unix.socket\n"})
	runner.queue("ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new root@198.51.100.10 "+remoteShell(bridgeSnippet), fakeRunResult{stdout: "incusbr0\n"})

	service := &Service{
		prompt:       prompt,
		runner:       runner,
		hostDetector: fakeHostDetector{host: Host{GOOS: "darwin", Hostname: "test-client"}},
		out:          io.Discard,
		scriptWriter: func() (string, func(), error) { return "/tmp/capsule-script.sh", func() {}, nil },
		incus:        incus,
		tunnels:      tunnelOpener,
	}

	if err := service.Run(context.Background()); err != nil {
		t.Fatalf("Run returned an error: %v", err)
	}

	if len(incus.addRemoteCalls) != 1 || incus.addRemoteCalls[0].remoteName != "capsule-198-51-100-10-2" {
		t.Fatalf("expected next available remote name, got %+v", incus.addRemoteCalls)
	}
	if prompt.askDefaults[1] != "capsule-198-51-100-10-2" {
		t.Fatalf("expected next available remote name suggestion, got %q", prompt.askDefaults[1])
	}
}

type fakePrompter struct {
	selectChoices []int
	askAnswers    []string
	askQuestions  []string
	askDefaults   []string
	confirmAnswer bool
}

func (f *fakePrompter) Select(_ string, _ []string) (int, error) {
	if len(f.selectChoices) == 0 {
		return 0, nil
	}

	answer := f.selectChoices[0]
	f.selectChoices = f.selectChoices[1:]
	return answer, nil
}

func (f *fakePrompter) Confirm(_ string, _ bool) (bool, error) {
	return f.confirmAnswer, nil
}

func (f *fakePrompter) Ask(question, defaultValue string) (string, error) {
	f.askQuestions = append(f.askQuestions, question)
	f.askDefaults = append(f.askDefaults, defaultValue)

	if len(f.askAnswers) == 0 {
		return defaultValue, nil
	}

	answer := f.askAnswers[0]
	f.askAnswers = f.askAnswers[1:]
	if answer == "" {
		return defaultValue, nil
	}

	return answer, nil
}

type fakeHostDetector struct {
	host Host
	err  error
}

func (f fakeHostDetector) Detect() (Host, error) {
	return f.host, f.err
}

type fakeRunResult struct {
	stdout string
	stderr string
	err    error
}

type fakeIncusManager struct {
	statuses          []IncusStatus
	remoteExists      map[string][]bool
	statusErr         error
	remoteExistsErr   error
	addRemoteErr      error
	verifyRemoteErr   error
	switchRemoteErr   error
	bootstrapErr      error
	trustToken        string
	trustTokenErr     error
	addRemoteCalls    []fakeAddRemoteCall
	verifyRemoteCalls []string
	switchRemoteCalls []string
	bootstrapCalls    []fakeBootstrapCall
	trustTokenCalls   []fakeTrustTokenCall
}

type fakeAddRemoteCall struct {
	remoteName string
	address    string
	token      string
}

type fakeBootstrapCall struct {
	socketPath string
	bridgeName string
}

type fakeTrustTokenCall struct {
	socketPath string
	clientName string
}

func (f *fakeIncusManager) ConfiguredServerStatus(_ context.Context) (IncusStatus, error) {
	if len(f.statuses) == 0 {
		return IncusStatus{}, f.statusErr
	}

	status := f.statuses[0]
	f.statuses = f.statuses[1:]
	return status, f.statusErr
}

func (f *fakeIncusManager) HasRemote(_ context.Context, remoteName string) (bool, error) {
	queue := f.remoteExists[remoteName]
	if len(queue) == 0 {
		return false, f.remoteExistsErr
	}

	exists := queue[0]
	f.remoteExists[remoteName] = queue[1:]
	return exists, nil
}

func (f *fakeIncusManager) AddRemote(_ context.Context, remoteName, address, token string) error {
	f.addRemoteCalls = append(f.addRemoteCalls, fakeAddRemoteCall{
		remoteName: remoteName,
		address:    address,
		token:      token,
	})

	return f.addRemoteErr
}

func (f *fakeIncusManager) VerifyRemote(_ context.Context, remoteName string) error {
	f.verifyRemoteCalls = append(f.verifyRemoteCalls, remoteName)
	return f.verifyRemoteErr
}

func (f *fakeIncusManager) SwitchRemote(_ context.Context, remoteName string) error {
	f.switchRemoteCalls = append(f.switchRemoteCalls, remoteName)
	return f.switchRemoteErr
}

func (f *fakeIncusManager) BootstrapServer(_ context.Context, socketPath, bridgeName string) error {
	f.bootstrapCalls = append(f.bootstrapCalls, fakeBootstrapCall{
		socketPath: socketPath,
		bridgeName: bridgeName,
	})

	return f.bootstrapErr
}

func (f *fakeIncusManager) CreateTrustToken(_ context.Context, socketPath, clientName string) (string, error) {
	f.trustTokenCalls = append(f.trustTokenCalls, fakeTrustTokenCall{
		socketPath: socketPath,
		clientName: clientName,
	})

	return f.trustToken, f.trustTokenErr
}

type fakeSocketTunnelOpener struct {
	tunnel SocketTunnel
	err    error
	opens  []fakeSocketTunnelOpen
}

type fakeSocketTunnelOpen struct {
	target       string
	remoteSocket string
	sshOptions   []string
}

func (f *fakeSocketTunnelOpener) Open(_ context.Context, target, remoteSocket string, sshOptions []string) (SocketTunnel, error) {
	f.opens = append(f.opens, fakeSocketTunnelOpen{
		target:       target,
		remoteSocket: remoteSocket,
		sshOptions:   append([]string{}, sshOptions...),
	})

	return f.tunnel, f.err
}

type fakeSocketTunnel struct {
	path   string
	closed bool
}

func (f *fakeSocketTunnel) Path() string {
	return f.path
}

func (f *fakeSocketTunnel) Close() error {
	f.closed = true
	return nil
}

type fakeRunner struct {
	lookPath map[string]bool
	queued   map[string][]fakeRunResult
	calls    []string
	specs    []CommandSpec
}

func newFakeRunner(lookPath map[string]bool) *fakeRunner {
	return &fakeRunner{
		lookPath: lookPath,
		queued:   map[string][]fakeRunResult{},
	}
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if f.lookPath[file] {
		return "/usr/bin/" + file, nil
	}

	return "", errors.New("not found")
}

func (f *fakeRunner) Run(_ context.Context, spec CommandSpec) (Result, error) {
	key := strings.Join(append([]string{spec.Name}, spec.Args...), " ")
	f.calls = append(f.calls, key)
	f.specs = append(f.specs, spec)

	queue := f.queued[key]
	if len(queue) == 0 {
		return Result{}, errors.New("unexpected command: " + key)
	}

	result := queue[0]
	f.queued[key] = queue[1:]

	return Result{
		Stdout: result.stdout,
		Stderr: result.stderr,
	}, result.err
}

func (f *fakeRunner) queue(command string, result fakeRunResult) {
	f.queued[command] = append(f.queued[command], result)
}

func (f *fakeRunner) called(command string) bool {
	for _, call := range f.calls {
		if call == command {
			return true
		}
	}

	return false
}

func (f *fakeRunner) calledWithEnv(command, env string) bool {
	for _, spec := range f.specs {
		key := strings.Join(append([]string{spec.Name}, spec.Args...), " ")
		if key != command {
			continue
		}

		for _, entry := range spec.Env {
			if entry == env {
				return true
			}
		}
	}

	return false
}

func (f *fakeRunner) calledWithEnvPrefix(command, prefix string) bool {
	for _, spec := range f.specs {
		key := strings.Join(append([]string{spec.Name}, spec.Args...), " ")
		if key != command {
			continue
		}

		for _, entry := range spec.Env {
			if strings.HasPrefix(entry, prefix) {
				return true
			}
		}
	}

	return false
}
