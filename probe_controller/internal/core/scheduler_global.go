package core

import (
	"context"
	"log"
	"sync"
	"time"
)

type controllerScheduledJob struct {
	key     string
	runAt   time.Time
	timeout time.Duration
	fn      func(context.Context)
}

type controllerSchedulerEngine struct {
	mu      sync.Mutex
	jobs    map[string]controllerScheduledJob
	wakeCh  chan struct{}
	started bool
}

var controllerScheduler = &controllerSchedulerEngine{
	jobs:   map[string]controllerScheduledJob{},
	wakeCh: make(chan struct{}, 1),
}

func initControllerScheduler() {
	controllerScheduler.mu.Lock()
	if controllerScheduler.started {
		controllerScheduler.mu.Unlock()
		return
	}
	controllerScheduler.started = true
	controllerScheduler.mu.Unlock()

	go controllerScheduler.loop()
	log.Println("controller scheduler started")
}

func scheduleGlobalTask(key string, runAt time.Time, timeout time.Duration, fn func(context.Context)) {
	normalizedKey := key
	if normalizedKey == "" || fn == nil || runAt.IsZero() {
		return
	}

	controllerScheduler.mu.Lock()
	controllerScheduler.jobs[normalizedKey] = controllerScheduledJob{
		key:     normalizedKey,
		runAt:   runAt,
		timeout: timeout,
		fn:      fn,
	}
	controllerScheduler.mu.Unlock()
	controllerScheduler.wake()
}

func cancelGlobalTask(key string) {
	if key == "" {
		return
	}
	controllerScheduler.mu.Lock()
	delete(controllerScheduler.jobs, key)
	controllerScheduler.mu.Unlock()
	controllerScheduler.wake()
}

func (s *controllerSchedulerEngine) wake() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

func (s *controllerSchedulerEngine) loop() {
	for {
		dueJobs, waitFor := s.collectDueJobs(time.Now())
		for _, job := range dueJobs {
			go runGlobalScheduledJob(job)
		}

		timer := time.NewTimer(waitFor)
		select {
		case <-s.wakeCh:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (s *controllerSchedulerEngine) collectDueJobs(now time.Time) ([]controllerScheduledJob, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	due := make([]controllerScheduledJob, 0, 8)
	nextWait := 5 * time.Second
	hasFuture := false

	for key, job := range s.jobs {
		if !job.runAt.After(now) {
			due = append(due, job)
			delete(s.jobs, key)
			continue
		}

		wait := job.runAt.Sub(now)
		if !hasFuture || wait < nextWait {
			nextWait = wait
			hasFuture = true
		}
	}

	if nextWait < 100*time.Millisecond {
		nextWait = 100 * time.Millisecond
	}
	if nextWait > 5*time.Second {
		nextWait = 5 * time.Second
	}
	return due, nextWait
}

func runGlobalScheduledJob(job controllerScheduledJob) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("global scheduler panic in job %s: %v", job.key, recovered)
		}
	}()

	ctx := context.Background()
	cancel := func() {}
	if job.timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), job.timeout)
	}
	defer cancel()

	job.fn(ctx)
}
