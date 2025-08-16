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
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syncthing/syncthing/internal/gen/bep"

	"github.com/syncthing/syncthing/lib/protocol"
)

func repeatedDeviceID(v byte) (d protocol.DeviceID) {
	for i := range d {
		d[i] = v
	}
	return
}

func TestTunnelManager_ServeLocalListener(t *testing.T) {
	// Activate debug logging
	l.SetDebug("module", true)

	// Mock device ID and addresses
	serverDeviceID := repeatedDeviceID(0x11)
	listenAddress := fmt.Sprintf("127.0.0.1:%d", GetRandomFreePort())
	destinationAddress := fmt.Sprintf("127.0.0.1:%d", GetRandomFreePort())

	// Create a new TunnelManager
	proxyServiceName := "proxy"
	tm := NewTunnelManagerFromConfig(
		&bep.TunnelConfig{
			TunnelsOut: []*bep.TunnelOutbound{
				{
					LocalListenAddress: listenAddress,
					RemoteDeviceId:     serverDeviceID.String(),
					RemoteServiceName:  proxyServiceName,
					RemoteAddress:      &destinationAddress,
				},
			},
		},
		"no-file",
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the listener
	go func() {
		err := tm.Serve(ctx)
		if err != nil {
			t.Errorf("Serve failed: %v", err)
		}
	}()

	// Create a channel to capture the TunnelData sent to the device
	tunnelDataChan := make(chan *protocol.TunnelData, 1)
	tm.deviceConnections.RegisterDeviceConnection(
		&TunnelManagerDeviceConnection{
			DeviceID:     serverDeviceID,
			ConnectionID: "test-connection",
			TunnelOut:    tunnelDataChan,
		},
	)

	var conn net.Conn
	var err error
	for range 100 {
		// Give the listener some time to start
		time.Sleep(100 * time.Millisecond)

		// Connect to the listener
		conn, err = net.Dial("tcp", listenAddress)
		if err == nil {
			break
		}
	}
	require.NoError(t, err)

	// Wait for the TunnelData to be sent
	var tunnelID uint64
	select {
	case data := <-tunnelDataChan:
		assert.Equal(t, bep.TunnelCommand_TUNNEL_COMMAND_OPEN, data.D.Command)
		assert.Equal(t, proxyServiceName, *data.D.RemoteServiceName)
		assert.Equal(t, destinationAddress, *data.D.TunnelDestinationAddress)
		tunnelID = data.D.TunnelId
		assert.NotEqual(t, 0, tunnelID)
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for TunnelData")
	}

	msg := []byte("hello")
	n, err := conn.Write(msg)
	require.NoError(t, err)
	assert.Len(t, msg, n)

	// Wait for the TunnelData to be sent
	select {
	case data := <-tunnelDataChan:
		assert.Equal(t, bep.TunnelCommand_TUNNEL_COMMAND_DATA, data.D.Command)
		assert.Equal(t, msg, data.D.Data)
		assert.Equal(t, tunnelID, data.D.TunnelId)
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for TunnelData")
	}

	conn.Close()

	// Wait for the TunnelData to be sent
	select {
	case data := <-tunnelDataChan:
		assert.Equal(t, bep.TunnelCommand_TUNNEL_COMMAND_CLOSE, data.D.Command)
		assert.Equal(t, tunnelID, data.D.TunnelId)
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for TunnelData")
	}
}

func TestTunnelManager_HandleOpenRemoteCommand(t *testing.T) {
	// Activate debug logging
	l.SetDebug("module", true)

	clientDeviceID := repeatedDeviceID(0x22)

	// Create a new TunnelManager
	tm := NewTunnelManagerFromConfig(
		&bep.TunnelConfig{
			TunnelsIn: []*bep.TunnelInbound{
				{
					LocalServiceName: "proxy",
					LocalDialAddress: "any",
					AllowedRemoteDeviceIds: []string{
						clientDeviceID.String(),
					},
				},
			},
		},
		"no-file",
	)

	// Mock device ID and addresses
	destinationAddress := fmt.Sprintf("127.0.0.1:%d", GetRandomFreePort())

	// Create a channel to capture the TunnelData sent to the device
	tunnelDataChanIn := make(chan *protocol.TunnelData, 1)
	tunnelDataChanOut := make(chan *protocol.TunnelData, 1)
	tm.deviceConnections.RegisterDeviceConnection(
		&TunnelManagerDeviceConnection{
			DeviceID:     clientDeviceID,
			ConnectionID: "test-connection",
			TunnelIn:     tunnelDataChanIn,
			TunnelOut:    tunnelDataChanOut,
		},
	)

	// Start a listener on the destination address
	listener, err := net.Listen("tcp", destinationAddress)
	require.NoError(t, err)
	defer listener.Close()

	// Send an open command to the TunnelManager
	tunnelID := tm.generateTunnelID()
	proxyServiceName := "proxy"
	tunnelDataChanIn <- &protocol.TunnelData{
		D: &bep.TunnelData{
			TunnelId:                 tunnelID,
			Command:                  bep.TunnelCommand_TUNNEL_COMMAND_OPEN,
			RemoteServiceName:        &proxyServiceName,
			TunnelDestinationAddress: &destinationAddress,
		},
	}

	// Wait for the TunnelManager to connect to the listener
	conn, err := listener.Accept()
	require.NoError(t, err)

	// Verify the connection
	msg_from_server := []byte("hello from server")
	_, err = conn.Write(msg_from_server)
	require.NoError(t, err)

	// Wait for the TunnelData to be sent

loop1:
	for {
		select {
		case data := <-tunnelDataChanOut:
			if data.D.Command == bep.TunnelCommand_TUNNEL_COMMAND_OFFER {
				// Ignore the offer
				continue loop1
			}
			assert.Equal(t, bep.TunnelCommand_TUNNEL_COMMAND_DATA, data.D.Command)
			assert.Equal(t, msg_from_server, data.D.Data)
			assert.Equal(t, tunnelID, data.D.TunnelId)
			break loop1
		case <-time.After(1 * time.Second):
			t.Fatal("Timed out waiting for TunnelData")
		}
	}

	msg_from_client := []byte("hello from client")
	tunnelDataChanIn <- &protocol.TunnelData{
		D: &bep.TunnelData{
			TunnelId: tunnelID,
			Command:  bep.TunnelCommand_TUNNEL_COMMAND_DATA,
			Data:     msg_from_client,
		},
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, msg_from_client, buf[:n])

	conn.Close()

	// Wait for the TunnelData to be sent
	select {
	case data := <-tunnelDataChanOut:
		assert.Equal(t, bep.TunnelCommand_TUNNEL_COMMAND_CLOSE, data.D.Command)
		assert.Equal(t, tunnelID, data.D.TunnelId)
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for TunnelData")
	}
}

func TestTunnelManager_HandleOpenRemoteCommand_NamedService(t *testing.T) {
	// Activate debug logging
	l.SetDebug("module", true)

	clientDeviceID := repeatedDeviceID(0x33)
	// Mock device ID and addresses
	localDestinationAddress := fmt.Sprintf("127.0.0.1:%d", GetRandomFreePort())
	localServiceName := "http"

	// Create a new TunnelManager
	tm := NewTunnelManagerFromConfig(
		&bep.TunnelConfig{
			TunnelsIn: []*bep.TunnelInbound{
				{
					LocalServiceName: localServiceName,
					LocalDialAddress: localDestinationAddress,
					AllowedRemoteDeviceIds: []string{
						clientDeviceID.String(),
					},
				},
			},
		},
		"no-file",
	)

	// Create a channel to capture the TunnelData sent to the device
	tunnelDataChanIn := make(chan *protocol.TunnelData, 1)
	tunnelDataChanOut := make(chan *protocol.TunnelData, 1)
	tm.deviceConnections.RegisterDeviceConnection(
		&TunnelManagerDeviceConnection{
			DeviceID:     clientDeviceID,
			ConnectionID: "test-connection",
			TunnelIn:     tunnelDataChanIn,
			TunnelOut:    tunnelDataChanOut,
		},
	)

	// Start a listener on the destination address
	listener, err := net.Listen("tcp", localDestinationAddress)
	require.NoError(t, err)
	defer listener.Close()

	// Send an open command to the TunnelManager
	tunnelID := tm.generateTunnelID()
	tunnelDataChanIn <- &protocol.TunnelData{
		D: &bep.TunnelData{
			TunnelId:          tunnelID,
			Command:           bep.TunnelCommand_TUNNEL_COMMAND_OPEN,
			RemoteServiceName: &localServiceName,
		},
	}

	// Wait for the TunnelManager to connect to the listener
	conn, err := listener.Accept()
	require.NoError(t, err)

	// Verify the connection
	msg_from_server := []byte("hello from server")
	_, err = conn.Write(msg_from_server)
	require.NoError(t, err)

	// Wait for the TunnelData to be sent
loop1:
	for {
		select {
		case data := <-tunnelDataChanOut:
			if data.D.Command == bep.TunnelCommand_TUNNEL_COMMAND_OFFER {
				// Ignore the offer
				continue loop1
			}
			assert.Equal(t, bep.TunnelCommand_TUNNEL_COMMAND_DATA, data.D.Command)
			assert.Equal(t, msg_from_server, data.D.Data)
			assert.Equal(t, tunnelID, data.D.TunnelId)
			break loop1
		case <-time.After(1 * time.Second):
			t.Fatal("Timed out waiting for TunnelData")
		}
	}

	msg_from_client := []byte("hello from client")
	tunnelDataChanIn <- &protocol.TunnelData{
		D: &bep.TunnelData{
			TunnelId: tunnelID,
			Command:  bep.TunnelCommand_TUNNEL_COMMAND_DATA,
			Data:     msg_from_client,
		},
	}

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, msg_from_client, buf[:n])

	conn.Close()

	// Wait for the TunnelData to be sent
	select {
	case data := <-tunnelDataChanOut:
		assert.Equal(t, bep.TunnelCommand_TUNNEL_COMMAND_CLOSE, data.D.Command)
		assert.Equal(t, tunnelID, data.D.TunnelId)
	case <-time.After(1 * time.Second):
		t.Fatal("Timed out waiting for TunnelData")
	}
}

