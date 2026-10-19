package cli

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/agentchat-lan/agentchat/internal/mockhub"
)

func (a *App) cmdMockHub(args []string) int {
	fs := a.newFlagSet("mock-hub")
	listen := fs.String("listen", "127.0.0.1:8787", "listen address")
	codeExit, ok := a.parse(fs, args)
	if !ok {
		return codeExit
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           mockhub.New().Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("agentchat mock hub listening on http://%s", *listen)
		errCh <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return a.fail(fmt.Errorf("mock hub: %w", err))
		}
		return 0
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return a.fail(fmt.Errorf("shutdown mock hub: %w", err))
	}
	return 0
}
