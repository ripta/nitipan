// Package broker is the self-hosted lease broker: the HTTP server nodes connect to, the per-node
// event queues, the mirrors of each machine's leases, and the store behind them.
package broker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/space-armada/nitipan/internal/monoclock"
	"github.com/space-armada/nitipan/internal/oidc"
	"github.com/space-armada/nitipan/internal/wire"
)

const (
	// DefaultTerm is the lease term. It is a constant, and tests inject another through Config.
	DefaultTerm = 5 * time.Minute

	DefaultKeepalive = 15 * time.Second

	maxBody = 8 << 20
)

// ErrNoNode means no node with that key ever registered.
var ErrNoNode = errors.New("no such node")

// KeyInput is what the key policy decides on.
type KeyInput struct {
	// Registered is true when the broker holds a key for the node.
	Registered bool

	// Same is true when the presented key equals the registered one.
	Same bool

	// StreamLive is true when the node's stream had a heartbeat within one term.
	StreamLive bool

	// Pinned is true when the user approved a key for the node's label. A pinned label refuses every
	// other key, whether or not its stream is live.
	Pinned bool
}

// KeyPolicy decides whether a registering node may present its key. A nil result accepts, and the
// presented key becomes the registered one. This is the one place that rule lives.
type KeyPolicy func(KeyInput) error

// ErrKeyConflict means a different key arrived while the old stream was live.
var ErrKeyConflict = errors.New("a different instance key is presented while the node's stream is live")

// ErrKeyPinned means a different key arrived for a label whose key the user approved.
var ErrKeyPinned = errors.New("the label is pinned to another instance key. " +
	"If this machine was reinstalled, run nitipan broker forget on the broker host, then approve its new key")

// DefaultKeyPolicy accepts a new node and the same key. A pinned label refuses any other key. An
// unpinned label accepts a different key once the old stream is no longer live, and refuses it while
// the old stream is live.
func DefaultKeyPolicy(in KeyInput) error {
	switch {
	case !in.Registered || in.Same:
		return nil
	case in.Pinned:
		return ErrKeyPinned
	case !in.StreamLive:
		return nil
	}

	return ErrKeyConflict
}

// Config configures a broker.
type Config struct {
	Store *Store

	// Term is the lease term, and the age after which a silent node's mirrors read stale. Default:
	// DefaultTerm.
	Term time.Duration

	// Keepalive is how often an idle stream carries a comment. Default: DefaultKeepalive.
	Keepalive time.Duration

	// Clock paces terms. Default: CLOCK_MONOTONIC.
	Clock monoclock.Clock

	// Now is wall-clock time, used for display fields in the store. Default: time.Now.
	Now func() time.Time

	// Version is the broker's build version.
	Version string

	// DerivationFloor is the lowest root-set derivation a user claim may carry. Default:
	// DefaultDerivationFloor.
	DerivationFloor int

	// Derive makes a label. Default: DeriveLabel. A test injects a collision through it.
	Derive func(machineID string, uid int) string

	// AdminSocket is the path of the unix socket that serves the approval commands. Empty serves none,
	// and nothing can then be approved.
	AdminSocket string

	// OIDCConfig configures the OIDC identity provider. When nil, only the broker owner can bind labels.
	OIDCConfig *oidc.Config

	// Listen is the broker's listen address, used to check whether cross-user features can be enabled.
	Listen string

	// TLS reports whether the broker serves with TLS.
	TLS bool

	KeyPolicy KeyPolicy
	Logger    *slog.Logger
}

// Broker serves nodes.
type Broker struct {
	cfg   Config
	store *Store
	boot  string

	signInMgr *SignInManager

	mu    sync.Mutex
	nodes map[NodeKey]*nodeState

	// g is the typed lease state. dl holds each live lease's deadline on the monotonic clock. Nothing
	// lapses before graceUntil.
	g          *gstate
	dl         map[string]time.Duration
	graceUntil time.Duration
	lastSweep  time.Duration

	failOnce sync.Once
	failed   error
	done     chan struct{}
}

