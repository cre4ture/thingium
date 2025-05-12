// Copyright (C) 2021 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"time"

	"github.com/syncthing/syncthing/lib/logger"
)

type PerformanceStep struct {
	time time.Time
	name string
}

type PerformanceStopWatch struct {
	start   time.Time
	steps   []PerformanceStep
	enabled bool
}

func PerformanceStopWatchStart() *PerformanceStopWatch {
	return &PerformanceStopWatch{
		start:   time.Now(),
		steps:   []PerformanceStep{},
		enabled: true,
	}
}

func PerformanceStopWatchStartDisabled() *PerformanceStopWatch {
	return &PerformanceStopWatch{
		start:   time.Now(),
		steps:   []PerformanceStep{},
		enabled: false,
	}
}

func (sw *PerformanceStopWatch) Step(name string) {
	if !sw.enabled {
		return
	}
	sw.steps = append(sw.steps, PerformanceStep{
		time: time.Now(),
		name: name,
	})
}

func (sw *PerformanceStopWatch) LastStep(groupName, name string) {
	if !sw.enabled {
		return
	}
	sw.Step(name)
	statement := groupName + ": "
	start := sw.start
	for _, step := range sw.steps {
		delta := step.time.Sub(start)
		start = step.time
		statement = statement + step.name + ": " + delta.String() + " "
	}
	logger.DefaultLogger.Debugf("%s", statement)
}
