package nodebroker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The broker refusal codes, verbatim as both SDKs match on them.
const (
	CodeSecretNotDeclared = "secret_not_declared"
	CodeHandleConsumed    = "handle_consumed"
	CodeInvocationUnknown = "invocation_unknown"
)

// Request is the body both SDKs POST to /v1/secret.
type Request struct {
	Handle     string `json:"handle"`
	Invocation string `json:"invocation"`
	Name       string `json:"name"`
	Node       string `json:"node"`
}

// Response is the answer both SDKs decode.
type Response struct {
	Code  string `json:"code"`
	Found bool   `json:"found"`
	Value string `json:"value"`
}

// Answer is what the broker returns for one declared secret.
type Answer struct {
	Found bool
	Value string
}

// Broker answers a node's secret redemptions.
//
// It is deliberately strict: an unbound name, a wrong handle and a second
// redemption are all refused with the SDKs' own codes, so a caller proves the
// real redemption path rather than a permissive stub.
type Broker struct {
	// invocation, when non-empty, is the ONLY invocation id the broker answers.
	// Empty disables the check (the CALL's author owns the id).
	invocation string
	// handles maps a declared secret name to the ONE handle that redeems it.
	handles map[string]string
	// answers maps a declared secret name to what redeeming it yields.
	answers map[string]Answer

	// mu guards redeemed, which is keyed by HANDLE, not by name: a handle is
	// one-shot, so a handle bound to two names must still redeem exactly once.
	// Keying by name would let the same credential be spent twice under two
	// labels.
	mu       sync.Mutex
	redeemed map[string]bool
}

// New builds a broker over an explicit handle/answer binding.
func New(invocation string, handles map[string]string, answers map[string]Answer) *Broker {
	return &Broker{
		invocation: invocation,
		handles:    handles,
		answers:    answers,
		redeemed:   map[string]bool{},
	}
}

// ServeHTTP answers POST /v1/secret exactly as the SDKs expect.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/secret" {
		http.NotFound(w, r)
		return
	}
	var req Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		write(w, http.StatusForbidden, Response{Code: CodeSecretNotDeclared})
		return
	}
	if b.invocation != "" && req.Invocation != b.invocation {
		write(w, http.StatusNotFound, Response{Code: CodeInvocationUnknown})
		return
	}
	bound, declared := b.handles[req.Name]
	if !declared || bound == "" || req.Handle != bound {
		write(w, http.StatusForbidden, Response{Code: CodeSecretNotDeclared})
		return
	}

	b.mu.Lock()
	if b.redeemed[req.Handle] {
		b.mu.Unlock()
		write(w, http.StatusConflict, Response{Code: CodeHandleConsumed})
		return
	}
	b.redeemed[req.Handle] = true
	b.mu.Unlock()

	answer := b.answers[req.Name]
	write(w, http.StatusOK, Response{Found: answer.Found, Value: answer.Value})
}

func write(w http.ResponseWriter, statusCode int, body Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}

// Listen opens the unix socket the SDKs dial. It creates the parent dir,
// removes a stale socket file so a rerun in the same place works, and makes the
// socket world-connectable.
//
// The 0666 is ON PURPOSE and is not a weakening: a unix socket is created
// 0777 &^ umask (0755 under the usual umask) and connect(2) needs WRITE on the
// socket inode, so a broker listening as one uid is unreachable by a node
// running as the image's non-root USER — which is exactly the runtime's
// topology and exactly how the live v1.0.4 bundle failed (`dial unix
// /run/sentiae/broker.sock: connect: permission denied`). The socket is
// bind-mounted into exactly ONE sandbox, and the HANDLE — one-shot, bound per
// invocation — is the credential; the file mode is not the boundary
// (DESIGN §3.5/§3.7). A group-scoped 0660 was rejected because it would couple
// every node image to gid 65534.
func Listen(socket string) (net.Listener, error) {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// The DIRECTORY's mode is as load-bearing as the socket's write bit:
	// connect(2) needs SEARCH permission on every path component, so a 0700
	// parent locks the node out even with a 0666 socket. MkdirAll's mode is
	// narrowed by the umask and does nothing at all to a directory that already
	// exists, so the invariant is ENFORCED here rather than assumed — and before
	// Listen, so a directory we cannot open up never gets a socket in it.
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, fmt.Errorf("broker socket dir %s: chmod 0755: %w", dir, err)
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lis, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o666); err != nil {
		_ = lis.Close()
		_ = os.Remove(socket)
		return nil, fmt.Errorf("broker socket %s: chmod 0666: %w", socket, err)
	}
	return lis, nil
}

// Serve runs h on lis until the listener closes.
func Serve(lis net.Listener, h http.Handler) *http.Server {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer func() { _ = recover() }()
		_ = srv.Serve(lis)
	}()
	return srv
}

// NewHandle mints one redemption credential: `handle:` plus 32 lower-hex
// characters of 16 crypto/rand bytes. The handle IS the credential that crosses
// into the sandbox, so it is never derived from anything the node can predict —
// not the pin, not the secret's name, not the invocation id.
func NewHandle() string {
	var b [16]byte
	// crypto/rand.Read never returns an error; it fills b entirely or crashes
	// the program, which is the correct outcome for an unseedable system.
	_, _ = rand.Read(b[:])
	return "handle:" + hex.EncodeToString(b[:])
}
