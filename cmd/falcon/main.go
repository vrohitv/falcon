// Falcon is a fast, offline code viewer and editor with Git, for many repositories from one server.
//
//	falcon [flags] [folder ...]
//
// Folders given here are added to the workspace (and remembered); more can be added from the UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/vrohitv/falcon/internal/server"
	"github.com/vrohitv/falcon/internal/workspace"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "falcon:", err)
		os.Exit(1)
	}
}

func run() error {
	defaultData := ".falcon"
	if dir, err := os.UserConfigDir(); err == nil {
		defaultData = filepath.Join(dir, "falcon")
	}
	listen := flag.String("listen", "127.0.0.1:7700", "address to serve on")
	data := flag.String("data", defaultData, "where Falcon keeps its folder list and language-server indexes")
	assets := flag.String("assets", "", "serve the UI from this folder instead of the built-in one (development)")
	useLSP := flag.Bool("lsp", true, "run installed language servers for definitions, references, hover and problems")
	version := flag.Bool("version", false, "print the version and exit")
	openBrowser := flag.Bool("open", false, "open Falcon in the default browser")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: falcon [flags] [folder ...]\n\nServes the given Git repositories (and any added before) at http://%s\n\nFlags:\n", "127.0.0.1:7700")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *version {
		fmt.Println("falcon", server.Version)
		return nil
	}
	token := os.Getenv("FALCON_TOKEN")
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) && len(token) < 24 {
		return fmt.Errorf("listening on %s needs FALCON_TOKEN (24+ characters); or keep the default 127.0.0.1", host)
	}
	ws, err := workspace.Open(filepath.Join(*data, "workspace.json"))
	if err != nil {
		return err
	}
	for _, folder := range flag.Args() {
		rp, err := ws.Add(folder, false)
		if err != nil {
			return err
		}
		slog.Info("serving", "folder", rp.Path, "url", "http://"+*listen+"/r/"+rp.ID+"/")
	}
	if len(flag.Args()) == 0 && len(ws.List()) == 0 {
		if wd, err := os.Getwd(); err == nil {
			if rp, err := ws.Add(wd, false); err == nil {
				slog.Info("serving the current folder", "folder", rp.Path)
			}
		}
	}
	s := server.New(ws, token, *assets, *data)
	s.NoLSP = !*useLSP
	defer s.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	url := "http://" + *listen + "/"
	if list := ws.List(); len(list) == 1 {
		url += "r/" + list[0].ID + "/"
	}
	fmt.Printf("Falcon %s · %d folder(s) · %s\n", server.Version, len(ws.List()), url)
	if *openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			switch runtime.GOOS {
			case "darwin":
				_ = exec.Command("open", url).Start()
			case "windows":
				_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
			default:
				_ = exec.Command("xdg-open", url).Start()
			}
		}()
	}
	return server.Run(ctx, *listen, s.Handler)
}
