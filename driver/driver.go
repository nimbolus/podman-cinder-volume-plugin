package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/volumeattach"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const propagatedMount = "/var/lib/cinder"
const metadataFieldUID = "docker-volume-driver:uid"
const metadataFieldGID = "docker-volume-driver:gid"
const metadataFieldMode = "docker-volume-driver:mode"

type CinderDriver struct {
	storageClient *gophercloud.ServiceClient
	computeClient *gophercloud.ServiceClient
	defaultSize   int
	serverID      string
	volumePrefix  string
}

func NewDriver(authOpts gophercloud.AuthOptions, region string, defaultSize int, volumePrefix string) (*CinderDriver, error) {
	provider, err := openstack.AuthenticatedClient(authOpts)
	if err != nil {
		return nil, fmt.Errorf("could not create the provider client: %v", err)
	}

	endpointsOpts := gophercloud.EndpointOpts{
		Region: region,
	}

	storageClient, err := openstack.NewBlockStorageV3(provider, endpointsOpts)
	if err != nil {
		return nil, fmt.Errorf("could not create the block storage v3 client: %v", err)
	}

	computeClient, err := openstack.NewComputeV2(provider, endpointsOpts)
	if err != nil {
		return nil, fmt.Errorf("could not create the compute v2 client: %v", err)
	}

	serverID, err := getInstanceIDFromMetadataServer()
	if err != nil {
		return nil, fmt.Errorf("could not retrieve the ID of the OpenStack instance from metadata server: %v", err)
	}

	d := &CinderDriver{
		storageClient: storageClient,
		computeClient: computeClient,
		defaultSize:   defaultSize,
		serverID:      serverID,
		volumePrefix:  volumePrefix,
	}

	return d, nil
}

func (d *CinderDriver) Create(logger *logrus.Entry, req VolumeCreateReq) VolumeCreateResp {
	resp := VolumeCreateResp{}

	if !strings.HasPrefix(req.Name, d.volumePrefix) {
		resp.Err = fmt.Sprintf("volume name should be prefixed with %s", d.volumePrefix)
		return resp
	}

	size := d.defaultSize
	if req.Opts.Size != "" {
		var err error
		size, err = strconv.Atoi(req.Opts.Size)
		if err != nil {
			resp.Err = fmt.Sprintf("could not convert volume size (%s) from string to int: %v", req.Opts.Size, err)
			logger.Error(resp.Err)

			return resp
		}
	}

	opts := volumes.CreateOpts{
		Name:               req.Name,
		Size:               size,
		AvailabilityZone:   req.Opts.AvailabilityZone,
		ConsistencyGroupID: req.Opts.ConsistencyGroupID,
		Description:        req.Opts.Description,
		SnapshotID:         req.Opts.SnapshotID,
		BackupID:           req.Opts.BackupID,
		VolumeType:         req.Opts.VolumeType,
		Metadata: map[string]string{
			metadataFieldUID:  req.Opts.Uid,
			metadataFieldGID:  req.Opts.Gid,
			metadataFieldMode: req.Opts.Mode,
		},
	}

	vol, err := volumes.Create(d.storageClient, opts).Extract()
	if err != nil {
		resp.Err = fmt.Sprintf("could not create volume %s: %v", req.Name, err)
		logger.Error(resp.Err)

		return resp
	}

	if err := volumes.WaitForStatus(d.storageClient, vol.ID, "available", 60); err != nil {
		resp.Err = fmt.Sprintf("error waiting for volume creation to complete: %v", err)
		logger.Error(resp.Err)

		return resp
	}

	return resp
}

func (d *CinderDriver) Remove(logger *logrus.Entry, req VolumeRemoveReq) VolumeRemoveResp {
	resp := VolumeRemoveResp{}

	vol, err := d.findVolume(req.Name)
	if err != nil {
		resp.Err = err.Error()
		logger.Error(resp.Err)

		return resp
	}

	// Unmount() doesn't detach the volume from the server to make it faster to
	// mount it again later. As such, if the volume isn't mounted but is attached,
	// we have to detach it first.
	mountpoint := path.Join(propagatedMount, vol.ID)
	mounted, err := isMounted(mountpoint)
	if err != nil {
		resp.Err = fmt.Sprintf("checking if volume is still mounted: %v", err)
		logger.Error(resp.Err)

		return resp
	} else if err == nil && mounted {
		resp.Err = "volume is still mounted"
		logger.Error(resp.Err)

		return resp
	}

	if len(vol.Attachments) > 0 {
		if err := d.detachVolume(logger, vol, false, true); err != nil {
			resp.Err = fmt.Sprintf("detaching volume from current server: %v", err)
			logger.Error(resp.Err)

			return resp
		}
	}

	osResp := volumes.Delete(d.storageClient, vol.ID, nil)
	if err := osResp.ExtractErr(); err != nil {
		resp.Err = fmt.Sprintf("failed to delete volume: %v", err)
		logger.Error(resp.Err)

		return resp
	}

	err = d.waitForVolumeDeletion(vol.ID, 60)
	if err != nil {
		resp.Err = fmt.Sprintf("error waiting for volume deletion to complete: %v", err)
	}

	return resp
}

