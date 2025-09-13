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

func (tm *LocalListenerManager) ServeListenerTCP(
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

type llm_udpConnectionAdapter struct {
	conn net.PacketConn
	addr net.Addr
}

// write
func (a *llm_udpConnectionAdapter) Write(p []byte) (n int, err error) {
	return a.conn.WriteTo(p, a.addr)
}

// close
func (a *llm_udpConnectionAdapter) Close() error {
	return nil // no-op
}

func (tm *LocalListenerManager) ServeListenerUDP(
	ctx context.Context,
	listenAddress string,
	destinationDevice protocol.DeviceID,
	destinationServiceName string,
	destinationAddress *string,
) error {
	tl.Infoln("ServeListenerUDP started for address:", listenAddress, "destination device:", destinationDevice, "destination address:", destinationAddress)
	conn, err := net.ListenPacket("udp", listenAddress)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddress, err)
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	protocolUDP := bep.TunnelProtocol_TUNNEL_PROTOCOL_UDP

	robustSender := NewUnconnectedTmepmOptimizedTrySendTunnelData(destinationDevice)

	sourceAddr2TunnelId := make(map[string]uint64)
	buf := make([]byte, 1024*100)
	for {
		n, senderAddr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("failed to read from UDP connection: %w", err)
		}
		tl.Infoln("TunnelManager: Accepted UDP packet from:", senderAddr, "len:", n)

		// remember source address string for this addr
		tunnelID, exists := sourceAddr2TunnelId[senderAddr.String()]
		if !exists {
			tunnelID = tm.generateTunnelID()
			tl.Infoln("Accepted UDP connection, tunnel ID:", tunnelID)

			err := tm.sharedDeviceConnections.TrySendTunnelDataWithRetries(ctx, destinationDevice,
				&protocol.TunnelData{
					D: &bep.TunnelData{
						TunnelId:                 tunnelID,
						Command:                  bep.TunnelCommand_TUNNEL_COMMAND_OPEN,
						RemoteServiceName:        &destinationServiceName,
						TunnelDestinationAddress: destinationAddress,
						Protocol:                 &protocolUDP,
					},
				}, 5 /* retries */)

			if err != nil {
				tl.Warnln("Failure to send open command - forget this UDP endpoint")
				continue
			}

			sourceAddr2TunnelId[senderAddr.String()] = tunnelID
			tm.sharedEndpointMgr.registerLocalTunnelEndpoint(destinationDevice, tunnelID, &llm_udpConnectionAdapter{
				conn: conn,
				addr: senderAddr,
			})
		}

		robustSender.TrySendTunnelDataWithRetries(&protocol.TunnelData{
			D: &bep.TunnelData{
				TunnelId: tunnelID,
				Command:  bep.TunnelCommand_TUNNEL_COMMAND_DATA,
				Data:     buf[:n],
			},
		}, 5 /* retries */, tm.sharedEndpointMgr)
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
	tl.Debugln("Accepted tcp connection, tunnel ID:", tunnelID)
	tm.sharedEndpointMgr.registerLocalTunnelEndpoint(destinationDevice, tunnelID, conn)

	protocolTCP := bep.TunnelProtocol_TUNNEL_PROTOCOL_TCP
	err := tm.sharedDeviceConnections.TrySendTunnelDataWithRetries(ctx, destinationDevice,
		&protocol.TunnelData{
			D: &bep.TunnelData{
				TunnelId:                 tunnelID,
				Command:                  bep.TunnelCommand_TUNNEL_COMMAND_OPEN,
				RemoteServiceName:        &destinationServiceName,
				TunnelDestinationAddress: destinationAddress,
				Protocol:                 &protocolTCP,
			},
		}, 5 /* retries */)

	if err != nil {
		tl.Debugln("Stopping listener due to context done or failure to send open command")
		conn.Close()
		return
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
		isUdp := guf.DerefOr(tunnel.json.Protocol, bep.TunnelProtocol_TUNNEL_PROTOCOL_TCP) ==
			bep.TunnelProtocol_TUNNEL_PROTOCOL_UDP
		var err error
		if isUdp {
			err = tm.ServeListenerUDP(tunnel.ctx, tunnel.json.LocalListenAddress,
				device, tunnel.json.RemoteServiceName, tunnel.json.RemoteAddress)
		} else {
			err = tm.ServeListenerTCP(tunnel.ctx, tunnel.json.LocalListenAddress,
				device, tunnel.json.RemoteServiceName, tunnel.json.RemoteAddress)
		}
		if err != nil {
			proto := "TCP"
			if isUdp {
				proto = "UDP"
			}
			tl.Warnf("Failed to serve listener %s for tunnel %s: %v", proto, tunnel.descriptor, err)
		} else {
			tl.Debugln("Listener for tunnel", tunnel.descriptor, "stopped")
		}
	}()
}
