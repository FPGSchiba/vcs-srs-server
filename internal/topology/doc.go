// Package topology starts and inspects real multi-process VCS server
// topologies (one control process plus voice nodes) on the loopback
// interface, for integration tests and benchmark tooling.
//
// It is not imported by the server binary. itest/distributed and
// cmd/vcs-benchkit use it. It takes no *testing.T (callers own test
// lifecycle), so every helper returns errors instead of failing a test.
package topology
