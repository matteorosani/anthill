// Package agentproc starts an agent process for a runner adapter and ends
// its whole process tree (design D6): a process group on Unix, a Job
// Object on Windows. Stopping asks the tree to end, waits for the grace
// period, then kills it, and a process that ends on its own takes what
// it started with it.
//
// This is a guard, not a sandbox (design D4): on Unix, a process that
// leaves the group, with setsid for example, is out of reach. Process
// reports it when such a process still holds the output open.
package agentproc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/ports"
)

// StoppedExit is the exit of a process that Stop ended.
var StoppedExit = ports.Exit{Code: -1, Crashed: true}

// DefaultDrain is how long Wait waits, after the tree has ended, for the
// last output.
const DefaultDrain = 2 * time.Second

// MaxLine is the longest standard output line given to Spec.Line. A
// longer line is dropped.
const MaxLine = 64 << 20

// Spec is how to start one agent process.
type Spec struct {
	// Path is the program, and Args its arguments without the program.
	Path string
	Args []string
	// Dir is the working directory, and Env the whole environment.
	Dir string
	Env []string
	// Stdin keeps a pipe to the process's standard input open for Write.
	// Without it, standard input is empty.
	Stdin bool
	// Line, when set, is called with each line of standard output,
	// without its line ending, in order and from one goroutine. The
	// slice is only valid during the call.
	Line func(line []byte)
	// Stderr receives the process's standard error, or nothing when nil.
	Stderr io.Writer
	// Drain is how long Wait waits for the last output once the tree has
	// ended; zero means DefaultDrain.
	Drain time.Duration
}

// Process is one agent process and its tree. It is safe for concurrent
// use.
type Process struct {
	cmd   *exec.Cmd
	drain time.Duration

	// treeMu guards tree, which is nil once closed.
	treeMu sync.Mutex
	tree   *tree

	inMu  sync.Mutex
	stdin *os.File

	// readers are the parent's ends of the output pipes, and outDone is
	// closed when both are read to the end.
	readers []*os.File
	outDone chan struct{}

	// exited is closed when the process itself has ended, and done when
	// Wait has its result.
	exited chan struct{}
	done   chan struct{}

	mu      sync.Mutex
	stopped bool
	exit    ports.Exit
	escaped bool
}

// Start starts the process in a tree of its own. The process does not
// depend on any context: it runs until it ends or Stop ends it.
func Start(s Spec) (*Process, error) {
	cmd := exec.Command(s.Path, s.Args...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	prepare(cmd)

	// The output goes through pipes the process writes directly, so
	// that cmd.Wait returns as soon as the process itself ends, even
	// while something it started still holds them.
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeAll(outR, outW)
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	childEnds := []*os.File{outW, errW}
	var inW *os.File
	if s.Stdin {
		inR, w, err := os.Pipe()
		if err != nil {
			closeAll(outR, outW, errR, errW)
			return nil, err
		}
		cmd.Stdin, inW = inR, w
		childEnds = append(childEnds, inR)
	}
	if err := cmd.Start(); err != nil {
		closeAll(append(childEnds, outR, errR, inW)...)
		return nil, err
	}
	closeAll(childEnds...)
	t, err := attach(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		closeAll(outR, errR, inW)
		return nil, err
	}

	p := &Process{
		cmd:     cmd,
		tree:    t,
		drain:   s.Drain,
		stdin:   inW,
		readers: []*os.File{outR, errR},
		outDone: make(chan struct{}),
		exited:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	if p.drain <= 0 {
		p.drain = DefaultDrain
	}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		readLines(outR, MaxLine, s.Line)
	}()
	go func() {
		defer readers.Done()
		w := s.Stderr
		if w == nil {
			w = io.Discard
		}
		_, _ = io.Copy(w, errR)
	}()
	go func() {
		readers.Wait()
		close(p.outDone)
	}()
	go p.wait()
	return p, nil
}

// Pid is the process id of the agent process.
func (p *Process) Pid() int {
	return p.cmd.Process.Pid
}

// Write writes b to the process's standard input. It fails without
// Spec.Stdin, after CloseInput, and once the process has ended.
func (p *Process) Write(b []byte) error {
	p.inMu.Lock()
	defer p.inMu.Unlock()
	if p.stdin == nil {
		return errors.New("the standard input is closed")
	}
	if _, err := p.stdin.Write(b); err != nil {
		return fmt.Errorf("writing to the agent: %w", err)
	}
	return nil
}

// CloseInput closes the process's standard input, so that it reads the
// end of its input. Closing it again does nothing.
func (p *Process) CloseInput() error {
	p.inMu.Lock()
	defer p.inMu.Unlock()
	if p.stdin == nil {
		return nil
	}
	err := p.stdin.Close()
	p.stdin = nil
	return err
}

// Stop ends the whole tree: it asks the tree to end (SIGTERM on Unix,
// CTRL_BREAK on Windows), waits up to grace for the process to end, then
// kills the tree. It returns once Wait has its result. Stopping a
// process that has ended does nothing.
func (p *Process) Stop(grace time.Duration) error {
	select {
	case <-p.exited:
		<-p.done
		return nil
	default:
	}
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	_ = p.onTree((*tree).interrupt)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.exited:
	case <-timer.C:
	}
	err := p.onTree((*tree).kill)
	<-p.done
	return err
}

