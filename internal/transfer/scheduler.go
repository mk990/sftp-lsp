package transfer

import (
	"context"
	"sync"
)

// Task is a unit of work submitted to the Scheduler.
type Task func(ctx context.Context) error

// Scheduler runs tasks with bounded concurrency.
type Scheduler struct {
	sem chan struct{}
}

func NewScheduler(concurrency int) *Scheduler {
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Scheduler{sem: make(chan struct{}, concurrency)}
}

// RunAll executes all tasks concurrently up to the concurrency limit.
// It returns the first error encountered, but waits for all started tasks to finish.
func (s *Scheduler) RunAll(ctx context.Context, tasks []Task) error {
	var (
		wg      sync.WaitGroup
		once    sync.Once
		firstErr error
	)

	for _, t := range tasks {
		t := t
		select {
		case <-ctx.Done():
			once.Do(func() { firstErr = ctx.Err() })
			break
		case s.sem <- struct{}{}:
		}

		wg.Add(1)
		go func() {
			defer func() {
				<-s.sem
				wg.Done()
			}()
			if err := t(ctx); err != nil {
				once.Do(func() { firstErr = err })
			}
		}()
	}

	wg.Wait()
	return firstErr
}
