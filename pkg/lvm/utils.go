/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package lvm

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

// lvm related constants
const (
	DevPath       = "/dev/"
	DevMapperPath = "/dev/mapper/"
	// MinExtentRoundOffSize represents minimum size (256Mi) to roundoff the volume
	// group size in case of thin pool provisioning
	MinExtentRoundOffSize = 268435456

	// BlockCleanerCommand is the command used to clean filesystem on the device
	BlockCleanerCommand = "wipefs"
)

// lvm command related constants
const (
	VGCreate = "vgcreate"
	VGList   = "vgs"

	LVCreate = "lvcreate"
	LVRemove = "lvremove"
	LVExtend = "lvextend"
	LVList   = "lvs"

	PVList = "pvs"
	PVScan = "pvscan"

	YES        = "yes"
	LVThinPool = "thin-pool"
)

// ExecError holds the process output along with underlying
// error returned by exec.CombinedOutput function.
type ExecError struct {
	Output []byte
	Err    error
}

// Error implements the error interface.
func (e *ExecError) Error() string {
	return fmt.Sprintf("%v - %v", string(e.Output), e.Err)
}

// ListLVMVolumeGroup invokes `vgs` to list all the available volume
// groups in the node.
//
// In case reloadCache is false, we skip refreshing lvm metadata cache.
func ListLVMVolumeGroup(reloadCache bool) ([]VolumeGroup, error) {
	if reloadCache {
		if err := ReloadLVMMetadataCache(); err != nil {
			return nil, err
		}
	}

	args := []string{
		"--options", "vg_all",
		"--reportformat", "json",
		"--units", "b",
	}
	cmd := exec.CommandContext(context.Background(), VGList, args...)
	// output, err := cmd.CombinedOutput()
	output, err := cmd.Output()
	if err != nil {
		klog.Errorf("lvm: list volume group cmd %v: %v", args, err)
		return nil, err
	}

	return decodeVgsJSON(output)
}

func newExecError(output []byte, err error) error {
	if err == nil {
		return nil
	}

	return &ExecError{
		Output: output,
		Err:    err,
	}
}

// getLVSize will return current LVM volume size in bytes
func decodeVgsJSON(raw []byte) ([]VolumeGroup, error) {
	output := &struct {
		Report []struct {
			VolumeGroups []map[string]string `json:"vg"`
		} `json:"report"`
	}{}

	var err error
	if err = json.Unmarshal(raw, output); err != nil {
		return nil, fmt.Errorf("failed to decode vgs json: %w\n%s", err, string(raw))
	}

	if len(output.Report) != 1 {
		return nil, errors.New("expected exactly one lvm report")
	}

	items := output.Report[0].VolumeGroups

	vgs := make([]VolumeGroup, 0, len(items))
	for _, item := range items {
		var vg VolumeGroup
		if vg, err = parseVolumeGroup(item); err != nil {
			return vgs, fmt.Errorf("failed to parse vg: %w", err)
		}

		vgs = append(vgs, vg)
	}

	return vgs, nil
}

func parseVolumeGroup(m map[string]string) (VolumeGroup, error) {
	var (
		vg        VolumeGroup
		sizeBytes int64
		err       error
	)

	vg.Name = m[VGName]
	vg.UUID = m[VGUUID]

	int32Map := map[string]*int32{
		VGPVvount:           &vg.PVCount,
		VGLvCount:           &vg.LVCount,
		VGMaxLv:             &vg.MaxLV,
		VGMaxPv:             &vg.MaxPV,
		VGSnapCount:         &vg.SnapCount,
		VGMissingPvCount:    &vg.MissingPVCount,
		VGMetadataCount:     &vg.MetadataCount,
		VGMetadataUsedCount: &vg.MetadataUsedCount,
	}
	for key, value := range int32Map {
		count64, parseErr := strconv.ParseInt(m[key], 10, 32)
		if parseErr != nil {
			err = fmt.Errorf(
				"invalid format of %v=%v for vg %v: %w",
				key, m[key], vg.Name, parseErr,
			)
		}

		*value = int32(count64)
	}

	resQuantityMap := map[string]*resource.Quantity{
		VGSize:             &vg.Size,
		VGFreeSize:         &vg.Free,
		VGMetadataSize:     &vg.MetadataSize,
		VGMetadataFreeSize: &vg.MetadataFree,
	}

	for key, value := range resQuantityMap {
		sizeBytes, err = strconv.ParseInt(
			strings.TrimSuffix(strings.ToLower(m[key]), "b"),
			10, 64)
		if err != nil {
			err = fmt.Errorf("invalid format of %v=%v for vg %v: %w", key, m[key], vg.Name, err)
		}

		quantity := resource.NewQuantity(sizeBytes, resource.BinarySI)
		*value = *quantity //
	}

	vg.Permission = getIntFieldValue(VGPermissions, m[VGPermissions])
	vg.AllocationPolicy = getIntFieldValue(VGAllocationPolicy, m[VGAllocationPolicy])

	return vg, err
}

