package nodebroker

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

const testInvocation = "inv-0000000000000000"

func post(t *testing.T, srv *httptest.Server, req Request) (int, Response) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := srv.Client().Post(srv.URL+"/v1/secret", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var answer Response
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, answer
}

// shortTempDir is t.TempDir() with a SHORT name: a unix socket path is capped
// at ~104 bytes by sun_path, and t.TempDir() embeds the test's full name, which
// pushes the path over the limit on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nb-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestBroker_Protocol pins the broker against the SDKs' expectations — including
// the refusals. A permissive stub would prove nothing: the value of this broker
// is that a node exercises the REAL redemption path.
//
// The one-shot rule is keyed by HANDLE, not by name, and the last two rows are
// what prove it: the same handle is bound to two declared names, so keying by
// name would let one credential be spent twice under two labels.
//
// CONTROL: key `redeemed` by req.Name instead of req.Handle — the
// "the same handle under a second name is refused" row answers 200 and goes red.
func TestBroker_Protocol(t *testing.T) {
	const shared = "handle:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	handles := map[string]string{
		"greeting_suffix": "handle:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"api_key":         "handle:cccccccccccccccccccccccccccccccc",
		"twin_a":          shared,
		"twin_b":          shared,
		"unbindable":      "",
	}
	answers := map[string]Answer{
		"greeting_suffix": {Found: false},
		"api_key":         {Found: true, Value: "value-api_key"},
		"twin_a":          {Found: true, Value: "value-twin"},
		"twin_b":          {Found: true, Value: "value-twin"},
	}
	srv := httptest.NewServer(New(testInvocation, handles, answers))
	t.Cleanup(srv.Close)

	tests := []struct {
		name       string
		req        Request
		wantStatus int
		wantCode   string
		wantFound  bool
		wantValue  string
	}{
		{
			name:       "optional declared secret answers not-found",
			req:        Request{Handle: handles["greeting_suffix"], Invocation: testInvocation, Name: "greeting_suffix", Node: "greet"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "required declared secret answers a value",
			req:        Request{Handle: handles["api_key"], Invocation: testInvocation, Name: "api_key", Node: "greet"},
			wantStatus: http.StatusOK,
			wantFound:  true,
			wantValue:  "value-api_key",
		},
		{
			name:       "an undeclared name is refused",
			req:        Request{Handle: handles["api_key"], Invocation: testInvocation, Name: "other", Node: "greet"},
			wantStatus: http.StatusForbidden,
			wantCode:   CodeSecretNotDeclared,
		},
		{
			name:       "a declared name with no binding is refused",
			req:        Request{Handle: "", Invocation: testInvocation, Name: "unbindable", Node: "greet"},
			wantStatus: http.StatusForbidden,
			wantCode:   CodeSecretNotDeclared,
		},
		{
			name:       "a wrong handle is refused",
			req:        Request{Handle: "handle:00000000000000000000000000000000", Invocation: testInvocation, Name: "greeting_suffix", Node: "greet"},
			wantStatus: http.StatusForbidden,
			wantCode:   CodeSecretNotDeclared,
		},
		{
			name:       "a second redemption is refused",
			req:        Request{Handle: handles["api_key"], Invocation: testInvocation, Name: "api_key", Node: "greet"},
			wantStatus: http.StatusConflict,
			wantCode:   CodeHandleConsumed,
		},
		{
			name:       "a foreign invocation is refused",
			req:        Request{Handle: handles["greeting_suffix"], Invocation: "inv-9999999999999999", Name: "greeting_suffix", Node: "greet"},
			wantStatus: http.StatusNotFound,
			wantCode:   CodeInvocationUnknown,
		},
		{
			name:       "positive anchor: the shared handle redeems once under its first name",
			req:        Request{Handle: shared, Invocation: testInvocation, Name: "twin_a", Node: "greet"},
			wantStatus: http.StatusOK,
			wantFound:  true,
			wantValue:  "value-twin",
		},
		{
			name:       "the same handle under a second name is refused",
			req:        Request{Handle: shared, Invocation: testInvocation, Name: "twin_b", Node: "greet"},
			wantStatus: http.StatusConflict,
			wantCode:   CodeHandleConsumed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, answer := post(t, srv, tt.req)
			if status != tt.wantStatus {
				t.Fatalf("status: got %d, want %d (%+v)", status, tt.wantStatus, answer)
			}
			if answer.Code != tt.wantCode {
				t.Errorf("code: got %q, want %q", answer.Code, tt.wantCode)
			}
			if answer.Found != tt.wantFound {
				t.Errorf("found: got %v, want %v", answer.Found, tt.wantFound)
			}
			if answer.Value != tt.wantValue {
				t.Errorf("value: got %q, want %q", answer.Value, tt.wantValue)
			}
		})
	}
}

