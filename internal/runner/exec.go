package runner

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"

	"go.uber.org/zap"
)

// execSession serves one live-exec request (#132): docker exec inside the
// job container, output streamed to the control plane, stdin polled on a
// short interval. The session dies with the agent context, the container,
// or the command — whichever comes first.
func (a *Agent) execSession(ctx context.Context, offer execOffer) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer a.execSessions.Delete(offer.ID)
	log := a.log.With(zap.String("exec_id", offer.ID), zap.String("job_id", offer.JobID))

	if err := a.c.execStart(ctx, offer.ID); err != nil {
		log.Warn("exec start refused", zap.Error(err))
		return
	}
	container, ok := a.waitJobContainer(ctx, offer.JobID)
	if !ok {
		fctx, fc := context.WithTimeout(context.Background(), 10*time.Second)
		_ = a.c.execFinish(fctx, offer.ID, nil, "job container did not come up in time")
		fc()
		return
	}
	a.runExec(ctx, log, offer, container)
}

// waitJobContainer polls until the job's container name is registered, the
// job leaves the active set, or five minutes pass. Job prepare (checkout,
// image pull) legitimately takes minutes, so this is generous.
func (a *Agent) waitJobContainer(ctx context.Context, jobID string) (string, bool) {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if v, ok := a.jobContainers.Load(jobID); ok {
			if name, _ := v.(string); name != "" {
				return name, true
			}
			return "", false // host executor — no container to exec into
		}
		if _, active := a.activeJobs.Load(jobID); !active {
			return "", false
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", false
}

// runExec wires the docker exec process to the output poster and stdin
// pump, then reports the exit state.
func (a *Agent) runExec(ctx context.Context, log *zap.Logger, offer execOffer, container string) {
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	argv := []string{"exec", "-i", container, "sh", "-c", offer.Command}
	if offer.Interactive {
		// Shell resolution mirrors steps: bash when present, sh otherwise.
		argv = []string{"exec", "-i", container, "sh", "-c",
			"exec bash -l 2>/dev/null || exec sh -l"}
	}
	cmd := exec.CommandContext(execCtx, "docker", argv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		a.finishExec(offer.ID, nil, err.Error())
		return
	}
	poster := &execPoster{ctx: ctx, c: a.c, id: offer.ID, log: log}
	cmd.Stdout = poster
	cmd.Stderr = poster
	if err := cmd.Start(); err != nil {
		a.finishExec(offer.ID, nil, err.Error())
		return
	}
	posterDone := make(chan struct{})
	go func() { // keepalive flush for output smaller than the batch window
		defer close(posterDone)
		t := time.NewTicker(300 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-execCtx.Done():
				return
			case <-t.C:
				poster.flush()
			}
		}
	}()
	go a.pumpExecStdin(execCtx, offer.ID, stdin)

	err = cmd.Wait()
	execCancel()
	<-posterDone
	poster.flush()
	var code *int
	var errText string
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			c := ee.ExitCode()
			code = &c
		} else {
			errText = err.Error()
		}
	} else {
		c := 0
		code = &c
	}
	a.finishExec(offer.ID, code, errText)
}

func (a *Agent) finishExec(execID string, code *int, errText string) {
	fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.c.execFinish(fctx, execID, code, errText); err != nil {
		a.log.Warn("exec finish report failed", zap.String("exec_id", execID), zap.Error(err))
	}
}

// pumpExecStdin feeds the session's stdin blob into the docker exec pipe.
// Advances offset by len(data) (never total blob size) so >64 KiB bursts do
// not drop bytes, and tolerates transient HTTP hiccups before bailing.
// ponytail: half-second poll, no PTY framing — a line-mode shell; upgrade
// to TTY framing when interactive exec needs full-screen programs.
func (a *Agent) pumpExecStdin(ctx context.Context, execID string, stdin io.WriteCloser) {
	defer stdin.Close()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var off int64
	consecErrs := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		data, _, closed, err := a.c.execStdin(ctx, execID, off)
		if err != nil {
			consecErrs++
			if consecErrs >= 5 || ctx.Err() != nil {
				return
			}
			continue
		}
		consecErrs = 0
		if len(data) > 0 {
			if _, err := stdin.Write(data); err != nil {
				return
			}
			off += int64(len(data))
		}
		if closed {
			return
		}
	}
}

// execPoster batches docker exec output into append posts against the
// session blob. Serializes flushes without holding p.mu across network I/O,
// retaining failed chunks up to a 2 MiB cap for retry on the next tick.
type execPoster struct {
	ctx     context.Context
	c       *client
	id      string
	log     *zap.Logger
	mu      sync.Mutex
	flushMu sync.Mutex
	buf     []byte
}

const execPosterMaxRetain = 2 << 20

func (p *execPoster) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.buf = append(p.buf, b...)
	shouldFlush := len(p.buf) >= 16<<10
	p.mu.Unlock()
	if shouldFlush {
		p.flush()
	}
	return len(b), nil
}

func (p *execPoster) flush() {
	p.flushMu.Lock()
	defer p.flushMu.Unlock()
	for {
		p.mu.Lock()
		if len(p.buf) == 0 {
			p.mu.Unlock()
			return
		}
		n := min(len(p.buf), 512<<10)
		chunk := make([]byte, n)
		copy(chunk, p.buf[:n])
		p.buf = p.buf[n:]
		p.mu.Unlock()

		if err := p.c.execOutput(p.ctx, p.id, chunk); err != nil {
			p.log.Warn("exec output post failed; retaining chunk for retry", zap.Error(err))
			p.mu.Lock()
			if len(chunk)+len(p.buf) <= execPosterMaxRetain {
				p.buf = append(chunk, p.buf...)
			}
			p.mu.Unlock()
			return
		}
	}
}