type nodeState struct {
	snapshot    []wire.MirroredLease
	hasSnapshot bool

	lastBeat time.Duration
	hasBeat  bool

	stream *streamState

	queue   []queued
	nextSeq int

	user, host string

	// liveSessions is the set of session IDs the node's last snapshot reported as live.
	liveSessions map[string]bool

	// conn counts the node's registrations. A node registers, sends its complete mail report, and only
	// then opens its stream, so a registration is one connection. A stream belongs to the registration it
	// opened under, and a stream from an earlier one was left behind by the node.
	conn int

	// mailReported is true once the node's complete mail report arrived since it last registered. Mail
	// from another user is re-checked and sent only after it, and only on a stream of that registration.
	mailReported bool

	// sentSinceReport holds the mail from another user sent to the node since it last registered.
	//
	// The broker sends such mail only after the complete report, so the report cannot say whether the
	// node stored it. The re-check skips it until the node acks it or reports again on its next
	// connection. A stream whose connection is gone, which the broker has not noticed yet, then cannot
	// bounce mail the node may hold.
	sentSinceReport map[string]bool
}

type queued struct {
	seq   int
	event wire.Event
}

type streamState struct {
	cancel context.CancelFunc
	wake   chan struct{}

	// keyHash is the hash of the key the stream opened with. Mail goes only to a stream whose key is the
	// one the label is pinned to, so a stream left over from an earlier key never receives it.
	keyHash string

	// conn is the node's registration count when the stream opened.
	conn int
}

// New builds a broker over an opened store.
func New(cfg Config) *Broker {
	if cfg.Term == 0 {
		cfg.Term = DefaultTerm
	}

	if cfg.Keepalive == 0 {
		cfg.Keepalive = DefaultKeepalive
	}

	if cfg.Clock == nil {
		cfg.Clock = monoclock.System{}
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	if cfg.Derive == nil {
		cfg.Derive = DeriveLabel
	}

	if cfg.KeyPolicy == nil {
		cfg.KeyPolicy = DefaultKeyPolicy
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}

	var boot [4]byte

	_, _ = rand.Read(boot[:])

	if cfg.DerivationFloor == 0 {
		cfg.DerivationFloor = DefaultDerivationFloor
	}

	b := &Broker{
		cfg:   cfg,
		store: cfg.Store,
		boot:  hex.EncodeToString(boot[:]),
		nodes: map[NodeKey]*nodeState{},
		done:  make(chan struct{}),
	}

	// The client fetches the provider's discovery document on the first sign-in, so a provider that is down
	// now delays only sign-ins. A config the client refuses, such as a plain-HTTP issuer, fails the broker.
	if cfg.OIDCConfig != nil {
		oidcClient, err := oidc.New(*cfg.OIDCConfig)
		if err != nil {
			b.fail(fmt.Errorf("initializing OIDC client: %w", err))
		} else {
			b.signInMgr = NewSignInManager(b, oidcClient)
		}
	}

	if err := b.startGrants(); err != nil {
		b.fail(err)
	}

	return b
}

// Done is closed when a store write failed. The broker must then exit, and launchd restarts it from
// the file on disk.
func (b *Broker) Done() <-chan struct{} {
	return b.done
}

// Err returns the write failure that closed Done.
func (b *Broker) Err() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.failed
}

func (b *Broker) fail(err error) {
	b.failOnce.Do(func() {
		b.failed = err

		close(b.done)
	})
}

// commit applies fn to a copy of the state, persists the copy, and swaps it in. A failed write is
// fatal, and the change is not visible. The caller holds b.mu, and acks only after commit returns.
func (b *Broker) commit(fn func(*Data) error) error {
	next := b.store.Data().clone()

	if err := fn(next); err != nil {
		return err
	}

	if err := b.store.Persist(next); err != nil {
		b.fail(err)

		return fmt.Errorf("persisting the store: %w", err)
	}

	b.store.Swap(next)

	return nil
}

func (b *Broker) node(k NodeKey) *nodeState {
	ns, ok := b.nodes[k]
	if !ok {
		ns = &nodeState{}
		b.nodes[k] = ns
	}

	return ns
}

func (b *Broker) live(ns *nodeState) bool {
	return ns.stream != nil && ns.hasBeat && b.cfg.Clock.Now()-ns.lastBeat <= b.cfg.Term
}

func (b *Broker) beat(ns *nodeState) {
	ns.lastBeat = b.cfg.Clock.Now()
	ns.hasBeat = true
}

