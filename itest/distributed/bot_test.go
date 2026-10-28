//go:build integration

package distributed

import (
	"context"
	"encoding/binary"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/FPGSchiba/vcs-srs-server/internal/topology"
	pb "github.com/FPGSchiba/vcs-srs-server/srspb"
	"github.com/FPGSchiba/vcs-srs-server/voice"
)

// bot is a protocol-level VCS client. It speaks the server's own codec on
// both planes. It diverges from the real VCS client in two labelled ways:
//
//	(a) it advertises DISTRIBUTED (F1: the real client advertises only
//	    STANDALONE and cannot log in to a control-mode server;
//	    srs/auth_service.go:308-314);
//	(b) it hashes the password with bcrypt cost 4 (the server enforces only a
//	    maximum of 12).
type bot struct {
	t         *testing.T
	guid      string
	guidU     uuid.UUID
	token     string
	coalition string

	conn      *grpc.ClientConn
	srs       pb.SRSServiceClient
	ctx       context.Context
	updates   chan *pb.ServerUpdate
	streamErr chan error

	secret        string
	coalitionAddr string
	globalAddr    string

	udp map[string]*net.UDPConn
}

var botSeq atomic.Int64

// itestUnitID matches the server's unit id pattern ^[A-Z0-9]{2,4}$ (srs/utils.go:56).
const itestUnitID = "ITST"

// newBot logs a bot in against the shared topology.
func newBot(t *testing.T, coalition string) *bot {
	t.Helper()
	return newBotOn(t, top, coalition)
}

// newBotOn logs a bot in against tp: InitAuth, GuestLogin, SubscribeToUpdates,
// SyncClient.
func newBotOn(t *testing.T, tp *topology.Topology, coalition string) *bot {
	t.Helper()
	b := &bot{
		t:         t,
		coalition: coalition,
		updates:   make(chan *pb.ServerUpdate, 256),
		streamErr: make(chan error, 1),
		udp:       map[string]*net.UDPConn{},
	}

	conn, err := grpc.NewClient(tp.Addr(topology.Control), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("bot dial: %v", err)
	}
	b.conn = conn

	rpcCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	auth := pb.NewAuthServiceClient(conn)
	initResp, err := auth.InitAuth(rpcCtx, &pb.AuthInitRequest{Capabilities: &pb.ClientCapabilities{
		Version:                    "v0.1.0",
		SupportedDistributionModes: []pb.DistributionMode{pb.DistributionMode_DISTRIBUTED},
	}})
	if err != nil {
		t.Fatalf("InitAuth: %v", err)
	}
	if !initResp.GetSuccess() {
		t.Fatalf("InitAuth refused: %s", initResp.GetErrorMessage())
	}
	b.guid = initResp.GetResult().GetClientGuid()
	if b.guidU, err = uuid.Parse(b.guid); err != nil {
		t.Fatalf("InitAuth returned a non-UUID client guid %q: %v", b.guid, err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(coalitionPassword(t, coalition)), 4)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	login, err := auth.GuestLogin(rpcCtx, &pb.GuestLoginRequest{
		Name:       "itest-" + strconv.FormatInt(botSeq.Add(1), 10),
		Password:   string(hash),
		UnitId:     itestUnitID,
		ClientGuid: b.guid,
	})
	if err != nil {
		t.Fatalf("GuestLogin: %v", err)
	}
	if !login.GetSuccess() {
		t.Fatalf("GuestLogin refused: %s", login.GetErrorMessage())
	}
	b.token = login.GetResult().GetToken()

	b.ctx = metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+b.token)
	b.srs = pb.NewSRSServiceClient(conn)

	streamCtx, streamCancel := context.WithCancel(b.ctx)
	stream, err := b.srs.SubscribeToUpdates(streamCtx, &pb.Empty{})
	if err != nil {
		streamCancel()
		t.Fatalf("SubscribeToUpdates: %v", err)
	}
	hdr := make(chan error, 1)
	go func() {
		_, err := stream.Header()
		hdr <- err
	}()
	select {
	case err := <-hdr:
		if err != nil {
			streamCancel()
			t.Fatalf("update stream header: %v", err)
		}
	case <-time.After(2 * time.Second):
		streamCancel()
		t.Fatalf("update stream header not received within 2s")
	}
	go func() {
		for {
			u, err := stream.Recv()
			if err != nil {
				b.streamErr <- err
				close(b.updates)
				return
			}
			select {
			case b.updates <- u:
			default: // never block the receive loop; the buffer holds 256
			}
		}
	}()

	t.Cleanup(func() {
		for _, c := range b.udp {
			bye := voice.NewVCSKeepalivePacket(b.guidU)
			bye.Type = voice.PacketTypeBye
			_, _ = c.Write(bye.SerializePacket())
			_ = c.Close()
		}
		dctx, dcancel := context.WithTimeout(b.ctx, 2*time.Second)
		_, _ = b.srs.Disconnect(dctx, &pb.Empty{})
		dcancel()
		streamCancel()
		_ = b.conn.Close()
	})

	sync, err := b.srs.SyncClient(b.ctx, &pb.Empty{})
	if err != nil {
		t.Fatalf("SyncClient: %v", err)
	}
	if !sync.GetSuccess() {
		t.Fatalf("SyncClient refused: %s", sync.GetErrorMessage())
	}
	d := sync.GetData()
	b.secret = d.GetVoiceSecret()
	b.coalitionAddr = d.GetCoalitionVoiceAddr()
	b.globalAddr = d.GetGlobalVoiceAddr()
	return b
}

