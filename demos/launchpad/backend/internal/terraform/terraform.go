// Package terraform runs the Terraform binary inside Lambda.
package terraform

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"launchpad/internal/apperr"
	"launchpad/internal/services"
)

const Version = "1.16.3"

// checksums are the SHA-256 of the release zips, from
// releases.hashicorp.com/terraform/<Version>/..._SHA256SUMS.
var checksums = map[string]string{
	"amd64": "093b6ae9a2228af5029c41606bc96eb583553528aad1bfe7e0b4d62fc91e25d8",
	"arm64": "b6b293550af7a6a7ffd867a04900ce161db86ec9407b234a636c2adbae2ca9f0",
}

// StopGrace is how long Terraform gets to stop after an interrupt before it is killed.
const StopGrace = 90 * time.Second

const (
	downloadTimeout = 5 * time.Minute
	maxZipBytes     = 200 * 1024 * 1024
	progressEvery   = 10 * time.Second
	// How long to keep reading output from a command that has already exited,
	// for the sake of children that inherited its pipe.
	pipeDrain = 5 * time.Second
)

// EnsureBinary downloads Terraform into directory once per container, checking its checksum.
func EnsureBinary(ctx context.Context, svc *services.Services, directory string) (string, error) {
	return ensureBinary(ctx, svc, directory, checksums)
}

func ensureBinary(ctx context.Context, svc *services.Services, directory string, sums map[string]string) (string, error) {
	binary := filepath.Join(directory, "terraform")
	if _, err := os.Stat(binary); err == nil {
		return binary, nil
	}
	arch := runtime.GOARCH
	want, ok := sums[arch]
	if !ok {
		return "", fmt.Errorf("no Terraform release is known for %s", arch)
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	url := fmt.Sprintf("https://releases.hashicorp.com/terraform/%s/terraform_%s_linux_%s.zip", Version, Version, arch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "launchpad")
	resp, err := svc.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("download Terraform: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download Terraform: %s answered %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxZipBytes))
	if err != nil {
		return "", fmt.Errorf("download Terraform: %w", err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != want {
		return "", apperr.New(500, "terraform_checksum", "The downloaded Terraform did not match its checksum")
	}
	return binary, unzipBinary(data, directory, binary)
}

func unzipBinary(data []byte, directory, binary string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("open the Terraform zip: %w", err)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		if f.Name != "terraform" {
			continue
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		// Written under another name and renamed, so a half-written binary is never used.
		tmp := binary + ".part"
		out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, src); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(tmp, binary)
	}
	return errors.New("the Terraform zip has no terraform file")
}

// LogTail is the last lines of output, with secrets removed.
type LogTail struct {
	secrets  []string
	maxLines int
	maxChars int
	mu       sync.Mutex
	lines    []string
}

// NewLogTail returns a tail that hides secrets of four characters or more.
func NewLogTail(secrets ...string) *LogTail {
	t := &LogTail{maxLines: 200, maxChars: 30000}
	for _, s := range secrets {
		if len(s) >= 4 {
			t.secrets = append(t.secrets, s)
		}
	}
	return t
}

// Hide makes the tail hide secret from now on, if it is four characters or more. Lines
// already added are not changed.
func (t *LogTail) Hide(secret string) {
	if len(secret) < 4 {
		return
	}
	t.mu.Lock()
	t.secrets = append(t.secrets, secret)
	t.mu.Unlock()
}

// Add appends one line.
func (t *LogTail) Add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.secrets {
		line = strings.ReplaceAll(line, s, "***")
	}
	line = strings.TrimRight(line, "\n")
	t.lines = append(t.lines, line)
	if len(t.lines) > t.maxLines {
		t.lines = t.lines[len(t.lines)-t.maxLines:]
	}
}

// Text is the tail as one string.
func (t *LogTail) Text() string {
	t.mu.Lock()
	out := strings.Join(t.lines, "\n")
	t.mu.Unlock()
	if len(out) <= t.maxChars {
		return out
	}
	out = out[len(out)-t.maxChars:]
	for len(out) > 0 && !utf8.RuneStart(out[0]) {
		out = out[1:]
	}
	return out
}

// lineWriter splits what a command writes into lines for a LogTail.
type lineWriter struct {
	tail *LogTail
	mu   sync.Mutex
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		w.tail.Add(strings.ToValidUTF8(string(w.buf[:i]), "�"))
		w.buf = w.buf[i+1:]
	}
}

// flush adds a last line that had no newline, such as the value of `output -raw`.
func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.tail.Add(strings.ToValidUTF8(string(w.buf), "�"))
		w.buf = nil
	}
}

// Result is the outcome of one Terraform command.
type Result struct {
	Code        int
	Interrupted bool   // the deadline passed and Terraform was told to stop
	Output      string // the tail of the output
}

// Terraform runs commands of one binary in one directory.
type Terraform struct {
	Binary string
	Dir    string
	Env    []string // the whole environment of the process, as KEY=value
	Log    *LogTail
	OnTick func()        // called about every TickEvery while a command runs
	Grace  time.Duration // how long a command gets to stop after an interrupt
	// TickEvery is how often OnTick is called. Zero means every ten seconds.
	TickEvery time.Duration
}

// New returns a Terraform that gives commands StopGrace to stop.
func New(binary, dir string, env []string, log *LogTail, onTick func()) *Terraform {
	return &Terraform{Binary: binary, Dir: dir, Env: env, Log: log, OnTick: onTick, Grace: StopGrace}
}

// Run runs one Terraform command. At deadline, if it is not zero, the command is interrupted.
func (t *Terraform) Run(ctx context.Context, deadline time.Time, args ...string) (Result, error) {
	cmd := exec.Command(t.Binary, args...)
	cmd.Dir = t.Dir
	cmd.Env = t.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &lineWriter{tail: t.Log}
	cmd.Stdout, cmd.Stderr = out, out // one pipe, so the order of lines is kept
	cmd.WaitDelay = pipeDrain
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start terraform: %w", err)
	}

	done := make(chan struct{})
	var interrupted bool
	var watchdog sync.WaitGroup
	watchdog.Go(func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		every := t.TickEvery
		if every <= 0 {
			every = progressEvery
		}
		lastProgress := time.Now()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				// The invocation is being cut off. Stop Terraform now.
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				return
			case now := <-tick.C:
				if t.OnTick != nil && now.Sub(lastProgress) >= every {
					lastProgress = now
					t.OnTick()
				}
				if deadline.IsZero() || now.Before(deadline) {
					continue
				}
				interrupted = true // read only after the watchdog has finished
				t.Log.Add("Launchpad: time is nearly up, asking Terraform to stop")
				_ = cmd.Process.Signal(syscall.SIGINT)
				select {
				case <-done:
				case <-time.After(t.Grace):
					t.Log.Add("Launchpad: Terraform did not stop in time, killing it")
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
				return
			}
		}
	})

	waitErr := cmd.Wait()
	close(done)
	watchdog.Wait()
	out.flush()

	code := 0
	var exit *exec.ExitError
	switch {
	case errors.As(waitErr, &exit):
		code = exit.ExitCode() // -1 when a signal ended it
	case waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay):
		return Result{}, fmt.Errorf("run terraform: %w", waitErr)
	}
	return Result{Code: code, Interrupted: interrupted, Output: t.Log.Text()}, nil
}
