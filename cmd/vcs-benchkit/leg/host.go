package leg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// serverTopology is the part of *topology.Topology the environments use, so a
// test can stand a fake in for the processes.
type serverTopology interface {
	Addr(topology.Role) string
	PIDs() map[topology.Role]int
	Status(context.Context) (topology.DistributionStatus, error)
	LogText(topology.Role) (string, error)
	Errors() []string
	Stop() error
}

// hostEnv starts the real S or Seq system: a pinned standalone server and a
// pinned `vcs-loadbot run`. Every Start is a fresh server in a fresh directory.
type hostEnv struct {
	serverBin, loadbotBin, outDir, scenario string
	// scratchDir holds what must not reach the results artifact: the throwaway
	// coalition passwords and each server's data directory. Only the load bot's
	// stderr logs go under outDir.
	scratchDir string
	seed       int64
	// arm is the bot's --arm value ("" means S); serverPin is the server's CPU
	// list (S one cpu, Seq both) and serverGOMAXPROCS its GOMAXPROCS.
	arm              string
	serverPin        string
	serverGOMAXPROCS int
	serverCPUs       []int // the whole server set, for softirq
	genSet           string
	genNCPU          int
	// features is what the load bot's probe reported; it decides which v3 flags
	// the kit passes (a v2 bot rejects unknown flags) and gates Burst.
	features Features
	// bot holds the tunable bot flags and the grpc log level; nil = the defaults.
	bot *BotParams
	// onProgress receives the load bot's progress lines; nil drops them.
	onProgress func(Progress)
	// onControlLost receives each control-lost event as the bot reports it;
	// nil drops it (the driver still reads them through ControlLost).
	onControlLost func(ControlLostEvent)
	// logf receives the heartbeat lines (nil = no heartbeat); heartbeatEvery
	// overrides the 15 s period (tests).
	logf           func(format string, a ...any)
	heartbeatEvery time.Duration

	mu         sync.Mutex
	k          int
	serverErrs []string
	// live is the running system, and lastServerGz and lastBotLog the newest
	// saved server log and the newest bot stderr file: the diagnostics read the
	// live log while a system runs and the saved copy after it was closed.
	live         *hostSystem
	lastServerGz string
	lastBotLog   string
}

// ServerLogText is the newest server log: read from the live process, else from
// the copy kept in the artifact (diagSource).
func (e *hostEnv) ServerLogText() (string, error) {
	e.mu.Lock()
	live, gz := e.live, e.lastServerGz
	e.mu.Unlock()
	if live != nil {
		return live.LogText()
	}
	if gz == "" {
		return "", errors.New("no server log was kept")
	}
	return readGz(gz)
}

// BotStderrText is the newest load bot's stderr file (diagSource).
func (e *hostEnv) BotStderrText() (string, error) {
	e.mu.Lock()
	p := e.lastBotLog
	e.mu.Unlock()
	if p == "" {
		return "", errors.New("no load bot was started")
	}
	b, err := os.ReadFile(p)
	return string(b), err
}

// serverNCPU is how many cpus the server process is pinned to (1 when unpinned).
func (e *hostEnv) serverNCPU() int {
	if l, err := CPUList(e.serverPin); err == nil && len(l) > 0 {
		return len(l)
	}
	return 1
}

func (e *hostEnv) armName() string {
	if e.arm == "" {
		return "S"
	}
	return e.arm
}

func (e *hostEnv) errorsSeen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.serverErrs...)
}

func (e *hostEnv) serverDir(k int) string {
	return filepath.Join(e.scratchDir, fmt.Sprintf("server-%d", k))
}

func (e *hostEnv) passwordsFile() (string, error) { return writePasswords(e.scratchDir) }

