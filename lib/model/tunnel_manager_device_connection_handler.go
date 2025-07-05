// Copyright (C) 2019 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"net"
	"slices"

	"github.com/ek220/guf"
	"github.com/syncthing/syncthing/internal/gen/bep"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

// only for one device connection.
type TunnelManagerDeviceConnectionHandler struct {
	fromDevice       protocol.DeviceID
	sharedConfig     *utils.Protected[*tm_config]
	sharedEndpoints  *TunnelManagerEndpointManager
	inUseMap         map[uint64]*tm_localTunnelEP
	cancel           context.CancelFunc
	tunnelOut        chan<- *protocol.TunnelData
	serviceOfferings *utils.Protected[map[string]uint32]
}

func NewTunnelManagerDeviceConnectionHandler(
	cancel context.CancelFunc,
	fromDevice protocol.DeviceID,
	sharedConfig *utils.Protected[*tm_config],
	sharedEndpoints *TunnelManagerEndpointManager,
	tunnelOut chan<- *protocol.TunnelData,
) *TunnelManagerDeviceConnectionHandler {
	return &TunnelManagerDeviceConnectionHandler{
		fromDevice:       fromDevice,
		sharedConfig:     sharedConfig,
		sharedEndpoints:  sharedEndpoints,
		inUseMap:         make(map[uint64]*tm_localTunnelEP),
		cancel:           cancel,
		tunnelOut:        tunnelOut,
		serviceOfferings: utils.NewProtected(make(map[string]uint32)),
	}
}

func (tm *TunnelManagerDeviceConnectionHandler) GetCopyOfServiceOfferings() map[string]uint32 {
	return utils.DoProtected(tm.serviceOfferings, func(offerings map[string]uint32) map[string]uint32 {
		copied := make(map[string]uint32, len(offerings))
		for k, v := range offerings {
			copied[k] = v
		}
		return copied
	})
}

func (tm *TunnelManagerDeviceConnectionHandler) mainLoop(ctx context.Context, tunnelIn <-chan *protocol.TunnelData) {
	go tm.sharedConfig.DoProtected(func(config *tm_config) {
		// send tunnel service offerings
		for _, inboundSerice := range config.configIn {
			if slices.Contains(inboundSerice.json.AllowedRemoteDeviceIds, tm.fromDevice.String()) {
				tm.tunnelOut <- generateOfferCommand(inboundSerice.json)
			}
		}
	})

	defer func() {
		for _, ep := range tm.inUseMap {
			ep.inUse.CompareAndSwap(&tm.inUseMap, nil) // Clear the in-use map
		}
	}()

	for {
		select {
		case <-ctx.Done():
			tl.Debugln("Context done for device:", tm.fromDevice)
			return
		case data, ok := <-tunnelIn:
			if !ok {
				tl.Debugln("TunnelIn channel closed for device:", tm.fromDevice)
				return
			}
			tm.forwardRemoteDeviceTunnelData(data)
		}
	}
}

