package main

import (
	"bytes"
	"errors"
	"fmt"
	"gotcha/color"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type DiskUsage struct {
	MountPoint string
	Used       uint64
	Total      uint64
	UsedPct    float64
}

type Field struct {
	Name string
	Text string
}

func IsDisabled(name string) bool {
	if v, ok := config["DISABLE"]; ok {
		for p := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), name) {
				return true
			}
		}
	}
	return false
}

func FormatDuration(secs int) string {
	if secs < 0 {
		return "unknown duration"
	}

	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60

	return fmt.Sprintf("%dh %dm %ds", h, m, s)
}

func HumanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}

	div, exp := unit, 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	pre := "KMGTPE"[exp]

	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), pre)
}

var unknown string = color.Colorize("unknown", color.Red)

func GetDistro() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return unknown
	}

	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(value, `"'`)
		}
	}

	return unknown
}

func GetPkgCount(dbPath string) (int, error) {
	files, err := os.ReadDir(dbPath)
	if err != nil {
		return 0, err
	}
	pkgCount := 0
	for _, file := range files {
		if file.Type().IsDir() && file.Name()[0] != '.' {
			pkgCount++
		}
	}
	return pkgCount, nil
}

func GetPkgCounts() string {
	counts := make(map[string]int)

	pathExists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	// nix
	if pathExists("/run/current-system/sw/bin") {
		if entries, err := os.ReadDir("/run/current-system/sw/bin"); err == nil {
			counts["nix"] = len(entries)
		}
	}

	// dpkg
	if pathExists("/usr/bin/dpkg-query") {
		cmd := exec.Command("/usr/bin/dpkg-query", "-f", ".", "-W")
		if out, err := cmd.Output(); err == nil {
			counts["dpkg"] = len(out)
		}
	}

	// rpm
	if pathExists("/usr/bin/rpm") {
		cmd := exec.Command("/usr/bin/rpm", "-qa")
		if out, err := cmd.Output(); err == nil {
			counts["rpm"] = bytes.Count(out, []byte{'\n'})
		}
	}

	// pacman
	if pacmanPkgCount, err := GetPkgCount("/var/lib/pacman/local"); err == nil {
		counts["pacman"] = pacmanPkgCount
	}

	// flatpak
	if flatpakPkgCount, err := GetPkgCount("/var/lib/flatpak/app/"); err == nil {
		counts["flatpak"] = flatpakPkgCount
	}

	s := ""
	for k, v := range counts {
		if s != "" {
			s += ", "
		}
		s += fmt.Sprintf("%s - %d", k, v)
	}

	return s
}

func ParseMeminfo(total uint64, available uint64) string {
	if total == 0 {
		return unknown
	}
	totalBytes := total * 1024
	availableBytes := available * 1024
	usedBytes := totalBytes - availableBytes
	usedPct := float64(usedBytes) / float64(totalBytes) * 100

	var usageColor string
	switch {
	case usedPct >= 80:
		usageColor = color.BrightRed
	case usedPct >= 50:
		usageColor = color.BrightYellow
	default:
		usageColor = color.BrightGreen
	}

	return fmt.Sprintf("%s / %s (%s used)",
		HumanBytes(usedBytes),
		HumanBytes(totalBytes),
		color.Colorize(fmt.Sprintf("%.1f%%", usedPct), usageColor),
	)
}

func GetMemoryUsage() (string, string) {
	meminfo, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return unknown, unknown
	}

	var total, available uint64
	var swapTotal, swapFree uint64

	lines := bytes.SplitSeq(meminfo, []byte{'\n'})
	for line := range lines {
		if val, ok := bytes.CutPrefix(line, []byte("MemTotal:")); ok {
			total, _ = strconv.ParseUint(string(bytes.Fields(bytes.TrimSpace(val))[0]), 10, 64)
		} else if val, ok := bytes.CutPrefix(line, []byte("MemAvailable:")); ok {
			available, _ = strconv.ParseUint(string(bytes.Fields(bytes.TrimSpace(val))[0]), 10, 64)
		} else if val, ok := bytes.CutPrefix(line, []byte("SwapTotal:")); ok {
			swapTotal, _ = strconv.ParseUint(string(bytes.Fields(bytes.TrimSpace(val))[0]), 10, 64)
		} else if val, ok := bytes.CutPrefix(line, []byte("SwapFree:")); ok {
			swapFree, _ = strconv.ParseUint(string(bytes.Fields(bytes.TrimSpace(val))[0]), 10, 64)
		}
	}

	return ParseMeminfo(total, available), ParseMeminfo(swapTotal, swapFree)
}

func GetUptime() string {
	up, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return unknown
	}

	uptime := strings.Split(string(up), " ")[0]
	secs, err := strconv.ParseFloat(uptime, 64)
	if err != nil {
		return unknown
	}

	return FormatDuration(int(secs))
}

