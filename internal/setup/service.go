package setup

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	linuxInstallModeClient = "client"
	linuxInstallModeServer = "server"
)

type Service struct {
	prompt       Prompter
	runner       Runner
	hostDetector HostDetector
	out          io.Writer
	scriptWriter func() (string, func(), error)
	incus        IncusManager
	tunnels      SocketTunnelOpener
	selfPath     string
}

func NewService(prompt Prompter, runner Runner, hostDetector HostDetector, out io.Writer) *Service {
	selfPath, _ := os.Executable()

	return &Service{
		prompt:       prompt,
		runner:       runner,
		hostDetector: hostDetector,
		out:          out,
		scriptWriter: writeInstallerScript,
		incus:        newIncusManager(),
		tunnels:      newSocketTunnelOpener(),
		selfPath:     selfPath,
	}
}

func (s *Service) Run(ctx context.Context) error {
	choice, err := s.prompt.Select("Where do you want to set up Incus?", []string{
		"Install locally",
		"Connect to a remote Debian/Ubuntu host over SSH",
	})
	if err != nil {
		return err
	}

	switch choice {
	case 0:
		return s.runLocal(ctx)
	case 1:
		return s.runRemote(ctx)
	default:
		return fmt.Errorf("unsupported setup choice %d", choice)
	}
}

func (s *Service) runLocal(ctx context.Context) error {
	host, err := s.hostDetector.Detect()
	if err != nil {
		return err
	}

	switch host.GOOS {
	case "darwin":
		return s.setupLocalDarwin(ctx)
	case "linux":
		if !host.IsDebianLike() {
			return fmt.Errorf("local Linux setup currently supports Debian and Ubuntu only")
		}

		fmt.Fprintln(s.out, "Installing and initializing Incus locally on Linux...")
		return s.runLinuxInstaller(ctx, linuxInstallModeServer)
	default:
		return fmt.Errorf("local setup is not supported on %s", host.GOOS)
	}
}

func (s *Service) runRemote(ctx context.Context) error {
	host, err := s.hostDetector.Detect()
	if err != nil {
		return err
	}

	if err := s.requireCommands("ssh", "scp"); err != nil {
		return err
	}

	target, err := s.prompt.Ask("SSH target for the remote server (for example: root@203.0.113.10)", "")
	if err != nil {
		return err
	}
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("an SSH target is required")
	}

	defaultAddress := extractHostFromSSHTarget(target)
	if strings.TrimSpace(defaultAddress) == "" {
		return fmt.Errorf("the remote address is required")
	}
	remoteAddress := defaultAddress

	remoteName, err := s.chooseRemoteName(ctx, defaultAddress)
	if err != nil {
		return err
	}

	if err := s.ensureLocalIncusClient(ctx, host); err != nil {
		return err
	}

	if err := s.ensureRemoteAutomationAccess(ctx, target); err != nil {
		return err
	}

	if err := s.installRemoteServer(ctx, target); err != nil {
		return err
	}

	socketPath, err := s.remoteIncusSocketPath(ctx, target)
	if err != nil {
		return err
	}

	bridgeName, err := s.remoteBridgeName(ctx, target)
	if err != nil {
		return err
	}

	tunnel, err := s.openRemoteIncusTunnel(ctx, target, socketPath)
	if err != nil {
		return err
	}
	defer tunnel.Close()

	if err := s.runTaskStep(fmt.Sprintf("Initializing Incus on %s", target), func() (string, error) {
		return "", s.incusManager().BootstrapServer(ctx, tunnel.Path(), bridgeName)
	}); err != nil {
		return fmt.Errorf("initializing Incus on %s: %w", target, err)
	}

	if exists, err := s.incusManager().HasRemote(ctx, remoteName); err != nil {
		return err
	} else if !exists {
		var token string
		if err := s.runTaskStep(fmt.Sprintf("Creating a trust token on %s", target), func() (string, error) {
			var runErr error
			token, runErr = s.incusManager().CreateTrustToken(ctx, tunnel.Path(), host.Hostname)
			return token, runErr
		}); err != nil {
			return fmt.Errorf("creating a trust token on %s: %w", target, err)
		}

		if err := s.runTaskStep(fmt.Sprintf("Adding local Incus remote %s", remoteName), func() (string, error) {
			return "", s.incusManager().AddRemote(ctx, remoteName, fmt.Sprintf("https://%s:8443", remoteAddress), token)
		}); err != nil {
			return fmt.Errorf("adding Incus remote %q: %w", remoteName, err)
		}
	} else {
		fmt.Fprintf(s.out, "Incus remote %s already exists locally, reusing it.\n", remoteName)
	}

	if err := s.runTaskStep(fmt.Sprintf("Verifying remote Incus access for %s", remoteName), func() (string, error) {
		return "", s.incusManager().VerifyRemote(ctx, remoteName)
	}); err != nil {
		return fmt.Errorf("verifying remote Incus access for %q: %w", remoteName, err)
	}

	if err := s.runTaskStep(fmt.Sprintf("Selecting %s as the default Incus remote", remoteName), func() (string, error) {
		return "", s.incusManager().SwitchRemote(ctx, remoteName)
	}); err != nil {
		return fmt.Errorf("switching the default Incus remote to %q: %w", remoteName, err)
	}

	fmt.Fprintf(s.out, "Remote setup finished. Use capsule incus to interact with %s.\n", remoteName)
	return nil
}

