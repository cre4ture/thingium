package blockstorage

import (
	"context"
	"io"
	"sync"
	"time"
)

type AsyncCheckedDeleteService struct {
	io.Closer
	ctx            context.Context
	cancel         context.CancelFunc
	hbs            HashBlockStorageI
	serviceDone    sync.WaitGroup
	todoList       chan []byte // hashes
	pendingDeletes chan PendingDelete
}

type PendingDelete struct {
	time time.Time
	hash []byte
}

const MAX_PENDING_DELETES = 10        // this needs to be limited to keep timing constrains of pending deletes
const TIME_CONSTANT = time.Minute / 3 // grace period + deletion window + another grace period

func NewAsyncCheckedDeleteService(ctx context.Context, hbs HashBlockStorageI) *AsyncCheckedDeleteService {
	myCtx, cancel := context.WithCancel(ctx)
	instance := &AsyncCheckedDeleteService{
		ctx:            myCtx,
		cancel:         cancel,
		hbs:            hbs,
		serviceDone:    sync.WaitGroup{},
		todoList:       make(chan []byte, 10000),
		pendingDeletes: make(chan PendingDelete, MAX_PENDING_DELETES),
	}

	instance.serviceDone.Add(1)
	go instance.serveTodoList()
	instance.serviceDone.Add(1)
	go instance.servePendingList()

	return instance
}

func (ds *AsyncCheckedDeleteService) Close() error {
	ds.cancel()
	ds.serviceDone.Wait()
	return nil
}

func (ds *AsyncCheckedDeleteService) serveTodoList() {
	defer ds.serviceDone.Done()

	for {
		select {
		case <-ds.ctx.Done():
			return
		case currentJob := <-ds.todoList:
			// first check again, as the job might have been waiting in TODO list for a while
			currentState := ds.hbs.GetBlockHashState(currentJob)
			if currentState != HBS_AVAILABLE_FREE {
				continue
			}

			ds.hbs.AnnounceDelete(currentJob)
			ds.pendingDeletes <- PendingDelete{
				time: time.Now().Add(TIME_CONSTANT),
				hash: currentJob,
			}
		}
	}
}

func (ds *AsyncCheckedDeleteService) servePendingList() {
	defer ds.serviceDone.Done()

	for {
		select {
		case <-ds.ctx.Done():
			// abort pending operations:
			for currentJob := range ds.pendingDeletes {
				// clear all pending delete markers immediately
				ds.hbs.DeAnnounceDelete(currentJob.hash)
			}
			return
		case currentJob := <-ds.pendingDeletes:
			func() {
				defer ds.hbs.DeAnnounceDelete(currentJob.hash)

				timeTillDue := time.Until(currentJob.time)
				time.Sleep(timeTillDue)

				// check again, abort if state changed, delete when state same and we are still in time
				currentState := ds.hbs.GetBlockHashState(currentJob.hash)
				if currentState != HBS_AVAILABLE_FREE {
					// if state changed in grace period, this needs to be accepted
					return
				}

				timeSinceDue := time.Since(currentJob.time)
				if timeSinceDue >= TIME_CONSTANT {
					// if we where delayed by whatever being busy, we can't guarantee safe deletion
					return
				}

				// we are still in time window for deletion, no state changed, finally delete
				ds.hbs.UncheckedDelete(currentJob.hash)
			}()
		}
	}
}
