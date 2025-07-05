// Copyright (C) 2019 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"weak"

	"github.com/ek220/guf"
	"github.com/syncthing/syncthing/internal/gen/bep"
	"github.com/syncthing/syncthing/lib/logger"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/utils"
)

var tl = logger.DefaultLogger.NewFacility("tunnels", "the tunnel manager stuff")

var (
	ErrTunnelAlreadyExists   = errors.New("tunnel already exists")
	ErrTunnelNotFound        = errors.New("tunnel not found")
	ErrInboundTunnelNotFound = errors.New("inbound tunnel not found")
	ErrDeviceIDAlreadyExists = errors.New("allowed device ID already exists")
	ErrDeviceIDNotFound      = errors.New("disallowed device ID not found")
	ErrInvalidAction         = errors.New("invalid action")
)

func hashDescriptor(descriptor string) string {
	hash := sha256.Sum256([]byte(descriptor))
	return hex.EncodeToString(hash[:])
}

func getConfigDescriptorOutbound(cfg *bep.TunnelOutbound) string {
	plainDescriptor := fmt.Sprintf("out-%s>%s:%s:%s-%v",
		cfg.LocalListenAddress,
		cfg.RemoteDeviceId,
		cfg.RemoteServiceName,
		guf.DerefOrDefault(cfg.RemoteAddress),
		guf.DerefOr(cfg.Enabled, true),
	)
	return hashDescriptor(plainDescriptor)
}

func getConfigDescriptorInbound(cfg *bep.TunnelInbound, withAllowedDevices bool) string {
	plainDescriptor := fmt.Sprintf("in-%s:%s-%v",
		cfg.LocalServiceName,
		cfg.LocalDialAddress,
		guf.DerefOr(cfg.Enabled, true),
	)
	if withAllowedDevices {
		plainDescriptor += fmt.Sprintf("<%s", cfg.AllowedRemoteDeviceIds)
	}
	return hashDescriptor(plainDescriptor)
}

func getUiTunnelDescriptorOutbound(cfg *bep.TunnelOutbound) string {
	plainDescriptor := fmt.Sprintf("out-%s>%s:%s:%s",
		cfg.LocalListenAddress,
		cfg.RemoteDeviceId,
		cfg.RemoteServiceName,
		guf.DerefOrDefault(cfg.RemoteAddress),
	)
	return fmt.Sprintf("o-%s-%s", cfg.RemoteServiceName, hashDescriptor(plainDescriptor))
}

func getUiTunnelDescriptorInbound(cfg *bep.TunnelInbound) string {
	plainDescriptor := fmt.Sprintf("in-%s:%s",
		cfg.LocalServiceName,
		cfg.LocalDialAddress,
	)
	return fmt.Sprintf("i-%s-%s", cfg.LocalServiceName, hashDescriptor(plainDescriptor))
}

type tunnelBaseConfig struct {
	descriptor string
	ctx        context.Context
	cancel     context.CancelFunc
}

type tunnelOutConfig struct {
	tunnelBaseConfig
	json *bep.TunnelOutbound
}

type tunnelInConfig struct {
	tunnelBaseConfig
	allowedClients map[string]tunnelBaseConfig
	json           *bep.TunnelInbound
}

type tm_config struct {
	configIn       map[string]*tunnelInConfig
	configOut      map[string]*tunnelOutConfig
	serviceRunning bool
}

type tm_localTunnelEP struct {
	endpoint           io.ReadWriteCloser
	inUse              atomic.Pointer[map[uint64]*tm_localTunnelEP]
	nextPackageCounter uint32 // used to identify lost packages
}

type TunnelManager struct {
	config            *utils.Protected[*tm_config]
	configFile        string
	localEndpointMgr  *TunnelManagerEndpointManager
	deviceConnections *TunnelManagerDeviceConnectionsManager
	localListeners    *LocalListenerManager
}

func NewTunnelManager(configFile string) *TunnelManager {
	// Replace the filename with "tunnels.json"
	configFile = fmt.Sprintf("%s/tunnels.json", filepath.Dir(configFile))
	tl.Debugln("TunnelManager created with config file:", configFile)
	config, err := loadTunnelConfig(configFile)
	if err != nil {
		tl.Infoln("failed to load tunnel config:", err)
		config = &bep.TunnelConfig{}
	}
	return NewTunnelManagerFromConfig(config, configFile)
}

