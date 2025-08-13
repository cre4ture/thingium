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
	"time"

	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

var (
	ErrDeviceNotFound       = errors.New("device not found in TunnelManager")
	ErrTunnelDataSendFailed = errors.New("failed to send tunnel data")
)

type TunnelManagerDeviceConnection struct {
	DeviceID     protocol.DeviceID
	ConnectionID string
	TunnelIn     <-chan *protocol.TunnelData
	TunnelOut    chan<- *protocol.TunnelData
}

type tm_deviceConnections struct {
	// multiple connections are possible for the same device ID
	deviceConnections map[protocol.DeviceID]map[string]*TunnelManagerDeviceConnectionHandler
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
			deviceConnections: make(map[protocol.DeviceID]map[string]*TunnelManagerDeviceConnectionHandler),
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

			var connections = len(conn)
			if connections == 0 {
				return nil
			}

			// use last established connection
			connId := ""
			connTime := time.Time{}
			for k := range conn {
				i := conn[k]
				if i.connectionTime.After(connTime) {
					connId = k
					connTime = i.connectionTime
				}
			}

			return conn[connId].tunnelOut
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
		for deviceID, handlers := range dc.deviceConnections {
			for _, handler := range handlers {
				if _, exists := copied[deviceID]; !exists {
					copied[deviceID] = make(map[string]uint32)
				}

				// merge service offerings
				for service, port := range handler.GetCopyOfServiceOfferings() {
					copied[deviceID][service] = port
				}
			}
		}
		return copied
	})
}

func (tm *TunnelManagerDeviceConnectionsManager) RegisterDeviceConnection(
	conn *TunnelManagerDeviceConnection,
) {
	tl.Debugln("Registering device connection, device ID:", conn.DeviceID, "connection ID:", conn.ConnectionID)
	var myConnectionHandler *TunnelManagerDeviceConnectionHandler = nil
	connectionContext, cancel := context.WithCancel(context.Background()) // Create a cancellable context for the connection
	tm.deviceConnections.DoProtected(func(dc *tm_deviceConnections) {
		// Check if the device is already registered
		if _, exists := dc.deviceConnections[conn.DeviceID]; !exists {
			dc.deviceConnections[conn.DeviceID] = make(map[string]*TunnelManagerDeviceConnectionHandler)
		}

		myConnectionHandler = NewTunnelManagerDeviceConnectionHandler(
			cancel,
			conn.DeviceID,
			tm.sharedConfig,
			tm.sharedEndpointMgr,
			conn.TunnelOut)

		dc.deviceConnections[conn.DeviceID][conn.ConnectionID] = myConnectionHandler
	})

	// handle all incoming tunnel data for this device connection
	go func() {
		myConnectionHandler.mainLoop(connectionContext, conn.TunnelIn)
	}()
}

func (tm *TunnelManagerDeviceConnectionsManager) DeregisterDeviceConnection(
	device protocol.DeviceID, connectionID string,
) {
	tl.Debugln("Deregistering device connection, device ID:", device)
	tm.deviceConnections.DoProtected(func(dc *tm_deviceConnections) {
		if connectionMap, ok := dc.deviceConnections[device]; ok {
			delete(connectionMap, connectionID)
		}
	})
}
