package gitcore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/file"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

var (
	// ErrUnauthorized: the remote refused the credentials, or an SSH
	// server's host key is not trusted.
	ErrUnauthorized = errors.New("not authorised by the remote")
	// ErrTooLarge: a transfer exceeds its TransferLimits.
	ErrTooLarge = errors.New("transfer exceeds its limits")
	// ErrRejected: received Git data is malformed or unsafe.
	ErrRejected = errors.New("received data rejected")
	// ErrDiverged: neither history contains the other.
	ErrDiverged = errors.New("histories have diverged")
	// ErrBehind: the remote holds snapshots this repository has not fetched.
	ErrBehind = errors.New("the remote holds snapshots not fetched here")
)

// Remote is a Git repository that snapshots are exchanged with.
type Remote struct {
	Name string // local name; the tracking ref is refs/remotes/<Name>/<Branch>
	// URL is https://host/path, ssh://[user@]host[:port]/path, user@host:path
	// or an absolute local path. It never holds a password or token.
	URL      string
	Branch   string // the remote branch that holds the snapshots; "" is "main"
	CABundle []byte // PEM roots trusted for this HTTPS remote, besides the system's
}

func (rm Remote) branch() string {
	if rm.Branch == "" {
		return "main"
	}
	return rm.Branch
}

func (rm Remote) trackingRef() string { return "refs/remotes/" + rm.Name + "/" + rm.branch() }

// Credentials are supplied for each call; gitcore never stores or logs
// them, and they never become part of a URL.
type Credentials struct {
	Username      string // HTTPS; "" is "git"
	Password      string // HTTPS password or access token
	SSHKey        []byte // PEM private key
	SSHPassphrase string
	KnownHosts    string // known_hosts file that verifies the SSH server; required for SSH
}

func (Credentials) String() string     { return "gitcore.Credentials(redacted)" }
func (c Credentials) GoString() string { return c.String() }

// TransferLimits bound what one fetch may receive. The pack is spooled to
// the candidate's quarantine and stops at MaxPackBytes; its headers are then
// checked against the other limits before any object is indexed.
type TransferLimits struct {
	MaxPackBytes     int64 // received pack bytes
	MaxObjects       int   // objects in the pack
	MaxObjectBytes   int64 // inflated size of one object, or of a delta's result
	MaxInflatedBytes int64 // inflated size of all objects and delta results
	MaxCommits       int   // new snapshots in one candidate
}

var DefaultTransferLimits = TransferLimits{MaxPackBytes: 64 << 20, MaxObjects: 200_000, MaxObjectBytes: 8 << 20, MaxInflatedBytes: 512 << 20, MaxCommits: 10_000}

func (l TransferLimits) orDefault() TransferLimits {
	if l == (TransferLimits{}) {
		return DefaultTransferLimits
	}
	return l
}