func coalitionPassword(t *testing.T, name string) string {
	t.Helper()
	for _, c := range topology.DefaultCoalitions {
		if c.Name == name {
			return c.Password
		}
	}
	t.Fatalf("unknown coalition %q", name)
	return ""
}

// tune sets radios 1..n to the given frequencies (MHz), all enabled.
func (b *bot) tune(freqsMHz ...float32) {
	b.t.Helper()
	radios := make([]*pb.Radio, len(freqsMHz))
	for i, f := range freqsMHz {
		radios[i] = &pb.Radio{Id: uint32(i + 1), Name: "R" + strconv.Itoa(i+1), Frequency: f * 1e6, Enabled: true}
	}
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	resp, err := b.srs.UpdateRadioInfo(ctx, &pb.RadioInfo{Radios: radios})
	if err != nil {
		b.t.Fatalf("UpdateRadioInfo: %v", err)
	}
	if !resp.GetSuccess() {
		b.t.Fatalf("UpdateRadioInfo refused: %s", resp.GetErrorMessage())
	}
}

// sock returns the bot's UDP socket towards addr, dialling it on first use.
func (b *bot) sock(addr string) *net.UDPConn {
	b.t.Helper()
	if c, ok := b.udp[addr]; ok {
		return c
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		b.t.Fatalf("resolve %s: %v", addr, err)
	}
	c, err := net.DialUDP("udp", nil, ua)
	if err != nil {
		b.t.Fatalf("dial udp %s: %v", addr, err)
	}
	b.udp[addr] = c
	return c
}

// hello sends a HELLO to addr every 100 ms until a HelloAck arrives or the
// deadline passes. It reports whether it was acknowledged.
func (b *bot) hello(addr string, within time.Duration) bool {
	b.t.Helper()
	c := b.sock(addr)
	pkt := voice.NewVCSHelloPacket(b.guidU, b.secret).SerializePacket()
	deadline := time.Now().Add(within)
	buf := make([]byte, 2048)
	for time.Now().Before(deadline) {
		if _, err := c.Write(pkt); err != nil {
			// A refused loopback send surfaces as an ICMP error on the
			// next call; treat it as "no ack yet".
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		for {
			n, err := c.Read(buf)
			if err != nil {
				break
			}
			if p, perr := voice.ParsePacket(buf[:n]); perr == nil && p.Type == voice.PacketTypeHelloAck {
				return true
			}
		}
	}
	return false
}

// dialGlobal HELLOs the global node.
//
// not real-client behaviour (client internal/app/voice.go:699 drops the global address)
func (b *bot) dialGlobal() bool {
	b.t.Helper()
	return b.hello(b.globalAddr, 2*time.Second)
}

// framePayload is the 80-byte opaque voice payload: [seq BE u32 | 76 bytes
// 0xA5]. 80 bytes is above the server's 5-byte drop floor (voice/server.go:505).
func framePayload(seq uint32) []byte {
	p := make([]byte, 80)
	binary.BigEndian.PutUint32(p, seq)
	for i := 4; i < len(p); i++ {
		p[i] = 0xA5
	}
	return p
}

// send transmits one voice frame per seq on freqKHz to addr.
func (b *bot) send(addr string, freqKHz uint32, seqs ...uint32) {
	b.t.Helper()
	c := b.sock(addr)
	for _, s := range seqs {
		pkt := voice.NewVCSVoicePacket(b.guidU, s, freqKHz, framePayload(s))
		if _, err := c.Write(pkt.SerializePacket()); err != nil {
			b.t.Fatalf("send: %v", err)
		}
	}
}

// recvFrames collects the voice payloads received from addr on freqKHz
// within window, in arrival order.
func (b *bot) recvFrames(addr string, freqKHz uint32, window time.Duration) [][]byte {
	b.t.Helper()
	c := b.sock(addr)
	var out [][]byte
	buf := make([]byte, 2048)
	_ = c.SetReadDeadline(time.Now().Add(window))
	for {
		n, err := c.Read(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return out
			}
			continue // e.g. a stale ICMP refusal; keep reading until the deadline
		}
		p, perr := voice.ParsePacket(buf[:n])
		if perr != nil || p.Type != voice.PacketTypeVoice || p.Frequency != freqKHz || len(p.Payload) < 4 {
			continue
		}
		out = append(out, append([]byte(nil), p.Payload...))
	}
}

// recv returns the voice sequence numbers received from addr on freqKHz
// within window.
func (b *bot) recv(addr string, freqKHz uint32, window time.Duration) []uint32 {
	b.t.Helper()
	var seqs []uint32
	for _, p := range b.recvFrames(addr, freqKHz, window) {
		seqs = append(seqs, binary.BigEndian.Uint32(p))
	}
	return seqs
}

// freqKHz converts MHz to the wire's kHz.
func freqKHz(mhz float32) uint32 { return uint32(mhz*1000 + 0.5) }