// This function returns the integer equivalent for different string values for the LVM component(vg,lv) field.
// -1 represents undefined.
func getIntFieldValue(fieldName, fieldValue string) int {
	mv := -1
	for i, v := range Enums[fieldName] {
		if v == fieldValue {
			mv = i
			break
		}
	}

	return mv
}

// ReloadLVMMetadataCache refreshes lvmetad daemon cache used for
// serving vgs or other lvm utility.
func ReloadLVMMetadataCache() error {
	args := []string{"--cache"}
	cmd := exec.CommandContext(context.Background(), PVScan, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Errorf("lvm: reload lvm metadata cache: %v - %v", string(output), err)
		return err
	}

	return nil
}

// Function to get LVM Logical volume device
// It returns LVM logical volume device(dm-*).
// This is used as a label in metrics(lvm_lv_total_size) which helps us to map lv_name to device.
//
// Example: pvc-f147582c-adbd-4015-8ca9-fe3e0a4c2452(lv_name) -> dm-0(device)
func getLvDeviceName(path string) (string, error) {
	dmPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		klog.Errorf("failed to resolve device mapper from lv path %v: %v", path, err)
		return "", err
	}

	deviceName := strings.Split(dmPath, "/")

	return deviceName[len(deviceName)-1], nil
}

// To parse the output of lvs command and store it in LogicalVolume
// It returns LogicalVolume.
//
//	Example: LogicalVolume{
//			Name:               "pvc-082c7975-9af2-4a50-9d24-762612b35f94",
//			FullName:           "vg_thin/pvc-082c7975-9af2-4a50-9d24-762612b35f94"
//			UUID:               "FBqcEe-Ln72-SmWO-fR4j-t4Ga-1Y90-0vieKW"
//			Size:                4294967296,
//			Path:                "/dev/vg_thin/pvc-082c7975-9af2-4a50-9d24-762612b35f94",
//			DMPath:              "/dev/mapper/vg_thin-pvc--082c7975--9af2--4a50--9d24--762612b35f94"
//			Device:              "dm-5"
//			VGName:              "vg_thin"
//			SegType:             "thin"
//			Permission:          1
//			BehaviourWhenFull:   -1
//			HealthStatus:        0
//			RaidSyncAction:      -1
//			ActiveStatus:        "active"
//			Host:                "node1-virtual-machine"
//			PoolName:            "vg_thin_thinpool"
//			UsedSizePercent:     0
//			MetadataSize:        0
//			MetadataUsedPercent: 0
//			SnapshotUsedPercent: 0
//		}
func parseLogicalVolume(m map[string]string) (LogicalVolume, error) {
	var (
		lv        LogicalVolume
		err       error
		sizeBytes int64
		count     float64
	)

	lv.Name = m[LVName]
	lv.FullName = m[LVFullName]
	lv.UUID = m[LVUUID]
	lv.Path = m[LVPath]
	lv.DMPath = m[LVDmPath]
	lv.VGName = m[VGName]
	lv.ActiveStatus = m[LVActive]

	int64Map := map[string]*int64{
		LVSize:         &lv.Size,
		LVMetadataSize: &lv.MetadataSize,
	}
	for key, value := range int64Map {
		// Check if the current LV is not a thin pool. If not then
		// metadata size will not be present as metadata is only
		// stored for thin pools.
		if m[LVSegtype] != LVThinPool && key == LVMetadataSize {
			sizeBytes = 0
		} else {
			sizeBytes, err = strconv.ParseInt(
				strings.TrimSuffix(strings.ToLower(m[key]), "b"),
				10,
				64,
			)
			if err != nil {
				err = fmt.Errorf("invalid format of %v=%v for vg %v: %w", key, m[key], lv.Name, err)
				return lv, err
			}
		}

		*value = sizeBytes
	}

	lv.SegType = m[LVSegtype]
	lv.Host = m[LVHost]
	lv.PoolName = m[LVPool]
	lv.Permission = getIntFieldValue(LVPermissions, m[LVPermissions])
	lv.BehaviourWhenFull = getIntFieldValue(LVWhenFull, m[LVWhenFull])
	lv.HealthStatus = getIntFieldValue(LVHealthStatus, m[LVHealthStatus])
	lv.RaidSyncAction = getIntFieldValue(RaidSyncAction, m[RaidSyncAction])

	float64Map := map[string]*float64{
		LVDataPercent:     &lv.UsedSizePercent,
		LVMetadataPercent: &lv.MetadataUsedPercent,
		LVSnapPercent:     &lv.SnapshotUsedPercent,
	}
	for key, value := range float64Map {
		if m[key] == "" {
			count = 0
		} else {
			count, err = strconv.ParseFloat(m[key], 64)
			if err != nil {
				err = fmt.Errorf("invalid format of %v=%v for lv %v: %w", key, m[key], lv.Name, err)
				return lv, err
			}
		}

		*value = count
	}

	return lv, err
}

