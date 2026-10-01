package baresipmedia

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var ErrProcessUnavailable = errors.New("baresip process could not be started")

// Process supervises one foreground Baresip process. SIP traces and provider
// output are discarded so credentials or authorization headers cannot enter
// application logs. Cancellation and Close reap the child deterministically.
type Process struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan error
	once  sync.Once
}

func StartProcess(ctx context.Context, executable, profileDir string) (*Process, error) {
	if ctx == nil || strings.TrimSpace(executable) == "" || strings.TrimSpace(profileDir) == "" {
		return nil, ErrProcessUnavailable
	}
	cmd := exec.Command(executable, "-f", profileDir)
	cmd.Env = baresipEnvironment(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, ErrProcessUnavailable
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, ErrProcessUnavailable
	}
	process := &Process{cmd: cmd, stdin: stdin, done: make(chan error, 1)}
	go func() { process.done <- cmd.Wait(); close(process.done) }()
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Close()
		case <-process.done:
		}
	}()
	return process, nil
}

func baresipEnvironment(source []string) []string {
	const (
		geminiPrefix     = "GEMINI_"
		openrouterPrefix = "OPENROUTER_"
		postgresPrefix   = "PG"
		postgresFull     = "POSTGRES_"
		supabasePrefix   = "SUPABASE_"
	)
	env := make([]string, 0, len(source))
	for _, item := range source {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "OWNER_API_TOKEN" || key == "REDIS_PASSWORD" || key == "RABBITMQ_DEFAULT_PASS" || strings.HasPrefix(key, geminiPrefix) || strings.HasPrefix(key, openrouterPrefix) || strings.HasPrefix(key, postgresPrefix) || strings.HasPrefix(key, postgresFull) || strings.HasPrefix(key, supabasePrefix) {
			continue
		}
		env = append(env, item)
	}
	return env
}

func (p *Process) Close() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	p.once.Do(func() {
		_ = p.stdin.Close()
		_ = p.cmd.Process.Kill()
	})
	<-p.done
	return nil
}
