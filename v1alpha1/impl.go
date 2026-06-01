package v1alpha1

import (
	"context"
	"os"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// New returns an unconfigured DaemonImpl. The root `daemonize.NewDaemon`
// façade wraps this and returns the v1.Daemon[any] interface; callers
// reaching directly into v1alpha1 use this for the concrete struct.
func New[T any]() *DaemonImpl[T] {
	return &DaemonImpl[T]{}
}

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