// decodeLvsJSON([]bytes): Decode json format and pass the unmarshalled json to parseLogicalVolume to store logical volumes in LogicalVolume
//
// Output of lvs command will be in json format:
//
//	{
//		"report": [
//			{
//				"lv": [
//						{
//							"lv_name":"pvc-082c7975-9af2-4a50-9d24-762612b35f94",
//							...
//						}
//					]
//			}
//		]
//	}
//
// This function is used to decode the output of lvs command.
// It returns []LogicalVolume.
//
//	Example: []LogicalVolume{
//		{
//			Name:               "pvc-082c7975-9af2-4a50-9d24-762612b35f94",
//			FullName:           "vg_thin/pvc-082c7975-9af2-4a50-9d24-762612b35f94"
//			UUID:               "FBqcEe-Ln72-SmWO-fR4j-t4Ga-1Y90-0vieKW"
//			Size:                4294967296,
//			Path:                "/dev/vg_thin/pvc-082c7975-9af2-4a50-9d24-762612b35f94",
//			DMPath:              "/dev/mapper/vg_thin-pvc--082c7975--9af2--4a50--9d24--762612b35f94"
//			Device:              "dm-5"
//			VGName:              "vg_thin"
//			SegType:             "thin"
//			Permission:          1
//			BehaviourWhenFull:   -1
//			HealthStatus:        0
//			RaidSyncAction:      -1
//			ActiveStatus:        "active"
//			Host:                "node1-virtual-machine"
//			PoolName:            "vg_thin_thinpool"
//			UsedSizePercent:     0
//			MetadataSize:        0
//			MetadataUsedPercent: 0
//			SnapshotUsedPercent: 0
//		}
//	}
func decodeLvsJSON(raw []byte) ([]LogicalVolume, error) {
	output := &struct {
		Report []struct {
			LogicalVolumes []map[string]string `json:"lv"`
		} `json:"report"`
	}{}

	var err error
	if err = json.Unmarshal(raw, output); err != nil {
		return nil, err
	}

	if len(output.Report) != 1 {
		return nil, errors.New("expected exactly one lvm report")
	}

	items := output.Report[0].LogicalVolumes

	lvs := make([]LogicalVolume, 0, len(items))
	for _, item := range items {
		var lv LogicalVolume
		if lv, err = parseLogicalVolume(item); err != nil {
			return lvs, err
		}

		deviceName, err := getLvDeviceName(lv.Path)
		if err != nil {
			klog.Error(err)
			return nil, err
		}

		lv.Device = deviceName
		lvs = append(lvs, lv)
	}

	return lvs, nil
}

func ListLVMLogicalVolume() ([]LogicalVolume, error) {
	args := []string{
		"--options", "lv_all,vg_name,segtype",
		"--reportformat", "json",
		"--units", "b",
	}
	cmd := exec.CommandContext(context.Background(), LVList, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Errorf("lvm: error while running command %s %v: %v", LVList, args, err)
		return nil, err
	}

	return decodeLvsJSON(output)
}

func ResizeVolume(vol LogicalVolume, sizeByte string, resizeFs bool) error {
	var args []string

	dev := DevPath + vol.VGName + "/" + vol.Name
	size := sizeByte + "b"

	// -r = --resizefs
	args = append(args, dev, "-L", size)
	if resizeFs {
		args = append(args, "-r")
	}

	cmd := exec.CommandContext(context.Background(), LVExtend, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return newExecError(output, err)
	}

	return nil
}

