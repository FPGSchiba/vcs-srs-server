package leg

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FPGSchiba/vcs-srs-server/cmd/vcs-benchkit/sweep"
	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
)

// expectedNodes is what control's distributionStatus lists for a healthy D2
// topology: nodeA, nodeB and global. Fewer during a hold is a breach.
const expectedNodes = 3

// nodePollEvery is the period of the distributionStatus poller during a window.
const nodePollEvery = 5 * time.Second

// distRoles are the D2 server processes in report order.
var distRoles = []topology.Role{topology.Control, topology.NodeA, topology.NodeB, topology.Global}

// startTopology starts a distributed topology; a test replaces it (the
// topology package's own seams are package-private).
var startTopology = func(ctx context.Context, sp topology.Spec) (serverTopology, error) {
	t, err := topology.Start(ctx, sp)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// envBase is what every arm's environment shares.
type envBase struct {
	serverBin, loadbotBin, outDir, scratchDir, scenario string
	seed                                                int64
	serverCPUs                                          []int
	features                                            Features
	bot                                                 *BotParams
	onProgress                                          func(Progress)
	onControlLost                                       func(ControlLostEvent)
	logf                                                func(format string, a ...any)
	heartbeatEvery                                      time.Duration
}

// NewArmEnv builds the environment of an arm: S and Seq run one standalone
// process (one cpu, or two), D2 a pinned distributed topology.
func NewArmEnv(plan ArmPlan, base envBase) Env {
	pin := plan.Server[topology.Standalone]
	h := &hostEnv{
		serverBin: base.serverBin, loadbotBin: base.loadbotBin, outDir: base.outDir, scratchDir: base.scratchDir,
		scenario: base.scenario, seed: base.seed, arm: plan.Arm,
		serverPin: pin.CPUs, serverGOMAXPROCS: pin.GOMAXPROCS, serverCPUs: base.serverCPUs,
		genSet: plan.Gen.CPUs, genNCPU: plan.Gen.GOMAXPROCS,
		features: base.features, bot: base.bot,
		onProgress: base.onProgress, onControlLost: base.onControlLost, logf: base.logf, heartbeatEvery: base.heartbeatEvery,
	}
	if !plan.Distributed {
		return h
	}
	return &distEnv{hostEnv: h, plan: plan}
}

// distEnv starts the D2 arm: control, nodeA, nodeB and global, pinned per the
// plan, with the load bot dialling the two nodes.
type distEnv struct {
	*hostEnv
	plan ArmPlan
	// lastGz are the newest saved per-role logs (guarded by hostEnv.mu).
	lastGz map[topology.Role]string
}

func (e *distEnv) botArgv(top serverTopology, pw string) []string {
	return BotArgv("D2", e.scenario, ArmAddrs{
		Control: top.Addr(topology.Control),
		Nodes:   []string{top.Addr(topology.NodeA), top.Addr(topology.NodeB)},
	}, pw, e.seed, e.features, botParamsOr(e.bot))
}

// serverProcs are the processes the sampler reads: each with its own cpu count,
// and a voice socket for every role but control.
func (e *distEnv) serverProcs(top serverTopology) []serverProc {
	pids := top.PIDs()
	var out []serverProc
	for _, r := range distRoles {
		n := 1
		if l, err := CPUList(e.plan.Server[r].CPUs); err == nil && len(l) > 0 {
			n = len(l)
		}
		sp := serverProc{Role: string(r), PID: pids[r], NCPU: n}
		if r != topology.Control {
			sp.VoiceKey = top.Addr(r)
		}
		out = append(out, sp)
	}
	return out
}

func (e *distEnv) Start(ctx context.Context) (System, error) {
	e.mu.Lock()
	e.k++
	k := e.k
	e.mu.Unlock()
	top, err := startTopology(ctx, topology.Spec{Binary: e.serverBin, Dir: e.serverDir(k), Pin: e.plan.Server})
	if err != nil {
		return nil, fmt.Errorf("start distributed topology: %w", err)
	}
	pw, err := writePasswords(e.scratchDir)
	if err != nil {
		_ = top.Stop()
		return nil, err
	}
	hs, err := e.newSystem(k, top, e.botArgv(top, pw), e.serverProcs(top), func() (string, error) { return distLogText(top) })
	if err != nil {
		return nil, err
	}
	return &distSystem{hostSystem: hs, e: e, poller: &nodePoller{top: top, every: nodePollEvery}}, nil
}

// ServerLogText is the four role logs: read from the live processes, else from
// the copies kept in the artifact.
func (e *distEnv) ServerLogText() (string, error) {
	e.mu.Lock()
	live := e.live
	gz := make(map[topology.Role]string, len(e.lastGz))
	for r, p := range e.lastGz {
		gz[r] = p
	}
	e.mu.Unlock()
	if live != nil {
		return live.LogText()
	}
	if len(gz) == 0 {
		return "", fmt.Errorf("no server log was kept")
	}
	var b strings.Builder
	for _, r := range distRoles {
		if gz[r] == "" {
			continue
		}
		t, err := readGz(gz[r])
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "== %s ==\n%s", r, t)
	}
	return b.String(), nil
}

