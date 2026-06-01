package daemonize

import (
	"context"
	"os"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// DaemonImpl is the default Daemon implementation. T is the wrapped value's
// type (and what Into returns).
type DaemonImpl[T any] struct {
	inner     T
	detachSig *(<-chan struct{}) // nil = unset
	reloadSig *syscall.Signal    // nil = reload disabled
	name      *string            // nil = derive from command path
	group     *string            // group title; nil (with groupSet) = ungrouped
	groupSet  bool               // true once WithGroup was called

	// ctxParent is the parent context set via WithContext. ctxParentSet
	// distinguishes "WithContext(nil)" (opt-out) from "WithContext never
	// called" (use defaults).
	ctxParent    context.Context
	ctxParentSet bool

	// shutdownSigs are the signals registered via WithShutdownSignal;
	// shutdownSigsSet records whether the method was called.
	shutdownSigs    []os.Signal
	shutdownSigsSet bool

	// ctxCancel releases the signal.NotifyContext goroutine. Set during
	// buildCobra when context auto-wiring is configured; defaults to a no-op so callers
	// of Stop/Reload/Status can defer it unconditionally.
	ctxCancel context.CancelFunc

	// State-file paths and the base name they derive from, resolved in buildCobra.
	pidFile string
	logFile string
	base    string

	// daemonCmd is the daemon-owned foreground command; start re-execs along its
	// full path so the daemon root can be mounted under a larger cobra tree.
	daemonCmd *cobra.Command

	// Into builds once; subsequent calls return the cached result.
	builtOnce sync.Once
	built     T
}

// NewDaemon returns an unconfigured builder. Call FromCobra to wrap a command.
func NewDaemon() Daemon[any] {
	return &DaemonImpl[any]{}
}

// FromCobra is a shorthand for NewDaemon().FromCobra(command). Use it when you
// already know you are wrapping a cobra command and don't need the untyped
// Daemon[any] bootstrap:
//
//	cmd := daemonize.FromCobra(serve).WithReload(syscall.SIGHUP).DetachOn(ready)
func FromCobra(command *cobra.Command) Daemon[*cobra.Command] {
	return NewDaemon().FromCobra(command)
}

// FromCobra wraps command, retyping the builder to *cobra.Command so Into
// returns the assembled root command. Configure with the With* methods after
// this call; reload is disabled until WithReload sets a signal.
func (d *DaemonImpl[T]) FromCobra(command *cobra.Command) Daemon[*cobra.Command] {
	return &DaemonImpl[*cobra.Command]{inner: command}
}

// DetachOn records the readiness channel, then assembles the lifecycle wrapper
// for the wrapped value and returns it (as T), dispatching on inner's type.
func (d *DaemonImpl[T]) DetachOn(detachSig <-chan struct{}) T {
	d.detachSig = &detachSig
	switch any(d.inner).(type) {
	case *cobra.Command:
		d.builtOnce.Do(func() { d.built = any(d.buildCobra()).(T) })
		return d.built
	default:
		panic("daemonize: unsupported wrapped type")
	}
}

func (d *DaemonImpl[T]) WithReload(sig syscall.Signal) Daemon[T] {
	d.reloadSig = &sig
	return d
}

func (d *DaemonImpl[T]) WithName(name string) Daemon[T] {
	d.name = &name
	return d
}

func (d *DaemonImpl[T]) WithGroup(name *string) Daemon[T] {
	d.group = name
	d.groupSet = true
	return d
}

func (d *DaemonImpl[T]) WithContext(parent context.Context) Daemon[T] {
	d.ctxParent = parent
	d.ctxParentSet = true
	return d
}

func (d *DaemonImpl[T]) WithShutdownSignal(sigs ...os.Signal) Daemon[T] {
	d.shutdownSigs = sigs
	d.shutdownSigsSet = true
	return d
}