/*
ListLVMPhysicalVolume invokes `pvs` to list all the available LVM physical volumes in the node.
*/
func ListLVMPhysicalVolume() ([]PhysicalVolume, error) {
	if err := ReloadLVMMetadataCache(); err != nil {
		return nil, err
	}

	args := []string{
		"--options", "pv_all,vg_name",
		"--reportformat", "json",
		"--units", "b",
	}
	cmd := exec.CommandContext(context.Background(), PVList, args...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		klog.Errorf("lvm: error while running command %s %v: %v", PVList, args, err)
		return nil, err
	}

	return decodePvsJSON(output)
}

// To parse the output of pvs command and store it in PhysicalVolume
// It returns PhysicalVolume.
//
//	Example: PhysicalVolume{
//			Name:         "/dev/sdc",
//	     UUID:         "UAdQl0-dK00-gM1V-6Vda-zYeu-XUdQ-izs8KW"
//			Size:         21441282048
//			Used:         8657043456
//			Free:         12784238592
//			MetadataSize: 1044480
//			MetadataFree: 518656
//			DeviceSize:   21474836480
//			Allocatable:  "allocatable"
//			InUse:        "used"
//			Missing:      ""
//			VGName:       "vg_thin"
//		}
func parsePhysicalVolume(m map[string]string) (PhysicalVolume, error) {
	var (
		pv        PhysicalVolume
		err       error
		sizeBytes int64
	)

	pv.Name = m[PVName]
	pv.UUID = m[PVUUID]
	pv.InUse = m[PVInUse]
	pv.Allocatable = m[PVAllocatable]
	pv.Missing = m[PVMissing]
	pv.VGName = m[VGName]

	resQuantityMap := map[string]*resource.Quantity{
		PVSize:             &pv.Size,
		PVFreeSize:         &pv.Free,
		PVUsedSize:         &pv.Used,
		PVMetadataSize:     &pv.MetadataSize,
		PVMetadataFreeSize: &pv.MetadataFree,
		PVDeviceSize:       &pv.DeviceSize,
	}

	for key, value := range resQuantityMap {
		sizeBytes, err = strconv.ParseInt(
			strings.TrimSuffix(strings.ToLower(m[key]), "b"),
			10, 64)
		if err != nil {
			err = fmt.Errorf("invalid format of %v=%v for pv %v: %w", key, m[key], pv.Name, err)
			return pv, err
		}

		quantity := resource.NewQuantity(sizeBytes, resource.BinarySI)
		*value = *quantity
	}

	return pv, err
}

// decodeLvsJSON([]bytes): Decode json format and pass the unmarshalled json to parsePhysicalVolume to store physical volumes in PhysicalVolume
//
// Output of pvs command will be in json format:
//
//	{
//		"report": [
//			{
//				"pv": [
//						{
//							"pv_name":"/dev/sdc",
//							...
//						}
//					]
//			}
//		]
//	}
//
// This function is used to decode the output of pvs command.
// It returns []PhysicalVolume.
//
//	Example: []PhysicalVolume{
//		{
//			Name:         "/dev/sdc",
//	     UUID:         "UAdQl0-dK00-gM1V-6Vda-zYeu-XUdQ-izs8KW"
//			Size:         21441282048
//			Used:         8657043456
//			Free:         12784238592
//			MetadataSize: 1044480
//			MetadataFree: 518656
//			DeviceSize:   21474836480
//			Allocatable:  "allocatable"
//			InUse:        "used"
//			Missing:      ""
//			VGName:       "vg_thin"
//		}
//	}
func decodePvsJSON(raw []byte) ([]PhysicalVolume, error) {
	output := &struct {
		Report []struct {
			PhysicalVolume []map[string]string `json:"pv"`
		} `json:"report"`
	}{}

	var err error
	if err = json.Unmarshal(raw, output); err != nil {
		return nil, err
	}

	if len(output.Report) != 1 {
		return nil, errors.New("expected exactly one lvm report")
	}

	items := output.Report[0].PhysicalVolume

	pvs := make([]PhysicalVolume, 0, len(items))
	for _, item := range items {
		var pv PhysicalVolume
		if pv, err = parsePhysicalVolume(item); err != nil {
			return pvs, err
		}

		pvs = append(pvs, pv)
	}

	return pvs, nil
}

// lvThinExists verifies if thin pool/volume already exists for given volumegroup