// beatIfStreaming counts a heartbeat or snapshot only while the node's stream is attached. A node
// that keeps posting after its stream closed still reads stale one term later.
func (b *Broker) beatIfStreaming(ns *nodeState) {
	if ns.stream != nil {
		b.beat(ns)
	}
}

// Handler returns the HTTP handler. It carries no address policy. ListenAndServe applies that.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST "+wire.PathRegister, b.handleRegister)
	mux.HandleFunc("GET "+wire.PathStream, b.authed(b.handleStream))
	mux.HandleFunc("POST "+wire.PathAck, b.authed(b.handleAck))
	mux.HandleFunc("POST "+wire.PathSnapshot, b.authed(b.handleSnapshot))
	mux.HandleFunc("POST "+wire.PathHeartbeat, b.authed(b.handleHeartbeat))
	mux.HandleFunc("POST "+wire.PathPing, b.authed(b.handlePing))
	mux.HandleFunc("POST "+wire.PathPair, b.handlePair)
	mux.HandleFunc("POST "+wire.PathClaim, b.authed(b.handleClaim))
	mux.HandleFunc("POST "+wire.PathRenew, b.authed(b.handleRenew))
	mux.HandleFunc("POST "+wire.PathRelease, b.authed(b.handleRelease))
	mux.HandleFunc("POST "+wire.PathBreak, b.authed(b.handleBreak))
	mux.HandleFunc("POST "+wire.PathList, b.authed(b.handleList))
	mux.HandleFunc("POST "+wire.PathFence, b.authed(b.handleFence))
	mux.HandleFunc("POST "+wire.PathWhoHolds, b.authed(b.handleWhoHolds))
	mux.HandleFunc("POST "+wire.PathWhoIs, b.authed(b.handleWhoIs))
	mux.HandleFunc("POST "+wire.PathWhoAmI, b.authed(b.handleWhoAmI))
	mux.HandleFunc("POST "+wire.PathMailSend, b.authed(b.handleMailSend))
	mux.HandleFunc("POST "+wire.PathMailAck, b.authed(b.handleMailAck))
	mux.HandleFunc("POST "+wire.PathMailReport, b.authed(b.handleMailReport))
	mux.HandleFunc("POST "+wire.PathMailStatus, b.authed(b.handleMailStatus))

	if b.signInMgr != nil {
		mux.HandleFunc("GET /signin", b.handleSignInPage)
		mux.HandleFunc("POST /bind", b.handleBindLabel)
		mux.HandleFunc("POST /grant", b.handleGrantDelegation)
		mux.HandleFunc("POST /revoke", b.handleRevokeDelegation)
		mux.HandleFunc("POST /mail-grant", b.handleStartMailGrant)
		mux.HandleFunc("POST /mail-revoke", b.handleStartMailRevoke)
		mux.HandleFunc("GET /callback", b.handleCallback)
		mux.HandleFunc("POST /bind-submit", b.handleBindSubmit)
		mux.HandleFunc("POST /grant-submit", b.handleGrantSubmit)
		mux.HandleFunc("POST /revoke-submit", b.handleRevokeSubmit)
		mux.HandleFunc("POST /mail-grant-submit", b.handleMailGrantSubmit)
		mux.HandleFunc("POST /mail-revoke-submit", b.handleMailRevokeSubmit)
	}

	return mux
}

func keyHash(key string) string { return wire.KeyHash(key) }

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")

	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok {
		return ""
	}

	return strings.TrimSpace(tok)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, wire.Error{Version: wire.Current, Code: code, Message: msg})
}

func decodeBody(w http.ResponseWriter, r *http.Request, into any, version func() wire.Version) bool {
	body := http.MaxBytesReader(w, r.Body, maxBody)

	if err := json.NewDecoder(body).Decode(into); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "decoding the request: "+err.Error())

		return false
	}

	if err := version().Check(); err != nil {
		writeErr(w, http.StatusBadRequest, wire.CodeVersion, err.Error())

		return false
	}

	return true
}

type nodeHandler func(w http.ResponseWriter, r *http.Request, key NodeKey)

// unboundRefusal is what the broker tells a label nobody bound once cross-user features are on.
const unboundRefusal = "this label is not bound to a user. Bind it on the broker host with 'nitipan broker approve', " +
	"or on the broker's sign-in page with 'nitipan node pair' and the pairing code"

