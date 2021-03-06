package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/procfs"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const VERSION = "gnw-0.0.1"

type Data struct {
	XMLName    xml.Name `xml:"data"`
	SystemData struct {
		Status      string `xml:"status"`
		Hostname    string `xml:"hostname"`
		Description string `xml:"description"`
		Geo         struct {
			Lat float64 `xml:"lat"`
			Lng float64 `xml:"lng"`
		} `xml:"geo"`
		PositionComment              string   `xml:"position_comment"`
		Contact                      string   `xml:"contact"`
		Hood                         string   `xml:"hood"`
		Hoodid                       string   `xml:"hoodid"`
		Distname                     string   `xml:"distname"`
		Distversion                  string   `xml:"distversion"`
		Chipset                      string   `xml:"chipset"`
		Cpu                          []string `xml:"cpu"`
		Model                        string   `xml:"model"`
		MemoryTotal                  int      `xml:"memory_total"`
		MemoryFree                   int      `xml:"memory_free"`
		MemoryBuffering              int      `xml:"memory_buffering"`
		MemoryCaching                int      `xml:"memory_caching"`
		Loadavg                      float64  `xml:"loadavg"`
		Processes                    string   `xml:"processes"`
		Uptime                       int64    `xml:"uptime"`
		Idletime                     float64  `xml:"idletime"`
		LocalTime                    int64    `xml:"local_time"`
		BatmanAdvancedVersion        string   `xml:"batman_advanced_version"`
		KernelVersion                string   `xml:"kernel_version"`
		NodewatcherVersion           string   `xml:"nodewatcher_version"`
		FirmwareVersion              string   `xml:"firmware_version"`
		FirmwareRevision             string   `xml:"firmware_revision"`
		OpenwrtCoreRevision          string   `xml:"openwrt_core_revision"`
		OpenwrtFeedsPackagesRevision string   `xml:"openwrt_feeds_packages_revision"`
		VpnActive                    int      `xml:"vpn_active"`
	} `xml:"system_data"`
	InterfaceData struct {
		Interfaces []Interface
	} `xml:"interface_data"`
	BatmanAdvInterfaces  string `xml:"batman_adv_interfaces"`
	BatmanAdvOriginators string `xml:"batman_adv_originators"`
	BatmanAdvGatewayMode string `xml:"batman_adv_gateway_mode"`
	BatmanAdvGatewayList string `xml:"batman_adv_gateway_list"`
	BabelNeighbours      struct {
		Neighbours []BabelNeighbour `xml:"neighbour"`
	} `xml:"babel_neighbours"`
	ClientCount int `xml:"client_count"`
	Clients     struct {
		Num []ClientNum
	} `xml:"clients"`
}

type BabelNeighbour struct {
	MacAddr           string `xml:",chardata"`
	OutgoingInterface string `xml:"outgoing_interface"`
}

type Interface struct {
	XMLName   xml.Name
	Name      string `xml:"name"`
	Mtu       int    `xml:"mtu"`
	MacAddr   string `xml:"mac_addr"`
	TrafficRx uint64 `xml:"traffic_rx"`
	TrafficTx uint64 `xml:"traffic_tx"`
}

type ClientNum struct {
	XMLName xml.Name
	N       int `xml:",chardata"`
}

func getBabelNeighbours() []BabelNeighbour {
	conn, err := net.Dial("tcp6", "[::1]:33123")
	if err != nil {
		return nil
	}
	defer conn.Close()

	go fmt.Fprintln(conn, "dump")

	scanner := bufio.NewScanner(conn)

	var neighs []BabelNeighbour
	// skip the startup "ok"
	for scanner.Scan() {
		if scanner.Text() == "ok" {
			break
		}
	}
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 1 && fields[0] == "ok" {
			break
		}
		if len(fields) < 21 || fields[1] != "neighbour" {
			continue
		}
		neighs = append(neighs, BabelNeighbour{
			MacAddr:           fields[4],
			OutgoingInterface: fields[6],
		})

	}
	if scanner.Err() != nil {
		return nil
	}

	return neighs
}

