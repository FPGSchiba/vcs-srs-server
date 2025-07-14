package voice

import (
	"errors"
	"fmt"
	"github.com/google/uuid"
)

// PacketType represents the different packet types
type PacketType uint8

const (
	currentVersion uint8 = 1 // Current protocol version
)

const (
	PacketTypeVoice PacketType = iota
	PacketTypeHello
	PacketTypeHelloAck
	PacketTypeKeepalive
	PacketTypeBye
)

// String returns the string representation of PacketType
func (pt PacketType) String() string {
	switch pt {
	case PacketTypeVoice:
		return "VOICE"
	case PacketTypeHello:
		return "HELLO"
	case PacketTypeHelloAck:
		return "HELLO_ACK"
	case PacketTypeKeepalive:
		return "KEEPALIVE"
	case PacketTypeBye:
		return "BYE"
	default:
		return "UNKNOWN"
	}
}

// VCSPacket represents a parsed VCS protocol packet
type VCSPacket struct {
	Magic     [3]byte    // Protocol identifier (VCS)
	Version   uint8      // Protocol version (4 bits)
	Type      PacketType // Packet type (4 bits)
	Flags     uint8      // Flags (1. bit PTT, 2. bit Intercom, 6 bits reserved)
	Sequence  uint32     // 24-bit sequence number
	Frequency uint32     // 24-bit frequency in kHz
	SenderID  uuid.UUID  // UUIDv4 session identifier
	Payload   []byte     // Variable payload data
}

// Constants
const (
	HeaderSize = 27 // Total header size in bytes
	MagicVCS   = "VCS"
)

func NewVCSHalloAckPacket(clientId uuid.UUID) *VCSPacket {
	return &VCSPacket{
		Magic:     [3]byte{'V', 'C', 'S'},
		Version:   currentVersion,
		Type:      PacketTypeHelloAck,
		Flags:     0,               // No flags set
		Sequence:  0,               // Initial sequence number
		Frequency: 0,               // Default frequency
		SenderID:  clientId,        // Generate a new session ID
		Payload:   make([]byte, 0), // Empty payload
	}
}

func NewVCSVoicePacket(clientId uuid.UUID, sequence uint32, frequency uint32, payload []byte) *VCSPacket {
	return &VCSPacket{
		Magic:     [3]byte{'V', 'C', 'S'},
		Version:   currentVersion,
		Type:      PacketTypeVoice,
		Flags:     0,         // No flags set
		Sequence:  sequence,  // Set sequence number
		Frequency: frequency, // Set frequency in kHz
		SenderID:  clientId,  // Use provided session ID
		Payload:   payload,   // Set payload data
	}
}

func NewVCSKeepalivePacket(clientId uuid.UUID) *VCSPacket {
	return &VCSPacket{
		Magic:     [3]byte{'V', 'C', 'S'},
		Version:   currentVersion,
		Type:      PacketTypeKeepalive,
		Flags:     0,               // No flags set
		Sequence:  0,               // No sequence number needed
		Frequency: 0,               // Default frequency
		SenderID:  clientId,        // Use provided session ID
		Payload:   make([]byte, 0), // Empty payload
	}
}

// IsPTTActive returns true if the PTT flag is set
func (p *VCSPacket) IsPTTActive() bool {
	return (p.Flags & 0x01) != 0
}

// SetPTT sets or clears the PTT flag
func (p *VCSPacket) SetPTT(active bool) {
	if active {
		p.Flags |= 0x01
	} else {
		p.Flags &= 0xFE
	}
}

// IsIntercom returns true if the Intercom flag is set
func (p *VCSPacket) IsIntercom() bool {
	return (p.Flags & 0x02) != 0
}

// SetIntercom sets or clears the Intercom flag
func (p *VCSPacket) SetIntercom(active bool) {
	if active {
		p.Flags |= 0x02
	} else {
		p.Flags &= 0xFD
	}
}

// FrequencyMHz returns the frequency in MHz as a float64
func (p *VCSPacket) FrequencyMHz() float64 {
	return float64(p.Frequency) / 1000.0
}

// SetFrequencyMHz sets the frequency from MHz (converts to kHz internally)
func (p *VCSPacket) SetFrequencyMHz(freqMHz float64) {
	p.Frequency = uint32(freqMHz * 1000)
}

// ParsePacket parses a raw UDP packet into a VCSPacket struct
func ParsePacket(data []byte) (*VCSPacket, error) {
	if len(data) < HeaderSize {
		return nil, errors.New("packet too short")
	}

	packet := &VCSPacket{}

	// Parse magic (3 bytes)
	copy(packet.Magic[:], data[0:3])
	if string(packet.Magic[:]) != MagicVCS {
		return nil, fmt.Errorf("invalid magic: expected %s, got %s", MagicVCS, string(packet.Magic[:]))
	}

	// Parse version/type (1 byte)
	versionType := data[3]
	packet.Version = (versionType >> 4) & 0x0F
	if packet.Version != currentVersion {
		return nil, fmt.Errorf("unsupported protocol version: %d", packet.Version)
	}
	packet.Type = PacketType(versionType & 0x0F)

	// Parse flags (1 byte)
	packet.Flags = data[4]

	// Parse sequence (3 bytes, big-endian)
	packet.Sequence = uint32(data[5])<<16 | uint32(data[6])<<8 | uint32(data[7])

	// Parse frequency (3 bytes, big-endian)
	packet.Frequency = uint32(data[8])<<16 | uint32(data[9])<<8 | uint32(data[10])

	// Parse session ID (16 bytes)
	sessionBytes := data[11:27]
	var err error
	packet.SenderID, err = uuid.FromBytes(sessionBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid session ID: %v", err)
	}

	// Parse payload (remaining bytes)
	if len(data) > HeaderSize {
		packet.Payload = make([]byte, len(data)-HeaderSize)
		copy(packet.Payload, data[HeaderSize:])
	}

	return packet, nil
}

// SerializePacket converts a VCSPacket struct back to raw bytes
func (p *VCSPacket) SerializePacket() []byte {
	data := make([]byte, HeaderSize+len(p.Payload))

	// Magic (3 bytes)
	copy(data[0:3], p.Magic[:])

	// Version/Type (1 byte)
	data[3] = (p.Version << 4) | uint8(p.Type)

	// Flags (1 byte)
	data[4] = p.Flags

	// Sequence (3 bytes, big-endian)
	data[5] = byte(p.Sequence >> 16)
	data[6] = byte(p.Sequence >> 8)
	data[7] = byte(p.Sequence)

	// Frequency (3 bytes, big-endian)
	data[8] = byte(p.Frequency >> 16)
	data[9] = byte(p.Frequency >> 8)
	data[10] = byte(p.Frequency)

	// Session ID (16 bytes)
	sessionBytes, _ := p.SenderID.MarshalBinary()
	copy(data[11:27], sessionBytes)

	// Payload
	if len(p.Payload) > 0 {
		copy(data[HeaderSize:], p.Payload)
	}

	return data
}

// String returns a string representation of the packet for debugging
func (p *VCSPacket) String() string {
	return fmt.Sprintf("VCSPacket{Magic: %s, Version: %d, Type: %s, PTT: %t, Seq: %d, Freq: %.3f MHz, SenderID: %s, PayloadLen: %d}",
		string(p.Magic[:]), p.Version, p.Type, p.IsPTTActive(), p.Sequence, p.FrequencyMHz(), p.SenderID, len(p.Payload))
}

func (p *VCSPacket) FrequencyAsFloat64() float64 {
	return float64(p.Frequency) / 1000.0
}
