// Команда go-dlp — CLI-загрузчик видео.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Disaldr/go-dlp/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// первый Ctrl+C — штатная остановка (запись эфира сохраняется и сливается),
	// второй — немедленный выход: после stop() сигнал обрабатывается по умолчанию
	go func() {
		<-ctx.Done()
		stop()
	}()
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
