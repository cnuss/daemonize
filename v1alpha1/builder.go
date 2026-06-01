package v1alpha1

import (
	"context"
	"os"
	"syscall"

	"github.com/cnuss/daemonize/v1"
	"github.com/spf13/cobra"
)

// FromCobra wraps command, retyping the builder to *cobra.Command so Into
// returns the assembled root command. Configure with the With* methods after
// this call; reload is disabled until WithReload sets a signal.
func (d *DaemonImpl[T]) FromCobra(command *cobra.Command) v1.Daemon[*cobra.Command] {
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

func (d *DaemonImpl[T]) WithReload(sig syscall.Signal) v1.Daemon[T] {
	d.reloadSig = &sig
	return d
}

func (d *DaemonImpl[T]) WithName(name string) v1.Daemon[T] {
	d.name = &name
	return d
}

func (d *DaemonImpl[T]) WithGroup(name *string) v1.Daemon[T] {
	d.group = name
	d.groupSet = true
	return d
}

func (d *DaemonImpl[T]) WithContext(parent context.Context) v1.Daemon[T] {
	d.ctxParent = parent
	d.ctxParentSet = true
	return d
}

func (d *DaemonImpl[T]) WithShutdownSignal(sigs ...os.Signal) v1.Daemon[T] {
	d.shutdownSigs = sigs
	d.shutdownSigsSet = true
	return d
}
