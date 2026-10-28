package leg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// hostEnv starts the real S-arm system: a pinned standalone server and a
// pinned `vcs-loadbot run`. Every Start is a fresh server in a fresh directory.
type hostEnv struct {
	serverBin, loadbotBin, outDir, scenario string
	// scratchDir holds what must not reach the results artifact: the throwaway
	// coalition passwords and each server's data directory. Only the load bot's
	// stderr logs go under outDir.
	scratchDir string
	seed                                    int64
	serverPin                               string // the S arm's one CPU
	serverGOMAXPROCS                        int
	serverCPUs                              []int // the whole server set, for softirq
	genSet                                  string
	genNCPU                                 int

	mu         sync.Mutex
	k          int
	serverErrs []string
}

func (e *hostEnv) errorsSeen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.serverErrs...)
}

func (e *hostEnv) serverDir(k int) string {
	return filepath.Join(e.scratchDir, fmt.Sprintf("server-%d", k))
}

func (e *hostEnv) passwordsFile() (string, error) {
	p := filepath.Join(e.scratchDir, "passwords.json")
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
	name, argv, env, err := PinCommand(runtime.GOOS, e.loadbotBin, []string{
		"run", "--scenario", e.scenario, "--arm", "S",
		"--control", control, "--voice", voiceAddr, "--allow", control + "," + voiceAddr,
		"--passwords", pw, "--unit", "LDBT", "--seed", strconv.FormatInt(e.seed, 10),
	}, e.genSet)
	if err != nil {
		_ = top.Stop()
		return nil, err
	}
	cmd := exec.Command(name, argv...)
	cmd.Env = append(os.Environ(), env...)
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
	logf, err := os.Create(filepath.Join(e.outDir, fmt.Sprintf("loadbot-%d.log", k)))
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
		env: e, top: top, cmd: cmd, stdin: stdin, logf: logf,
		bot:  NewBot(stdin, stdout),
		done: make(chan struct{}),
		sampler: &procSampler{
			serverPID: top.PIDs()[topology.Standalone], genPID: cmd.Process.Pid,
			serverNCPU: 1, serverCPUs: e.serverCPUs, genNCPU: e.genNCPU,
			voiceKey: voiceAddr, read: os.ReadFile,
			ss: func() ([]byte, error) { return runCmd("ss", "-uamp") },
		},
	}
	go func() { _ = cmd.Wait(); close(s.done) }()
	return s, nil
}

type hostSystem struct {
	env     *hostEnv
	top     *topology.Topology
	cmd     *exec.Cmd
	stdin   interface{ Close() error }
	logf    *os.File
	bot     *Bot
	done    chan struct{}
	sampler *procSampler

	mu    sync.Mutex
	holds int
}

func (s *hostSystem) SetN(ctx context.Context, n int) error { return s.bot.SetN(ctx, n) }

// Hold bounds each call: the hold itself, its 200 ms drain and a margin. The
// first hold also absorbs the first ramp, which is as long as the bots need.
func (s *hostSystem) Hold(ctx context.Context, d time.Duration, label string) (Step, error) {
	s.mu.Lock()
	s.holds++
	first := s.holds == 1
	s.mu.Unlock()
	limit := d + 90*time.Second
	if first {
		limit += 10 * time.Minute
	}
	hctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return s.bot.Hold(hctx, d, label)
}

func (s *hostSystem) LogText() (string, error) { return s.top.LogText(topology.Standalone) }

func (s *hostSystem) BeginSample() func() Window { return s.sampler.begin(time.Second) }

func (s *hostSystem) Close() error {
	_ = s.bot.Quit()
	_ = s.stdin.Close()
	select {
	case <-s.done:
	case <-time.After(20 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.done
	}
	_ = s.logf.Close()
	errs := s.top.Errors()
	s.env.mu.Lock()
	s.env.serverErrs = append(s.env.serverErrs, errs...)
	s.env.mu.Unlock()
	return s.top.Stop()
}
