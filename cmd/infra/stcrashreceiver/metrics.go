// Copyright (C) 2023 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	metricCrashReportsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "crash_reports_total",
		Help:      "Total number of crash reports handled by the crash receiver.",
	}, []string{"result"})
	metricFailureReportsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "failure_reports_total",
		Help:      "Total number of failure reports handled by the crash receiver.",
	}, []string{"result"})
	metricDiskstoreFilesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "diskstore_files_total",
		Help:      "Current number of crash report files stored on disk.",
	})
	metricDiskstoreBytesTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "diskstore_bytes_total",
		Help:      "Current total size in bytes of crash reports stored on disk.",
	})
	metricDiskstoreOldestAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "diskstore_oldest_age_seconds",
		Help:      "Age in seconds of the oldest crash report currently stored on disk.",
	})
	metricSentryReportsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "sentry_reports_total",
		Help:      "Total number of crash reports queued for Sentry by result.",
	}, []string{"result"})
	metricIgnoreMatchesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "ignore_matches_total",
		Help:      "Total number of reports ignored due to a configured ignore pattern.",
	}, []string{"pattern"})
	metricSourceCodeLoadsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "source_code_loads_total",
		Help:      "Total number of source code loading attempts by result.",
	}, []string{"result"})
	metricSourceCodeCacheSize = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "syncthing",
		Subsystem: "crashreceiver",
		Name:      "source_code_cache_size",
		Help:      "Current number of source code entries held in the cache.",
	})
)
