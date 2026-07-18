// MeshRoom — виртуальная локальная сеть без внешних серверов:
// комнаты, чат и WireGuard-туннели между участниками.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"meshroom/internal/app"
	"meshroom/internal/tunnel"
	"meshroom/web"
)

func main() {
	helperMode := flag.Bool("helper", false, "запуск привилегированного помощника туннелей")
	port := flag.Int("port", 8790, "порт локального интерфейса")
	noBrowser := flag.Bool("no-browser", false, "не открывать браузер автоматически")
	flag.Parse()

	if *helperMode {
		if err := tunnel.RunHelper(); err != nil {
			log.Fatalf("helper: %v", err)
		}
		return
	}

	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("executable path: %v", err)
	}
	a, err := app.New(exe)
	if err != nil {
		log.Fatalf("init: %v", err)
	}

	srv, ln, err := a.NewHTTPServer(web.FS, *port)
	if err != nil {
		// порт занят (второй запуск?) — просто открываем существующий UI
		if *port != 0 {
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", *port))
		}
		log.Fatalf("listen: %v", err)
	}
	addr := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	log.Printf("MeshRoom UI: %s", addr)
	if !*noBrowser {
		openBrowser(addr)
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		a.Close()
		os.Exit(0)
	}()

	if err := srv.Serve(ln); err != nil {
		log.Fatal(err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