func (s *Service) ensureLocalIncusClient(ctx context.Context, host Host) error {
	if _, err := s.runner.LookPath("incus"); err == nil {
		return nil
	}

	switch host.GOOS {
	case "darwin":
		return s.ensureHomebrewFormulas(ctx, []string{"incus"})
	case "linux":
		if !host.IsDebianLike() {
			return fmt.Errorf("automatic local Incus client setup on Linux currently supports Debian and Ubuntu only")
		}

		fmt.Fprintln(s.out, "Installing the local Incus CLI...")
		return s.runLinuxInstaller(ctx, linuxInstallModeClient)
	default:
		return fmt.Errorf("automatic local Incus client setup is not supported on %s", host.GOOS)
	}
}

func (s *Service) requireCommands(commands ...string) error {
	var missing []string
	for _, command := range commands {
		if _, err := s.runner.LookPath(command); err != nil {
			missing = append(missing, command)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("missing required command(s): %s", strings.Join(missing, ", "))
	}

	return nil
}

func (s *Service) setupLocalDarwin(ctx context.Context) error {
	if err := s.ensureHomebrewFormulas(ctx, []string{"colima", "incus"}); err != nil {
		return err
	}

	status, err := s.incusManager().ConfiguredServerStatus(ctx)
	if err != nil {
		return err
	}

	if status.Reachable {
		fmt.Fprintln(s.out, "Incus is already configured locally.")
		return nil
	}

	fmt.Fprintln(s.out, "No local Incus server detected, starting Colima with the Incus runtime...")
	incusEnv, err := CapsuleIncusEnv()
	if err != nil {
		return err
	}

	colimaEnv := append([]string{}, incusEnv...)
	colimaEnv = append(colimaEnv, "COLIMA_PROFILE=capsule")

	if _, err := s.runCommandStep(ctx, "Starting Colima with the Incus runtime", CommandSpec{
		Name: "colima",
		Args: []string{
			"start",
			"--activate", "false",
			"--port-forwarder", "none",
			"--mount", "none",
			"--runtime", "incus",
			"--cpu", "4",
			"--memory", "8",
			"--nested-virtualization",
			"--vm-type", "vz",
		},
		Env: colimaEnv,
	}); err != nil {
		return fmt.Errorf("starting Colima: %w", err)
	}

	status, err = s.incusManager().ConfiguredServerStatus(ctx)
	if err != nil {
		return err
	}

	if status.Reachable {
		fmt.Fprintln(s.out, "Local Incus setup is ready.")
		return nil
	}

	return fmt.Errorf("Incus did not report a reachable server after setup")
}

func (s *Service) ensureHomebrewFormulas(ctx context.Context, formulas []string) error {
	var missing []string
	for _, formula := range formulas {
		if _, err := s.runner.LookPath(formula); err != nil {
			missing = append(missing, formula)
		}
	}

	if len(missing) == 0 {
		return nil
	}

	if _, err := s.runner.LookPath("brew"); err != nil {
		return fmt.Errorf("Homebrew is required to install %s", strings.Join(missing, ", "))
	}

	install, err := s.prompt.Confirm(
		fmt.Sprintf("Install %s with Homebrew?", strings.Join(missing, ", ")),
		true,
	)
	if err != nil {
		return err
	}
	if !install {
		return fmt.Errorf("missing required packages: %s", strings.Join(missing, ", "))
	}

	if _, err := s.runCommandStep(ctx, fmt.Sprintf("Installing %s with Homebrew", strings.Join(missing, ", ")), CommandSpec{
		Name: "brew",
		Args: append([]string{"install"}, missing...),
	}); err != nil {
		return fmt.Errorf("installing Homebrew packages %s: %w", strings.Join(missing, ", "), err)
	}

	return nil
}

func (s *Service) runLinuxInstaller(ctx context.Context, mode string) error {
	scriptPath, cleanup, err := s.scriptWriter()
	if err != nil {
		return err
	}
	defer cleanup()

	runSnippet := fmt.Sprintf(
		"if [ \"$(id -u)\" -eq 0 ]; then %s --mode=%s; else sudo %s --mode=%s; fi",
		shellQuote(scriptPath),
		shellQuote(mode),
		shellQuote(scriptPath),
		shellQuote(mode),
	)

	if _, err := s.runner.Run(ctx, CommandSpec{
		Name:        "sh",
		Args:        []string{"-lc", runSnippet},
		Interactive: true,
	}); err != nil {
		return fmt.Errorf("running Linux installer in %s mode: %w", mode, err)
	}

	if mode != linuxInstallModeServer {
		return nil
	}

	if err := s.runTaskStep("Configuring the local Incus server", func() (string, error) {
		return "", s.runLocalLinuxBootstrap(ctx)
	}); err != nil {
		return fmt.Errorf("configuring the local Incus server: %w", err)
	}

	return nil
}

func (s *Service) runLocalLinuxBootstrap(ctx context.Context) error {
	selfPath, err := s.executablePath()
	if err != nil {
		return err
	}

	runSnippet := fmt.Sprintf(
		"if [ \"$(id -u)\" -eq 0 ]; then %s __bootstrap-local-linux-server; else sudo %s __bootstrap-local-linux-server; fi",
		shellQuote(selfPath),
		shellQuote(selfPath),
	)

	if _, err := s.runner.Run(ctx, CommandSpec{
		Name: "sh",
		Args: []string{"-lc", runSnippet},
	}); err != nil {
		return err
	}

	return nil
}

func (s *Service) executablePath() (string, error) {
	if strings.TrimSpace(s.selfPath) != "" {
		return s.selfPath, nil
	}

	selfPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locating the current capsule executable: %w", err)
	}

	s.selfPath = selfPath
	return selfPath, nil
}