func NewTunnelManagerFromConfig(config *bep.TunnelConfig, configFile string) *TunnelManager {
	if config == nil {
		panic("TunnelManager config is nil")
	}
	sharedConfig := utils.NewProtected(&tm_config{
		configIn:       make(map[string]*tunnelInConfig),
		configOut:      make(map[string]*tunnelOutConfig),
		serviceRunning: false,
	})

	localEndpointMgr := NewTunnelManagerEndpointManager()
	sharedDeviceConnections := NewTunnelManagerDeviceConnectionsManager(
		sharedConfig, localEndpointMgr)

	// use weak pointer to avoid circular references
	localEndpointMgr.SetSharedDeviceConnections(weak.Make(sharedDeviceConnections))

	tm := &TunnelManager{
		configFile:        configFile,
		config:            sharedConfig,
		localEndpointMgr:  localEndpointMgr,
		deviceConnections: sharedDeviceConnections,
		localListeners:    NewLocalListenerManager(sharedConfig, localEndpointMgr, sharedDeviceConnections),
	}
	// use update logic to set the initial config as well.
	// this avoids code duplication
	tm.localListeners.updateOutConfig(config.TunnelsOut)
	tm.updateInConfig(config.TunnelsIn)
	return tm
}

func (m *TunnelManager) TunnelStatus() []map[string]interface{} {
	return m.Status()
}

func (m *TunnelManager) AddTunnelOutbound(localListenAddress string, remoteDeviceID protocol.DeviceID, remoteServiceName string) error {
	return m.AddOutboundTunnel(localListenAddress, remoteDeviceID, remoteServiceName)
}

func (tm *TunnelManager) updateInConfig(newInTunnels []*bep.TunnelInbound) {
	tm.config.DoProtected(func(config *tm_config) {
		// Generate a new map of inbound tunnels
		newConfigIn := make(map[string]*tunnelInConfig)
		for _, newTun := range newInTunnels {
			descriptor := getConfigDescriptorInbound(newTun, false)
			if existingTun, exists := config.configIn[descriptor]; exists {
				// Reuse existing context and cancel function
				existingTun.json = newTun // update e.g. suggested port
				// Update allowed devices
				allowedClients := make(map[string]tunnelBaseConfig)
				for _, deviceIDStr := range newTun.AllowedRemoteDeviceIds {
					deviceID, err := protocol.DeviceIDFromString(deviceIDStr)
					if err != nil {
						tl.Warnf("failed to parse device ID: %v", err)
						continue
					}
					if _, exists := existingTun.allowedClients[deviceIDStr]; !exists {
						ctx, cancel := context.WithCancel(existingTun.ctx)
						allowedClients[deviceIDStr] = tunnelBaseConfig{
							descriptor: descriptor,
							ctx:        ctx,
							cancel:     cancel,
						}
						go func() {
							_ = tm.deviceConnections.TrySendTunnelData(deviceID, generateOfferCommand(newTun))
						}()
					} else {
						allowedClients[deviceIDStr] = existingTun.allowedClients[deviceIDStr]
					}
				}
				// Cancel and remove devices no longer allowed
				for deviceID, existingClient := range existingTun.allowedClients {
					if _, exists := allowedClients[deviceID]; !exists {
						existingClient.cancel()
					}
				}
				existingTun.allowedClients = allowedClients

				newConfigIn[descriptor] = existingTun

			} else {
				// Create new context and cancel function
				ctx, cancel := context.WithCancel(context.Background())
				newConfigIn[descriptor] = &tunnelInConfig{
					tunnelBaseConfig: tunnelBaseConfig{
						descriptor: descriptor,
						ctx:        ctx,
						cancel:     cancel,
					},
					json:           newTun,
					allowedClients: make(map[string]tunnelBaseConfig),
				}
			}
		}

		// Cancel and remove tunnels that are no longer in the new configuration
		for descriptor, existing := range config.configIn {
			if _, exists := newConfigIn[descriptor]; !exists {
				existing.cancel()
			}
		}

		// Replace the old configuration with the new one
		config.configIn = newConfigIn
	})
}