func (tm *TunnelManagerDeviceConnectionHandler) forwardRemoteDeviceTunnelData(data *protocol.TunnelData) {
	tl.Debugln("Forwarding remote tunnel data, from device ID:", tm.fromDevice, "command:", data.D.Command)
	switch data.D.Command {
	case bep.TunnelCommand_TUNNEL_COMMAND_OPEN:
		if data.D.RemoteServiceName == nil {
			tl.Warnf("No remote service name specified")
			return
		}
		service := tm.getEnabledInboundServiceDeviceIdChecked(*data.D.RemoteServiceName)
		if service == nil {
			return
		}
		var TunnelDestinationAddress string
		if service.json.LocalDialAddress == "any" {
			if data.D.TunnelDestinationAddress == nil {
				tl.Warnf("No tunnel destination specified")
				return
			}
			TunnelDestinationAddress = *data.D.TunnelDestinationAddress
		} else {
			TunnelDestinationAddress = service.json.LocalDialAddress
		}

		addr, err := net.ResolveTCPAddr("tcp", TunnelDestinationAddress)
		if err != nil {
			tl.Warnf("Failed to resolve tunnel destination: %v", err)
			return
		}
		conn, err := net.DialTCP("tcp", nil, addr)
		if err != nil {
			tl.Warnf("Failed to dial tunnel destination: %v", err)
			return
		}
		tm.sharedEndpoints.registerLocalTunnelEndpoint(tm.fromDevice, data.D.TunnelId, conn)
		go tm.sharedEndpoints.handleLocalTunnelEndpoint(service.ctx, data.D.TunnelId, conn, tm.fromDevice, *data.D.RemoteServiceName, TunnelDestinationAddress)

	case bep.TunnelCommand_TUNNEL_COMMAND_DATA:
		tcpConn, ok := tm.getAndLockTunnelEP(data.D.TunnelId)
		if ok {
			func() {
				if data.D.DataPackageCounter != nil {
					expectedDataCounter := tcpConn.nextPackageCounter
					if expectedDataCounter != *data.D.DataPackageCounter {
						tl.Warnf("close connection due to data counter missmatch (expected %d, got %d)",
							expectedDataCounter, *data.D.DataPackageCounter)

						// close connection as we have no way to recover from this
						tcpConn.endpoint.Close()
						return
					}
				}

				tcpConn.nextPackageCounter++
				_, err := tcpConn.endpoint.Write(data.D.Data)
				if err != nil {
					tl.Warnf("Failed to forward tunnel data: %v", err)
				}
			}()
		} else {
			tl.Infof("Data: No TCP connection found for device %v, TunnelID: %d", tm.fromDevice, data.D.TunnelId)
		}
	case bep.TunnelCommand_TUNNEL_COMMAND_CLOSE:
		tm.sharedEndpoints.closeLocalTunnelEndpoint(tm.fromDevice, data.D.TunnelId)
	case bep.TunnelCommand_TUNNEL_COMMAND_OFFER:
		tm.serviceOfferings.DoProtected(func(offerings map[string]uint32) {
			offerings[*data.D.RemoteServiceName] = parseUint32Or(guf.DerefOrDefault(data.D.TunnelDestinationAddress), 0)
		})
	default: // unknown command
		tl.Warnf("Unknown tunnel command: %v", data.D.Command)
	}
}

func (tm *TunnelManagerDeviceConnectionHandler) getInboundService(name string) *tunnelInConfig {
	return utils.DoProtected(tm.sharedConfig, func(config *tm_config) *tunnelInConfig {
		for _, service := range config.configIn {
			if service.json.LocalServiceName == name {
				// Explicitly copy the struct (as we leave the mutex protected scope)
				copied := *service
				return &copied
			}
		}
		return nil
	})
}

func (tm *TunnelManagerDeviceConnectionHandler) getEnabledInboundServiceDeviceIdChecked(name string) *tunnelInConfig {
	service := tm.getInboundService(name)
	if service == nil {
		return nil
	}
	if !guf.DerefOr(service.json.Enabled, true) {
		tl.Warnf("Device %v tries to access disabled service %s", tm.fromDevice, name)
		return nil
	}
	for _, device := range service.json.AllowedRemoteDeviceIds {
		deviceID, err := protocol.DeviceIDFromString(device)
		if err != nil {
			tl.Warnln("failed to parse device ID:", err)
			continue
		}
		if tm.fromDevice == deviceID {
			return service
		}
	}
	tl.Warnf("Device %v is not allowed to access service %s", tm.fromDevice, name)
	return nil
}

func (tm *TunnelManagerDeviceConnectionHandler) getAndLockTunnelEP(tunnelId uint64) (*tm_localTunnelEP, bool) {

	// first, check if the inUseMap already has the tunnelId
	if tcpConn, exists := tm.inUseMap[tunnelId]; exists {
		return tcpConn, true // already locked, return it
	}

	ltep := tm.sharedEndpoints.getLocalTunnelEndpoint(tm.fromDevice, tunnelId)
	if ltep == nil {
		tl.Warnf("No local tunnel endpoint found for device %v, TunnelID: %d", tm.fromDevice, tunnelId)
		return nil, false // no endpoint found
	}

	// Check if the connection is in use by another goroutine
	swapped := ltep.inUse.CompareAndSwap(nil, &tm.inUseMap)
	if !swapped {
		tl.Warnf("Connection for device %v, TunnelID: %d is already in use", tm.fromDevice, tunnelId)
		return nil, false // another goroutine is already using this connection
	}

	// If we reach here, we have the connection and can use it
	tm.inUseMap[tunnelId] = ltep // save the locked connection in our inUseMap

	return ltep, true // tm_localTunnelEP is thread-safe
}