func (s *Service) ensureRemoteAutomationAccess(ctx context.Context, target string) error {
	_, err := s.runCommandStep(ctx, fmt.Sprintf("Checking remote automation access on %s", target), CommandSpec{
		Name: "ssh",
		Args: append(s.sshOptions(),
			target,
			remoteShell(`if [ "$(id -u)" -eq 0 ] || sudo -n true >/dev/null 2>&1; then printf ok; else exit 1; fi`),
		),
	})
	if err != nil {
		return fmt.Errorf("remote setup requires SSH access as root or a user with passwordless sudo on %s", target)
	}

	return nil
}

func (s *Service) installRemoteServer(ctx context.Context, target string) error {
	scriptPath, cleanup, err := s.scriptWriter()
	if err != nil {
		return err
	}
	defer cleanup()

	result, err := s.runCommandStep(ctx, fmt.Sprintf("Creating a remote installer path on %s", target), CommandSpec{
		Name: "ssh",
		Args: append(s.sshOptions(), target, "mktemp", "/tmp/capsule-incus.XXXXXX"),
	})
	if err != nil {
		return fmt.Errorf("creating remote installer path on %s: %w", target, err)
	}

	remotePath := strings.TrimSpace(result.Stdout)
	if remotePath == "" {
		return fmt.Errorf("could not determine remote installer path on %s", target)
	}

	if _, err := s.runCommandStep(ctx, fmt.Sprintf("Uploading the installer to %s", target), CommandSpec{
		Name: "scp",
		Args: append(s.scpOptions(), scriptPath, fmt.Sprintf("%s:%s", target, remotePath)),
	}); err != nil {
		return fmt.Errorf("uploading installer to %s: %w", target, err)
	}

	executeSnippet := fmt.Sprintf(
		"chmod +x %s && if [ \"$(id -u)\" -eq 0 ]; then %s --mode=%s; else sudo %s --mode=%s; fi; status=$?; rm -f %s; exit $status",
		shellQuote(remotePath),
		shellQuote(remotePath),
		shellQuote(linuxInstallModeServer),
		shellQuote(remotePath),
		shellQuote(linuxInstallModeServer),
		shellQuote(remotePath),
	)

	if _, err := s.runCommandStep(ctx, fmt.Sprintf("Provisioning Incus on %s", target), CommandSpec{
		Name: "ssh",
		Args: append(s.sshOptions(), target, remoteShell(executeSnippet)),
	}); err != nil {
		return fmt.Errorf("running remote installer on %s: %w", target, err)
	}

	return nil
}

