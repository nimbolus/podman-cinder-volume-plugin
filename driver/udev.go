package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"
)

var (
	errDeviceNotFound   = errors.New("device not found")
	errMoreThanOneFound = errors.New("more than one device with provided serial found")
)

func findDevWithSerial(expectedSerial string) (string, error) {
	entries, err := ioutil.ReadDir("/sys/class/block")
	if err != nil {
		return "", fmt.Errorf("could not read dir /sys/class/block: %v", err)
	}

	devices := make([]string, 0)
	for _, entry := range entries {
		sysname := entry.Name()

		var major, minor string
		major, minor, err = readUevent(sysname)
		if err != nil {
			return "", err
		}

		var devSerial string
		devSerial, err = readUdevData(major, minor)
		if err != nil {
			return "", err
		}

		if devSerial == expectedSerial {
			devices = append(devices, sysname)
		}
	}

	if len(devices) == 0 {
		return "", errDeviceNotFound
	}
	if len(devices) > 1 {
		return "", errMoreThanOneFound
	}

	return path.Join("/dev", devices[0]), nil
}

func readUevent(sysname string) (string, string, error) {
	uevent := path.Join("/sys/class/block", sysname, "uevent")

	f, err := os.Open(uevent)
	if err != nil {
		return "", "", fmt.Errorf("could not open %s: %v", uevent, err)
	}
	defer f.Close()

	var major, minor string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		tokens := strings.Split(scanner.Text(), "=")
		if tokens[0] == "MAJOR" {
			major = tokens[1]
		}
		if tokens[0] == "MINOR" {
			minor = tokens[1]
		}
	}
	if err = scanner.Err(); err != nil {
		return "", "", fmt.Errorf("could not parse uevent file: %v", err)
	}

	if major == "" || minor == "" {
		return "", "", fmt.Errorf("either or both MAJOR and/or MINOR fields were not found in %s", uevent)
	}

	return major, minor, nil
}

func readUdevData(major, minor string) (string, error) {
	udev := fmt.Sprintf("/run/udev/data/b%s:%s", major, minor)

	f, err := os.Open(udev)
	if err != nil {
		return "", fmt.Errorf("could not open %s: %v", udev, err)
	}
	defer f.Close()

	var serial string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		tokens := strings.Split(scanner.Text(), "=")
		if tokens[0] == "E:ID_SERIAL_SHORT" {
			serial = tokens[1]
			break
		}
	}
	if err = scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to parse udev data file: %v", err)
	}

	return serial, nil
}
