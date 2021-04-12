package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime/pprof"
	"strconv"

	"github.com/docker/docker/volume"
	"github.com/docker/go-plugins-helpers/sdk"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/sirupsen/logrus"
)

type VolumeCreateReq struct {
	Name string
	Opts VolumeCreateOpts
}

type VolumeCreateOpts struct {
	Size               string `json:"size"`
	AvailabilityZone   string `json:"availability_zone"`
	ConsistencyGroupID string `json:"consistency_group"`
	Description        string `json:"description"`
	SnapshotID         string `json:"source_snapshot"`
	BackupID           string `json:"source_backup"`
	VolumeType         string `json:"volume_type"`
	Uid                int    `json:"uid"`
	Gid                int    `json:"gid"`
	Mode               string `json:"mode"`
}

type VolumeCreateResp struct {
	Err string
}

type VolumeRemoveReq struct {
	Name string
}

type VolumeRemoveResp struct {
	Err string
}

type VolumeMountReq struct {
	Name string
	ID   string
}

type VolumeMountResp struct {
	Mountpoint string
	Err        string
}

type VolumePathReq struct {
	Name string
}

type VolumePathResp struct {
	Mountpoint string
	Err        string
}

type VolumeUnmountReq struct {
	Name string
	ID   string
}

type VolumeUnmountResp struct {
	Err string
}

type VolumeGetReq struct {
	Name string
}

type VolumeGetResp struct {
	Volume struct {
		Name       string
		Mountpoint string
		Status     map[string]interface{}
	}
	Err string
}

type VolumeListResp struct {
	Volumes []ListVolume
	Err     string
}

type ListVolume struct {
	Name       string
	Mountpoint string
}

func main() {
	if logLevelCfg, ok := os.LookupEnv("LOG_LEVEL"); ok {
		logLevel, err := logrus.ParseLevel(logLevelCfg)
		if err != nil {
			logrus.WithError(err).Fatal("Failed to parse log level.")
		}
		logrus.SetLevel(logLevel)
	}

	region, ok := os.LookupEnv("OS_REGION_NAME")
	if !ok {
		logrus.Fatal("No OS_REGION_NAME env var provided.")
	}

	defaultSize := 20
	if ds, ok := os.LookupEnv("DEFAULT_SIZE"); ok {
		var err error
		defaultSize, err = strconv.Atoi(ds)
		if err != nil {
			logrus.Fatal("Provided DEFAULT_SIZE is invalid: %v.", err)
		}
	}

	volumePrefix := os.Getenv("VOLUME_PREFIX")

	authOpts, err := openstack.AuthOptionsFromEnv()
	if err != nil {
		logrus.Fatal(err)
	}

	d, err := NewDriver(authOpts, region, defaultSize, volumePrefix)
	if err != nil {
		logrus.Fatal(fmt.Errorf("Could not create CinderDriver: %v.", err))
	}

	h := sdk.NewHandler(`{"Implements": ["VolumeDriver"]}`)
	setUpHandlers(&h, d)

	if debug, _ := os.LookupEnv("DEBUG"); debug != "" {
		h.HandleFunc("/pprof/trace", func(w http.ResponseWriter, r *http.Request) {
			_ = pprof.Lookup("goroutine").WriteTo(w, 1)
		})
	}

	logrus.Info("Start serving on UNIX socket...")
	if err := h.ServeUnix("cinder", 0); err != nil {
		panic(err)
	}
}

func setUpHandlers(h *sdk.Handler, d *CinderDriver) {
	h.HandleFunc("/VolumeDriver.Create", func(w http.ResponseWriter, r *http.Request) {
		logger := logrus.WithField("route", "/VolumeDriver.Create")
		logger.Debug("New request received")

		var req VolumeCreateReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		logger = logger.WithField("Req", fmt.Sprintf("%+v", req))

		resp := d.Create(logger, req)
		_ = json.NewEncoder(w).Encode(resp)
	})

	h.HandleFunc("/VolumeDriver.Remove", func(w http.ResponseWriter, r *http.Request) {
		logger := logrus.WithField("route", "/VolumeDriver.Remove")
		logger.Debug("New request received")

		var req VolumeRemoveReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		logger = logger.WithField("Req", fmt.Sprintf("%+v", req))

		resp := d.Remove(logger, req)
		_ = json.NewEncoder(w).Encode(resp)
	})

	h.HandleFunc("/VolumeDriver.Mount", func(w http.ResponseWriter, r *http.Request) {
		logger := logrus.WithField("route", "/VolumeDriver.Mount")
		logger.Debug("New request received")

		var req VolumeMountReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		logger = logger.WithField("Req", fmt.Sprintf("%+v", req))

		resp := d.Mount(logger, req)
		_ = json.NewEncoder(w).Encode(resp)
	})

	h.HandleFunc("/VolumeDriver.Path", func(w http.ResponseWriter, r *http.Request) {
		logger := logrus.WithField("route", "/VolumeDriver.Path")
		logger.Debug("New request received")

		var req VolumePathReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		logger = logger.WithField("Req", fmt.Sprintf("%+v", req))

		resp := d.Path(logger, req)
		_ = json.NewEncoder(w).Encode(resp)
	})

	h.HandleFunc("/VolumeDriver.Unmount", func(w http.ResponseWriter, r *http.Request) {
		logger := logrus.WithField("route", "/VolumeDriver.Unmount")
		logger.Debug("New request received")

		var req VolumeUnmountReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		logger = logger.WithField("Req", fmt.Sprintf("%+v", req))

		resp := d.Unmount(logger, req)
		_ = json.NewEncoder(w).Encode(resp)
	})

	h.HandleFunc("/VolumeDriver.Get", func(w http.ResponseWriter, r *http.Request) {
		logger := logrus.WithField("route", "/VolumeDriver.Get")
		logger.Debug("New request received")

		var req VolumeGetReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			logger.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		logger = logger.WithField("Req", fmt.Sprintf("%+v", req))

		resp := d.Get(logger, req)
		_ = json.NewEncoder(w).Encode(resp)
	})

	h.HandleFunc("/VolumeDriver.List", func(w http.ResponseWriter, r *http.Request) {
		logger := logrus.WithField("route", "/VolumeDriver.List")
		logger.Debug("New request received")

		resp := d.List(logger)
		_ = json.NewEncoder(w).Encode(resp)
	})

	h.HandleFunc("/VolumeDriver.Capabilities", func(w http.ResponseWriter, r *http.Request) {
		logrus.WithField("route", "/VolumeDriver.Capabilities").Debug("New request received")

		_ = json.NewEncoder(w).Encode(struct {
			Cap volume.Capability
		}{
			Cap: volume.Capability{Scope: volume.GlobalScope},
		})
	})
}