func (d *CinderDriver) waitForVolumeDeletion(volID string, secs int) error {
	url := d.storageClient.ServiceURL("volumes", volID)

	return gophercloud.WaitFor(secs, func() (bool, error) {
		ret, err := d.storageClient.Get(url, nil, nil)

		if err != nil {
			if ret != nil && ret.StatusCode == 404 {
				return true, nil
			}

			return false, err
		}

		return false, nil
	})
}

var errVolumeNotFound = errors.New("volume not found")

func (d *CinderDriver) findVolume(name string) (volumes.Volume, error) {
	vols, err := d.listVolumes()
	if err != nil {
		return volumes.Volume{}, fmt.Errorf("failed to find volume %s: %v", name, err)
	}

	for _, vol := range vols {
		if vol.Name == name {
			return vol, nil
		}
	}

	return volumes.Volume{}, errVolumeNotFound
}

func (d *CinderDriver) Mount(logger *logrus.Entry, req VolumeMountReq) VolumeMountResp {
	resp := VolumeMountResp{}

	vol, err := d.findVolume(req.Name)
	if err != nil {
		resp.Err = err.Error()
		logger.Error(resp.Err)

		return resp
	}

	logger = logger.WithField("VolID", vol.ID)

	var dev string // Device path (under /dev).
	// Indicates whether the volume is already attached to the current server. It's used to not
	// try to reattach the volume if it's already attached and save time.
	var alreadyAttached bool

	dev, err = findDevWithSerial(vol.ID)
	if err != nil && err != errDeviceNotFound {
		resp.Err = fmt.Sprintf("failed to probe if %s is already attached: %v", req.Name, err)
		logger.Error(resp.Err)

		return resp
	} else if err == nil {
		alreadyAttached = true
	}

	if vol.Multiattach == false && len(vol.Attachments) > 0 {
		if err := d.detachVolume(logger, vol, true, false); err != nil {
			resp.Err = err.Error()
			logger.Error(resp.Err)

			return resp
		}
	}

	if !alreadyAttached {
		dev, err = d.attachVolume(logger, vol)
		if err != nil {
			resp.Err = err.Error()
			logger.Error(resp.Err)

			return resp
		}
	}

	logger = logger.WithField("Device", dev)

	if fsDetected, err := isExt4(dev); err != nil {
		resp.Err = err.Error()
		logger.Error(resp.Err)

		return resp
	} else if !fsDetected {
		logger.Info("No filesystem detected. Formatting...")

		if err := d.format(dev); err != nil {
			resp.Err = err.Error()
			logger.Error(resp.Err)

			return resp
		}
	}

	mountpoint := path.Join(propagatedMount, vol.ID)
	logger = logger.WithField("mountpoint", mountpoint)

	if mounted, err := isMounted(mountpoint); err != nil {
		resp.Err = fmt.Sprintf("checking if dev is already mounted: %v", err)
		logger.Error(resp.Err)

		return resp
	} else if !mounted {
		logger.Debug("Mounting the filesystem...")
		if err := d.mount(dev, mountpoint); err != nil {
			resp.Err = fmt.Sprintf("failed to mount volume %s: %v", req.Name, err)
			logger.Error(resp.Err)

			return resp
		}
	}

	// rexray/cinder uses the data subfolder as mountpoint, so we need to do the same to be compatible.
	datadir := path.Join(mountpoint, "data")
	if _, err := os.Stat(datadir); err != nil && !os.IsNotExist(err) {
		resp.Err = fmt.Sprintf("stat %s failed: %v", datadir, err)
		logger.Error(resp.Err)

		return resp
	} else if os.IsNotExist(err) {
		uid, gid, mode, err := getPermsMetadata(vol)
		if err != nil {
			resp.Err = err.Error()
			logger.Error(resp.Err)

			return resp
		}

		logger.Debugf("Create the datadir with filemode and perms: %#o %d:%d.", mode, uid, gid)

		if err := os.Mkdir(datadir, os.FileMode(mode)); err != nil {
			resp.Err = err.Error()
			logger.Error(resp.Err)

			return resp
		}
		if err := os.Chown(datadir, uid, gid); err != nil {
			resp.Err = err.Error()
			logger.Error(resp.Err)

			return resp
		}
	}

	resp.Mountpoint = datadir

	return resp
}

