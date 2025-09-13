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

func (m *TunnelManagerDeviceConnectionsManager) TryGetDeviceChannel(
	deviceID protocol.DeviceID, ignoreTunnelId string,
) (chan<- *protocol.TunnelData, string) {
	tunnelOut, connId := utils.DoProtected2(m.deviceConnections,
		func(dc *tm_deviceConnections) (chan<- *protocol.TunnelData, string) {
			conn, ok := dc.deviceConnections[deviceID]
			if !ok {
				return nil, ""
			}

			if len(conn) == 0 {
				return nil, ""
			}

			// use last established connection
			connId := ""
			connTime := time.Time{}
			for k := range conn {
				i := conn[k]
				if (i.connectionTime.After(connTime)) && (k != ignoreTunnelId) {
					connId = k
					connTime = i.connectionTime
				}
			}

			if connId == "" {
				return nil, ""
			}

			return conn[connId].tunnelOut, connId
		})
	return tunnelOut, connId
}

func (m *TunnelManagerDeviceConnectionsManager) TrySendTunnelDataWithRetries(
	ctx context.Context,
	deviceID protocol.DeviceID,
	data *protocol.TunnelData,
	maxRetries uint,
) error {

	var err error
	var try uint
	for try = 0; (try < maxRetries) && ctx.Err() == nil; try++ {
		err = m.TrySendTunnelData(deviceID, data)
		if err == nil {
			return nil
		} else {
			// sleep and retry - device might not yet be connected
			retry := func() bool {
				timer := time.NewTimer(time.Second)
				defer timer.Stop()

				tl.Warnf("Failed to send tunnel data to device %v: %v", deviceID, err)
				select {
				case <-ctx.Done():
					tl.Debugln("Context done, stopping listener")
					return false
				case <-timer.C:
					tl.Debugln("Retrying to send tunnel data to device", deviceID)
					return true
				}
			}()

			if !retry {
				return context.Canceled
			}
		}
	}

	return err
}

func (m *TunnelManagerDeviceConnectionsManager) TrySendTunnelData(deviceID protocol.DeviceID, data *protocol.TunnelData) error {
	tunnelOut, _ := m.TryGetDeviceChannel(deviceID, "")
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

func (m *TunnelManagerDeviceConnectionsManager) GetCopyOfAllServiceOfferings() map[protocol.DeviceID]map[string]offeringData {
	return utils.DoProtected(m.deviceConnections, func(dc *tm_deviceConnections) map[protocol.DeviceID]map[string]offeringData {
		copied := make(map[protocol.DeviceID]map[string]offeringData, len(dc.deviceConnections))
		for deviceID, handlers := range dc.deviceConnections {
			for _, handler := range handlers {
				if _, exists := copied[deviceID]; !exists {
					copied[deviceID] = make(map[string]offeringData)
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
