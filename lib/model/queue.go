// Copyright (C) 2014 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"sort"
	"time"

	"github.com/samber/lo"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/rand"
	"github.com/syncthing/syncthing/lib/sync"
)

type jobQueue struct {
	progress []jobQueueEntry
	queued   []jobQueueEntry
	cond     *sync.TimeoutCond
}

type jobQueueProgressFn func(deltaBytes int64, done bool)

type jobQueueEntry struct {
	name             string
	size             int64
	modified         int64
	progressCallback jobQueueProgressFn
}

func newJobQueue() *jobQueue {
	return &jobQueue{
		progress: make([]jobQueueEntry, 0, 16),
		queued:   make([]jobQueueEntry, 0, 16),
		cond:     sync.NewTimeoutCond(sync.NewMutex()),
	}
}

func (q *jobQueue) PushIfNew(file string, size int64, modified time.Time, fn jobQueueProgressFn) bool {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	for i := range q.queued {
		if q.queued[i].name == file {
			return false
		}
	}
	// The range of UnixNano covers a range of reasonable timestamps.
	q.queued = append(q.queued, jobQueueEntry{file, size, modified.UnixNano(), fn})
	q.cond.Broadcast()
	return true
}

func (q *jobQueue) Push(file string, size int64, modified time.Time) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	// The range of UnixNano covers a range of reasonable timestamps.
	q.queued = append(q.queued, jobQueueEntry{file, size, modified.UnixNano(), func(deltaBytes int64, done bool) {}})
	q.cond.Broadcast()
}

func (q *jobQueue) popIntern() (*jobQueueEntry, error) {
	if q.queued == nil {
		return nil, context.Canceled
	}

	if len(q.queued) == 0 {
		return nil, nil
	}

	f := q.queued[0]
	q.queued = q.queued[1:]
	q.progress = append(q.progress, f)

	return &f, nil
}

func (q *jobQueue) Pop() (jobQueueEntry, bool) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	job, err := q.popIntern()
	if err != nil {
		return jobQueueEntry{}, false
	} else {
		return *job, true
	}
}

func (q *jobQueue) tryPopWithTimeout(duration time.Duration) (*jobQueueEntry, error) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for {
		job, err := q.popIntern()
		if err != nil {
			return nil, err
		}

		if job != nil {
			return job, nil
		}

		waiter := q.cond.SetupWait(duration)
		if waiter.Wait() {
			continue
		} else {
			return nil, nil
		}
	}
}

func (q *jobQueue) BringToFront(filename string) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for i, cur := range q.queued {
		if cur.name == filename {
			if i > 0 {
				// Shift the elements before the selected element one step to
				// the right, overwriting the selected element
				copy(q.queued[1:i+1], q.queued[0:])
				// Put the selected element at the front
				q.queued[0] = cur
			}
			return
		}
	}
}

func (q *jobQueue) Done(file string) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for i := range q.progress {
		toCheck := &q.progress[i]
		if toCheck.name == file {
			toCheck.progressCallback(0, true)
			copy(q.progress[i:], q.progress[i+1:])
			q.progress = q.progress[:len(q.progress)-1]
			return
		}
	}
}

// Jobs returns a paginated list of file currently being pulled and files queued
// to be pulled. It also returns how many items were skipped.
func (q *jobQueue) Jobs(page, perpage int) ([]string, []string, int) {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	toSkip := (page - 1) * perpage
	plen := len(q.progress)
	qlen := len(q.queued)

	if tot := plen + qlen; tot <= toSkip {
		return nil, nil, tot
	}

	if plen >= toSkip+perpage {
		progress := lo.Map(q.progress[:perpage], func(j jobQueueEntry, i int) string { return j.name })
		return progress, nil, toSkip
	}

	var progress []string
	if plen > toSkip {
		progress = lo.Map(q.progress[toSkip:plen], func(j jobQueueEntry, i int) string { return j.name })
		toSkip = 0
	} else {
		toSkip -= plen
	}

	var queued []string
	if qlen-toSkip < perpage-len(progress) {
		queued = make([]string, qlen-toSkip)
	} else {
		queued = make([]string, perpage-len(progress))
	}
	for i := range queued {
		queued[i] = q.queued[i+toSkip].name
	}

	return progress, queued, (page - 1) * perpage
}

func (q *jobQueue) Shuffle() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	rand.Shuffle(q.queued)
}

func (q *jobQueue) Reset() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	q.progress = nil
	q.queued = nil
}

func (q *jobQueue) lenQueued() int {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	return len(q.queued)
}

func (q *jobQueue) lenProgress() int {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()
	return len(q.progress)
}

func (q *jobQueue) Close() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	for _, job := range q.queued {
		job.progressCallback(0, true)
	}
	q.queued = nil
	q.cond.Broadcast()
}

func (q *jobQueue) SortAccordingToConfig(Order config.PullOrder) {
	switch Order {
	case config.PullOrderRandom:
		q.Shuffle()
	case config.PullOrderAlphabetic:
	// The queue is already in alphabetic order.
	case config.PullOrderSmallestFirst:
		q.SortSmallestFirst()
	case config.PullOrderLargestFirst:
		q.SortLargestFirst()
	case config.PullOrderOldestFirst:
		q.SortOldestFirst()
	case config.PullOrderNewestFirst:
		q.SortNewestFirst()
	}
}

func (q *jobQueue) SortSmallestFirst() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	sort.Sort(smallestFirst(q.queued))
}

func (q *jobQueue) SortLargestFirst() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	sort.Sort(sort.Reverse(smallestFirst(q.queued)))
}

func (q *jobQueue) SortOldestFirst() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	sort.Sort(oldestFirst(q.queued))
}

func (q *jobQueue) SortNewestFirst() {
	q.cond.L.Lock()
	defer q.cond.L.Unlock()

	sort.Sort(sort.Reverse(oldestFirst(q.queued)))
}

// The usual sort.Interface boilerplate

type smallestFirst []jobQueueEntry

func (q smallestFirst) Len() int           { return len(q) }
func (q smallestFirst) Less(a, b int) bool { return q[a].size < q[b].size }
func (q smallestFirst) Swap(a, b int)      { q[a], q[b] = q[b], q[a] }

type oldestFirst []jobQueueEntry

func (q oldestFirst) Len() int           { return len(q) }
func (q oldestFirst) Less(a, b int) bool { return q[a].modified < q[b].modified }
func (q oldestFirst) Swap(a, b int)      { q[a], q[b] = q[b], q[a] }