func (d *CinderDriver) attachVolume(logger *logrus.Entry, vol volumes.Volume) (string, error) {
	att, err := volumeattach.Create(d.computeClient, d.serverID, &volumeattach.CreateOpts{
		VolumeID: vol.ID,
	}).Extract()
	if err != nil {
		return "", fmt.Errorf("failed to attach volume %s: %v", vol.Name, err)
	}

	if err := d.waitForVolumeAttachStatus(att.VolumeID, att.ServerID, true, 60*time.Second); err != nil {
		return "", fmt.Errorf("error waiting for volume %s to be attached: %v", vol.Name, err)
	}

	logger.Debugf("Volume %s has been attached to server %s.", vol.Name, d.serverID)

	// The value in att.Device is guessed by OpenStack based on the number of volumes attached
	// to the instance. When two volumes are attached at the same time, OpenStack might assign a letter
	// to the volume that doesn't match what Linux assigns. For instance, OpenStack guesses vol1 gets sdb
	// and vol2 gets sdc whereas Linux actually assigns sdc to vol1 and sdb to vol2. Another edge case: udev
	// might rename the device based on some rules, making OpenStack guesses wrong.
	// The only way to actually know what device name is assigned to a Block Storage disk is to read the serial
	// number of disks attached to the instance and find the one matching the UUID of the Block Storage volume.
	//
	// The plugin might try to read the udev file for the newly attached disk before the kernel make it available,
	// so better wait a bit before reading it.
	time.Sleep(200 * time.Millisecond)

	return findDevWithSerial(att.VolumeID)
}

func (d *CinderDriver) detachVolume(logger *logrus.Entry, vol volumes.Volume, skipCurrent, onlyCurrent bool) error {
	for _, att := range vol.Attachments {
		if skipCurrent && att.ServerID == d.serverID {
			continue
		}
		if onlyCurrent && att.ServerID != d.serverID {
			continue
		}

		r := volumeattach.Delete(d.computeClient, att.ServerID, vol.ID)
		if err := r.ExtractErr(); err != nil {
			return fmt.Errorf("could not detach volume %s from server %s: %v", vol.Name, att.ServerID, err)
		}

		if err := d.waitForVolumeAttachStatus(att.VolumeID, att.ServerID, false, 60*time.Second); err != nil {
			return fmt.Errorf("error waiting for volume %s to be detached from server %s: %v", vol.Name, att.ServerID, err)
		}

		logger.Debugf("Volume %s has been detached from server %s.", vol.Name, att.ServerID)
	}

	return nil
}

func (d *CinderDriver) waitForVolumeAttachStatus(volID, serverID string, attachmentNeeded bool, timeout time.Duration) error {
	return gophercloud.WaitFor(int(timeout.Seconds()), func() (bool, error) {
		vol, err := volumes.Get(d.storageClient, volID).Extract()

		if err != nil {
			return false, err
		}

		for _, att := range vol.Attachments {
			if att.ServerID == serverID {
				if attachmentNeeded {
					return true, nil
				} else {
					return false, nil
				}
			}
		}

		if !attachmentNeeded {
			return true, nil
		}

		return false, nil
	})
}

func (d *CinderDriver) mount(dev, mountpoint string) error {
	if err := os.MkdirAll(mountpoint, 0750); err != nil {
		return fmt.Errorf("failed to create mountpoint directory %s: %v", mountpoint, err)
	}

	mountFlags := uintptr(unix.MS_RELATIME)
	if err := unix.Mount(dev, mountpoint, "ext4", mountFlags, ""); err != nil {
		return fmt.Errorf("mount syscall failed: %v", err)
	}

	return nil
}

func (d *CinderDriver) format(dev string) error {
	if err := exec.Command("mkfs.ext4", "-F", dev).Run(); err != nil {
		return fmt.Errorf("mkfs.ext4 on %s failed: %v", dev, err)
	}

	return nil
}