// Wait blocks until the process has ended, the rest of its tree has
// been killed and its output read, and returns how it ended. A process
// that Stop ended returns StoppedExit.
func (p *Process) Wait() ports.Exit {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit
}

// Escaped reports, once Wait has returned, whether the output was still
// held open after the tree was killed: a process the agent started had
// left the tree and still runs.
func (p *Process) Escaped() bool {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.escaped
}

// wait waits for the process, then ends the rest of its tree, reads the
// last output and records the exit.
func (p *Process) wait() {
	err := p.cmd.Wait()
	close(p.exited)
	_ = p.onTree((*tree).kill)
	escaped := false
	select {
	case <-p.outDone:
	case <-time.After(p.drain):
		escaped = true
		// Unblock the readers; what is still running keeps writing to a
		// closed pipe.
		closeAll(p.readers...)
	}
	p.treeMu.Lock()
	p.tree.close()
	p.tree = nil
	p.treeMu.Unlock()
	_ = p.CloseInput()

	exit := exitOf(p.cmd.ProcessState, err)
	p.mu.Lock()
	if p.stopped {
		exit = StoppedExit
	}
	p.exit, p.escaped = exit, escaped
	p.mu.Unlock()
	close(p.done)
}

// onTree calls f on the tree unless it is closed.
func (p *Process) onTree(f func(*tree) error) error {
	p.treeMu.Lock()
	defer p.treeMu.Unlock()
	if p.tree == nil {
		return nil
	}
	return f(p.tree)
}

// exitOf is how the process ended: its exit code, and whether a signal
// or an abnormal end, rather than the process itself, ended it.
func exitOf(state *os.ProcessState, err error) ports.Exit {
	if state == nil {
		return ports.Exit{Code: -1, Crashed: true}
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return ports.Exit{Code: -1, Crashed: true}
	}
	return ports.Exit{Code: state.ExitCode(), Crashed: crashed(state)}
}

// readLines calls line with each line of r, without its line ending,
// until r ends. Empty lines, and lines longer than limit, are dropped.
func readLines(r io.Reader, limit int, line func([]byte)) {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case tooLong:
		case len(buf)+len(chunk) > limit:
			tooLong, buf = true, nil
		default:
			buf = append(buf, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if l := trimEOL(buf); !tooLong && len(l) > 0 && line != nil {
			line(l)
		}
		buf, tooLong = buf[:0], false
		if err != nil {
			return
		}
	}
}

func trimEOL(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

func closeAll(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}
