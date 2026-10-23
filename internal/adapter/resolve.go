package adapter

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/space-armada/nitipan/internal/proc"
	"github.com/space-armada/nitipan/internal/record"
)

// maxDepth bounds the walk, so a corrupt process table cannot loop it.
const maxDepth = 512

// Errors that make a holder command fail closed.
var (
	// ErrFailClosed matches every walk failure below.
	ErrFailClosed = errors.New("cannot identify the calling agent")

	// ErrNoAdapter means no agent was found, and none of its variables are set.
	ErrNoAdapter = errors.New("no supported agent found")
)

// PIDNotAncestorError means an agent's PID variable names a process that is not an ancestor, and no
// ancestor matches any adapter. A `nohup` or tmux job looks like this.
type PIDNotAncestorError struct {
	Var   string
	Value string
}

// Error implements error.
func (e *PIDNotAncestorError) Error() string {
	return fmt.Sprintf("%s=%s names a process that is not an ancestor of this command, and no ancestor is a "+
		"supported agent", e.Var, e.Value)
}

// Is matches ErrFailClosed.
func (e *PIDNotAncestorError) Is(target error) bool { return target == ErrFailClosed }

// StrayVariablesError means agent variables are set but no ancestor is that agent.
type StrayVariablesError struct {
	Vars []string
}

// Error implements error.
func (e *StrayVariablesError) Error() string {
	return fmt.Sprintf("agent variables %s are set, but no ancestor of this command is that agent",
		strings.Join(e.Vars, ", "))
}

// Is matches ErrFailClosed.
func (e *StrayVariablesError) Is(target error) bool { return target == ErrFailClosed }

// UnreadableAncestorError means the walk met an ancestor it could not read, as across a PID
// namespace. Skipping it could attribute the call to an outer agent.
//
// Vars lists the agent variables that were set when the walk stopped. They are named in the message,
// because the unreadable ancestor is why they could not be matched to an agent.
type UnreadableAncestorError struct {
	PID  int
	Err  error
	Vars []string
}

// Error implements error.
func (e *UnreadableAncestorError) Error() string {
	msg := fmt.Sprintf("cannot read ancestor process %d: %v", e.PID, e.Err)
	if len(e.Vars) == 0 {
		return msg
	}

	return fmt.Sprintf("%s; agent variables %s could not be matched to an ancestor", msg, strings.Join(e.Vars, ", "))
}

// Unwrap exposes the read error.
func (e *UnreadableAncestorError) Unwrap() error { return e.Err }

// Is matches ErrFailClosed.
func (e *UnreadableAncestorError) Is(target error) bool { return target == ErrFailClosed }

// Holder is a call made by a supported agent.
type Holder struct {
	Adapter   Adapter
	PID       int
	Start     record.ProcessStart
	SessionID string
	User      string
}

// MissingSession reports a multi-session agent with no session ID. Such a holder cannot be told
// from another session of the same process, so `acquire` must refuse it.
func (h Holder) MissingSession() bool {
	return h.Adapter.MultiSession && h.SessionID == ""
}

// Record returns the holder fields a lease stores. The handle and name are the caller's to fill.
func (h Holder) Record() record.Holder {
	return record.Holder{
		Agent:     h.Adapter.Name,
		SessionID: h.SessionID,
		PID:       h.PID,
		Start:     h.Start,
		User:      h.User,
	}
}

// Anonymous is a caller with no adapter: the OS user and the parent process.
type Anonymous struct {
	User  string
	PID   int
	Start record.ProcessStart
	Name  string
}

// Record returns the identity an anonymous caller is recorded under. It has no agent.
func (a Anonymous) Record() record.Holder {
	return record.Holder{Name: a.Name, PID: a.PID, Start: a.Start, User: a.User}
}

// Resolution is the outcome of identifying a call.
type Resolution struct {
	// Holder is set when an ancestor matched an adapter.
	Holder *Holder

	// Anonymous is the caller identity when no adapter matched.
	Anonymous Anonymous

	// AnonymousErr is set when the anonymous identity could not be read.
	AnonymousErr error

	// Failure is set when the walk failed closed. Holder commands refuse. Commands that only read or
	// break run as the anonymous caller.
	Failure error

	// Adapters are the adapters the resolver tried, for error messages.
	Adapters []Adapter
}

