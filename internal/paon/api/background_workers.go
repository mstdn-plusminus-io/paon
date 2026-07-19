package api

import (
	"context"
	"errors"
	"sync"
)

type BackgroundWorkers struct {
	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

func newBackgroundWorkers() *BackgroundWorkers {
	return &BackgroundWorkers{done: make(chan struct{})}
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
