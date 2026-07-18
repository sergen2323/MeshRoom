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
	"meshroom/internal/ui"
	"meshroom/web"
)

func main() {
	helperMode := flag.Bool("helper", false, "запуск привилегированного помощника туннелей")
	port := flag.Int("port", 8790, "порт локального интерфейса")
	browserMode := flag.Bool("browser", false, "открыть UI в браузере вместо окна приложения")
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
		// порт занят — уже запущенный экземпляр; показываем его окно нельзя,
		// но можно открыть его UI в браузере, чтобы не плодить процессы
		openBrowser(fmt.Sprintf("http://127.0.0.1:%d", *port))
		log.Fatalf("listen: %v", err)
	}
	addr := fmt.Sprintf("http://127.0.0.1:%d", ln.Addr().(*net.TCPAddr).Port)
	log.Printf("MeshRoom UI: %s", addr)

	// сервер — в фоне; главная горутина остаётся окну (требование macOS)
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Fatal(err)
		}
	}()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		a.Close()
		os.Exit(0)
	}()

	if !*browserMode && ui.Native {
		// нативное окно приложения; выход из него = выход из MeshRoom
		if err := ui.Run(addr, "MeshRoom", 1180, 760); err == nil {
			a.Close()
			return
		}
		log.Printf("native window failed, falling back to browser")
	}

	openBrowser(addr)
	select {} // серверный режим: живём до Ctrl-C
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