func getPermsMetadata(vol volumes.Volume) (int, int, int, error) {
	var err error

	uid := 0
	if v, ok := vol.Metadata[metadataFieldUID]; ok && v != "" {
		if uid, err = strconv.Atoi(v); err != nil {
			err = fmt.Errorf("reading %s: %v", metadataFieldUID, err)
			return 0, 0, 0, err
		}
	}

	gid := 0
	if v, ok := vol.Metadata[metadataFieldGID]; ok && v != "" {
		if gid, err = strconv.Atoi(v); err != nil {
			err = fmt.Errorf("reading %s: %v", metadataFieldGID, err)
			return 0, 0, 0, err
		}
	}

	mode := 0750
	if v, ok := vol.Metadata[metadataFieldMode]; ok && v != "" {
		if mode, err = strconv.Atoi(v); err != nil {
			err = fmt.Errorf("reading %s: %v", metadataFieldMode, err)
			return 0, 0, 0, err
		}
	}

	return uid, gid, mode, nil
}

// See:
//   - https://ext4.wiki.kernel.org/index.php/Ext4_Disk_Layout#Layout
//   - https://ext4.wiki.kernel.org/index.php/Ext4_Disk_Layout#The_Super_Block
var EXT4_SUPER_MAGIC_OFFSET = 1024 + 0x38
var EXT4_SUPER_MAGIC = []byte("\x53\xEF")

func isExt4(dev string) (bool, error) {
	f, err := os.Open(dev)
	if err != nil {
		return false, fmt.Errorf("opening device to read ext4 magic number: %v", err)
	}
	defer f.Close()

	buf := make([]byte, len(EXT4_SUPER_MAGIC))
	l, err := f.ReadAt(buf, int64(EXT4_SUPER_MAGIC_OFFSET))
	if err != nil {
		return false, fmt.Errorf("reading ext4 magic number: %v", err)
	}

	if l != len(EXT4_SUPER_MAGIC) {
		return false, errors.New("reading ext4 magic number: could not read enough bytes")
	}

	return bytes.Equal(EXT4_SUPER_MAGIC, buf), nil
}

func (d *CinderDriver) Path(logger *logrus.Entry, req VolumePathReq) VolumePathResp {
	resp := VolumePathResp{}

	vol, err := d.findVolume(req.Name)
	if err != nil {
		resp.Err = err.Error()
		logger.Error(resp.Err)

		return resp
	}

	logger = logger.WithField("VolID", vol.ID)

	mountpoint := path.Join(propagatedMount, vol.ID)
	if ok, err := isMounted(mountpoint); err != nil {
		resp.Err = fmt.Sprintf("checking if volume %s is already mounted: %v", req.Name, err)
		logger.Error(resp.Err)

		return resp
	} else if !ok {
		resp.Err = "volume not mounted"
		return resp
	}

	resp.Mountpoint = path.Join(mountpoint, "data")
	return resp
}

func (d *CinderDriver) Unmount(logger *logrus.Entry, req VolumeUnmountReq) VolumeUnmountResp {
	resp := VolumeUnmountResp{}

	vol, err := d.findVolume(req.Name)
	if err != nil {
		resp.Err = err.Error()
		logger.Error(resp.Err)

		return resp
	}

	logger = logger.WithField("VolID", vol.ID)

	mountpoint := path.Join(propagatedMount, vol.ID)
	if ok, err := isMounted(mountpoint); err != nil {
		resp.Err = fmt.Sprintf("checking if volume %s is already mounted: %v", req.Name, err)
		logger.Error(resp.Err)

		return resp
	} else if !ok {
		resp.Err = fmt.Sprintf("volume %s is not mounted", req.Name)
		return resp
	}

	if err := unix.Unmount(mountpoint, 0); err != nil {
		resp.Err = fmt.Sprintf("unmounting volume %s: %v", req.Name, err)
		logger.Error(resp.Err)

		return resp
	}

	if err := os.Remove(mountpoint); err != nil {
		logger.Error("failed to remove mountpoint directory %s after unmount: %v", mountpoint, err)
	}

	// We don't try to detach the volume from the server to save time
	// if the next mount happens on the same server.

	return resp
}

func isMounted(expectedMountpoint string) (bool, error) {
	mounts, err := listMounts()
	if err != nil {
		return false, fmt.Errorf("checking if a device is mounted on %s: %v", expectedMountpoint, err)
	}

	for _, mountpoint := range mounts {
		if mountpoint == expectedMountpoint {
			return true, nil
		}
	}

	return false, nil
}

