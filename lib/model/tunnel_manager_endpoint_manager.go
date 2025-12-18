// Copyright (C) 2019 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"fmt"
	"io"
	"time"
	"weak"

	"github.com/syncthing/syncthing/internal/gen/bep"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

type tm_localTunnelEPs struct {
	localTunnelEndpoints map[protocol.DeviceID]map[uint64]*tm_localTunnelEP
}

type TunnelManagerEndpointManager struct {
	weakSharedDeviceConnections *utils.Protected[*weak.Pointer[TunnelManagerDeviceConnectionsManager]]
	localTunnelEndpoints        *utils.Protected[*tm_localTunnelEPs]
}

func NewTunnelManagerEndpointManager() *TunnelManagerEndpointManager {
	return &TunnelManagerEndpointManager{
		weakSharedDeviceConnections: utils.NewProtected(&weak.Pointer[TunnelManagerDeviceConnectionsManager]{}),
		localTunnelEndpoints: utils.NewProtected(&tm_localTunnelEPs{
			localTunnelEndpoints: make(map[protocol.DeviceID]map[uint64]*tm_localTunnelEP),
		}),
	}
}

func (tm *TunnelManagerEndpointManager) SetSharedDeviceConnections(sharedDeviceConnections weak.Pointer[TunnelManagerDeviceConnectionsManager]) {
	tm.weakSharedDeviceConnections.DoProtected(func(dc *weak.Pointer[TunnelManagerDeviceConnectionsManager]) {
		*dc = sharedDeviceConnections
	})
}

func (tm *TunnelManagerEndpointManager) registerLocalTunnelEndpoint(deviceID protocol.DeviceID, tunnelID uint64, conn io.WriteCloser) {
	tl.Debugln("Registering local tunnel endpoint, device ID:", deviceID, "tunnel ID:", tunnelID)
	tm.localTunnelEndpoints.DoProtected(func(eps *tm_localTunnelEPs) {
		if eps.localTunnelEndpoints[deviceID] == nil {
			eps.localTunnelEndpoints[deviceID] = make(map[uint64]*tm_localTunnelEP)
		}
		eps.localTunnelEndpoints[deviceID][tunnelID] = &tm_localTunnelEP{
			endpoint:           conn,
			nextPackageCounter: 0,
		}
	})
}

func (tm *TunnelManagerEndpointManager) deregisterLocalTunnelEndpoint(deviceID protocol.DeviceID, tunnelID uint64) {
	tl.Debugln("Deregistering local tunnel endpoint, device ID:", deviceID, "tunnel ID:", tunnelID)
	tm.localTunnelEndpoints.DoProtected(func(eps *tm_localTunnelEPs) {
		delete(eps.localTunnelEndpoints[deviceID], tunnelID)
		if len(eps.localTunnelEndpoints[deviceID]) == 0 {
			delete(eps.localTunnelEndpoints, deviceID) // Ensure map cleanup
		}
	})
}

func (tm *TunnelManagerEndpointManager) closeLocalTunnelEndpoint(deviceID protocol.DeviceID, tunnelID uint64) {
	tl.Debugln("Closing local tunnel endpoint, device ID:", deviceID, "tunnel ID:", tunnelID)
	tcpConn, ok := utils.DoProtected2(tm.localTunnelEndpoints, func(eps *tm_localTunnelEPs) (*tm_localTunnelEP, bool) {
		tcpConn, ok := eps.localTunnelEndpoints[deviceID][tunnelID]
		return tcpConn, ok // tm_localTunnelEP is thread-safe
	})
	if ok {
		tcpConn.endpoint.Close()
	} else {
		tl.Debugf("Close: No TCP connection found for device %v, TunnelID: %d", deviceID, tunnelID)
	}
}

func (tm *TunnelManagerEndpointManager) getLocalTunnelEndpoint(deviceID protocol.DeviceID, tunnelID uint64) *tm_localTunnelEP {
	return utils.DoProtected(tm.localTunnelEndpoints, func(eps *tm_localTunnelEPs) *tm_localTunnelEP {
		tcpConn, ok := eps.localTunnelEndpoints[deviceID][tunnelID]
		if !ok || tcpConn == nil {
			return nil
		}
		return tcpConn // tm_localTunnelEP is thread-safe
	})
}

func (tm *TunnelManagerEndpointManager) tryGetSharedDeviceConnections() *TunnelManagerDeviceConnectionsManager {
	return utils.DoProtected(tm.weakSharedDeviceConnections,
		func(weak *weak.Pointer[TunnelManagerDeviceConnectionsManager]) *TunnelManagerDeviceConnectionsManager {
			return weak.Value()
		})
}

type tmepm_OptimizedTrySendTunnelData struct {
	destinationDevice         protocol.DeviceID
	destinationDeviceTunnel   chan<- *protocol.TunnelData
	destinationDeviceTunnelId string
}

func NewConnectedTmepmOptimizedTrySendTunnelData(destinationDevice protocol.DeviceID, tm *TunnelManagerEndpointManager) *tmepm_OptimizedTrySendTunnelData {
	instance := NewUnconnectedTmepmOptimizedTrySendTunnelData(destinationDevice)
	instance.tryGetNewDeviceConnection(tm)
	if instance.destinationDeviceTunnel == nil {
		tl.Warnf("No tunnel channel found for device %v", destinationDevice)
		return nil
	}

	return instance
}

func NewUnconnectedTmepmOptimizedTrySendTunnelData(destinationDevice protocol.DeviceID) *tmepm_OptimizedTrySendTunnelData {
	return &tmepm_OptimizedTrySendTunnelData{
		destinationDevice:         destinationDevice,
		destinationDeviceTunnel:   nil,
		destinationDeviceTunnelId: "",
	}
}