func TestTunnelManager_HandleOpenRemoteCommand_DisallowedClient(t *testing.T) {
	// Activate debug logging
	l.SetDebug("module", true)

	clientDeviceID := repeatedDeviceID(0x33)
	disallowedClientDeviceID := repeatedDeviceID(0x44)
	// Mock device ID and addresses
	localDestinationAddress := fmt.Sprintf("127.0.0.1:%d", GetRandomFreePort())
	localServiceName := "http"

	// Create a new TunnelManager
	tm := NewTunnelManagerFromConfig(
		&bep.TunnelConfig{
			TunnelsIn: []*bep.TunnelInbound{
				{
					LocalServiceName: localServiceName,
					LocalDialAddress: localDestinationAddress,
					AllowedRemoteDeviceIds: []string{
						clientDeviceID.String(),
					},
				},
			},
		},
		"no-file",
	)

	// Create a channel to capture the TunnelData sent to the device
	tunnelDataChanIn := make(chan *protocol.TunnelData, 1)
	tunnelDataChanOut := make(chan *protocol.TunnelData, 1)
	tm.deviceConnections.RegisterDeviceConnection(
		&TunnelManagerDeviceConnection{
			DeviceID:     disallowedClientDeviceID,
			ConnectionID: "test-connection",
			TunnelIn:     tunnelDataChanIn,
			TunnelOut:    tunnelDataChanOut,
		},
	)

	// Start a listener on the destination address
	listener, err := net.Listen("tcp", localDestinationAddress)
	require.NoError(t, err)
	defer listener.Close()

	// Send an open command to the TunnelManager
	tunnelID := tm.generateTunnelID()
	tunnelDataChanIn <- &protocol.TunnelData{
		D: &bep.TunnelData{
			TunnelId:          tunnelID,
			Command:           bep.TunnelCommand_TUNNEL_COMMAND_OPEN,
			RemoteServiceName: &localServiceName,
		},
	}

	// Wait for the TunnelManager to connect to the listener
	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		t.Fatal("listener is not a *net.TCPListener")
	}
	require.NoError(t, tcpListener.SetDeadline(time.Now().Add(300*time.Millisecond)))
	_, err = listener.Accept()
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
}