func listMounts() (map[string]string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return map[string]string{}, fmt.Errorf("opening /proc/mounts: %v", err)
	}

	mounts := map[string]string{}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		w := strings.Split(scanner.Text(), " ")

		if !strings.HasPrefix(w[1], propagatedMount) {
			continue
		}

		mounts[w[0]] = w[1]
	}

	return mounts, nil
}

func (d *CinderDriver) Get(logger *logrus.Entry, req VolumeGetReq) VolumeGetResp {
	resp := VolumeGetResp{}

	vol, err := d.findVolume(req.Name)
	if err != nil {
		resp.Err = err.Error()
		logger.Error(resp.Err)

		return resp
	}

	resp.Volume.Name = vol.Name
	resp.Volume.Status = map[string]interface{}{
		"ID":                 vol.ID,
		"AvailabilityZone":   vol.AvailabilityZone,
		"ConsistencyGroupID": vol.ConsistencyGroupID,
		"Description":        vol.Description,
		"Size":               vol.Size,
		"Type":               vol.VolumeType,
		"CreatedAt":          vol.CreatedAt.String(),
		"UpdatedAt":          vol.UpdatedAt.String(),
		"Metadata":           vol.Metadata,
	}

	mountpoint := path.Join(propagatedMount, vol.ID)
	if ok, err := isMounted(mountpoint); err != nil {
		resp.Err = fmt.Sprintf("checking if volume %s is already mounted: %v", req.Name, err)
		logger.Error(resp.Err)

		return resp
	} else if ok {
		resp.Volume.Mountpoint = path.Join(mountpoint, "data")
	}

	return resp
}

func (d *CinderDriver) List(logger *logrus.Entry) VolumeListResp {
	resp := VolumeListResp{
		Volumes: make([]ListVolume, 0),
	}

	osVols, err := d.listVolumes()
	if err != nil {
		resp.Err = err.Error()
		logger.Error(err)

		return resp
	}

	for _, vol := range osVols {
		v := ListVolume{
			Name:       vol.Name,
			Mountpoint: "",
		}

		mountpoint := path.Join(propagatedMount, vol.ID)
		if ok, err := isMounted(mountpoint); err != nil {
			resp.Err = fmt.Sprintf("checking if volume %s is already mounted: %v", vol.Name, err)
			logger.Error(resp.Err)

			return resp
		} else if ok {
			v.Mountpoint = path.Join(mountpoint, "data")
		}

		resp.Volumes = append(resp.Volumes, v)
	}

	return resp
}

func (d *CinderDriver) listVolumes() ([]volumes.Volume, error) {
	vols := make([]volumes.Volume, 0)

	allPages, err := volumes.List(d.storageClient, nil).AllPages()
	if err != nil {
		return vols, fmt.Errorf("listing openstack volumes: %v", err)
	}

	list, err := volumes.ExtractVolumes(allPages)
	if err != nil {
		return vols, fmt.Errorf("extracing openstack volumes from api response: %v", err)
	}

	for _, v := range list {
		if strings.HasPrefix(v.Name, d.volumePrefix) {
			vols = append(vols, v)
		}
	}

	return vols, err
}

func getInstanceIDFromMetadataServer() (string, error) {
	const metadataURL = "http://169.254.169.254/openstack/latest/meta_data.json"
	httpClient := http.Client{
		Timeout: time.Duration(5 * time.Second),
	}

	resp, err := httpClient.Get(metadataURL)
	if err != nil {
		return "", fmt.Errorf("error getting server metadata from openstack api: %v", err)
	}
	defer resp.Body.Close()

	metadataBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("io error reading metadata: %v", err)
	}

	return parseUUID(metadataBytes)
}

func parseUUID(metadata []byte) (string, error) {
	var decodedJSON interface{}
	err := json.Unmarshal(metadata, &decodedJSON)
	if err != nil {
		return "", fmt.Errorf("error unmarshalling metadata: %v", err)
	}
	decodedJSONMap, ok := decodedJSON.(map[string]interface{})
	if !ok {
		return "", errors.New("error casting metadata decoded JSON")
	}
	uuid, ok := decodedJSONMap["uuid"].(string)
	if !ok {
		return "", errors.New("error casting metadata uuid field")
	}

	return uuid, nil
}
