package daemonize

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// daemonEnvFor derives the env-var name used to mark a child launched by start,
// from the daemon's resolved Name (e.g. "widget" -> "WIDGET_DAEMON"). The
// readiness relay reads it to decide whether to signal the parent. Parent and
// child compute it identically, so they always agree.
func daemonEnvFor(base string) string {
	return strings.ToUpper(strings.ReplaceAll(base, "-", "_")) + "_DAEMON"
}

// stateFiles builds the pid/log paths under <cache>/.<name>/ (name is the
// wrapped command's name), e.g. ".../.serve/server-serve.{pid,log}". Falls back
// to the working directory if the cache dir is unavailable.
func stateFiles(name, base string) (pid, log string) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = "."
	} else {
		dir = filepath.Join(dir, "."+name)
		os.MkdirAll(dir, 0o755)
	}
	return filepath.Join(dir, base+".pid"), filepath.Join(dir, base+".log")
}

// commandFileBase joins a command's name with its ancestors', root first
// (e.g. "server-serve"), for use as a state-file base name. It recurses up the
// parent chain until reaching the root (Parent() == nil).
func commandFileBase(cmd *cobra.Command) string {
	if cmd.Parent() == nil {
		return cmd.Name()
	}
	return commandFileBase(cmd.Parent()) + "-" + cmd.Name()
}

// logTail streams newly appended log bytes to stdout. A nil file (log missing,
// e.g. a foreground server) makes copy/Close no-ops.
type logTail struct {
	f   *os.File
	buf []byte
}

// openLog opens the log for streaming. seekEnd=true starts at the current end
// (stop: only new shutdown output); false starts at the beginning (start: all
// startup output).
func (d *DaemonImpl[T]) openLog(seekEnd bool) *logTail {
	fh, err := os.Open(d.logFile)
	if err != nil {
		return &logTail{}
	}
	if seekEnd {
		fh.Seek(0, io.SeekEnd)
	}
	return &logTail{f: fh, buf: make([]byte, 4096)}
}

func (t *logTail) copy() {
	if t.f == nil {
		return
	}
	for {
		n, _ := t.f.Read(t.buf)
		if n == 0 {
			return
		}
		os.Stdout.Write(t.buf[:n])
	}
}

func (t *logTail) Close() {
	if t.f != nil {
		t.f.Close()
	}
}