// crossUserOffRefusal is what the broker tells a label bound to a user other than its owner while
// cross-user features are off.
const crossUserOffRefusal = "this label is bound to another user, and cross-user features are off on this broker. " +
	"The broker owner turns them on with 'nitipan broker cross-user true' on the broker host"

// nodeKeyOf reads the machine ID and uid a request names in its headers.
func nodeKeyOf(r *http.Request) (NodeKey, bool) {
	uid, err := strconv.Atoi(r.Header.Get(wire.HeaderUID))
	machine := r.Header.Get(wire.HeaderMachine)

	return NodeKey{MachineID: machine, UID: uid}, err == nil && machine != ""
}

// authed refuses a request unless its bearer key matches the key registered for the machine ID and
// uid in its headers, and the label acts in a scope. See scopeRefusalLocked for which labels act in none.
func (b *Broker) authed(next nodeHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, ok := nodeKeyOf(r)

		if !ok || !b.checkKey(key, bearer(r)) {
			writeErr(w, http.StatusUnauthorized, wire.CodeUnauthorized, "the instance key does not match the one registered for this node")

			return
		}

		b.mu.Lock()
		refusal := b.scopeRefusalLocked(b.store.Data().Nodes[key.String()].Label)
		b.mu.Unlock()

		if refusal != "" {
			writeErr(w, http.StatusForbidden, wire.CodeForbidden, refusal)

			return
		}

		next(w, r, key)
	}
}

func (b *Broker) checkKey(k NodeKey, presented string) bool {
	if presented == "" {
		return false
	}

	b.mu.Lock()
	rec, ok := b.store.Data().Nodes[k.String()]
	b.mu.Unlock()

	if !ok {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(rec.KeyHash), []byte(keyHash(presented))) == 1
}

func (b *Broker) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req wire.Register

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	key := bearer(r)
	if key == "" {
		writeErr(w, http.StatusUnauthorized, wire.CodeUnauthorized, "register with an instance key as a bearer token")

		return
	}

	if req.MachineID == "" {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the registration names no machine ID")

		return
	}

	nk := NodeKey{MachineID: req.MachineID, UID: req.UID}
	label := b.cfg.Derive(req.MachineID, req.UID)

	b.mu.Lock()
	defer b.mu.Unlock()

	if row, seen := b.store.Data().Labels[label]; seen && (row.MachineID != req.MachineID || row.UID != req.UID) {
		now := LabelRecord{MachineID: req.MachineID, UID: req.UID, Host: req.Host, User: req.User}

		writeErr(w, http.StatusConflict, wire.CodeLabelConflict, fmt.Sprintf("label %q is derived by both %s and %s: %v",
			label, describeMachine(row), describeMachine(now), ErrLabelCollision))

		return
	}

	ns := b.node(nk)
	reg, registered := b.store.Data().Nodes[nk.String()]
	hash := keyHash(key)

	// A key the user approved before it arrived pins on arrival, and replaces whatever key holds the label.
	pin := b.store.Data().Pins[label]
	awaited := pin.PinnedHash == "" && pin.Awaiting != "" && fingerprintMatches(hash, pin.Awaiting)

	var err error

	if !awaited {
		err = b.cfg.KeyPolicy(KeyInput{
			Registered: registered,
			Same:       registered && sameKey(reg, hash),
			StreamLive: b.live(ns),
			Pinned:     pin.PinnedHash != "",
		})
	}

	if err != nil {
		writeErr(w, http.StatusConflict, wire.CodeKeyConflict, fmt.Sprintf("node %s: %v", nk, err))

		return
	}

	if registered && !sameKey(reg, hash) && ns.stream != nil {
		ns.stream.cancel()
	}

	if err := b.recordRegistrationLocked(req, label, hash, awaited); err != nil {
		writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

		return
	}

	ns.user, ns.host = req.User, req.Host
	ns.conn++
	ns.mailReported = false
	ns.sentSinceReport = nil
	b.beat(ns)

	b.cfg.Logger.Info("node registered", "label", label, "uid", req.UID, "node_version", req.NodeVersion, "https", req.HTTPS)

	crossUserEnabled := b.store.Data().CrossUserEnabled

	writeJSON(w, http.StatusOK, wire.Registered{
		Version:         wire.Current,
		Label:           label,
		LabelVersion:    LabelDerivation,
		BrokerVersion:   b.cfg.Version,
		TermMillis:      b.cfg.Term.Milliseconds(),
		KeepaliveMillis: b.cfg.Keepalive.Milliseconds(),
		CrossUser:       crossUserEnabled,

		ActingForExplicit: true,
		MailReplies:       true,
	})
}