// distLogText is the four role logs, each under an "== <role> ==" header, in
// control, nodeA, nodeB, global order. CountLog over it sums every role.
func distLogText(top serverTopology) (string, error) {
	var b strings.Builder
	for _, r := range distRoles {
		t, err := top.LogText(r)
		if err != nil {
			return "", fmt.Errorf("%s log: %w", r, err)
		}
		fmt.Fprintf(&b, "== %s ==\n%s", r, t)
		if t != "" && !strings.HasSuffix(t, "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// distSystem is one running D2 topology plus its load bot. The bot-facing
// methods are the host system's; the log, the sampling window and Close differ.
type distSystem struct {
	*hostSystem
	e      *distEnv
	poller *nodePoller
}

// BeginSample runs the 1 s samplers and the distributionStatus poller together.
func (s *distSystem) BeginSample() func() Window {
	end := s.hostSystem.BeginSample()
	stop := s.poller.start()
	return func() Window {
		w := end()
		w.Nodes = stop()
		return w
	}
}

func (s *distSystem) Close() error {
	s.stopBot()
	errs := s.top.Errors()
	s.env.mu.Lock()
	s.env.serverErrs = append(s.env.serverErrs, errs...)
	s.env.mu.Unlock()
	// As for the standalone log: read before and after the stop, keep the
	// stop's own lines, and never lose a log to an unreadable second read.
	before := map[topology.Role]string{}
	for _, r := range distRoles {
		if t, err := s.top.LogText(r); err == nil {
			before[r] = t
		}
	}
	stopErr := s.top.Stop()
	gz := map[topology.Role]string{}
	for _, r := range distRoles {
		t, err := s.top.LogText(r)
		if err != nil {
			t = before[r]
		}
		p, serr := SaveRoleLog(s.env.outDir, s.k, string(r), t)
		if serr != nil {
			if s.env.logf != nil {
				s.env.logf("%s log not kept: %v", r, serr)
			}
			continue
		}
		gz[r] = p
	}
	s.env.mu.Lock()
	if s.env.live == s.hostSystem {
		s.env.live = nil
	}
	if len(gz) > 0 {
		s.e.lastGz = gz
	}
	s.env.mu.Unlock()
	return stopErr
}

// NodeWindow is the distributionStatus reading over a window. Polls counts the
// answered polls (0 = not measured), Errors the failed ones, Min the fewest
// nodes any answer listed, and Clients the last answer's connectedClients per
// role.
type NodeWindow struct {
	Polls, Errors, Min int
	Clients            map[string]int
}

// nodePoller reads control's distributionStatus: once as the window opens (so a
// 2 s hold still gets a reading), then every `every`, and once more when it ends.
type nodePoller struct {
	top   serverTopology
	every time.Duration
}

func (p *nodePoller) start() func() NodeWindow {
	var (
		mu   sync.Mutex
		w    = NodeWindow{Min: -1}
		quit = make(chan struct{})
		done = make(chan struct{})
	)
	roleOf := map[string]string{}
	for _, r := range []topology.Role{topology.NodeA, topology.NodeB, topology.Global} {
		roleOf[p.top.Addr(r)] = string(r)
	}
	poll := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		st, err := p.top.Status(ctx)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			w.Errors++
			return
		}
		w.Polls++
		if w.Min < 0 || len(st.Nodes) < w.Min {
			w.Min = len(st.Nodes)
		}
		w.Clients = map[string]int{}
		for _, n := range st.Nodes {
			if r := roleOf[n.Address]; r != "" {
				w.Clients[r] = n.ConnectedClients
			}
		}
	}
	go func() {
		defer close(done)
		poll()
		t := time.NewTicker(p.every)
		defer t.Stop()
		for {
			select {
			case <-quit:
				return
			case <-t.C:
				poll()
			}
		}
	}()
	return func() NodeWindow {
		close(quit)
		<-done
		poll()
		mu.Lock()
		defer mu.Unlock()
		out := w
		if out.Min < 0 {
			out.Min = 0
		}
		return out
	}
}

// noteNodeWindow turns a hold whose every distributionStatus poll failed into a
// breach: control stopped answering, which must not read as an unmeasured S or
// Seq hold. A worse outcome (invalid) is kept; the reason is added either way.
func noteNodeWindow(row *Row, nw NodeWindow) {
	if nw.Polls > 0 || nw.Errors == 0 {
		return
	}
	row.Reasons = append(row.Reasons, fmt.Sprintf("distributionStatus unreachable (%d failed polls)", nw.Errors))
	if row.Outcome == sweep.Pass {
		row.Outcome = sweep.Breach
	}
}

// nodeCells are the two steps.csv cells appended by W6 S5: the fewest nodes the
// poller saw and each role's connected clients. A row with no answered poll
// (S, Seq, or no hold) is empty cells, never zero.
func nodeCells(r Row) []string {
	cells := make([]string, 2)
	if r.Nodes.Polls == 0 {
		return cells
	}
	cells[0] = strconv.Itoa(r.Nodes.Min)
	roles := make([]string, 0, len(r.Nodes.Clients))
	for k := range r.Nodes.Clients {
		roles = append(roles, k)
	}
	sort.Strings(roles)
	parts := make([]string, len(roles))
	for i, k := range roles {
		parts[i] = k + "=" + strconv.Itoa(r.Nodes.Clients[k])
	}
	cells[1] = strings.Join(parts, ";")
	return cells
}