func (tm *TunnelManager) Serve(ctx context.Context) error {
	tl.Debugln("TunnelManager Serve started")

	tm.localListeners.startListeners()

	<-ctx.Done()
	tl.Debugln("TunnelManager Serve stopping")

	// Cancel all active tunnels
	tm.config.DoProtected(func(config *tm_config) {
		config.serviceRunning = false
		for _, tunnel := range config.configIn {
			tunnel.cancel()
		}
		for _, tunnel := range config.configOut {
			tunnel.cancel()
		}
	})

	tl.Debugln("TunnelManager Serve stopped")
	return nil
}

func generateOfferCommand(json *bep.TunnelInbound) *protocol.TunnelData {
	suggestedPort := strconv.FormatUint(uint64(guf.DerefOr(json.SuggestedPort, 0)), 10)
	return &protocol.TunnelData{
		D: &bep.TunnelData{
			Command:                  bep.TunnelCommand_TUNNEL_COMMAND_OFFER,
			RemoteServiceName:        &json.LocalServiceName,
			TunnelDestinationAddress: &suggestedPort,
		},
	}
}

func parseUint32Or(input string, defaultValue uint32) uint32 {
	// Parse the input string as a uint32
	value, err := strconv.ParseUint(input, 10, 32)
	if err != nil {
		tl.Warnf("Failed to parse %s as uint32: %v", input, err)
		return defaultValue
	}
	return uint32(value)
}

func (tm *TunnelManager) generateTunnelID() uint64 {
	return tm.localListeners.generateTunnelID()
}

func getRandomFreePort() int {
	a, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		tl.Warnf("Failed to resolve TCP address: %v", err)
		return 0
	}

	l, err := net.ListenTCP("tcp", a)
	if err != nil {
		tl.Warnf("Failed to listen on TCP address: %v", err)
		return 0
	}
	defer l.Close()

	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		tl.Warnf("Failed to get TCP address from listener")
		return 0
	}

	tl.Debugf("Found free port: %d", addr.Port)
	return addr.Port
	panic("no free ports")
}