func writePasswords(scratchDir string) (string, error) {
	p := filepath.Join(scratchDir, "passwords.json")
	m := map[string]string{}
	for _, c := range topology.DefaultCoalitions {
		m[c.Name] = c.Password
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return p, os.WriteFile(p, b, 0o600)
}

func (e *hostEnv) Start(ctx context.Context) (System, error) {
	e.mu.Lock()
	e.k++
	k := e.k
	e.mu.Unlock()

	top, err := topology.StartStandalone(ctx, topology.Spec{
		Binary: e.serverBin,
		Dir:    e.serverDir(k),
		Pin: map[topology.Role]topology.Pin{
			topology.Standalone: {CPUs: e.serverPin, GOMAXPROCS: e.serverGOMAXPROCS},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("start standalone server: %w", err)
	}
	pw, err := e.passwordsFile()
	if err != nil {
		_ = top.Stop()
		return nil, err
	}
	control, voiceAddr := top.Addr(topology.Control), top.Addr(topology.Standalone)
	argv := BotArgv(e.armName(), e.scenario, ArmAddrs{Control: control, Voice: voiceAddr}, pw, e.seed, e.features, botParamsOr(e.bot))
	return e.newSystem(k, top, argv, []serverProc{{Role: "standalone", PID: top.PIDs()[topology.Standalone], NCPU: e.serverNCPU(), VoiceKey: voiceAddr}}, nil)
}

// newSystem starts the pinned load bot with argv against a running topology and
// wraps both as a system. logText replaces the standalone log read (nil keeps
// it). On any failure it stops the topology.
func (e *hostEnv) newSystem(k int, top serverTopology, botArgv []string, servers []serverProc, logText func() (string, error)) (*hostSystem, error) {
	name, argv, pinEnv, err := PinCommand(runtime.GOOS, e.loadbotBin, botArgv, e.genSet)
	if err != nil {
		_ = top.Stop()
		return nil, err
	}
	cmd := exec.Command(name, argv...)
	cmd.Env = processEnvFor(os.Environ(), pinEnv, "run", botParamsOr(e.bot).GRPCLog, botParamsOr(e.bot).GeneratorGC)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = top.Stop()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = top.Stop()
		return nil, err
	}
	botLogPath := filepath.Join(e.outDir, fmt.Sprintf("loadbot-%d.log", k))
	logf, err := os.Create(botLogPath)
	if err != nil {
		_ = top.Stop()
		return nil, err
	}
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		_ = top.Stop()
		return nil, fmt.Errorf("start vcs-loadbot: %w", err)
	}
	s := &hostSystem{
		k: k, env: e, top: top, cmd: cmd, stdin: stdin, logf: logf,
		bot:  NewBot(stdin, stdout, BotHooks{OnProgress: e.onProgress, OnControlLost: e.onControlLost, Features: e.features}),
		done: make(chan struct{}), logText: logText,
		sampler: &procSampler{
			servers: servers,
			genPID:  cmd.Process.Pid, serverCPUs: e.serverCPUs, genNCPU: e.genNCPU,
			read: os.ReadFile,
			ss:   func() ([]byte, error) { return runCmd("ss", "-uamp") },
		},
	}
	go func() { _ = cmd.Wait(); close(s.done) }()
	e.mu.Lock()
	e.live, e.lastBotLog = s, botLogPath
	e.mu.Unlock()
	return s, nil
}

type hostSystem struct {
	k       int
	env     *hostEnv
	top     serverTopology
	cmd     *exec.Cmd
	stdin   interface{ Close() error }
	logf    *os.File
	bot     *Bot
	done    chan struct{}
	sampler *procSampler

	mu    sync.Mutex
	holds int
	// curN is the bot count the last SetN or Burst asked for (heartbeat lines).
	curN int
	// logText replaces the standalone log read; the distributed system sets it.
	logText func() (string, error)
}

func (s *hostSystem) SetN(ctx context.Context, n int) (Joins, error) {
	s.mu.Lock()
	s.curN = n
	s.mu.Unlock()
	defer s.beat("ramp", n)()
	return s.bot.SetN(ctx, n)
}

// beat starts the heartbeat of a phase and returns the func that ends it. It
// prints nothing for a phase shorter than one period, and nothing at all when
// the env has no log writer.
func (s *hostSystem) beat(phase string, n int) (stop func()) {
	e := s.env
	if e.logf == nil {
		return func() {}
	}
	every := e.heartbeatEvery
	if every <= 0 {
		every = heartbeatEvery
	}
	prev, _ := s.sampler.take(time.Now())
	hello := func() uint64 {
		text, err := s.LogText()
		if err != nil {
			return 0
		}
		return CountLog(text).HelloAccepted
	}
	h := &heartbeat{
		phase: phase, n: n, start: time.Now(), prevHello: hello(), now: time.Now,
		sample: func() heartbeatSample {
			var w Window
			w, prev = s.sampler.peek(prev)
			if math.IsNaN(w.ServerCPU) && w.Err != "" {
				e.logf("heartbeat: sampler: %s", w.Err)
			}
			return heartbeatSample{ServerCPU: w.ServerCPU, GenCPU: w.GenCPU, HelloAccepted: hello(), Dead: s.bot.DeadCount()}
		},
		emit: func(line string) { e.logf("%s", line) },
	}
	t := time.NewTicker(every)
	stopc, done := make(chan struct{}), make(chan struct{})
	go func() { defer close(done); runHeartbeat(stopc, t.C, h) }()
	return func() { t.Stop(); close(stopc); <-done }
}

// ControlLost is the bot's control-lost events so far (System).
func (s *hostSystem) ControlLost() []ControlLostEvent { return s.bot.ControlLost() }

// Burst runs a join burst on the bot (System); ErrNotSupported for a v2 bot.
func (s *hostSystem) Burst(ctx context.Context, add, inFlight int, talker string) (Joins, error) {
	s.mu.Lock()
	n := s.curN + add
	s.curN = n
	s.mu.Unlock()
	defer s.beat("burst", n)()
	return s.bot.Burst(ctx, add, inFlight, talker)
}

// SupportsBurst reports whether the bot has the burst command (burstSupporter).
func (s *hostSystem) SupportsBurst() bool { return s.bot.features.Has("burst") }

// LastJoins is the bot's most recent joins line (lastJoiner).
func (s *hostSystem) LastJoins() (Joins, bool) { return s.bot.LastJoins() }

// Hold bounds each call: the hold itself, its 200 ms drain and a margin. The
// first hold also absorbs the first ramp, which is as long as the bots need.
func (s *hostSystem) Hold(ctx context.Context, d time.Duration, label string) (Step, error) {
	s.mu.Lock()
	s.holds++
	first := s.holds == 1
	n := s.curN
	s.mu.Unlock()
	phase := "hold"
	if strings.HasPrefix(label, "probe-") {
		phase = "settle"
	}
	defer s.beat(phase, n)()
	limit := d + 90*time.Second
	if first {
		limit += 10 * time.Minute
	}
	hctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return s.bot.Hold(hctx, d, label)
}

func (s *hostSystem) LogText() (string, error) {
	if s.logText != nil {
		return s.logText()
	}
	return s.top.LogText(topology.Standalone)
}

func (s *hostSystem) BeginSample() func() Window { return s.sampler.begin(time.Second) }

// stopBot ends the load bot: quit, close stdin, wait (killing after 20 s) and
// close its stderr file.
func (s *hostSystem) stopBot() {
	_ = s.bot.Quit()
	_ = s.stdin.Close()
	select {
	case <-s.done:
	case <-time.After(20 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	_ = s.logf.Close()
}

func (s *hostSystem) Close() error {
	s.stopBot()
	errs := s.top.Errors()
	s.env.mu.Lock()
	s.env.serverErrs = append(s.env.serverErrs, errs...)
	s.env.mu.Unlock()
	// Read the log before and after the stop: the stop's own lines are worth
	// keeping, but a log that cannot be read afterwards must not lose the rest.
	before, berr := s.LogText()
	stopErr := s.top.Stop()
	text, err := s.LogText()
	if err != nil {
		text, err = before, berr
	}
	gz := ""
	if err == nil {
		gz, err = SaveServerLog(s.env.outDir, s.k, text)
	}
	if err != nil && s.env.logf != nil {
		s.env.logf("server log not kept: %v", err)
	}
	s.env.mu.Lock()
	if s.env.live == s {
		s.env.live = nil
	}
	if gz != "" {
		s.env.lastServerGz = gz
	}
	s.env.mu.Unlock()
	return stopErr
}
