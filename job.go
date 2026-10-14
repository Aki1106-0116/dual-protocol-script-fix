package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var jobSequence uint64

type JobStep struct {
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type JobView struct {
	ID       string    `json:"id"`
	Summary  string    `json:"summary"`
	Steps    []JobStep `json:"steps"`
	Done     bool      `json:"done"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitempty"`
}

type Job struct {
	mu   sync.RWMutex
	view JobView
}

func newJob(summary string, labels []string) *Job {
	raw := make([]byte, 8)
	jobID := hex.EncodeToString(raw)
	if _, err := rand.Read(raw); err != nil {
		jobID = fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&jobSequence, 1))
	} else {
		jobID = hex.EncodeToString(raw)
	}
	job := &Job{view: JobView{ID: jobID, Summary: summary, Started: time.Now()}}
	for _, label := range labels {
		job.view.Steps = append(job.view.Steps, JobStep{Label: label, Status: "pending"})
	}
	return job
}

func (j *Job) ID() string {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.view.ID
}

func (j *Job) Set(index int, status, detail string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if index >= 0 && index < len(j.view.Steps) {
		j.view.Steps[index].Status = status
		j.view.Steps[index].Detail = detail
	}
}

func (j *Job) Finish() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.view.Done = true
	j.view.Finished = time.Now()
}

func (j *Job) View() JobView {
	j.mu.RLock()
	defer j.mu.RUnlock()
	copyView := j.view
	copyView.Steps = append([]JobStep(nil), j.view.Steps...)
	return copyView
}

type JobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

func (s *JobStore) Add(job *Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs == nil {
		s.jobs = map[string]*Job{}
	}
	for id, old := range s.jobs {
		v := old.View()
		if v.Done && time.Since(v.Finished) > time.Hour {
			delete(s.jobs, id)
		}
	}
	if len(s.jobs) >= 100 {
		var oldest string
		var started time.Time
		for id, old := range s.jobs {
			v := old.View()
			if v.Done && (oldest == "" || v.Started.Before(started)) {
				oldest = id
				started = v.Started
			}
		}
		if oldest != "" {
			delete(s.jobs, oldest)
		}
	}
	s.jobs[job.ID()] = job
}

func (s *JobStore) Views() []JobView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]JobView, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, job.View())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Done != out[j].Done {
			return !out[i].Done
		}
		return out[i].Started.After(out[j].Started)
	})
	return out
}

func (s *JobStore) Dismiss(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.jobs, id)
}