func (m *TunnelManager) AddOutboundTunnel(localListenAddress string, remoteDeviceID protocol.DeviceID, remoteServiceName string) error {
	err := utils.DoProtected(m.config, func(config *tm_config) error {
		if localListenAddress == "127.0.0.1:0" {
			suggestedPort := getRandomFreePort()
			localListenAddress = fmt.Sprintf("127.0.0.1:%d", suggestedPort)
		}

		newConfig := &bep.TunnelOutbound{
			LocalListenAddress: localListenAddress,
			RemoteDeviceId:     remoteDeviceID.String(),
			RemoteServiceName:  remoteServiceName,
		}
		descriptor := getConfigDescriptorOutbound(newConfig)

		// Check if the tunnel already exists
		if _, exists := config.configOut[descriptor]; exists {
			return fmt.Errorf("%w: descriptor %s", ErrTunnelAlreadyExists, descriptor)
		}

		bepConfig := &bep.TunnelConfig{
			TunnelsIn:  config.getInboundTunnelsConfig(),
			TunnelsOut: config.getOutboundTunnelsConfig(),
		}

		bepConfig.TunnelsOut = append(bepConfig.TunnelsOut, newConfig)

		if err := saveTunnelConfig(m.configFile, bepConfig); err != nil {
			return fmt.Errorf("failed to save tunnel config: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	return m.reloadConfig()
}

// Status returns information about active tunnels
func (m *TunnelManager) Status() []map[string]interface{} {
	offerings := make(map[string]map[string]map[string]interface{})

	offeringsIntern := m.deviceConnections.GetCopyOfAllServiceOfferings()

	for deviceID, deviceOffers := range offeringsIntern {
		if offerings[deviceID.String()] == nil {
			offerings[deviceID.String()] = make(map[string]map[string]interface{})
		}
		for serviceName, suggestedPort := range deviceOffers {
			info := map[string]interface{}{
				"localListenAddress": "127.0.0.1:" + strconv.Itoa(int(suggestedPort)),
				"remoteDeviceID":     deviceID.String(),
				"serviceID":          serviceName,
				"offered":            true,
				"type":               "outbound",
				"uiID": getUiTunnelDescriptorOutbound(&bep.TunnelOutbound{
					LocalListenAddress: "127.0.0.1:" + strconv.Itoa(int(suggestedPort)),
					RemoteDeviceId:     deviceID.String(),
					RemoteServiceName:  serviceName,
					RemoteAddress:      nil,
				}),
			}
			offerings[deviceID.String()][serviceName] = info
		}
	}

	status := utils.DoProtected(m.config, func(config *tm_config) []map[string]interface{} {
		status := make([]map[string]interface{}, 0, len(config.configIn)+len(config.configOut))

		for descriptor, tunnel := range config.configIn {
			info := map[string]interface{}{
				"id":                     descriptor,
				"serviceID":              tunnel.json.LocalServiceName,
				"allowedRemoteDeviceIDs": tunnel.json.AllowedRemoteDeviceIds,
				"localDialAddress":       tunnel.json.LocalDialAddress,
				"active":                 guf.DerefOr(tunnel.json.Enabled, true),
				"type":                   "inbound",
				"uiID":                   getUiTunnelDescriptorInbound(tunnel.json),
			}
			status = append(status, info)
		}
		for descriptor, tunnel := range config.configOut {

			// remove offering when already used
			delete(offerings[tunnel.json.RemoteDeviceId], tunnel.json.RemoteServiceName)

			info := map[string]interface{}{
				"id":                 descriptor,
				"localListenAddress": tunnel.json.LocalListenAddress,
				"remoteDeviceID":     tunnel.json.RemoteDeviceId,
				"serviceID":          tunnel.json.RemoteServiceName,
				"remoteAddress":      tunnel.json.RemoteAddress,
				"active":             guf.DerefOr(tunnel.json.Enabled, true),
				"type":               "outbound",
				"uiID":               getUiTunnelDescriptorOutbound(tunnel.json),
			}
			status = append(status, info)
		}

		return status
	})

	// add remaining offerings:
	for _, services := range offerings {
		for _, info := range services {
			status = append(status, info)
		}
	}

	// sort by uiID
	slices.SortFunc(status, func(a map[string]interface{}, b map[string]interface{}) int {
		aID, aOk := a["uiID"].(string)
		bID, bOk := b["uiID"].(string)
		if !aOk && !bOk {
			return 0
		}
		if !aOk {
			return -1
		}
		if !bOk {
			return 1
		}
		return strings.Compare(aID, bID)
	})

	return status
}

func loadTunnelConfig(path string) (*bep.TunnelConfig, error) {
	tl.Debugln("Loading tunnel config from file:", path)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config bep.TunnelConfig
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		return nil, err
	}

	tl.Debugln("Loaded tunnel config:", &config)
	return &config, nil
}

func saveTunnelConfig(path string, config *bep.TunnelConfig) error {
	tl.Debugln("Saving tunnel config to file:", path)

	// Create directory if it doesn't exist
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create a temporary file in the same directory
	tmpFile, err := os.CreateTemp(dir, "tunnels.*.json.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Clean up in case of failure
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Write to the temporary file
	encoder := json.NewEncoder(tmpFile)
	encoder.SetIndent("", "  ") // Pretty print with 2-space indentation
	if err := encoder.Encode(config); err != nil {
		tmpFile.Close()
		return fmt.Errorf("failed to encode config: %w", err)
	}

	// Close the file before renaming
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}

	// Atomic rename to ensure the config file is not corrupted if the process is interrupted
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("failed to replace config file: %w", err)
	}

	success = true
	tl.Debugln("Saved tunnel config:", config)
	return nil
}

// setter for TunnelConfig.TunnelsIn/Out.Enabled (by id "inbound/outbound-idx") which also saves the config to file
func (tm *TunnelManager) ModifyTunnel(id string, action string, params map[string]string) error {
	if err := tm.modifyAndSaveConfig(id, action, params); err != nil {
		return err
	}

	return tm.reloadConfig()
}

func (tm *TunnelManager) modifyAndSaveConfig(id string, action string, params map[string]string) error {
	return utils.DoProtected(tm.config, func(config *tm_config) error {
		if action == "enable" || action == "disable" {
			enabled := action == "enable"
			// Check if the ID corresponds to an inbound tunnel
			if tunnel, exists := config.configIn[id]; exists {
				tunnel.json.Enabled = &enabled
				return config.saveFullConfig_no_lock(tm.configFile)
			}

			// Check if the ID corresponds to an outbound tunnel
			if tunnel, exists := config.configOut[id]; exists {
				tunnel.json.Enabled = &enabled
				return config.saveFullConfig_no_lock(tm.configFile)
			}
		} else if action == "delete" {
			// Check if the ID corresponds to an inbound tunnel
			if tunnel, exists := config.configIn[id]; exists {
				tunnel.cancel()
				delete(config.configIn, id)
				return config.saveFullConfig_no_lock(tm.configFile)
			}
			// Check if the ID corresponds to an outbound tunnel
			if tunnel, exists := config.configOut[id]; exists {
				tunnel.cancel()
				delete(config.configOut, id)
				return config.saveFullConfig_no_lock(tm.configFile)
			}
		} else if action == "add-allowed-device" {
			// Check if the ID corresponds to an inbound tunnel
			if tunnel, exists := config.configIn[id]; exists {
				// Add the allowed device ID to the tunnel
				newAllowedDeviceID := params["deviceID"]
				if index := slices.IndexFunc(tunnel.json.AllowedRemoteDeviceIds,
					func(device string) bool { return device == newAllowedDeviceID }); index < 0 {
					tunnel.json.AllowedRemoteDeviceIds = append(tunnel.json.AllowedRemoteDeviceIds, newAllowedDeviceID)
					return config.saveFullConfig_no_lock(tm.configFile)
				}
				return fmt.Errorf("%w: device ID %s in tunnel %s", ErrDeviceIDAlreadyExists, newAllowedDeviceID, id)
			}
			return fmt.Errorf("%w: %s", ErrInboundTunnelNotFound, id)
		} else if action == "remove-allowed-device" {
			// Check if the ID corresponds to an inbound tunnel
			if tunnel, exists := config.configIn[id]; exists {
				// Remove the allowed device ID from the tunnel
				disallowedDeviceID := params["deviceID"]
				if index := slices.IndexFunc(tunnel.json.AllowedRemoteDeviceIds,
					func(device string) bool { return device == disallowedDeviceID }); index >= 0 {
					tunnel.json.AllowedRemoteDeviceIds = slices.Delete(tunnel.json.AllowedRemoteDeviceIds, index, index+1)
					return config.saveFullConfig_no_lock(tm.configFile)
				}
				return fmt.Errorf("%w: device ID %s in tunnel %s", ErrDeviceIDNotFound, disallowedDeviceID, id)
			}
			return fmt.Errorf("%w: %s", ErrInboundTunnelNotFound, id)
		} else {
			return fmt.Errorf("%w: %s", ErrInvalidAction, action)
		}

		// If the ID is not found, return an error
		return fmt.Errorf("%w: %s", ErrTunnelNotFound, id)
	})
}

func (tm *TunnelManager) reloadConfig() error {
	config, err := loadTunnelConfig(tm.configFile)
	if err != nil {
		return fmt.Errorf("failed to reload tunnel config: %w", err)
	}

	tm.updateInConfig(config.TunnelsIn)
	tm.localListeners.updateOutConfig(config.TunnelsOut)

	return nil
}

// Helper method to retrieve all inbound tunnels as a slice
func (tm *tm_config) getInboundTunnelsConfig() []*bep.TunnelInbound {
	tunnels := make([]*bep.TunnelInbound, 0, len(tm.configIn))
	for _, tunnel := range tm.configIn {
		tunnels = append(tunnels, tunnel.json)
	}
	return tunnels
}

// Helper method to retrieve all outbound tunnels as a slice
func (tm *tm_config) getOutboundTunnelsConfig() []*bep.TunnelOutbound {
	tunnels := make([]*bep.TunnelOutbound, 0, len(tm.configOut))
	for _, tunnel := range tm.configOut {
		tunnels = append(tunnels, tunnel.json)
	}
	return tunnels
}

// SaveFullConfig saves the current configuration (both inbound and outbound tunnels) to the config file.
func (tm *tm_config) saveFullConfig_no_lock(filename string) error {
	config := &bep.TunnelConfig{
		TunnelsIn:  tm.getInboundTunnelsConfig(),
		TunnelsOut: tm.getOutboundTunnelsConfig(),
	}

	return saveTunnelConfig(filename, config)
}

func (tm *TunnelManager) ReloadConfig() error {
	config, err := loadTunnelConfig(tm.configFile)
	if err != nil {
		return fmt.Errorf("failed to reload tunnel config: %w", err)
	}

	tm.updateInConfig(config.TunnelsIn)
	tm.localListeners.updateOutConfig(config.TunnelsOut)

	return nil
}