// recordRegistrationLocked stores a node's registration under its label. An awaited key was approved on
// the broker host, so it pins and binds to the owner in the same commit. A store whose owner record is
// missing pins it alone.
func (b *Broker) recordRegistrationLocked(req wire.Register, label, hash string, awaited bool) error {
	nk := NodeKey{MachineID: req.MachineID, UID: req.UID}
	owner, hasOwner := b.ownerLocked()

	var (
		next    *gstate
		senders []string
	)

	if awaited && hasOwner {
		next = b.g.clone()
	}

	err := b.commit(func(d *Data) error {
		d.Nodes[nk.String()] = NodeRecord{KeyHash: hash, Label: label, MailCap: req.MailCapability, HTTPS: req.HTTPS}
		notePending(d, label, hash, b.cfg.Now())

		d.Labels[label] = LabelRecord{
			MachineID:  req.MachineID,
			UID:        req.UID,
			Host:       req.Host,
			User:       req.User,
			Derivation: LabelDerivation,
			LastSeen:   b.cfg.Now(),
		}

		noteScheme(d, hash, req.HTTPS)

		switch {
		case next != nil:
			senders = b.bindInto(d, next, label, owner, hash, b.cfg.Now())

			return next.encode(d)
		case awaited:
			d.Pins[label] = PinRecord{PinnedHash: hash}
		}

		return nil
	})
	if err != nil {
		return err
	}

	if next != nil {
		b.g = next
	}

	for _, l := range senders {
		b.wakeSendersLocked(l)
	}

	return nil
}

// Schemes a key can be marked with in KeySchemes.
const (
	schemeHTTP       = "http"
	schemeHTTPS      = "https"
	schemeUnreported = "unreported"
)

// noteScheme records the scheme a key reported at registration. Only an https mark gives way, to http.
// A key that once crossed plain HTTP, or came from a store before scheme reports, keeps its mark, since it
// may have been read on a plain hop. It runs inside a commit.
func noteScheme(d *Data, hash string, https bool) {
	cur, seen := d.KeySchemes[hash]

	switch {
	case !seen && https:
		d.KeySchemes[hash] = schemeHTTPS
	case !https && (!seen || cur == schemeHTTPS):
		d.KeySchemes[hash] = schemeHTTP
	}
}

func sameKey(rec NodeRecord, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(rec.KeyHash), []byte(hash)) == 1
}

