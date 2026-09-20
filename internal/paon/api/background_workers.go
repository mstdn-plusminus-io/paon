package api

import (
	"context"
	"errors"
	"sync"
)

type BackgroundWorkers struct {
	done      chan struct{}
	ready     chan struct{}
	wg        sync.WaitGroup
	once      sync.Once
	readyOnce sync.Once
	readyMu   sync.Mutex
	readyErr  error
}

func newBackgroundWorkers() *BackgroundWorkers {
	return &BackgroundWorkers{done: make(chan struct{}), ready: make(chan struct{})}
}

func (workers *BackgroundWorkers) markReady(err error) {
	if workers == nil {
		return
	}
	workers.readyOnce.Do(func() {
		workers.readyMu.Lock()
		workers.readyErr = err
		workers.readyMu.Unlock()
		if workers.ready != nil {
			close(workers.ready)
		}
	})
}

// WaitReady waits for the queue consumer to register its handlers and enter
// the running state. A zero-value BackgroundWorkers remains immediately ready
// so callers and compatibility tests that do not own worker startup do not
// deadlock.
func (workers *BackgroundWorkers) WaitReady(ctx context.Context) error {
	if workers == nil || workers.ready == nil {
		return nil
	}
	select {
	case <-workers.ready:
		workers.readyMu.Lock()
		defer workers.readyMu.Unlock()
		return workers.readyErr
	case <-ctx.Done():
		return errors.Join(errors.New("background worker initialization canceled"), ctx.Err())
	}
}

func (workers *BackgroundWorkers) Go(ctx context.Context, runner func(context.Context)) {
	if workers == nil || runner == nil {
		return
	}
	workers.wg.Add(1)
	go func() {
		defer workers.wg.Done()
		runner(ctx)
	}()
}

func (workers *BackgroundWorkers) Seal() {
	if workers == nil {
		return
	}
	workers.once.Do(func() {
		go func() {
			workers.wg.Wait()
			close(workers.done)
		}()
	})
}

func (workers *BackgroundWorkers) Wait(ctx context.Context) error {
	if workers == nil {
		return nil
	}
	workers.Seal()
	select {
	case <-workers.done:
		return nil
	case <-ctx.Done():
		return errors.Join(errors.New("background worker drain timed out"), ctx.Err())
	}
}