// TestBroker_RejectsOtherRoutes pins that the broker is exactly one endpoint:
// anything else is a 404, so a mistyped path can never be read as an answer.
func TestBroker_RejectsOtherRoutes(t *testing.T) {
	srv := httptest.NewServer(New(testInvocation, map[string]string{}, map[string]Answer{}))
	t.Cleanup(srv.Close)

	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/secret"},
		{http.MethodPost, "/v1/secrets"},
		{http.MethodPost, "/"},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, srv.URL+tt.path, strings.NewReader("{}"))
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status: got %d, want 404", resp.StatusCode)
			}
		})
	}
}

// TestListen_SocketIsWorldConnectable pins the socket mode. A unix socket is
// created 0777 &^ umask, and connect(2) needs WRITE on the socket inode — so
// under any ordinary umask a broker listening as one uid is unreachable by a
// node running as the image's non-root USER. That is the runtime's topology, and
// it is exactly how the live v1.0.4 bundle failed: `dial unix
// /run/sentiae/broker.sock: connect: permission denied`.
//
// The umask is forced to 0077 so the test states the mode the CHMOD produces,
// not the mode the developer's umask happens to allow.
//
// CONTROL: drop the os.Chmod on the socket from Listen — the socket comes out
// 0700 under this umask (0755 under the usual one) and the assertion goes red.
func TestListen_SocketIsWorldConnectable(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	socket := filepath.Join(shortTempDir(t), "s", "broker.sock")
	lis, err := Listen(socket)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = lis.Close() }()

	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o666 {
		t.Fatalf("socket mode: got %04o, want 0666 (a node running as the image's USER could not connect)", got)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Errorf("positive anchor: %s is not a socket (%s)", socket, info.Mode())
	}

	// The DIRECTORY matters as much: connect(2) needs search permission on every
	// path component, so a 0700 parent locks the node out even with a 0666
	// socket. MkdirAll under this umask would make it 0700, so only the chmod in
	// Listen can make this assertion pass.
	dirInfo, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o755 {
		t.Fatalf("socket dir mode: got %04o, want 0755 (a 0666 socket is still unreachable through it)", got)
	}
}

// TestListen_RemovesAStaleSocket proves a rerun in the same directory works: a
// leftover socket file from a previous invocation must not make Listen fail.
func TestListen_RemovesAStaleSocket(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "broker.sock")
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}
	lis, err := Listen(socket)
	if err != nil {
		t.Fatalf("Listen over a stale socket: %v", err)
	}
	defer func() { _ = lis.Close() }()
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket (%s)", socket, info.Mode())
	}
}

var handleRx = regexp.MustCompile(`^handle:[0-9a-f]{32}$`)

// TestNewHandle pins the handle grammar and its unpredictability. The handle IS
// the credential that crosses into the sandbox, so it must be random rather than
// derived: a handle a node could compute from its own pin, name or invocation id
// would let it redeem a secret it was never issued.
//
// CONTROL: derive the handle deterministically (e.g. sha256 of a fixed string)
// and the distinctness row goes red.
func TestNewHandle(t *testing.T) {
	const draws = 1000
	seen := make(map[string]struct{}, draws)
	for i := 0; i < draws; i++ {
		h := NewHandle()
		if !handleRx.MatchString(h) {
			t.Fatalf("handle %q does not match %s", h, handleRx)
		}
		if len(h) < 24 {
			t.Fatalf("handle %q is %d bytes, under the ABI's 24-byte floor", h, len(h))
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(h, "handle:")); err != nil {
			t.Fatalf("handle %q is not lower-hex: %v", h, err)
		}
		if _, dup := seen[h]; dup {
			t.Fatalf("handle %q drawn twice in %d draws", h, draws)
		}
		seen[h] = struct{}{}
	}
}