func (b *Broker) handleHeartbeat(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.Heartbeat

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	b.beatIfStreaming(b.node(key))
	b.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (b *Broker) handleSnapshot(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.Snapshot

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()

	ns := b.node(key)
	ns.snapshot = req.Leases
	ns.hasSnapshot = true

	b.noteSessionsLocked(key, ns, req.Sessions)

	b.beatIfStreaming(ns)

	b.sweepLocked()

	mine := b.afterSnapshot(key, req.Acked)
	overlaps := b.overlapsForNode(key)
	b.mu.Unlock()

	if len(mine) == 0 && len(overlaps) == 0 {
		w.WriteHeader(http.StatusNoContent)

		return
	}

	writeJSON(w, http.StatusOK, wire.SnapshotAck{Version: wire.Current, Leases: mine, Overlaps: overlaps})
}

func (b *Broker) handleAck(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.Ack

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()

	ns := b.node(key)
	ns.queue = removeEvent(ns.queue, req.EventID)

	b.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func removeEvent(q []queued, id string) []queued {
	for i, e := range q {
		if e.event.ID == id {
			return append(q[:i:i], q[i+1:]...)
		}
	}

	return q
}

func (b *Broker) handlePing(w http.ResponseWriter, r *http.Request, key NodeKey) {
	var req wire.Ping

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	b.mu.Lock()
	label := b.store.Data().Nodes[key.String()].Label
	b.mu.Unlock()

	writeJSON(w, http.StatusOK, wire.Pong{Version: wire.Current, Label: label})
}

// handlePair stores the hash of a pairing code the node printed, tied to the key that sent it, and answers
// with the label the code binds. A pending key sends it, which the label presented at registration and
// nobody approved yet, so the route needs no binding. That is the state pairing exists to leave.
func (b *Broker) handlePair(w http.ResponseWriter, r *http.Request) {
	key, ok := nodeKeyOf(r)
	hash := keyHash(bearer(r))

	if !ok || bearer(r) == "" {
		writeErr(w, http.StatusUnauthorized, wire.CodeUnauthorized, "pair with the node's key and its machine headers")

		return
	}

	var req wire.Pair

	if !decodeBody(w, r, &req, func() wire.Version { return req.Version }) {
		return
	}

	if req.PairingHash == "" {
		writeErr(w, http.StatusBadRequest, wire.CodeBadRequest, "the pairing request carries no code hash")

		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	rec, registered := b.store.Data().Nodes[key.String()]
	pinned := b.store.Data().Pins[rec.Label].PinnedHash

	switch {
	case !registered:
		writeErr(w, http.StatusUnauthorized, wire.CodeUnauthorized, "no node registered for this machine and user. Run the node, then pair again")

		return
	case pinned != "" && subtle.ConstantTimeCompare([]byte(pinned), []byte(hash)) == 1:
		writeErr(w, http.StatusConflict, wire.CodeKeyConflict, fmt.Sprintf("label %s is already approved under this key. "+
			"If its machine was reinstalled, run nitipan broker forget on the broker host first", rec.Label))

		return
	case !b.pendingKeyLocked(rec.Label, hash):
		writeErr(w, http.StatusUnauthorized, wire.CodeUnauthorized, "this key is not waiting for approval under the node's label. "+
			"Run the node so it registers the key, then pair again")

		return
	}

	err := b.commit(func(d *Data) error {
		d.PairingCodes[rec.Label] = PairingCodeRecord{Hash: req.PairingHash, KeyHash: hash, ExpiresAt: b.cfg.Now().Add(PairingCodeExpiry)}

		return nil
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, wire.CodeUnavailable, err.Error())

		return
	}

	writeJSON(w, http.StatusOK, wire.Paired{Version: wire.Current, Label: rec.Label})
}

func (b *Broker) handleStream(w http.ResponseWriter, r *http.Request, key NodeKey) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, wire.CodeUnavailable, "the connection cannot stream")

		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	st := &streamState{cancel: cancel, wake: make(chan struct{}, 1), keyHash: keyHash(bearer(r))}

	b.mu.Lock()

	ns := b.node(key)
	if ns.stream != nil {
		ns.stream.cancel()
	}

	st.conn = ns.conn
	ns.stream = st
	b.beat(ns)

	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if ns.stream == st {
			ns.stream = nil
		}
		b.mu.Unlock()
	}()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := wire.WriteComment(w, "stream open"); err != nil {
		return
	}

	flusher.Flush()

	tick := time.NewTicker(b.cfg.Keepalive)
	defer tick.Stop()

	sent := 0
	sentMail := map[string]bool{}

	for {
		var err error

		sent, err = b.flush(w, key, sent)
		if err != nil {
			return
		}

		if err := b.flushMail(w, key, st, sentMail); err != nil {
			return
		}

		flusher.Flush()

		select {
		case <-ctx.Done():
			return
		case <-b.done:
			return
		case <-st.wake:
		case <-tick.C:
			if err := wire.WriteComment(w, "keepalive"); err != nil {
				return
			}

			flusher.Flush()
		}
	}
}

// flush writes every queued event after seq sent, and returns the new high-water mark. An event
// stays queued after it is written. Only an ack removes it.
func (b *Broker) flush(w http.ResponseWriter, key NodeKey, sent int) (int, error) {
	b.mu.Lock()

	var out []queued

	for _, q := range b.node(key).queue {
		if q.seq > sent {
			out = append(out, q)
		}
	}

	b.mu.Unlock()

	for _, q := range out {
		data, err := json.Marshal(q.event.Data)
		if err != nil {
			return sent, fmt.Errorf("encoding event %s: %w", q.event.ID, err)
		}

		if err := wire.WriteSSE(w, wire.SSE{ID: q.event.ID, Event: q.event.Kind, Data: string(data)}); err != nil {
			return sent, err
		}

		sent = q.seq
	}

	return sent, nil
}

