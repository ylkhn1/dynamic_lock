package logger

import (
	"os"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

const ringCapacity = 500

var (
	Log *logrus.Logger
	mem = newRing(ringCapacity)
)

func init() {
	Log = logrus.New()
	Log.SetOutput(os.Stdout)
	Log.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: "2006-01-02 15:04:05",
	})
	Log.SetLevel(logrus.InfoLevel)
	Log.AddHook(&ringHook{r: mem})
}

// SetLevel parses a level string and applies it; falls back to info on error.
func SetLevel(level string) {
	l, err := logrus.ParseLevel(level)
	if err != nil {
		Log.Warnf("unknown log level %q, keeping info", level)
		return
	}
	Log.SetLevel(l)
}

// Tail returns the last n formatted log lines from the in-memory ring buffer.
// Used by the Telegram /logs command — avoids reading files or shelling out
// to journalctl.
func Tail(n int) []string { return mem.tail(n) }

// ---- in-memory ring buffer --------------------------------------------------

type ring struct {
	mu    sync.Mutex
	lines []string
	head  int
	full  bool
}

func newRing(size int) *ring { return &ring{lines: make([]string, size)} }

func (r *ring) add(s string) {
	r.mu.Lock()
	r.lines[r.head] = s
	r.head = (r.head + 1) % len(r.lines)
	if r.head == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

func (r *ring) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []string
	if r.full {
		all = append(all, r.lines[r.head:]...)
		all = append(all, r.lines[:r.head]...)
	} else {
		all = append(all, r.lines[:r.head]...)
	}
	if n < len(all) {
		all = all[len(all)-n:]
	}
	return all
}

type ringHook struct{ r *ring }

func (h *ringHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *ringHook) Fire(e *logrus.Entry) error {
	s, err := e.String()
	if err != nil {
		return err
	}
	h.r.add(strings.TrimRight(s, "\n"))
	return nil
}
