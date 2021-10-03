package skyfi

import (
	"fmt"
	"net"
	"strings"
	"time"

	reuse "github.com/libp2p/go-reuseport"
	log "github.com/sirupsen/logrus"
)

const (
	SKYFI_PORT = 4031
)

/*
 * Based on the client IP, we need to figure out what local IP to respond with.
 */
func findIPinCIDR(ip string) (string, error) {
	ip_check := net.ParseIP(ip)

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("Unable to query network interfaces: %s", err.Error())
	}

	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			log.Errorf("Unable to query addresses for %s: %s", iface.Name, err.Error())
			continue
		}

		for _, addr := range addrs {
			ip, network, err := net.ParseCIDR(addr.String())
			if err != nil {
				continue
			}
			if ip.To4() == nil {
				continue // skip non-IPv4 addresses
			}

			if network.Contains(ip_check) {
				return ip.String(), nil
			}
		}
	}
	return "", fmt.Errorf("Unable to find a local IP for %s", ip)
}

/*
 * Go Routine for handling SkyFi Discovery
 */
func ReplyDiscover() {
	pc, err := reuse.ListenPacket("udp4", fmt.Sprintf(":%d", SKYFI_PORT))
	if err != nil {
		log.Errorf("Unable to open SkyFi discovery listen socket: %s", err.Error())
		return
	}
	defer pc.Close()
	log.Infof("Starting SkyFi Discovery service on UDP/%d", SKYFI_PORT)

	buf := make([]byte, 1024)
	for true {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			log.Warnf("Unable to read from SkyFi discovery listen socket: %s", err.Error())
			continue
		}

		bufs := string(buf)
		if !strings.HasPrefix(bufs, "skyfi") {
			log.Warnf("Unexpected query of %d bytes from %s: %s", n, addr.String(), bufs)
			continue
		}

		// replace the '?' at the end with a '@'
		if buf[n-1] == 0x3f {
			buf[n-1] = 0x40
		}
		send_buf := []byte{}
		send_buf = append(send_buf, buf[:n]...)

		// figure out our local IP to thie client and append to end of reply
		client := strings.Split(addr.String(), ":")
		ip, err := findIPinCIDR(client[0])
		if err != nil {
			log.Errorf("%s", err.Error())
			continue
		}
		send_buf = append(send_buf, []byte(ip)...)

		_, err = pc.WriteTo(send_buf, addr)
		if err != nil {
			log.Errorf("Unable to send SkyFi discovery reply: %s", err.Error())
		}
	}
}

type DiscoveryPacket struct {
	Buff []byte
	Len  int
	Addr net.Addr
}

/*
 * Go Routine for handling SkyFi Discovery with shutdown
 */
func ReplyDiscoverWithShutdown(shutdown chan bool) error {
	quit := false
	pc, err := reuse.ListenPacket("udp4", fmt.Sprintf(":%d", SKYFI_PORT))
	if err != nil {
		return fmt.Errorf("Unable to start SkyFi discovery: %s", err.Error())
	}

	defer pc.Close()
	log.Infof("Starting SkyFi Discovery service on UDP/%d", SKYFI_PORT)

	discoChan := make(chan DiscoveryPacket)

	// goroutine to read packets
	go func(pc net.PacketConn) {
		buf := make([]byte, 1024)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if quit {
				return
			}
			if err != nil {
				log.Warnf("Unable to read from SkyFi discovery listen socket: %s", err.Error())
				continue
			}
			log.Errorf("Received SkyFi packet")
			discoChan <- DiscoveryPacket{
				Buff: buf,
				Len:  n,
				Addr: addr,
			}
		}
	}(pc)

	for {
		select {
		case <-shutdown:
			log.Errorf("Shutting down SkyFi")
			quit = true
			return nil

		case disco := <-discoChan:
			n := disco.Len
			bufs := string(disco.Buff)
			if !strings.HasPrefix(bufs, "skyfi") {
				log.Warnf("Unexpected query of %d bytes from %s: %s", n, disco.Addr.String(), bufs)
				continue
			}

			// replace the '?' at the end with a '@'
			if disco.Buff[n-1] == 0x3f {
				disco.Buff[n-1] = 0x40
			}
			send_buf := []byte{}
			send_buf = append(send_buf, disco.Buff[:n]...)

			// figure out our local IP to thie client and append to end of reply
			client := strings.Split(disco.Addr.String(), ":")
			ip, err := findIPinCIDR(client[0])
			if err != nil {
				log.Errorf("%s", err.Error())
				continue
			}
			send_buf = append(send_buf, []byte(ip)...)

			_, err = pc.WriteTo(send_buf, disco.Addr)
			if err != nil {
				log.Errorf("Unable to send SkyFi discovery reply: %s", err.Error())
			}
		}
	}
}

/*
 * This function is really for informative purposes only.  It impliments
 * enough of the client side SkyFi protocol to get a SkyFi device to respond
 */
func GetDiscover(name string, tries int) ([]byte, error) {
	pc, err := reuse.ListenPacket("udp4", fmt.Sprintf(":%d", SKYFI_PORT))
	if err != nil {
		return []byte{}, fmt.Errorf("Unable to open SkyFi discovery listen socket: %s", err.Error())
	}
	defer pc.Close()

	send_addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", SKYFI_PORT))
	if err != nil {
		return []byte{}, fmt.Errorf("Unable to resolve SkyFi broadcast address: %s", err.Error())
	}

	var msg string
	// the query has an optional name, but based on the code I've seen it doesn't matter?
	if len(name) > 0 {
		msg = fmt.Sprintf("skyfi:%s?", name)
	} else {
		msg = "skyfi?"
	}
	msg_bytes := []byte(msg)
	buf := make([]byte, 1024)

	for i := 0; i < tries; i++ {
		_, err = pc.WriteTo(msg_bytes, send_addr)
		if err != nil {
			return []byte{}, fmt.Errorf("Unable to send SkyFi discovery message: %s", err.Error())
		}

		deadline := time.Now().Add(time.Second * 1)
		for true {
			pc.SetReadDeadline(deadline)
			err = nil
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				log.Warnf("Failed to discover SkyFi server: %s", err.Error())
				break // don't try reading again this cycle
			} else if string(buf[:n]) == msg {
				continue // this is the message we sent
			}

			log.Infof("receved %d bytes from %s via discovery: %v", n, addr.String(), buf[:n])
			return buf[:n], nil
		}
	}
	return []byte{}, fmt.Errorf("No reply from SkyFi")
}
