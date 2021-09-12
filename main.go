package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/Dreamacro/clash/constant"
	"github.com/Dreamacro/clash/listener/socks"
	"github.com/pkg/errors"
	"github.com/v2fly/v2ray-core/v4/common/task"
	wgConn "golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	fs := flag.NewFlagSet("wgsocks", flag.ExitOnError)
	addr := fs.String("a", "10.0.0.2", "local address")
	dns := fs.String("d", "1.0.0.1:53", "dns server")
	conf := fs.String("c", "wireguard.conf", "config file")
	bind := fs.String("b", "127.0.0.1:1080", "socks5 bind address")
	mtu := fs.Int("m", 1420, "mtu")
	_ = fs.Parse(os.Args[1:])

	b, err := ioutil.ReadFile(*conf)
	if err != nil {
		log.Fatalln(errors.WithMessage(err, "read conf"))
	}

	resolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return net.Dial("tcp", *dns)
		},
	}

	cc := string(b)
	for _, line := range strings.Split(cc, "\n") {
		parts := strings.Split(line, "=")
		if len(parts) < 2 {
			continue
		}
		if parts[0] == "endpoint" {
			address := strings.Split(parts[1], ":")
			if len(address) < 2 {
				break
			}

			for i := 0; i < 5; i++ {
				ip, err := resolver.LookupIP(context.Background(), "ip", address[0])
				if err != nil || len(ip) == 0 {
					if err != nil {
						log.Println(err.Error())
					}
					log.Println("failed to resolve endpoint address")
					time.Sleep(time.Second)
					continue
				}
				cc = strings.ReplaceAll(cc, line, parts[0]+"="+net.JoinHostPort(ip[0].String(), address[1]))
			}
		}
	}

	var addrs []net.IP
	for _, ipAddr := range strings.Split(*addr, ",") {
		addrs = append(addrs, net.ParseIP(ipAddr))
	}
	tun, tnet, err := CreateNetTUN(addrs, resolver, *mtu)
	if err != nil {
		log.Fatalln(errors.WithMessage(err, "create net tun").Error())
	}
	dev := device.NewDevice(tun, wgConn.NewStdNetBind(), device.NewLogger(device.LogLevelVerbose, ""))
	err = dev.IpcSet(cc)
	if err != nil {
		log.Fatalln(errors.WithMessage(err, "load conf").Error())
	}

	in := make(chan constant.ConnContext, 100)
	ln, err := socks.New(*bind, in)
	if err != nil {
		log.Fatalf(errors.WithMessage(err, "create socks5 server").Error())
	}

	go func() {
		for conn := range in {
			conn := conn
			metadata := conn.Metadata()
			go func() {
				ctx := context.Background()
				rc, err := tnet.DialContext(ctx, metadata.NetWork.String(), metadata.RemoteAddress())
				if err != nil {
					log.Printf(errors.WithMessagef(err, "dial to %s failed", metadata.RemoteAddress()).Error())
					return
				}
				_ = task.Run(ctx, func() error {
					fmt.Printf("[%s] %s => %s\n", strings.ToUpper(metadata.NetWork.String()), metadata.SourceAddress(), metadata.RemoteAddress())
					return nil
				}, func() error {
					_, err := io.Copy(rc, conn.Conn())
					if err == nil {
						err = io.EOF
					}
					return err
				}, func() error {
					_, err := io.Copy(conn.Conn(), rc)
					if err == nil {
						err = io.EOF
					}
					return err
				})
				_ = rc.Close()
				_ = conn.Conn().Close()
			}()
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	close(in)
	ln.Close()

}
