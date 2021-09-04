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
	"wssocks/netstack"
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
	cc := string(b)

	tun, tnet, err := netstack.CreateNetTUN([]net.IP{net.ParseIP(*addr)}, *dns, *mtu)
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
			fmt.Printf("[%s] %s => %s\n", strings.ToUpper(metadata.NetWork.String()), metadata.SourceAddress(), metadata.RemoteAddress())
			go func() {
				ctx := context.Background()
				rc, err := tnet.DialContext(ctx, metadata.NetWork.String(), metadata.RemoteAddress())
				if err != nil {
					log.Printf(errors.WithMessagef(err, "dial to %s failed", metadata.RemoteAddress()).Error())
					return
				}
				_ = task.Run(ctx, func() error {
					_, err := io.Copy(rc, conn.Conn())
					if err == nil {
						err = io.EOF
					} else {
						log.Printf(errors.WithMessage(err, "server closed conn").Error())
					}
					return err
				}, func() error {
					_, err := io.Copy(conn.Conn(), rc)
					if err == nil {
						err = io.EOF
					} else {
						log.Printf(errors.WithMessage(err, "client closed conn").Error())
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