func (s *Service) remoteIncusSocketPath(ctx context.Context, target string) (string, error) {
	snippet := `for path in /run/incus/unix.socket /var/lib/incus/unix.socket; do if [ -S "$path" ]; then printf '%s\n' "$path"; exit 0; fi; done; exit 1`

	result, err := s.runCommandStep(ctx, fmt.Sprintf("Locating the Incus socket on %s", target), CommandSpec{
		Name: "ssh",
		Args: append(s.sshOptions(), target, remoteShell(snippet)),
	})
	if err != nil {
		return "", fmt.Errorf("locating the Incus socket on %s: %w", target, err)
	}

	socketPath := lastNonEmptyLine(result.Stdout)
	if socketPath == "" {
		return "", fmt.Errorf("could not determine the Incus socket path on %s", target)
	}

	return socketPath, nil
}

func (s *Service) remoteBridgeName(ctx context.Context, target string) (string, error) {
	snippet := `index=0; while [ -e "/sys/class/net/incusbr${index}" ]; do index=$((index + 1)); done; printf 'incusbr%s\n' "$index"`

	result, err := s.runCommandStep(ctx, fmt.Sprintf("Choosing an Incus bridge name on %s", target), CommandSpec{
		Name: "ssh",
		Args: append(s.sshOptions(), target, remoteShell(snippet)),
	})
	if err != nil {
		return "", fmt.Errorf("choosing an Incus bridge name on %s: %w", target, err)
	}

	bridgeName := lastNonEmptyLine(result.Stdout)
	if bridgeName == "" {
		return "", fmt.Errorf("could not determine an Incus bridge name on %s", target)
	}

	return bridgeName, nil
}

func (s *Service) openRemoteIncusTunnel(ctx context.Context, target, remoteSocket string) (SocketTunnel, error) {
	var tunnel SocketTunnel
	if err := s.runTaskStep(fmt.Sprintf("Opening an SSH tunnel to %s", target), func() (string, error) {
		var runErr error
		tunnel, runErr = s.tunnelOpener().Open(ctx, target, remoteSocket, s.sshOptions())
		return "", runErr
	}); err != nil {
		return nil, fmt.Errorf("opening an SSH tunnel to %s: %w", target, err)
	}

	return tunnel, nil
}

