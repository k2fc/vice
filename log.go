// log.go
// Copyright(c) 2022 Matt Pharr, licensed under the GNU Public License, Version 3.
// SPDX: GPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Logger provides a simple logging system with a few different log levels;
// debugging and verbose output may both be suppressed independently.
type Logger struct {
	// Note that CircularLogBuffer is not thread-safe, so Logger must hold
	// this mutex before calling into it.
	mu            sync.Mutex
	printToStderr bool
	verbose       *CircularLogBuffer
	err           *CircularLogBuffer
	start         time.Time
}

// Each log message is stored using a LogEntry, which also records the time
// it was submitted.
type LogEntry struct {
	message string
	time    time.Time
}

func (l LogEntry) String() string {
	return l.message // time is already encoded in it
}

// CircularLogBuffer stores a fixed maximum number of logging messages; this
// lets us hold on to logging messages for use in bug reports without worrying
// about whether we're using too much memory to do so.
type CircularLogBuffer struct {
	rb    *RingBuffer[LogEntry]
	start time.Time
}

func (c *CircularLogBuffer) Add(s string) {
	c.rb.Add(LogEntry{message: s, time: time.Now()})
}

func (c *CircularLogBuffer) String() string {
	var b strings.Builder
	for i := 0; i < c.rb.Size(); i++ {
		b.WriteString(c.rb.Get(i).String())
	}
	return b.String()
}

func (c *CircularLogBuffer) Get() []string {
	var strs []string
	for i := 0; i < c.rb.Size(); i++ {
		strs = append(strs, c.rb.Get(i).String())
	}
	return strs
}

func NewCircularLogBuffer(maxLines int) *CircularLogBuffer {
	return &CircularLogBuffer{rb: NewRingBuffer[LogEntry](maxLines), start: time.Now()}
}

func NewLogger(verbose bool, printToStderr bool, maxLines int) *Logger {
	l := &Logger{printToStderr: printToStderr, start: time.Now()}
	if verbose {
		l.verbose = NewCircularLogBuffer(maxLines)
	}
	l.err = NewCircularLogBuffer(maxLines)

	// Start out the logs with some basic information about the system
	// we're running on and the build of vice that's being used.
	l.Printf("Hello logging at %s", time.Now())
	l.Printf("Arch: %s OS: %s CPUs: %d", runtime.GOARCH, runtime.GOOS, runtime.NumCPU())
	if bi, ok := debug.ReadBuildInfo(); ok {
		l.Printf("Build: go %s path %s", bi.GoVersion, bi.Path)
		for _, dep := range bi.Deps {
			if dep.Replace == nil {
				l.Printf("Module %s @ %s", dep.Path, dep.Version)
			} else {
				l.Printf("Module %s @ %s replaced by %s @ %s", dep.Path, dep.Version,
					dep.Replace.Path, dep.Replace.Version)
			}
		}
		for _, setting := range bi.Settings {
			l.Printf("Build setting %s = %s", setting.Key, setting.Value)
		}
	}

	return l
}

// Printf adds the given message, specified using Printf-style format
// string, to the "verbose" log.  If verbose logging is not enabled, the
// message is discarded.
func (l *Logger) Printf(f string, args ...interface{}) {
	l.printf(3, f, args...)
}

// PrintfUp1 adds the given message to the error log, but with reported the
// source file and line number are one level up in the call stack from the
// function that called it.
func (l *Logger) PrintfUp1(f string, args ...interface{}) {
	l.printf(4, f, args...)
}

func (l *Logger) printf(levels int, f string, args ...interface{}) {
	if l == nil {
		fmt.Fprintf(os.Stderr, f, args...)
		return
	}

	if l.verbose == nil {
		return
	}

	msg := l.format(levels, f, args...)
	if l.printToStderr {
		fmt.Fprint(os.Stderr, msg)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.verbose.Add(msg)
}

// Errorf adds the given message, specified using Printf-style format
// string, to the error log.
func (l *Logger) Errorf(f string, args ...interface{}) {
	if l == nil {
		fmt.Fprintf(os.Stderr, "ERROR: "+f, args...)
		return
	}
	l.errorf(3, f, args...)
}

// ErrorfUp1 adds the given message to the error log, though the source
// file and line number logged are one level up in the call stack from the
// function that calls ErrorfUp1. (This can be useful for functions that
// are called from many places and where the context of the calling
// function is more likely to be useful for debugging the error.)
func (l *Logger) ErrorfUp1(f string, args ...interface{}) {
	l.errorf(4, f, args...)
}

func (l *Logger) errorf(levels int, f string, args ...interface{}) {
	msg := l.format(levels, "\033[1;31mERROR: "+f+"\033[0m: ", args...)

	// Always print it
	fmt.Fprint(os.Stderr, msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	l.err.Add(msg)
}

func (l *Logger) GetVerboseLog() string {
	if l.verbose == nil {
		return ""
	}
	return l.verbose.String()
}

func (l *Logger) GetErrorLog() []string {
	return l.err.Get()
}

func (l *Logger) GetLog() []string {
	return l.verbose.Get()
}

// format is a utility function for formatting logging messages. It
// prepends the source file and line number of the logging call to the
// returned message string.
func (l *Logger) format(levels int, f string, args ...interface{}) string {
	// Go up the call stack the specified nubmer of levels
	_, fn, line, _ := runtime.Caller(levels)

	// Current time
	s := time.Now().Format(time.RFC1123) + " "

	// Source file and line
	fnline := path.Base(fn) + fmt.Sprintf(":%d", line)
	s += fmt.Sprintf("%-20s ", fnline)

	// Add the provided logging message.
	s += fmt.Sprintf(f, args...)

	// The message shouldn't have a newline at the end but if it does, we
	// won't gratuitously add another one.
	if !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// Stats collects a few statistics related to rendering and time spent in
// various phases of the system.
type Stats struct {
	render    RendererStats
	renderUI  RendererStats
	drawImgui time.Duration
	drawPanes time.Duration
	startTime time.Time
	redraws   int
}

var startupMallocs uint64

// LogStats adds the proivded Stats to the log and also includes information about
// the current system performance, memory use, etc.
func (l *Logger) LogStats(stats Stats) {
	lg.Printf("Redraws per second: %.1f", float64(stats.redraws)/time.Since(stats.startTime).Seconds())

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	if startupMallocs == 0 {
		startupMallocs = mem.Mallocs
	}

	elapsed := time.Since(l.start).Seconds()
	mallocsPerSecond := int(float64(mem.Mallocs-startupMallocs) / elapsed)
	active1000s := (mem.Mallocs - mem.Frees) / 1000
	lg.Printf("Stats: mallocs/second %d (%dk active) %d MB in use", mallocsPerSecond, active1000s,
		mem.HeapAlloc/(1024*1024))

	lg.Printf("Stats: draw panes %s draw imgui %s", stats.drawPanes.String(), stats.drawImgui.String())

	lg.Printf("Stats: rendering: %s", stats.render.String())
	lg.Printf("Stats: UI rendering: %s", stats.renderUI.String())
}

func (l *Logger) SaveLogs() {
	dir, err := os.UserConfigDir()
	if err != nil {
		lg.Errorf("Unable to find user config dir: %v", err)
		dir = "."
	}

	fn := path.Join(dir, "Vice", "vice.log")
	s := l.verbose.String() + "\n-----\nErrors:\n" + l.err.String()
	os.WriteFile(fn, []byte(s), 0600)
}
