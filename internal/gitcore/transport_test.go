package gitcore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// httpsRemote serves the bare repositories under root with git's own
// smart-HTTP backend, behind TLS and basic authentication. mangle, when
// set, may rewrite pack responses.
func httpsRemote(t *testing.T, root, user, pass string, mangle func(w http.ResponseWriter) http.ResponseWriter) (string, []byte) {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	backend := &cgi.Handler{Path: bin, Args: []string{"http-backend"}, Env: []string{
		"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "REMOTE_USER=" + user,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + t.TempDir()}, InheritEnv: []string{"PATH"}, Stderr: io.Discard}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="knowledge"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if mangle != nil && strings.HasSuffix(r.URL.Path, "/git-upload-pack") {
			w = mangle(w)
		}
		backend.ServeHTTP(w, r)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// afterBytes calls at once the response has carried n bytes.
type afterBytes struct {
	http.ResponseWriter
	n, sent int64
	at      func(p []byte, i int) []byte
	done    atomic.Bool
	drop    bool
}

func (a *afterBytes) Write(p []byte) (int, error) {
	if a.drop {
		return len(p), nil
	}
	n := len(p)
	if !a.done.Load() && a.sent+int64(len(p)) > a.n {
		a.done.Store(true)
		p = a.at(append([]byte(nil), p...), int(a.n-a.sent))
		a.drop = len(p) < n
	}
	a.sent += int64(len(p))
	if _, err := a.ResponseWriter.Write(p); err != nil {
		return 0, err
	}
	return n, nil
}

func (a *afterBytes) Flush() {
	if f, ok := a.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func TestHTTPSRemote(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runGit(t, "init", "--quiet", "--bare", "--initial-branch=main", filepath.Join(root, "k.git"))
	const user, pass = "polaroid", "s3cret-token-value"
	base, ca := httpsRemote(t, root, user, pass, nil)
	rm := Remote{Name: "hub", URL: base + "/k.git", CABundle: ca}
	good := Credentials{Username: user, Password: pass}

	a := open(t, t.TempDir())
	s1 := publish(t, a, "", put("p/a.json", "1"))
	if _, err := a.Push(ctx, rm, Credentials{Username: user, Password: "wrong"}); !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), "wrong") {
		t.Fatalf("push with a wrong token: %v", err)
	}
	if _, err := a.Push(ctx, rm, good); err != nil {
		t.Fatal(err)
	}
	b := open(t, t.TempDir())
	for _, cred := range []Credentials{{}, {Username: user, Password: "wrong"}} {
		if _, err := b.Fetch(ctx, rm, cred, TransferLimits{}); !errors.Is(err, ErrUnauthorized) || strings.Contains(err.Error(), pass) || strings.Contains(err.Error(), "wrong") {
			t.Fatalf("fetch with %v: %v", cred, err)
		}
	}
	if _, err := b.Fetch(ctx, Remote{Name: "hub", URL: rm.URL}, good, TransferLimits{}); err == nil || strings.Contains(err.Error(), pass) {
		t.Fatalf("fetch from an untrusted certificate: %v", err)
	}
	c, err := b.Fetch(ctx, rm, good, TransferLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, b.Import(ctx, c))
	check(t, advance(b, "", s1))
	if read(t, b, s1, "p/a.json") != "1" {
		t.Fatal("read over HTTPS")
	}
}

// A connection cut mid-pack and a pack damaged in transit both fail the
// fetch, and leave nothing behind.
func TestDamagedTransfers(t *testing.T) {
	ctx := context.Background()
	for name, at := range map[string]func(p []byte, i int) []byte{
		"interrupted": func(p []byte, i int) []byte { panic(http.ErrAbortHandler) },
		"corrupted": func(p []byte, i int) []byte {
			p[i] ^= 0x55
			return p
		},
		"truncated": func(p []byte, i int) []byte { return p[:i] },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			runGit(t, "init", "--quiet", "--bare", "--initial-branch=main", filepath.Join(root, "k.git"))
			var cut atomic.Bool
			base, ca := httpsRemote(t, root, "u", "p", func(w http.ResponseWriter) http.ResponseWriter {
				if !cut.Load() {
					return w
				}
				return &afterBytes{ResponseWriter: w, n: 400, at: at}
			})
			rm := Remote{Name: "hub", URL: base + "/k.git", CABundle: ca}
			cred := Credentials{Username: "u", Password: "p"}
			a := open(t, t.TempDir())
			var changes []Change
			for i := range 40 {
				changes = append(changes, put("p/f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".json", strings.Repeat(string(rune('a'+i%26)), 200+i)))
			}
			publish(t, a, "", changes...)
			if _, err := a.Push(ctx, rm, cred); err != nil {
				t.Fatal(err)
			}
			cut.Store(true)
			dir := t.TempDir()
			b := open(t, dir)
			c, err := b.Fetch(ctx, rm, cred, TransferLimits{})
			if err == nil {
				_, err = c.Verify(ctx)
				_ = c.Close()
			}
			if err == nil {
				t.Fatal("a damaged transfer was accepted")
			}
			t.Log(err)
			if tr, _ := b.Tracking(ctx, rm); tr != "" || active(t, b) != "" || quarantines(t, dir) != nil {
				t.Fatal("a failed transfer left state behind")
			}
		})
	}
}

// sshRemote serves git-upload-pack and git-receive-pack for repositories
// under root over SSH, to one client key.
func sshRemote(t *testing.T, root string, client ssh.PublicKey) (string, ssh.PublicKey) {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	_, hostPriv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(hostPriv)
	check(t, err)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(client.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("unknown key")
	}}
	cfg.AddHostKey(signer)
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	check(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	serve := func(ch ssh.Channel, cmdline string) {
		defer func() { _ = ch.Close() }()
		name, arg, _ := strings.Cut(cmdline, " ")
		path := strings.Trim(arg, "'")
		code := uint32(1)
		if (name == "git-upload-pack" || name == "git-receive-pack") && strings.HasPrefix(filepath.Clean(path), root) {
			cmd := exec.CommandContext(context.Background(), bin, strings.TrimPrefix(name, "git-"), path)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			in, _ := cmd.StdinPipe()
			cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
			go func() { _, _ = io.Copy(in, ch); _ = in.Close() }()
			if cmd.Run() == nil {
				code = 0
			}
		}
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], code)
		_, _ = ch.SendRequest("exit-status", false, b[:])
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					_ = conn.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "session" {
						_ = nc.Reject(ssh.UnknownChannelType, "session only")
						continue
					}
					ch, creqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for req := range creqs {
							if req.Type == "exec" && len(req.Payload) > 4 {
								_ = req.Reply(true, nil)
								go serve(ch, string(req.Payload[4:]))
								continue
							}
							_ = req.Reply(req.Type == "env", nil)
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func sshKey(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKey(priv, "")
	check(t, err)
	sp, err := ssh.NewPublicKey(pub)
	check(t, err)
	return pem.EncodeToMemory(block), sp
}

func TestSSHRemote(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	check(t, err)
	repo := filepath.Join(root, "k.git")
	runGit(t, "init", "--quiet", "--bare", "--initial-branch=main", repo)
	key, pub := sshKey(t)
	addr, hostKey := sshRemote(t, root, pub)
	hosts := filepath.Join(t.TempDir(), "known_hosts")
	check(t, os.WriteFile(hosts, []byte(knownhosts.Line([]string{knownhosts.Normalize(addr)}, hostKey)+"\n"), 0o600))
	rm := Remote{Name: "box", URL: "ssh://git@" + addr + repo}
	cred := Credentials{SSHKey: key, KnownHosts: hosts}

	a := open(t, t.TempDir())
	s1 := publish(t, a, "", put("p/a.json", "1"))
	if _, err := a.Push(ctx, rm, cred); err != nil {
		t.Fatal(err)
	}
	b := open(t, t.TempDir())
	c, err := b.Fetch(ctx, rm, cred, TransferLimits{})
	check(t, err)
	if commits, err := c.Verify(ctx); err != nil || len(commits) != 1 {
		t.Fatalf("commits = %+v, %v", commits, err)
	}
	check(t, b.Import(ctx, c))
	check(t, advance(b, "", s1))

	otherKey, _ := sshKey(t)
	if _, err := b.Fetch(ctx, rm, Credentials{SSHKey: otherKey, KnownHosts: hosts}, TransferLimits{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("fetch with an unknown key: %v", err)
	}
	_, impostor := sshKey(t)
	wrongHosts := filepath.Join(t.TempDir(), "known_hosts")
	check(t, os.WriteFile(wrongHosts, []byte(knownhosts.Line([]string{knownhosts.Normalize(addr)}, impostor)+"\n"), 0o600))
	if _, err := b.Fetch(ctx, rm, Credentials{SSHKey: key, KnownHosts: wrongHosts}, TransferLimits{}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("fetch from a server with another host key: %v", err)
	}
}
