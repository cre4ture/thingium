// Copyright (C) 2019 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/ek220/guf"
	"github.com/hitoshi44/go-uid64"
	"github.com/syncthing/syncthing/internal/gen/bep"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

type LocalListenerManager struct {
	sharedConfig            *utils.Protected[*tm_config]
	sharedEndpointMgr       *TunnelManagerEndpointManager
	sharedDeviceConnections *TunnelManagerDeviceConnectionsManager
	idGenerator             *uid64.Generator // is thread-safe!
}

func NewLocalListenerManager(
	sharedConfig *utils.Protected[*tm_config],
	sharedEndpointMgr *TunnelManagerEndpointManager,
	sharedDeviceConnections *TunnelManagerDeviceConnectionsManager,
) *LocalListenerManager {
	gen, err := uid64.NewGenerator(0)
	if err != nil {
		panic(err)
	}

	return &LocalListenerManager{
		sharedConfig:            sharedConfig,
		sharedEndpointMgr:       sharedEndpointMgr,
		sharedDeviceConnections: sharedDeviceConnections,
		idGenerator:             gen,
	}
}

func (tm *LocalListenerManager) ServeListener(
	ctx context.Context,
	listenAddress string,
	destinationDevice protocol.DeviceID,
	destinationServiceName string,
	destinationAddress *string,
) error {
	tl.Infoln("ServeListener started for address:", listenAddress, "destination device:", destinationDevice, "destination address:", destinationAddress)
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddress, err)
	}
	defer listener.Close()

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("failed to accept connection: %w", err)
		}
		tl.Debugln("TunnelManager: Accepted connection from:", conn.RemoteAddr())

		go tm.handleAcceptedListenConnection(ctx, conn, destinationDevice, destinationServiceName, destinationAddress)
	}
}

func (tm *LocalListenerManager) handleAcceptedListenConnection(
	ctx context.Context,
	conn net.Conn,
	destinationDevice protocol.DeviceID,
	destinationServiceName string,
	destinationAddress *string,
) {
	defer conn.Close()

	tunnelID := tm.generateTunnelID()
	tl.Debugln("Accepted connection, tunnel ID:", tunnelID)
	tm.sharedEndpointMgr.registerLocalTunnelEndpoint(destinationDevice, tunnelID, conn)

	// send open command to the destination device
	maxRetries := 5
	for try := 0; (try < maxRetries) && ctx.Err() == nil; try++ {
		err := tm.sharedDeviceConnections.TrySendTunnelData(destinationDevice, &protocol.TunnelData{
			D: &bep.TunnelData{
				TunnelId:                 tunnelID,
				Command:                  bep.TunnelCommand_TUNNEL_COMMAND_OPEN,
				RemoteServiceName:        &destinationServiceName,
				TunnelDestinationAddress: destinationAddress,
			},
		})

		if err == nil {
			break
		} else {
			// sleep and retry - device might not yet be connected
			retry := func() bool {
				timer := time.NewTimer(time.Second)
				defer timer.Stop()

				tl.Warnf("Failed to send tunnel data to device %v: %v", destinationDevice, err)
				select {
				case <-ctx.Done():
					tl.Debugln("Context done, stopping listener")
					return false
				case <-timer.C:
					tl.Debugln("Retrying to send tunnel data to device", destinationDevice)
					return true
				}
			}()

			if !retry {
				tl.Debugln("Stopping listener due to context done")
				conn.Close()
				return
			}
		}
	}

	var optionalDestinationAddress string
	if destinationAddress == nil {
		optionalDestinationAddress = "by-remote"
	} else {
		optionalDestinationAddress = *destinationAddress
	}
	tm.sharedEndpointMgr.handleLocalTunnelEndpoint(ctx, tunnelID, conn, destinationDevice, destinationServiceName, optionalDestinationAddress)
}

func (tm *LocalListenerManager) generateTunnelID() uint64 {
	id, err := tm.idGenerator.Gen()
	if err != nil {
		panic(err)
	}
	tl.Debugln("Generated tunnel ID:", id)
	return uint64(id)
}

func (tm *LocalListenerManager) startListeners() {
	tm.sharedConfig.DoProtected(func(config *tm_config) {
		config.serviceRunning = true
		for _, tunnel := range config.configOut {
			tunnelCopy := *tunnel
			go tm.serveOutboundTunnel(&tunnelCopy)
		}
	})
}

func (tm *LocalListenerManager) updateOutConfig(newOutTunnels []*bep.TunnelOutbound) {
	tm.sharedConfig.DoProtected(func(config *tm_config) {

		// Generate a new map of outbound tunnels
		newConfigOut := make(map[string]*tunnelOutConfig)
		for _, tunnel := range newOutTunnels {
			descriptor := getConfigDescriptorOutbound(tunnel)
			if existing, exists := config.configOut[descriptor]; exists {
				// Reuse existing context and cancel function
				newConfigOut[descriptor] = existing
			} else {
				// Create new context and cancel function
				ctx, cancel := context.WithCancel(context.Background())
				newConfigOut[descriptor] = &tunnelOutConfig{
					tunnelBaseConfig: tunnelBaseConfig{
						descriptor: descriptor,
						ctx:        ctx,
						cancel:     cancel,
					},
					json: tunnel,
				}
				if config.serviceRunning {
					tunnelCopy := *newConfigOut[descriptor]
					go tm.serveOutboundTunnel(&tunnelCopy)
				}
			}
		}

		// Cancel and remove tunnels that are no longer in the new configuration
		for descriptor, existing := range config.configOut {
			if _, exists := newConfigOut[descriptor]; !exists {
				existing.cancel()
			}
		}

		// Replace the old configuration with the new one
		config.configOut = newConfigOut
	})
}

func (tm *LocalListenerManager) serveOutboundTunnel(tunnel *tunnelOutConfig) {
	if !guf.DerefOr(tunnel.json.Enabled, true) {
		tl.Debugln("Tunnel is disabled, skipping:", tunnel)
		return
	}

	tl.Debugln("Starting listener for tunnel, device:", tunnel)
	device, err := protocol.DeviceIDFromString(tunnel.json.RemoteDeviceId)
	if err != nil {
		tl.Warnln("failed to parse device ID:", err)
	}
	go func() {
		err := tm.ServeListener(tunnel.ctx, tunnel.json.LocalListenAddress,
			device, tunnel.json.RemoteServiceName, tunnel.json.RemoteAddress)
		if err != nil {
			tl.Warnf("Failed to serve listener for tunnel %s: %v", tunnel.descriptor, err)
		} else {
			tl.Debugln("Listener for tunnel", tunnel.descriptor, "stopped")
		}
	}()
}
