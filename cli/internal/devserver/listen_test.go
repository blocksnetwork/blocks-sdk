package devserver

import (
	"fmt"
	"net"
	"testing"
)

// TestListenWithRetry_FirstPortFree verifies the helper binds the requested
// port when it's available.
func TestListenWithRetry_FirstPortFree(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	freePort := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ln, bound, err := listenWithRetry(freePort, 5)
	if err != nil {
		t.Fatalf("listenWithRetry: %v", err)
	}
	defer ln.Close()
	if bound != freePort {
		t.Errorf("bound = %d, want %d", bound, freePort)
	}
}

// TestListenWithRetry_FallsForwardOnEADDRINUSE verifies retry semantics: when
// the base port is held, the helper advances to port+1 and succeeds there.
func TestListenWithRetry_FallsForwardOnEADDRINUSE(t *testing.T) {
	// Reserve a contiguous pair: hold N, leave N+1 free.
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve first port: %v", err)
	}
	defer first.Close()
	basePort := first.Addr().(*net.TCPAddr).Port

	// Probe that basePort+1 is free; if not, skip rather than flake.
	probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", basePort+1))
	if err != nil {
		t.Skipf("basePort+1 (%d) was not free at probe time; cannot exercise retry deterministically", basePort+1)
	}
	probe.Close()

	ln, bound, err := listenWithRetry(basePort, 5)
	if err != nil {
		t.Fatalf("listenWithRetry: %v", err)
	}
	defer ln.Close()
	if bound != basePort+1 {
		t.Errorf("bound = %d, want %d (retry should advance past held port)", bound, basePort+1)
	}
}

// TestListenWithRetry_GivesUpAfterAttempts verifies the helper returns an
// error mentioning the port range when every attempt is held.
func TestListenWithRetry_GivesUpAfterAttempts(t *testing.T) {
	const attempts = 3

	// Hold `attempts` consecutive ports. Bind to :0 once to pick a base, then
	// retry-probe upward until we have a contiguous block; skip on contention.
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve base: %v", err)
	}
	defer first.Close()
	base := first.Addr().(*net.TCPAddr).Port

	held := []net.Listener{first}
	for i := 1; i < attempts; i++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base+i))
		if err != nil {
			for _, l := range held[1:] {
				l.Close()
			}
			t.Skipf("could not reserve contiguous port %d; skipping", base+i)
		}
		held = append(held, ln)
	}
	defer func() {
		for _, l := range held[1:] {
			l.Close()
		}
	}()

	ln, _, err := listenWithRetry(base, attempts)
	if err == nil {
		ln.Close()
		t.Fatal("expected error when all ports in range are held")
	}
	if !contains(err.Error(), "in use") {
		t.Errorf("error should mention 'in use', got: %v", err)
	}
}

// TestListenWithRetry_ZeroPortPassesThrough verifies port==0 path skips retry
// and lets the OS pick.
func TestListenWithRetry_ZeroPortPassesThrough(t *testing.T) {
	ln, bound, err := listenWithRetry(0, 5)
	if err != nil {
		t.Fatalf("listenWithRetry(0): %v", err)
	}
	defer ln.Close()
	if bound == 0 {
		t.Errorf("bound = 0, want OS-assigned port")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
