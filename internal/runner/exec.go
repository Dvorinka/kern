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
	argv := []string{"exec", "-i", container, "sh", "-c", offer.Command}
	if offer.Interactive {
		// Shell resolution mirrors steps: bash when present, sh otherwise.
		argv = []string{"exec", "-i", container, "sh", "-c",
			"exec bash -l 2>/dev/null || exec sh -l"}
	}
	cmd := exec.CommandContext(ctx, "docker", argv...)
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
			case <-ctx.Done():
				return
			case <-t.C:
				poster.flush()
			}
		}
	}()
	go a.pumpExecStdin(ctx, offer.ID, stdin)

	err = cmd.Wait()
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
// ponytail: half-second poll, no PTY framing — a line-mode shell; upgrade
// to TTY framing when interactive exec needs full-screen programs.
func (a *Agent) pumpExecStdin(ctx context.Context, execID string, stdin io.WriteCloser) {
	defer stdin.Close()
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	var off int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		data, size, closed, err := a.c.execStdin(ctx, execID, off)
		if err != nil {
			return // control plane unreachable or session finished — cmd.Wait ends us
		}
		if len(data) > 0 {
			if _, err := stdin.Write(data); err != nil {
				return
			}
		}
		off = size
		if closed {
			return
		}
	}
}

// execPoster batches docker exec output into append posts against the
// session blob. Failed posts drop the chunk — a wedged control plane must
// not grow this buffer without bound; the finish call reports the session
// outcome regardless.
type execPoster struct {
	ctx context.Context
	c   *client
	id  string
	log *zap.Logger
	mu  sync.Mutex
	buf []byte
}

func (p *execPoster) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	if len(p.buf) >= 16<<10 {
		p.flushLocked()
	}
	return len(b), nil
}

func (p *execPoster) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushLocked()
}

func (p *execPoster) flushLocked() {
	for len(p.buf) > 0 {
		n := min(len(p.buf), 512<<10)
		if err := p.c.execOutput(p.ctx, p.id, p.buf[:n]); err != nil {
			p.log.Warn("exec output post failed; dropping chunk", zap.Error(err))
		}
		p.buf = p.buf[n:]
	}
}