// connect validates the remote and prepares its transport. Credentials in
// the URL are refused, so a URL can be stored and reported safely.
func (rm Remote) connect(cred Credentials) (*transport.Endpoint, transport.Transport, transport.AuthMethod, error) {
	if !safeName(rm.Name) || !safeName(rm.branch()) {
		return nil, nil, nil, invalid("remote name %q and branch %q must be safe path segments", rm.Name, rm.Branch)
	}
	raw := rm.URL
	if !filepath.IsAbs(raw) && !strings.Contains(raw, "://") && !strings.Contains(raw, ":") {
		return nil, nil, nil, invalid("remote URL %q: a local remote must be an absolute path", raw)
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		if _, ok := u.User.Password(); ok || u.Scheme != "ssh" {
			return nil, nil, nil, invalid("remote URL must not hold credentials; supply them per call")
		}
	}
	ep, err := transport.NewEndpoint(raw)
	if err != nil {
		return nil, nil, nil, invalid("remote URL: %v", err)
	}
	switch ep.Protocol {
	case "file":
		return ep, file.DefaultClient, nil, nil
	case "https":
		ep.CaBundle = rm.CABundle
		if cred.Password == "" {
			return ep, githttp.DefaultClient, nil, nil
		}
		user := cred.Username
		if user == "" {
			user = "git"
		}
		return ep, githttp.DefaultClient, &githttp.BasicAuth{Username: user, Password: cred.Password}, nil
	case "ssh":
		if cred.KnownHosts == "" || len(cred.SSHKey) == 0 {
			return nil, nil, nil, invalid("an SSH remote needs a private key and a known_hosts file")
		}
		user := ep.User
		if user == "" {
			user = "git"
		}
		keys, err := gitssh.NewPublicKeys(user, cred.SSHKey, cred.SSHPassphrase)
		if err != nil {
			return nil, nil, nil, invalid("the SSH private key cannot be used")
		}
		hosts, err := gitssh.NewKnownHostsDb(cred.KnownHosts)
		if err != nil {
			return nil, nil, nil, invalid("known_hosts: %v", err)
		}
		port := ep.Port
		if port == 0 {
			port = 22
		}
		keys.HostKeyCallback = hosts.HostKeyCallback()
		keys.HostKeyAlgorithms = hosts.HostKeyAlgorithms(net.JoinHostPort(strings.Trim(ep.Host, "[]"), strconv.Itoa(port)))
		return ep, gitssh.DefaultClient, keys, nil
	}
	return nil, nil, nil, invalid("remote protocol %q is not supported: use https, ssh or a local path", ep.Protocol)
}

// remoteErr classifies a transport error.
func remoteErr(err error) error {
	msg := err.Error()
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed),
		strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("%w: %w", ErrUnauthorized, err)
	case strings.Contains(msg, "knownhosts") || strings.Contains(msg, "host key"):
		return fmt.Errorf("%w: the server's host key is not trusted: %w", ErrUnauthorized, err)
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return fmt.Errorf("remote repository: %w", ErrNotFound)
	}
	return fmt.Errorf("remote: %w", err)
}

// Fetch receives the remote's snapshot into a quarantine and returns it as
// a Candidate. Nothing in the repository changes: the candidate must be
// verified (Candidate.Verify) and imported (Import) before any snapshot of
// it can be read through the repository, and only Advance makes one active.
func (r *Repository) Fetch(ctx context.Context, rm Remote, cred Credentials, lim TransferLimits) (*Candidate, error) {
	lim = lim.orDefault()
	ep, cl, auth, err := rm.connect(cred)
	if err != nil {
		return nil, err
	}
	prev, err := r.readRef(rm.trackingRef())
	if err != nil {
		return nil, err
	}
	c := &Candidate{Remote: rm, Previous: prev, r: r, lim: lim}
	sess, err := cl.NewUploadPackSession(ep, auth)
	if err != nil {
		return nil, remoteErr(err)
	}
	defer func() { _ = sess.Close() }()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return c, nil
	}
	if err != nil {
		return nil, remoteErr(err)
	}
	head, ok := adv.References[plumbing.NewBranchReferenceName(rm.branch()).String()]
	if !ok {
		return c, nil
	}
	c.Snapshot = Snapshot(head.String())
	if r.st.HasEncodedObject(head) == nil {
		// Stored already, so verified with everything it reaches when it was.
		return c, nil
	}
	req := packp.NewUploadPackRequestFromCapabilities(adv.Capabilities)
	// A plain, self-contained pack: every delta base is in it.
	for _, cp := range []capability.Capability{capability.Sideband, capability.Sideband64k, capability.ThinPack, capability.MultiACK, capability.MultiACKDetailed} {
		req.Capabilities.Delete(cp)
	}
	req.Wants = []plumbing.Hash{head}
	if req.Haves, err = r.haves(ctx); err != nil {
		return nil, err
	}
	q, err := r.newQuarantine()
	if err != nil {
		return nil, err
	}
	c.q = q
	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		_ = c.Close()
		return nil, remoteErr(err)
	}
	defer func() { _ = resp.Close() }()
	if c.Bytes, c.Objects, err = q.receive(resp, lim); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// haves are the snapshots the remote need not send again: the active one
