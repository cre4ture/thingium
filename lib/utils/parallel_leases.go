package utils

import (
	"github.com/syncthing/syncthing/lib/logger"
)

type ParallelLeases struct {
	jobGroupName string
	count        uint
	freeLeases   chan uint
}

func NewParallelLeases(count uint, jobGroupName string) *ParallelLeases {
	freeLeases := make(chan uint, count)
	for i := uint(0); i < count; i++ {
		freeLeases <- i
	}

	return &ParallelLeases{
		jobGroupName: jobGroupName,
		count:        count,
		freeLeases:   freeLeases,
	}
}

func (pl *ParallelLeases) AsyncRunOneWithDoneFn(name string, fn func(doneFn func())) {
	// get and potentially wait for free lease
	leaseNR := <-pl.freeLeases
	logger.DefaultLogger.Infof("START[%v, %v] with leaseNR: %v", pl.jobGroupName, name, leaseNR)
	go fn(func() {
		// return lease
		pl.freeLeases <- leaseNR
		logger.DefaultLogger.Infof("DONE[%v, %v] with leaseNR: %v", pl.jobGroupName, name, leaseNR)
	})
}

func (pl *ParallelLeases) AsyncRunOne(name string, fn func()) {
	pl.AsyncRunOneWithDoneFn(name, func(doneFn func()) {
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