func (s *Service) incusManager() IncusManager {
	if s.incus == nil {
		s.incus = newIncusManager()
	}

	return s.incus
}

func (s *Service) tunnelOpener() SocketTunnelOpener {
	if s.tunnels == nil {
		s.tunnels = newSocketTunnelOpener()
	}

	return s.tunnels
}

func (s *Service) chooseRemoteName(ctx context.Context, address string) (string, error) {
	const defaultName = "capsule"

	exists, err := s.incusManager().HasRemote(ctx, defaultName)
	if err != nil {
		return "", err
	}
	if !exists {
		return defaultName, nil
	}

	suggestion, err := s.nextAvailableRemoteName(ctx, fallbackRemoteName(address))
	if err != nil {
		return "", err
	}
	for {
		remoteName, err := s.prompt.Ask("Local name for the Incus remote (capsule is already in use)", suggestion)
		if err != nil {
			return "", err
		}

		remoteName = sanitizeRemoteName(remoteName)
		if remoteName == "" {
			fmt.Fprintln(s.out, "The remote name must contain at least one letter or number.")
			continue
		}

		exists, err := s.incusManager().HasRemote(ctx, remoteName)
		if err != nil {
			return "", err
		}
		if !exists {
			return remoteName, nil
		}

		fmt.Fprintf(s.out, "Incus remote %s already exists locally. Choose another name.\n", remoteName)
		suggestion, err = s.nextAvailableRemoteName(ctx, remoteName)
		if err != nil {
			return "", err
		}
	}
}

func (s *Service) nextAvailableRemoteName(ctx context.Context, base string) (string, error) {
	base = sanitizeRemoteName(base)
	if base == "" {
		base = "capsule-remote"
	}

	candidate := base
	for suffix := 2; ; suffix++ {
		exists, err := s.incusManager().HasRemote(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}

		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
}

func (s *Service) runTaskStep(message string, task func() (string, error)) error {
	return newTaskUI(s.out).Run(message, task)
}

func (s *Service) runCommandStep(ctx context.Context, message string, spec CommandSpec) (Result, error) {
	var result Result
	err := s.runTaskStep(message, func() (string, error) {
		var runErr error
		result, runErr = s.runner.Run(ctx, spec)
		return joinOutput(result.Stdout, result.Stderr), runErr
	})

	return result, err
}

func joinOutput(parts ...string) string {
	nonEmpty := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}

	return strings.Join(nonEmpty, "\n")
}

func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line != "" {
			return line
		}
	}

	return ""
}

func extractHostFromSSHTarget(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return "remote"
	}

	if atIndex := strings.LastIndex(target, "@"); atIndex >= 0 {
		target = target[atIndex+1:]
	}

	if strings.HasPrefix(target, "[") && strings.Contains(target, "]") {
		end := strings.Index(target, "]")
		return target[1:end]
	}

	if colonCount := strings.Count(target, ":"); colonCount == 1 {
		host, _, found := strings.Cut(target, ":")
		if found {
			return host
		}
	}

	return target
}

func defaultRemoteName(host string) string {
	host = extractHostFromSSHTarget(host)
	if host == "" {
		return "remote"
	}

	if net.ParseIP(host) == nil {
		host = strings.TrimSuffix(host, filepath.Ext(host))
	}
	if host == "" {
		return "remote"
	}

	return sanitizeRemoteName(host)
}

func fallbackRemoteName(host string) string {
	name := defaultRemoteName(host)
	if name == "" || name == "remote" {
		return "capsule-remote"
	}

	return "capsule-" + name
}

func sanitizeRemoteName(value string) string {
	replacer := regexp.MustCompile(`[^a-zA-Z0-9-]+`)
	value = strings.TrimSpace(strings.ToLower(value))
	value = replacer.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")

	return value
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func remoteShell(script string) string {
	return "sh -lc " + shellQuote(script)
}

func (s *Service) sshOptions() []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
}

func (s *Service) scpOptions() []string {
	return []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
}