func (o *tmepm_OptimizedTrySendTunnelData) tryGetNewDeviceConnection(
	tm *TunnelManagerEndpointManager,
) error {
	sharedDeviceConnections := tm.tryGetSharedDeviceConnections()
	if sharedDeviceConnections == nil {
		tl.Warnf("Shutdown phase, cannot re-get device channel")
		return fmt.Errorf("shutdown phase, cannot re-get device channel")
	}

	newDestinationDeviceTunnel, newConnId := sharedDeviceConnections.TryGetDeviceChannel(
		o.destinationDevice, o.destinationDeviceTunnelId)
	if newDestinationDeviceTunnel == nil {
		tl.Warnf("No (new) tunnel channel found for device %v, retry with old ...", o.destinationDevice)
	} else {
		o.destinationDeviceTunnelId = newConnId
		o.destinationDeviceTunnel = newDestinationDeviceTunnel
		tl.Infoln("Re-trying to send data to device", o.destinationDevice, " with new connection ID:", newConnId)
	}

	return nil
}

func (o *tmepm_OptimizedTrySendTunnelData) TrySendTunnelDataWithRetries(
	data *protocol.TunnelData,
	maxRetries uint,
	tm *TunnelManagerEndpointManager,
) error {
	var retryCount uint = 0
	for ; retryCount <= maxRetries; retryCount++ {
		if (o.destinationDeviceTunnel == nil) || (retryCount > 0) {
			err := o.tryGetNewDeviceConnection(tm)
			if err != nil {
				tl.Warnf("Shutdown phase, cannot re-get device channel")
				return err
			}
		}
		if o.destinationDeviceTunnel == nil {
			tl.Warnf("No tunnel channel found for device %v, cannot send data, retrying ...", o.destinationDevice)
			time.Sleep(1 * time.Second)
			continue // skip sending, try to get a new connection
		}

		select {
		case o.destinationDeviceTunnel <- data:
			// sent successfully
			return nil
		case <-time.After(1 * time.Second):
			// timeout, try to get a new connection
		}
	}
	return fmt.Errorf("failed to send data to device %v after %d retries", o.destinationDevice, retryCount)
}

func (tm *TunnelManagerEndpointManager) handleLocalTunnelEndpoint(
	ctx context.Context,
	tunnelID uint64,
	conn io.ReadWriteCloser,
	destinationDevice protocol.DeviceID,
	destinationServiceName string,
	destinationAddress string,
) {
	tl.Debugln("TunnelManager: Handling local tunnel endpoint, tunnel ID:", tunnelID,
		"destination device:", destinationDevice,
		"destination service name:", destinationServiceName,
		"destination address:", destinationAddress)

	defer tm.deregisterLocalTunnelEndpoint(destinationDevice, tunnelID)
	defer func() {
		// send close command to the destination device
		sharedDeviceConnections := tm.tryGetSharedDeviceConnections()
		if sharedDeviceConnections == nil {
			// in shutdown phase, sharedDeviceConnections might be nil
			return
		}

		_ = sharedDeviceConnections.TrySendTunnelData(destinationDevice, &protocol.TunnelData{
			D: &bep.TunnelData{
				TunnelId: tunnelID,
				Command:  bep.TunnelCommand_TUNNEL_COMMAND_CLOSE,
			},
		})
		tl.Debugln("Closed local tunnel endpoint, tunnel ID:", tunnelID)
	}()

	stop := context.AfterFunc(ctx, func() {
		tl.Debugln("Stopping local tunnel endpoint, tunnel ID:", tunnelID)
		conn.Close()
	})

	defer func() {
		stop()
	}()

	robustSender := NewConnectedTmepmOptimizedTrySendTunnelData(destinationDevice, tm)
	if robustSender == nil {
		tl.Warnf("No tunnel channel found for device %v, cannot handle local tunnel endpoint",
			destinationDevice)
		return
	}

	tl.Infoln("Starting to forward data for local tunnel endpoint, tunnel ID:", tunnelID,
		"to device:", destinationDevice,
		"address:", destinationAddress)

	// Example: Forward data to the destination address
	// This is a placeholder implementation
	var dataPackageCounter uint32 = 0
	for {
		select {
		case <-ctx.Done():
			tl.Debugln("Context done for tunnel ID:", tunnelID)
			return
		default:
			// Read data from the connection and forward it
			buffer := make([]byte, 1024*4)
			n, err := conn.Read(buffer)
			if err != nil {
				tl.Debugf("Error reading from connection: %v", err)
				return
			}
			// manage package counter
			thisPackageCounter := dataPackageCounter
			dataPackageCounter++

			// Forward data to the destination
			// This is a placeholder implementation
			tl.Debugf("Forwarding data to device %v, %s (%d tunnel id): len: %d, counter: %d\n",
				destinationDevice, destinationAddress, tunnelID, n, thisPackageCounter)

			// Send the data to the destination device, handle if channel is closed
			err = robustSender.TrySendTunnelDataWithRetries(
				&protocol.TunnelData{
					D: &bep.TunnelData{
						TunnelId:           tunnelID,
						Command:            bep.TunnelCommand_TUNNEL_COMMAND_DATA,
						Data:               buffer[:n],
						DataPackageCounter: &thisPackageCounter,
					},
				}, 5 /* retries */, tm)
			if err != nil {
				tl.Warnf("Failed to send tunnel data to device %v after retries: %v", destinationDevice, err)
				return
			}
		}
	}
}
