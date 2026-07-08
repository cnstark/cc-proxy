package proxy

import (
	"context"
	"fmt"
	"github.com/cnstark/cc-proxy/internal/config"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type Server struct {
	httpServer *http.Server
	watcher    *config.Watcher
	log        *slog.Logger
}

func NewServer(watcher *config.Watcher, handler http.Handler, log *slog.Logger) *Server {
	return &Server{
		httpServer: &http.Server{Handler: handler},
		watcher:    watcher,
		log:        log,
	}
}

func (s *Server) Start(listenAddr string) error {
	s.httpServer.Addr = listenAddr
	s.log.Info("ccp-proxy 启动", "listen_addr", listenAddr)

	snap, err := s.watcher.Current()
	if err == nil {
		s.log.Info("配置已加载",
			"upstreams", len(snap.Upstreams),
			"projects", len(snap.Projects),
			"config_path", s.watcher.Path(),
		)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case err := <-errCh:
		return fmt.Errorf("服务器启动失败: %w", err)
	case sig := <-sigCh:
		s.log.Info("收到信号，优雅退出中", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("关闭服务器失败: %w", err)
	}
	s.log.Info("ccp-proxy 已退出")
	return nil
}