// Enqueue queues an event for a node. It stays queued until the node acks it, and a node that
// reconnects gets every unacked event again.
func (b *Broker) Enqueue(key NodeKey, kind string, data any) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.enqueueLocked(key, kind, data)
}

func (b *Broker) enqueueLocked(key NodeKey, kind string, data any) (string, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("encoding event data: %w", err)
	}

	if _, ok := b.store.Data().Nodes[key.String()]; !ok {
		return "", fmt.Errorf("node %s: %w", key, ErrNoNode)
	}

	ns := b.node(key)
	ns.nextSeq++

	ev := wire.Event{ID: b.boot + "." + strconv.Itoa(ns.nextSeq), Kind: kind, Data: raw}
	ns.queue = append(ns.queue, queued{seq: ns.nextSeq, event: ev})

	if ns.stream != nil {
		select {
		case ns.stream.wake <- struct{}{}:
		default:
		}
	}

	return ev.ID, nil
}

// Unacked returns the events still queued for a node.
func (b *Broker) Unacked(key NodeKey) []wire.Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]wire.Event, 0, len(b.node(key).queue))
	for _, q := range b.node(key).queue {
		out = append(out, q.event)
	}

	return out
}

// CloseStream ends a node's stream from the broker side. It reports whether a stream was open.
func (b *Broker) CloseStream(key NodeKey) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	ns, ok := b.nodes[key]
	if !ok || ns.stream == nil {
		return false
	}

	ns.stream.cancel()

	return true
}

// NodeView is what the broker knows of one node.
type NodeView struct {
	Key   NodeKey
	Label string
	User  string
	Host  string

	// Leases is the node's last snapshot. It is the mirror of the machine's leases for that OS user.
	Leases      []wire.MirroredLease
	HasSnapshot bool

	// Stale is true when the node has sent nothing for longer than one term, whether or not its
	// connection is still open.
	Stale bool

	// StreamLive is true when a stream is open and had a heartbeat within one term.
	StreamLive bool
}

// Nodes returns a view of every node that registered, in no particular order.
func (b *Broker) Nodes() []NodeView {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.cfg.Clock.Now()

	var out []NodeView

	for k, ns := range b.nodes {
		rec, ok := b.store.Data().Nodes[k.String()]
		if !ok {
			continue
		}

		out = append(out, NodeView{
			Key:         k,
			Label:       rec.Label,
			User:        ns.user,
			Host:        ns.host,
			Leases:      append([]wire.MirroredLease(nil), ns.snapshot...),
			HasSnapshot: ns.hasSnapshot,
			Stale:       !ns.hasBeat || now-ns.lastBeat > b.cfg.Term,
			StreamLive:  b.live(ns),
		})
	}

	return out
}

// Serve listens on addr and serves until ctx ends or the store fails. With a certificate and key it
// serves TLS. Without them it refuses a public or wildcard address.
func (b *Broker) Serve(ctx context.Context, addr, certFile, keyFile string) error {
	useTLS := certFile != "" || keyFile != ""
	if useTLS && (certFile == "" || keyFile == "") {
		return errors.New("cert_file and key_file go together")
	}

	if err := CheckListen(addr, useTLS); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", addr, err)
	}

	return b.serveOn(ctx, ln, certFile, keyFile)
}

func (b *Broker) serveOn(ctx context.Context, ln net.Listener, certFile, keyFile string) error {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	srv := &http.Server{
		Handler:           b.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return base },
	}

	errc := make(chan error, 1)

	go b.RunSweeper(base, max(b.cfg.Term/10, 10*time.Millisecond))

	if b.cfg.AdminSocket != "" {
		go func() {
			if err := b.ServeAdmin(base, b.cfg.AdminSocket); err != nil {
				select {
				case errc <- err:
				default:
				}
			}
		}()
	}

	go func() {
		if certFile != "" {
			errc <- srv.ServeTLS(ln, certFile, keyFile)

			return
		}

		errc <- srv.Serve(ln)
	}()

	select {
	case err := <-errc:
		return fmt.Errorf("serving: %w", err)
	case <-ctx.Done():
	case <-b.done:
	}

	cancelBase()

	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_ = srv.Shutdown(shut)

	if err := b.Err(); err != nil {
		return err
	}

	return nil
}