// and every tracked one, all verified when they were stored.
func (r *Repository) haves(ctx context.Context) ([]plumbing.Hash, error) {
	refs, err := r.trackingRefs()
	if err != nil {
		return nil, err
	}
	active, err := r.Active(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[Snapshot]bool{}
	var out []plumbing.Hash
	for _, s := range append(refs, active) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, plumbing.NewHash(string(s)))
		}
	}
	return out, nil
}

// PushResult describes one push.
type PushResult struct {
	Before   Snapshot // the remote branch before the push; "" if it did not exist
	After    Snapshot
	UpToDate bool
	Bytes    int64 // pack bytes sent
	Objects  int
}

// Push sends the active snapshot to the remote branch, but only as a
// fast-forward: the remote's snapshot must be the active one or one of its
// ancestors. The update names the remote's previous snapshot, so a
// concurrent update of the remote fails with ErrStale; nothing is ever
// forced. A remote holding snapshots not stored here is ErrBehind (fetch
// first); one whose history diverges from ours is ErrDiverged.
func (r *Repository) Push(ctx context.Context, rm Remote, cred Credentials) (PushResult, error) {
	var res PushResult
	ep, cl, auth, err := rm.connect(cred)
	if err != nil {
		return res, err
	}
	active, err := r.Active(ctx)
	if err != nil {
		return res, err
	}
	if active == "" {
		return res, invalid("nothing is published")
	}
	res.After = active
	sess, err := cl.NewReceivePackSession(ep, auth)
	if err != nil {
		return res, remoteErr(err)
	}
	defer func() { _ = sess.Close() }()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return res, remoteErr(err)
	}
	name := plumbing.NewBranchReferenceName(rm.branch())
	old, exists := adv.References[name.String()]
	if exists {
		res.Before = Snapshot(old.String())
		if res.Before == active {
			res.UpToDate = true
			return res, r.track(rm, res.Before)
		}
		if r.st.HasEncodedObject(old) != nil {
			return res, ErrBehind
		}
		ok, err := r.descends(r.st, active, res.Before)
		if err != nil {
			return res, err
		}
		if !ok {
			if back, err := r.descends(r.st, res.Before, active); err == nil && back {
				return res, ErrBehind
			}
			return res, ErrDiverged
		}
	}
	var ignore []plumbing.Hash
	if exists {
		if ignore, err = revlist.Objects(r.st, []plumbing.Hash{old}, nil); err != nil {
			return res, fmt.Errorf("%w: %w", ErrCorrupt, err)
		}
	}
	want, err := revlist.Objects(r.st, []plumbing.Hash{plumbing.NewHash(string(active))}, ignore)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	var pack bytes.Buffer
	if _, err := packfile.NewEncoder(&pack, r.st, false).Encode(want, 10); err != nil {
		return res, err
	}
	res.Bytes, res.Objects = int64(pack.Len()), len(want)
	req := packp.NewReferenceUpdateRequestFromCapabilities(adv.Capabilities)
	req.Commands = []*packp.Command{{Name: name, Old: old, New: plumbing.NewHash(string(active))}}
	req.Packfile = io.NopCloser(&pack)
	status, err := sess.ReceivePack(ctx, req)
	if err == nil && status != nil {
		err = status.Error()
	}
	if err != nil {
		if status != nil && status.Error() != nil {
			return res, fmt.Errorf("the remote branch changed during the push (%w): %w", ErrStale, err)
		}
		return res, remoteErr(err)
	}
	return res, r.track(rm, active)
}

// track records what the remote branch is known to hold.
func (r *Repository) track(rm Remote, s Snapshot) error {
	unlock, err := r.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cur, err := r.readRef(rm.trackingRef())
	if err != nil || cur == s {
		return err
	}
	return r.writeRef(rm.trackingRef(), s)
}
