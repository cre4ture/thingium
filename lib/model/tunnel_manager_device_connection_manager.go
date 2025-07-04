// Copyright (C) 2019 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"errors"
	"fmt"

	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

var (
	ErrDeviceNotFound       = errors.New("device not found in TunnelManager")
	ErrTunnelDataSendFailed = errors.New("failed to send tunnel data")
)

type tm_deviceConnections struct {
	deviceConnections map[protocol.DeviceID]*TunnelManagerDeviceConnectionHandler
}

type TunnelManagerDeviceConnectionsManager struct {
	sharedConfig      *utils.Protected[*tm_config]
	sharedEndpointMgr *TunnelManagerEndpointManager
	deviceConnections *utils.Protected[*tm_deviceConnections]
}

func NewTunnelManagerDeviceConnectionsManager(
	sharedConfig *utils.Protected[*tm_config],
	sharedEndpointMgr *TunnelManagerEndpointManager,
) *TunnelManagerDeviceConnectionsManager {
	return &TunnelManagerDeviceConnectionsManager{
		sharedConfig:      sharedConfig,
		sharedEndpointMgr: sharedEndpointMgr,
		deviceConnections: utils.NewProtected(&tm_deviceConnections{
			deviceConnections: make(map[protocol.DeviceID]*TunnelManagerDeviceConnectionHandler),
		}),
	}
}

func (m *TunnelManagerDeviceConnectionsManager) TryGetDeviceChannel(deviceID protocol.DeviceID) chan<- *protocol.TunnelData {
	tunnelOut := utils.DoProtected(m.deviceConnections,
		func(dc *tm_deviceConnections) chan<- *protocol.TunnelData {
			conn, ok := dc.deviceConnections[deviceID]
			if !ok {
				return nil
			}
			return conn.tunnelOut // channels are thread-safe
		})
	return tunnelOut
}

func (m *TunnelManagerDeviceConnectionsManager) TrySendTunnelData(deviceID protocol.DeviceID, data *protocol.TunnelData) error {
	tunnelOut := m.TryGetDeviceChannel(deviceID)
	if tunnelOut == nil {
		return fmt.Errorf("%w: %v", ErrDeviceNotFound, deviceID)
	}

	select {
	case tunnelOut <- data:
		return nil
	default:
		return fmt.Errorf("%w to device %v", ErrTunnelDataSendFailed, deviceID)
	}
}

func (m *TunnelManagerDeviceConnectionsManager) GetCopyOfAllServiceOfferings() map[protocol.DeviceID]map[string]uint32 {
	return utils.DoProtected(m.deviceConnections, func(dc *tm_deviceConnections) map[protocol.DeviceID]map[string]uint32 {
		copied := make(map[protocol.DeviceID]map[string]uint32, len(dc.deviceConnections))
		for deviceID, handler := range dc.deviceConnections {
			copied[deviceID] = handler.GetCopyOfServiceOfferings()
		}
		return copied
	})
}

func (tm *TunnelManagerDeviceConnectionsManager) RegisterDeviceConnection(device protocol.DeviceID, tunnelIn <-chan *protocol.TunnelData, tunnelOut chan<- *protocol.TunnelData) {
	tl.Debugln("Registering device connection, device ID:", device)
	var myConnectionHandler *TunnelManagerDeviceConnectionHandler = nil
	connectionContext, cancel := context.WithCancel(context.Background()) // Create a cancellable context for the connection
	tm.deviceConnections.DoProtected(func(dc *tm_deviceConnections) {
		// Check if the device is already registered
		if old, exists := dc.deviceConnections[device]; exists {
			old.cancel() // Cancel the old context to stop any existing operations
			tl.Debugln("Device connection already exists, replacing it:", device)
		}

		myConnectionHandler = NewTunnelManagerDeviceConnectionHandler(
			cancel,
			device,
			tm.sharedConfig,
			tm.sharedEndpointMgr,
			tunnelOut)
		dc.deviceConnections[device] = myConnectionHandler
	})

	// handle all incoming tunnel data for this device connection
	go func() {
		myConnectionHandler.mainLoop(connectionContext, tunnelIn)
	}()
}

func (tm *TunnelManagerDeviceConnectionsManager) DeregisterDeviceConnection(device protocol.DeviceID) {
	tl.Debugln("Deregistering device connection, device ID:", device)
	tm.deviceConnections.DoProtected(func(dc *tm_deviceConnections) {
		delete(dc.deviceConnections, device)
	})
}
