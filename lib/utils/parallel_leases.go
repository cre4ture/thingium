package utils

import (
	"github.com/syncthing/syncthing/lib/logger"
)

type ParallelLeases struct {
	count      uint
	freeLeases chan uint
}

func NewParallelLeases(count uint, id uint /* pure log readability purpose */) *ParallelLeases {
	freeLeases := make(chan uint, count)
	for i := uint(0); i < count; i++ {
		freeLeases <- (id * 10000) + i
	}

	return &ParallelLeases{
		count:      count,
		freeLeases: freeLeases,
	}
}

func (pl *ParallelLeases) AsyncRunOneWithDoneFn(fn func(doneFn func())) {
	// get and potentially wait for free lease
	leaseNR := <-pl.freeLeases
	logger.DefaultLogger.Infof("START with leaseNR: %v", leaseNR)
	go fn(func() {
		// return lease
		pl.freeLeases <- leaseNR
		logger.DefaultLogger.Infof("DONE with leaseNR: %v", leaseNR)
	})
}

func (pl *ParallelLeases) AsyncRunOne(fn func()) {
	pl.AsyncRunOneWithDoneFn(func(doneFn func()) {
		defer doneFn()
		fn()
	})
}

func (pl *ParallelLeases) WaitAllDone() {
	// wait for async operations to complete
	logger.DefaultLogger.Infof("PULL_X: wait for async operations to complete ...")
	for i := uint(0); i < pl.count; i++ {
		<-pl.freeLeases // take await all leases from the free channel
		logger.DefaultLogger.Infof("PULL_X: wait for async operations to complete ... %v/%v", (i + 1), pl.count)
	}
	logger.DefaultLogger.Infof("PULL_X: wait for async operations to complete - DONE")
}