func crawl() (d Data, err error) {
	stat, err := procfs.NewStat()
	if err != nil {
		return
	}

	{
		var mem meminfo
		mem, err = readMeminfo()
		if err != nil {
			return
		}

		var load loadavg
		load, err = readLoadavg()
		if err != nil {
			return
		}

		var sysinfo unix.Sysinfo_t
		if err = unix.Sysinfo(&sysinfo); err != nil {
			return
		}

		d.SystemData.Status = "online"
		d.SystemData.Idletime = stat.CPUTotal.Idle
		d.SystemData.Loadavg = load.load15
		d.SystemData.LocalTime = time.Now().Unix()
		d.SystemData.MemoryBuffering = mem.Buffers
		d.SystemData.MemoryCaching = mem.Cached
		d.SystemData.MemoryFree = mem.MemFree
		d.SystemData.MemoryTotal = mem.MemTotal
		d.SystemData.Processes = fmt.Sprintf("%d/%d", load.runnable, load.procs)
		d.SystemData.Uptime = sysinfo.Uptime
	}

	{
		var utsname unix.Utsname
		if err = unix.Uname(&utsname); err != nil {
			return
		}
		d.SystemData.KernelVersion = string(bytes.Trim(utsname.Release[:], "\x00"))
	}

	ifs, err := net.Interfaces()
	if err != nil {
		return
	}
	for _, i := range ifs {
		// skip lo
		if i.Name == "lo" {
			continue
		}

		link, err := netlink.LinkByIndex(i.Index)
		if err != nil {
			return d, err
		}

		d.InterfaceData.Interfaces = append(d.InterfaceData.Interfaces, Interface{
			XMLName: xml.Name{
				Local: i.Name,
			},
			Name:      i.Name,
			Mtu:       i.MTU,
			MacAddr:   i.HardwareAddr.String(),
			TrafficRx: link.Attrs().Statistics.RxBytes,
			TrafficTx: link.Attrs().Statistics.TxBytes,
		})

		neighAddrs := map[string]struct{}{}
		neighs, err := netlink.NeighList(i.Index, netlink.FAMILY_ALL)
		if err != nil {
			return d, err
		}

		for _, neigh := range neighs {
			if neigh.State&netlink.NUD_REACHABLE > 0 {
				neighAddrs[neigh.HardwareAddr.String()] = struct{}{}
			}
		}

		count := len(neighAddrs)
		d.ClientCount += count
		d.Clients.Num = append(d.Clients.Num, ClientNum{
			XMLName: xml.Name{
				Local: i.Name,
			},
			N: count,
		})
	}

	d.BabelNeighbours.Neighbours = getBabelNeighbours()
	return d, err
}

func parseUtsString(s [65]int8) string {
	var buf strings.Builder
	for _, c := range s {
		if c == 0 {
			break
		}
		buf.WriteByte(byte(c))
	}
	return buf.String()
}

func main() {
	c, err := getConfig()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	d, err := crawl()
	if err != nil {
		panic(err)
	}

	d.SystemData.Hostname = c.Hostname
	d.SystemData.Hood = c.Hood
	d.SystemData.Contact = c.Contact
	d.SystemData.Distname = c.Distname
	d.SystemData.Distversion = c.Distversion
	d.SystemData.FirmwareVersion = "Generic"
	d.SystemData.Geo.Lat = c.Lat
	d.SystemData.Geo.Lng = c.Lng
	d.SystemData.NodewatcherVersion = VERSION

	if c.Debug {
		fmt.Println("XML Output:")
		e := xml.NewEncoder(os.Stdout)
		e.Indent("", "\t")
		if err := e.Encode(d); err != nil {
			panic(err)
		}
		fmt.Println()
	}

	xpayload, err := xml.Marshal(d)
	if err != nil {
		panic(err)
	}

	if c.Debug {
		fmt.Println()
		fmt.Println("XML Payload:")
		fmt.Println()
		fmt.Println(string(xpayload))
	}

	var buf bytes.Buffer

	fmt.Fprintf(&buf, `{%q: {%q: %q}}`, "64", d.InterfaceData.Interfaces[0].MacAddr, `<?xml version='1.0' standalone='yes'?>`+string(xpayload))

	if c.Debug {
		fmt.Println()
		fmt.Println("JSON Output:")
		fmt.Println()
		fmt.Println(buf.String())
	}

	if !c.Dry {
		resp, err := http.Post("https://monitoring.freifunk-franken.de/api/alfred", "application/json; charset=UTF-8", &buf)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		if c.Debug {
			fmt.Println()
			fmt.Println("HTTP Response:")
			fmt.Println()
			io.Copy(os.Stdout, resp.Body)
		}
	}
}
