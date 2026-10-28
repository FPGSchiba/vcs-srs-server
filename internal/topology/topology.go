package topology

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/voiceontrol"
	"github.com/google/uuid"
)

const defaultReadyTimeout = 20 * time.Second

// loopback is the only host any topology process binds or is dialled on.
const loopback = "127.0.0.1"

// Spec describes a distributed topology: one control process, two
// non-global voice nodes and one global voice node, all on loopback.
type Spec struct {
	// Binary is the headless server binary; Dir receives configs, logs and keys.
	Binary, Dir string
	// Coalitions defaults to DefaultCoalitions.
	Coalitions []Coalition
	// NodeGlobalFrequencies is the globalFrequencies list of every voice node's
	// own YAML (default empty, as dev/config/voice-*.yaml).
	NodeGlobalFrequencies []float32
	// ReadyTimeout bounds the whole readiness ladder (0 means 20 s).
	ReadyTimeout time.Duration
	// Allow lists ERROR lines the caller expects; a race report never is.
	Allow Allow

	// Test seams, set only by this package's tests.
	argvHook func(role Role, argv []string) []string
	procHook func(role Role, cmd *exec.Cmd)
}

type proc struct {
	cmd     *exec.Cmd
	logPath string
	log     *os.File
	done    chan struct{}
}

// Topology is a running set of server processes.
type Topology struct {
	sp                            Spec
	httpPort, controlPort, vcPort int
	voicePorts                    map[Role]int
	apiKey, certFile, keyFile     string
	coalitionNames                []string

	mu      sync.Mutex
	procs   map[Role]*proc
	extra   map[string]*proc
	stopped bool
}

// bindError marks a start failure caused by a lost port race.
type bindError struct{ err error }

func (b bindError) Error() string { return b.err.Error() }
func (b bindError) Unwrap() error { return b.err }

// Start launches the topology and returns once the readiness ladder holds.
// On any failure every started process is killed and reaped first. A bind
// error during start-up tears everything down and retries once.
func Start(ctx context.Context, sp Spec) (*Topology, error) {
	if sp.Binary == "" || sp.Dir == "" {
		return nil, errors.New("topology: Spec.Binary and Spec.Dir are required")
	}
	bin, err := filepath.Abs(sp.Binary)
	if err != nil {
		return nil, err
	}
	sp.Binary = bin
	if len(sp.Coalitions) == 0 {
		sp.Coalitions = DefaultCoalitions
	}
	if sp.ReadyTimeout == 0 {
		sp.ReadyTimeout = defaultReadyTimeout
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			if cerr := ctx.Err(); cerr != nil {
				return nil, fmt.Errorf("topology: retry abandoned: %w (first failure: %v)", cerr, last)
			}
		}
		t, err := startOnce(ctx, sp)
		if err == nil {
			return t, nil
		}
		last = err
		var be bindError
		if !errors.As(err, &be) {
			break
		}
	}
	return nil, last
}

