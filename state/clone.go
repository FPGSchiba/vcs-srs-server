package state

import "github.com/google/uuid"

// Clone returns a copy of c that shares no memory with it. nil -> nil.
func (c *ClientState) Clone() *ClientState {
	if c == nil {
		return nil
	}
	cp := *c
	return &cp
}

// Clone returns a copy of r with its own Radios slice (nil stays nil). nil -> nil.
func (r *RadioState) Clone() *RadioState {
	if r == nil {
		return nil
	}
	cp := *r
	if r.Radios != nil {
		cp.Radios = make([]Radio, len(r.Radios))
		copy(cp.Radios, r.Radios)
	}
	return &cp
}

// CloneClientsLocked deep-copies Clients for an event snapshot or a reader
// outside the lock. The caller holds s (read or write). Nil entries are skipped.
func (s *ServerState) CloneClientsLocked() map[uuid.UUID]*ClientState {
	out := make(map[uuid.UUID]*ClientState, len(s.Clients))
	for k, v := range s.Clients {
		if v == nil {
			continue
		}
		out[k] = v.Clone()
	}
	return out
}

// CloneRadiosLocked deep-copies RadioClients. The caller holds s (read or
// write). Nil entries are skipped.
func (s *ServerState) CloneRadiosLocked() map[uuid.UUID]*RadioState {
	out := make(map[uuid.UUID]*RadioState, len(s.RadioClients))
	for k, v := range s.RadioClients {
		if v == nil {
			continue
		}
		out[k] = v.Clone()
	}
	return out
}