func TestTunnelManagerConfigUpdate_addAllowedDevice_removeAllowedDevice(t *testing.T) {
	clientDeviceID1 := repeatedDeviceID(0x33)
	clientDeviceID2 := repeatedDeviceID(0x44)
	// Mock device ID and addresses
	localDestinationAddress := fmt.Sprintf("127.0.0.1:%d", GetRandomFreePort())
	localServiceName := "http"

	// reserve a temporary file:
	tmpFile, err := os.CreateTemp("", "syncthing-tunnel-manager-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Create a new TunnelManager
	tm := NewTunnelManagerFromConfig(
		&bep.TunnelConfig{
			TunnelsIn: []*bep.TunnelInbound{
				{
					LocalServiceName:       localServiceName,
					LocalDialAddress:       localDestinationAddress,
					AllowedRemoteDeviceIds: []string{},
				},
			},
		},
		tmpFile.Name(),
	)

	status := tm.Status()
	assert.Len(t, status, 1)
	assert.Equal(t, localServiceName, status[0]["serviceID"])
	allowedDevices := status[0]["allowedRemoteDeviceIDs"].([]string)
	assert.Empty(t, allowedDevices)

	err = tm.ModifyTunnel(status[0]["id"].(string), "add-allowed-device",
		map[string]string{
			"deviceID": clientDeviceID1.String(),
		},
	)
	require.NoError(t, err)

	status = tm.Status()

	assert.Len(t, status, 1)
	assert.Equal(t, localServiceName, status[0]["serviceID"])
	assert.Equal(t, localDestinationAddress, status[0]["localDialAddress"])
	allowedDevices = status[0]["allowedRemoteDeviceIDs"].([]string)
	assert.Len(t, allowedDevices, 1)
	assert.Equal(t, clientDeviceID1.String(), allowedDevices[0])

	err = tm.ModifyTunnel(status[0]["id"].(string), "add-allowed-device",
		map[string]string{
			"deviceID": clientDeviceID2.String(),
		},
	)
	require.NoError(t, err)

	status = tm.Status()

	assert.Equal(t, 1, len(status))
	assert.Equal(t, localServiceName, status[0]["serviceID"])
	assert.Equal(t, localDestinationAddress, status[0]["localDialAddress"])
	allowedDevices = status[0]["allowedRemoteDeviceIDs"].([]string)
	assert.Equal(t, 2, len(allowedDevices))
	assert.Equal(t, clientDeviceID1.String(), allowedDevices[0])
	assert.Equal(t, clientDeviceID2.String(), allowedDevices[1])

	err = tm.ModifyTunnel(status[0]["id"].(string), "remove-allowed-device",
		map[string]string{
			"deviceID": clientDeviceID2.String(),
		},
	)
	require.NoError(t, err)

	status = tm.Status()

	assert.Equal(t, 1, len(status))
	assert.Equal(t, localServiceName, status[0]["serviceID"])
	assert.Equal(t, localDestinationAddress, status[0]["localDialAddress"])
	allowedDevices = status[0]["allowedRemoteDeviceIDs"].([]string)
	assert.Equal(t, 1, len(allowedDevices))
	assert.Equal(t, clientDeviceID1.String(), allowedDevices[0])

	err = tm.ModifyTunnel(status[0]["id"].(string), "remove-allowed-device",
		map[string]string{
			"deviceID": clientDeviceID1.String(),
		},
	)
	require.NoError(t, err)

	status = tm.Status()

	assert.Equal(t, 1, len(status))
	assert.Equal(t, 0, len(status[0]["allowedRemoteDeviceIDs"].([]string)))
	assert.Equal(t, localServiceName, status[0]["serviceID"])
	assert.Equal(t, localDestinationAddress, status[0]["localDialAddress"])
	allowedDevices = status[0]["allowedRemoteDeviceIDs"].([]string)
	assert.Empty(t, allowedDevices)
}