func startOnce(ctx context.Context, sp Spec) (top *Topology, err error) {
	if err := os.MkdirAll(sp.Dir, 0o755); err != nil {
		return nil, err
	}
	t := &Topology{
		sp:         sp,
		voicePorts: map[Role]int{},
		procs:      map[Role]*proc{},
		extra:      map[string]*proc{},
		apiKey:     uuid.NewString(),
		certFile:   filepath.Join(sp.Dir, "vc-cert.pem"),
		keyFile:    filepath.Join(sp.Dir, "vc-key.pem"),
	}
	for _, c := range sp.Coalitions {
		t.coalitionNames = append(t.coalitionNames, c.Name)
	}
	// The deferred kill also runs when the ladder fails part-way.
	defer func() {
		if err != nil {
			_ = t.Stop()
			top = nil
		}
	}()

	if _, _, err = voiceontrol.LoadOrGenerateKeyPair(t.keyFile, t.certFile, "localhost"); err != nil {
		return nil, fmt.Errorf("topology: generate voicecontrol cert: %w", err)
	}
	for _, p := range []*int{&t.httpPort, &t.controlPort, &t.vcPort} {
		if *p, err = FreeTCPPort(); err != nil {
			return nil, err
		}
	}
	for _, r := range []Role{NodeA, NodeB, Global} {
		if t.voicePorts[r], err = FreeUDPPort(); err != nil {
			return nil, err
		}
	}

	deadline := time.Now().Add(sp.ReadyTimeout)
	lctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	if err = t.launch(Control, t.controlConfig()); err != nil {
		return nil, err
	}
	if err = t.poll(lctx, "control health", func(c context.Context) error {
		if err := checkHealth(c, t.vcAddr(), t.certFile); err != nil {
			return err
		}
		return t.GraphQL(c, "{__typename}", nil, nil)
	}); err != nil {
		return nil, err
	}

	for i, r := range []Role{NodeA, NodeB, Global} {
		var cfg ProcConfig
		if cfg, err = t.nodeConfigOn(r, t.voicePorts[r], ""); err != nil {
			return nil, err
		}
		if err = t.launch(r, cfg); err != nil {
			return nil, err
		}
		want := i + 1
		if err = t.poll(lctx, fmt.Sprintf("%s registered", r), func(c context.Context) error {
			st, err := t.Status(c)
			if err != nil {
				return err
			}
			if len(st.Nodes) < want {
				return fmt.Errorf("%d of %d nodes registered", len(st.Nodes), want)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	if err = t.poll(lctx, "distribution settled", func(c context.Context) error {
		st, err := t.Status(c)
		if err != nil {
			return err
		}
		return DistributedReady(st, t.want(), t.coalitionNames)
	}); err != nil {
		return nil, err
	}

	// Never wait for isHealthy: it stays false until the first heartbeat.
	if err = t.poll(lctx, "control streams established", func(c context.Context) error {
		st, err := t.Status(c)
		if err != nil {
			return err
		}
		log, err := t.LogText(Control)
		if err != nil {
			return err
		}
		got := StreamEstablished(log)
		for _, n := range st.Nodes {
			if !got[n.ID] {
				return fmt.Errorf("no stream established line for node %s", n.ID)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Topology) want() map[Role]string {
	return map[Role]string{
		NodeA:  t.Addr(NodeA),
		NodeB:  t.Addr(NodeB),
		Global: t.Addr(Global),
	}
}

func (t *Topology) vcAddr() string {
	return net.JoinHostPort(loopback, strconv.Itoa(t.vcPort))
}

func (t *Topology) controlConfig() ProcConfig {
	return ProcConfig{
		Role: Control, HTTPPort: t.httpPort, ControlPort: t.controlPort,
		VoicePort: 0, VoiceControlPort: t.vcPort,
		Coalitions:        t.sp.Coalitions,
		GlobalFrequencies: []float32{249.0}, TestFrequencies: []float32{247.2},
		APIKey: t.apiKey, VCCert: t.certFile, VCKey: t.keyFile,
		RemoteHost: loopback,
	}
}

func (t *Topology) nodeConfigOn(r Role, voicePort int, serverName string) (ProcConfig, error) {
	hp, err := FreeTCPPort()
	if err != nil {
		return ProcConfig{}, err
	}
	cp, err := FreeTCPPort()
	if err != nil {
		return ProcConfig{}, err
	}
	return ProcConfig{
		Role: r, HTTPPort: hp, ControlPort: cp,
		VoicePort: voicePort, VoiceControlPort: t.vcPort,
		GlobalFrequencies: t.sp.NodeGlobalFrequencies,
		APIKey:            t.apiKey, VCCert: t.certFile, VCKey: t.keyFile,
		RemoteHost: loopback,
		PublicAddr: net.JoinHostPort(loopback, strconv.Itoa(voicePort)),
		ServerName: serverName,
	}, nil
}

// spawn renders cfg into <Dir>/<name>/ and starts one process there.
func (t *Topology) spawn(name string, role Role, cfg ProcConfig) (*proc, error) {
	dir := filepath.Join(t.sp.Dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cfg.TokenPrivateKey = filepath.Join(dir, "token-key.pem")
	cfg.TokenPublicKey = filepath.Join(dir, "token-pub.pem")
	yml, err := RenderConfig(cfg)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, yml, 0o600); err != nil {
		return nil, err
	}
	banned := filepath.Join(dir, "banned.json")
	if err := os.WriteFile(banned, []byte("[]"), 0o644); err != nil {
		return nil, err
	}
	mode := "voice"
	if role == Control {
		mode = "control"
	}
	argv := []string{"--mode", mode, "--config", cfgPath, "--banned", banned, "--file-log=false"}
	if role == Global {
		argv = append(argv, "--global")
	}
	if t.sp.argvHook != nil {
		argv = t.sp.argvHook(role, argv)
	}
	logPath := filepath.Join(t.sp.Dir, name+".log")
	lf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(t.sp.Binary, argv...)
	cmd.Dir = dir
	// The *os.File itself, not a pipe: no copy goroutine can stall the server.
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		_ = lf.Close()
		return nil, fmt.Errorf("topology: start %s: %w", name, err)
	}
	p := &proc{cmd: cmd, logPath: logPath, log: lf, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	if t.sp.procHook != nil {
		t.sp.procHook(role, cmd)
	}
	return p, nil
}

func (t *Topology) launch(role Role, cfg ProcConfig) error {
	p, err := t.spawn(string(role), role, cfg)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.procs[role] = p
	t.mu.Unlock()
	return nil
}

// poll runs fn every 50 ms until it returns nil, the ctx deadline passes, a
// process dies, or a log shows an ERROR (or a race). After every attempt the
// logs are scanned; a hit stops the ladder with the offending lines.
func (t *Topology) poll(ctx context.Context, what string, fn func(context.Context) error) error {
	for {
		last := fn(ctx)
		if err := t.scan(); err != nil {
			return err
		}
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("topology: not ready within %s waiting for %s: %v", t.sp.ReadyTimeout, what, last)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// scan returns an error when a log has a disallowed ERROR line or a
// started process has exited. Bind failures come back as bindError.
func (t *Topology) scan() error {
	t.mu.Lock()
	roles := make([]Role, 0, len(t.procs))
	for r := range t.procs {
		roles = append(roles, r)
	}
	t.mu.Unlock()
	for _, r := range roles {
		log, err := t.LogText(r)
		if err != nil {
			return err
		}
		if IsBindError(log) {
			return bindError{fmt.Errorf("topology: %s lost a port race:\n%s", r, strings.Join(ErrorLines(log, nil), "\n"))}
		}
		if lines := ErrorLines(log, t.sp.Allow); len(lines) > 0 {
			return fmt.Errorf("topology: %s logged errors:\n%s", r, strings.Join(lines, "\n"))
		}
		t.mu.Lock()
		p := t.procs[r]
		t.mu.Unlock()
		select {
		case <-p.done:
			return fmt.Errorf("topology: %s exited early; log:\n%s", r, tail(log, 20))
		default:
		}
	}
	return nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Addr is the UDP voice address of a node role, or the client gRPC
// host:port for Control.
func (t *Topology) Addr(role Role) string {
	if role == Control {
		return net.JoinHostPort(loopback, strconv.Itoa(t.controlPort))
	}
	return net.JoinHostPort(loopback, strconv.Itoa(t.voicePorts[role]))
}

// HTTP is the control process's admin HTTP host:port.
func (t *Topology) HTTP() string {
	return net.JoinHostPort(loopback, strconv.Itoa(t.httpPort))
}

// APIKey is the control process's GraphQL API key.
func (t *Topology) APIKey() string { return t.apiKey }

// GraphQL runs one query or mutation against the control process.
func (t *Topology) GraphQL(ctx context.Context, query string, vars map[string]any, out any) error {
	return postGraphQL(ctx, t.HTTP(), t.apiKey, query, vars, out)
}

const statusQuery = "{distributionStatus{globalAddr nodes{id address isGlobal coalitions connectedClients} clientAssignments{clientId nodeId}}}"

// Status reads the control process's distributionStatus.
func (t *Topology) Status(ctx context.Context) (DistributionStatus, error) {
	var out struct {
		DistributionStatus struct {
			GlobalAddr string `json:"globalAddr"`
			Nodes      []struct {
				ID               string   `json:"id"`
				Address          string   `json:"address"`
				IsGlobal         bool     `json:"isGlobal"`
				Coalitions       []string `json:"coalitions"`
				ConnectedClients int      `json:"connectedClients"`
			} `json:"nodes"`
			ClientAssignments []struct {
				ClientID string `json:"clientId"`
				NodeID   string `json:"nodeId"`
			} `json:"clientAssignments"`
		} `json:"distributionStatus"`
	}
	if err := t.GraphQL(ctx, statusQuery, nil, &out); err != nil {
		return DistributionStatus{}, err
	}
	d := out.DistributionStatus
	st := DistributionStatus{GlobalAddr: d.GlobalAddr}
	for _, n := range d.Nodes {
		st.Nodes = append(st.Nodes, NodeStatus{ID: n.ID, Address: n.Address, IsGlobal: n.IsGlobal, Coalitions: n.Coalitions, ConnectedClients: n.ConnectedClients})
	}
	for _, a := range d.ClientAssignments {
		st.ClientAssignments = append(st.ClientAssignments, struct{ ClientID, NodeID string }{a.ClientID, a.NodeID})
	}
	return st, nil
}

// CoalitionMap maps each coalition to the non-global node role serving it.
func (t *Topology) CoalitionMap(ctx context.Context) (map[string]Role, error) {
	st, err := t.Status(ctx)
	if err != nil {
		return nil, err
	}
	return CoalitionOwner(st, t.want()), nil
}

// LogText returns the current log of a main role.
func (t *Topology) LogText(role Role) (string, error) {
	t.mu.Lock()
	p := t.procs[role]
	t.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("topology: no process for role %q", role)
	}
	b, err := os.ReadFile(p.logPath)
	return string(b), err
}

// Errors returns every disallowed ERROR or race line across the main
// processes' logs. Extra nodes' logs are not scanned here or by the start-up
// ladder; the caller reads them via the path StartExtraNode returned.
func (t *Topology) Errors() []string {
	t.mu.Lock()
	roles := make([]Role, 0, len(t.procs))
	for r := range t.procs {
		roles = append(roles, r)
	}
	t.mu.Unlock()
	var out []string
	for _, r := range roles {
		log, err := t.LogText(r)
		if err != nil {
			out = append(out, fmt.Sprintf("topology: read %s log: %v", r, err))
			continue
		}
		for _, l := range ErrorLines(log, t.sp.Allow) {
			out = append(out, string(r)+": "+l)
		}
	}
	return out
}

// StartExtraNode starts one more non-global voice node named name with the
// given VoiceControl TLS serverName (A8: a wrong name never registers). It
// returns an idempotent stop func and the node's log path. It fails once
// the topology is stopped, so no process can outlive Stop.
func (t *Topology) StartExtraNode(ctx context.Context, name string, serverName string) (stop func(), logPath string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	t.mu.Lock()
	stopped := t.stopped
	t.mu.Unlock()
	if stopped {
		return nil, "", errors.New("topology: StartExtraNode after Stop")
	}
	port, err := FreeUDPPort()
	if err != nil {
		return nil, "", err
	}
	cfg, err := t.nodeConfigOn(Role(name), port, serverName)
	if err != nil {
		return nil, "", err
	}
	p, err := t.spawn(name, Role(name), cfg)
	if err != nil {
		return nil, "", err
	}
	t.mu.Lock()
	if t.stopped { // Stop ran while spawning: nothing else will reap p
		t.mu.Unlock()
		killProc(p)
		return nil, "", errors.New("topology: Stop ran during StartExtraNode")
	}
	t.extra[name] = p
	t.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { killProc(p) }) }, p.logPath, nil
}

func killProc(p *proc) {
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.done
	_ = p.log.Close()
}

// Stop kills every process, waits for each, and closes the log files. It is
// idempotent. A stop func from StartExtraNode may race with it only through
// a double Close on the log file, which is harmless.
func (t *Topology) Stop() error {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return nil
	}
	t.stopped = true
	all := make([]*proc, 0, len(t.procs)+len(t.extra))
	for _, p := range t.procs {
		all = append(all, p)
	}
	for _, p := range t.extra {
		all = append(all, p)
	}
	t.mu.Unlock()
	for _, p := range all {
		killProc(p)
	}
	return nil
}

// goTool returns the absolute path of the go command of the toolchain that
// built this binary (GOROOT/bin/go), so BuildServer never resolves "go"
// through PATH.
func goTool() (string, error) {
	root := runtime.GOROOT() //nolint:staticcheck // the toolchain root is exactly what is wanted here
	if root == "" {
		return "", errors.New("topology: GOROOT unknown (binary built with -trimpath?); set VCS_SERVER_BIN instead")
	}
	name := "go"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(root, "bin", name)
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("topology: go tool not found at %s: %w; set VCS_SERVER_BIN instead", p, err)
	}
	return p, nil
}

// BuildServer builds the headless server binary into outPath from the
// module root, with the go command of the toolchain that built the caller.
func BuildServer(ctx context.Context, outPath string) error {
	gobin, err := goTool()
	if err != nil {
		return err
	}
	list := exec.CommandContext(ctx, gobin, "list", "-m", "-f", "{{.Dir}}")
	dirOut, err := list.Output()
	if err != nil {
		return fmt.Errorf("topology: go list -m: %w", err)
	}
	build := exec.CommandContext(ctx, gobin, "build", "-tags", "headless", "-o", outPath, ".")
	build.Dir = strings.TrimSpace(string(dirOut))
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("topology: go build: %w\n%s", err, out)
	}
	return nil
}