func GetShell() string {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = unknown
	}

	shell := strings.Split(sh, "/")

	return shell[len(shell)-1]
}

func GetBatteryCapacity() string {
	bat, err := os.ReadFile("/sys/class/power_supply/BAT0/capacity")
	if err != nil {
		return unknown
	}
	return strings.TrimSpace(string(bat))
}

func GetDisksUsage() []DiskUsage {
	mounts := config["MOUNTS"]
	if mounts == "" {
		mounts = "/boot,/"
	}
	var results []DiskUsage

	for mount := range strings.SplitSeq(mounts, ",") {
		stat := syscall.Statfs_t{}
		if err := syscall.Statfs(mount, &stat); err == nil {
			totalSpace := uint64(stat.Blocks) * uint64(stat.Bsize)
			occupiedSpace := totalSpace - (uint64(stat.Bfree) * uint64(stat.Bsize))
			results = append(results, DiskUsage{
				MountPoint: mount,
				Used:       occupiedSpace,
				Total:      totalSpace,
				UsedPct:    float64(occupiedSpace) / float64(totalSpace) * 100,
			})
		}
	}
	if len(results) == 0 {
		return nil
	}

	return results
}

func GetKernel() string {
	data, err := os.ReadFile("/proc/version")
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			return fields[2]
		}
	}

	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return unknown
	}
	return strings.TrimSpace(string(out))
}

func GetDE() string {
	de := os.Getenv("XDG_CURRENT_DESKTOP")
	if de == "" {
		de = os.Getenv("DESKTOP_SESSION")
		if de == "" {
			de = unknown
		}
	}

	return de
}

func GetCPU() string {
	cpuinfo, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return unknown
	}
	lines := bytes.SplitSeq(cpuinfo, []byte("\n"))

	for line := range lines {
		if out, ok := bytes.CutPrefix(line, []byte("model name\t:")); ok {
			return string(out)
		}
	}
	return unknown
}

const PciIDsPath = "/usr/share/hwdata/pci.ids"

func GetDeviceName(targetVendorID, targetDeviceID string) (string, error) {
	data, err := os.ReadFile(PciIDsPath)
	if err != nil {
		return "", err
	}

	targetVendor := []byte(targetVendorID)
	targetDevice := []byte(targetDeviceID)
	vendorLen := len(targetVendor)
	deviceLen := len(targetDevice)

	var vendorName []byte
	var vendorFound bool

	start := 0
	end := 0
	n := len(data)

	for end < n {
		for end < n && data[end] != '\n' {
			end++
		}
		line := data[start:end]
		end++
		start = end

		if len(line) == 0 || line[0] == '#' {
			continue
		}

		if line[0] != '\t' {
			vendorFound = false
			if len(line) > vendorLen && line[vendorLen] == ' ' &&
				bytes.Equal(line[:vendorLen], targetVendor) {
				vendorName = line[vendorLen+1:]
				vendorFound = true
			}
			continue
		}

		if vendorFound {
			i := 0
			for i < len(line) && line[i] == '\t' {
				i++
			}
			line = line[i:]

			if len(line) > deviceLen && line[deviceLen] == ' ' &&
				bytes.Equal(line[:deviceLen], targetDevice) {
				deviceName := line[deviceLen+1:]

				var out strings.Builder
				out.Grow(len(vendorName) + 1 + len(deviceName))
				out.Write(vendorName)
				out.WriteByte(' ')
				out.Write(deviceName)
				return out.String(), nil
			}
		}
	}

	return "", errors.New("not found")
}

const (
	VgaClassCode   = "0x030000\n"
	pciDevicesPath = "/sys/bus/pci/devices/"
)

func GetGPU() string {
	deviceDirs, err := os.ReadDir(pciDevicesPath)
	if err != nil {
		return unknown
	}
	for _, dir := range deviceDirs {
		devicePath := filepath.Join(pciDevicesPath, dir.Name())
		classFilePath := filepath.Join(devicePath, "class")
		classContent, err := os.ReadFile(classFilePath)
		if err != nil {
			continue
		}
		if bytes.HasPrefix(classContent, []byte(VgaClassCode)) {
			ueventPath := filepath.Join(devicePath, "uevent")
			ueventContent, err := os.ReadFile(ueventPath)
			if err != nil {
				return unknown
			}
			for line := range strings.SplitSeq(string(ueventContent), "\n") {
				if id, hasID := strings.CutPrefix(line, "PCI_ID="); hasID {
					lowerId := strings.ToLower(id)
					parts := strings.Split(lowerId, ":")
					if name, err := GetDeviceName(parts[0], parts[1]); err == nil {
						return name
					}
					return unknown
				}
			}
		}
	}
	return unknown
}
