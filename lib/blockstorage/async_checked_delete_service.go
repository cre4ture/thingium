package blockstorage

import (
	"context"
	"sync"
	"time"

	"github.com/syncthing/syncthing/lib/logger"
)

type AsyncCheckedDeleteService struct {
	// io.Closer
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

const MAX_PENDING_DELETES = 30        // this needs to be limited to keep timing constrains of pending deletes
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
	go func() {
		defer logger.DefaultLogger.Infof("AsyncCheckedDeleteService serveTodoList DONE")
		defer instance.serviceDone.Done()
		instance.serveTodoList()
	}()
	instance.serviceDone.Add(1)
	go func() {
		defer logger.DefaultLogger.Infof("AsyncCheckedDeleteService servePendingList DONE")
		defer instance.serviceDone.Done()
		instance.servePendingList()
	}()

	return instance
}

func (ds *AsyncCheckedDeleteService) Close() error {
	logger.DefaultLogger.Infof("AsyncCheckedDeleteService Close A")
	ds.cancel()
	logger.DefaultLogger.Infof("AsyncCheckedDeleteService Close B")
	ds.serviceDone.Wait()
	logger.DefaultLogger.Infof("AsyncCheckedDeleteService Close C")
	return nil
}

func (ds *AsyncCheckedDeleteService) RequestCheckedDelete(hash []byte) {
	ds.todoList <- hash
}

func (ds *AsyncCheckedDeleteService) serveTodoList() {
	for {
		select {
		case <-ds.ctx.Done():
			return
		case currentJob := <-ds.todoList:
			// first check again, as the job might have been waiting in TODO list for a while
			currentState := ds.hbs.GetBlockHashState(currentJob)
			if currentState.IsReservedBySomeone() {
				// logger.DefaultLogger.Infof("skip delete as still reserved: %v, state: %v",
				// 	hashutil.HashToStringMapKey(currentJob), currentState)
				continue
			}

			ds.hbs.AnnounceDelete(currentJob)
			pendingDeleteJob := PendingDelete{
				time: time.Now().Add(TIME_CONSTANT),
				hash: currentJob,
			}
			select {
			case <-ds.ctx.Done():
				ds.hbs.DeAnnounceDelete(currentJob)
				return
			case ds.pendingDeletes <- pendingDeleteJob:
			}
			// logger.DefaultLogger.Infof("announced delete for: %v", hashutil.HashToStringMapKey(currentJob))
		}
	}
}

func abortableTimeSleep(ctx context.Context, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return context.Canceled
	case <-time.After(duration):
		return nil
	}
}

func (ds *AsyncCheckedDeleteService) servePendingList() {
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
				err := abortableTimeSleep(ds.ctx, timeTillDue)
				if err != nil {
					return
				}

				// check again, abort if state changed, delete when state same and we are still in time
				currentState := ds.hbs.GetBlockHashState(currentJob.hash)
				if currentState.IsReservedBySomeone() {
					// if state changed in grace period, this needs to be accepted
					// logger.DefaultLogger.Infof("abort delete due to new reservation for: %v, state: %v",
					//	hashutil.HashToStringMapKey(currentJob.hash), currentState)
					return
				}

				timeSinceDue := time.Since(currentJob.time)
				if timeSinceDue >= TIME_CONSTANT {
					// if we where delayed by whatever being busy, we can't guarantee safe deletion
					return
				}

				// we are still in time window for deletion, no state changed, finally delete
				ds.hbs.UncheckedDelete(currentJob.hash)
				// logger.DefaultLogger.Infof("delete done for: %v",
				// 	hashutil.HashToStringMapKey(currentJob.hash))
			}()
		}
	}
}