// RequireHolder returns the holder or the reason there is none. `acquire`, `advertise`, and
// `release` call it.
func (r Resolution) RequireHolder() (Holder, error) {
	if r.Holder != nil {
		return *r.Holder, nil
	}

	if r.Failure != nil {
		return Holder{}, r.Failure
	}

	return Holder{}, fmt.Errorf("%w: nitipan recognizes %s", ErrNoAdapter, strings.Join(Names(r.Adapters), ", "))
}

// Caller is who a read-or-break command acts as: the holder when an agent matched, an anonymous
// process otherwise.
type Caller struct {
	Holder    *Holder
	Anonymous Anonymous
}

// Caller returns the caller for a command that needs no adapter.
func (r Resolution) Caller() (Caller, error) {
	if r.Holder != nil {
		return Caller{Holder: r.Holder}, nil
	}

	if r.AnonymousErr != nil {
		return Caller{}, fmt.Errorf("identifying the calling process: %w", r.AnonymousErr)
	}

	return Caller{Anonymous: r.Anonymous}, nil
}

// Resolver identifies the caller.
type Resolver struct {
	Adapters []Adapter
	Procs    proc.Table

	// Getenv returns an environment variable, empty when unset.
	Getenv func(string) string

	// User returns the OS user running the process.
	User func() (string, error)
}

// Resolve identifies the caller.
//
// With no agent variable set, the caller is anonymous and the process tree is never walked.
// Otherwise the walk goes up from nitipan's parent. The first ancestor that is an agent wins: the
// ancestor whose PID equals an adapter's PID variable, or whose command matches an adapter's
// signature. Only that adapter's variables are trusted.
func (r *Resolver) Resolve() Resolution {
	res := Resolution{Adapters: r.Adapters}

	user, userErr := r.User()
	res.Anonymous.User = user

	if err := r.fillAnonymous(&res.Anonymous); err != nil {
		res.AnonymousErr = err
	}

	if userErr != nil {
		res.AnonymousErr = errors.Join(res.AnonymousErr, fmt.Errorf("looking up the OS user: %w", userErr))
	}

	present := r.presentVars()
	if len(present) == 0 {
		return res
	}

	holder, found, err := r.walk()
	if err != nil {
		var unreadable *UnreadableAncestorError
		if errors.As(err, &unreadable) {
			unreadable.Vars = present
		}

		res.Failure = err

		return res
	}

	if !found {
		res.Failure = r.unmatched(present)

		return res
	}

	if userErr != nil {
		res.Failure = fmt.Errorf("looking up the OS user: %w", userErr)

		return res
	}

	holder.User = user
	res.Holder = &holder

	return res
}

// Host finds the agent process above a hook or plugin subprocess. It always walks, even with no
// agent variable set, because an agent's hook environment may carry none. The session ID is never
// read from the environment. The caller takes it from the hook's input.
//
// When the walk finds no agent, it fails closed.
func (r *Resolver) Host() (Holder, error) {
	user, err := r.User()
	if err != nil {
		return Holder{}, fmt.Errorf("looking up the OS user: %w", err)
	}

	holder, found, err := r.walk()
	if err != nil {
		return Holder{}, err
	}

	if !found {
		return Holder{}, fmt.Errorf("%w: no ancestor of this command is a supported agent", ErrFailClosed)
	}

	holder.SessionID = ""
	holder.User = user

	return holder, nil
}

// fillAnonymous reads the parent process, which identifies a caller with no adapter.
func (r *Resolver) fillAnonymous(a *Anonymous) error {
	self := r.Procs.Self()

	ppid, err := r.Procs.Parent(self)
	if err != nil {
		return fmt.Errorf("reading the parent of process %d: %w", self, err)
	}

	a.PID = ppid

	start, err := r.Procs.Start(ppid)
	if err != nil {
		return fmt.Errorf("reading the start of process %d: %w", ppid, err)
	}

	a.Start = start

	name, err := r.Procs.Name(ppid)
	if err != nil {
		return fmt.Errorf("reading the name of process %d: %w", ppid, err)
	}

	a.Name = name

	return nil
}

// presentVars lists the agent variables that are set, sorted.
func (r *Resolver) presentVars() []string {
	var present []string

	for _, a := range r.Adapters {
		for _, v := range a.vars() {
			if r.Getenv(v) != "" && !slices.Contains(present, v) {
				present = append(present, v)
			}
		}
	}

	slices.Sort(present)

	return present
}

// unmatched explains why agent variables are set while no ancestor is that agent.
func (r *Resolver) unmatched(present []string) error {
	for _, a := range r.Adapters {
		if a.PIDVar != "" && r.Getenv(a.PIDVar) != "" {
			return &PIDNotAncestorError{Var: a.PIDVar, Value: r.Getenv(a.PIDVar)}
		}
	}

	return &StrayVariablesError{Vars: present}
}

// walk returns the holder for the nearest agent ancestor. found is false when the chain ends with
// no match.
func (r *Resolver) walk() (Holder, bool, error) {
	self := r.Procs.Self()

	pid, err := r.Procs.Parent(self)
	if err != nil {
		return Holder{}, false, &UnreadableAncestorError{PID: self, Err: err}
	}

	for depth := 0; depth < maxDepth && pid > 0; depth++ {
		a, ok, err := r.match(pid)
		if err != nil {
			return Holder{}, false, err
		}

		if ok {
			h, err := r.holderFor(pid, a)
			if errors.Is(err, proc.ErrNoProcess) {
				return Holder{}, false, nil
			}

			return h, err == nil, err
		}

		next, err := r.Procs.Parent(pid)
		if errors.Is(err, proc.ErrNoProcess) {
			return Holder{}, false, nil
		}

		if err != nil {
			return Holder{}, false, &UnreadableAncestorError{PID: pid, Err: err}
		}

		if next == pid {
			return Holder{}, false, nil
		}

		pid = next
	}

	return Holder{}, false, nil
}

// match reports the adapter that recognizes the process.
func (r *Resolver) match(pid int) (Adapter, bool, error) {
	for _, a := range r.Adapters {
		if a.PIDVar != "" && strings.TrimSpace(r.Getenv(a.PIDVar)) == strconv.Itoa(pid) {
			return a, true, nil
		}
	}

	if !r.hasSignatures() {
		return Adapter{}, false, nil
	}

	args, err := r.Procs.Args(pid)
	if errors.Is(err, proc.ErrNoProcess) || (err != nil && pid == 1) {
		// A vanished process ends the chain. The init process is never an agent, and its arguments
		// are often unreadable.
		return Adapter{}, false, nil
	}

	if err != nil {
		return Adapter{}, false, &UnreadableAncestorError{PID: pid, Err: err}
	}

	name, err := r.Procs.Name(pid)
	if err != nil && !errors.Is(err, proc.ErrNoProcess) {
		return Adapter{}, false, &UnreadableAncestorError{PID: pid, Err: err}
	}

	for _, a := range r.Adapters {
		if a.Signature != nil && a.Signature(name, args) {
			return a, true, nil
		}
	}

	return Adapter{}, false, nil
}

func (r *Resolver) hasSignatures() bool {
	for _, a := range r.Adapters {
		if a.Signature != nil {
			return true
		}
	}

	return false
}

func (r *Resolver) holderFor(pid int, a Adapter) (Holder, error) {
	start, err := r.Procs.Start(pid)
	if errors.Is(err, proc.ErrNoProcess) {
		return Holder{}, err
	}

	if err != nil {
		return Holder{}, &UnreadableAncestorError{PID: pid, Err: err}
	}

	h := Holder{Adapter: a, PID: pid, Start: start}
	if a.SessionVar != "" {
		h.SessionID = strings.TrimSpace(r.Getenv(a.SessionVar))
	}

	return h, nil
}
