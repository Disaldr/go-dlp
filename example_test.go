package dlp_test

import (
	"context"
	"fmt"
	"strings"

	dlp "github.com/Disaldr/go-dlp"
	"github.com/Disaldr/go-dlp/extractor"
	"github.com/Disaldr/go-dlp/media"
)

// Реактивное обновление экстракторов: реестр слушает канал обновлений,
// а подписчики получают события об изменениях. Клиент сразу использует
// новые экстракторы — без пересоздания.
func Example_reactiveExtractors() {
	client, _ := dlp.New(dlp.Options{})
	reg := client.Registry()

	events, unsubscribe := reg.Subscribe(8)
	defer unsubscribe()

	updates := make(chan extractor.Update)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go reg.Watch(ctx, updates, nil)

	// Подключаем экстрактор «на лету» (например, загруженный из плагина).
	updates <- extractor.Add(&extractor.Func{
		Key:   "mysite",
		Match: func(u string) bool { return strings.HasPrefix(u, "mysite:") },
		ExtractF: func(_ context.Context, _ *extractor.Env, u string) (*media.Info, error) {
			id := strings.TrimPrefix(u, "mysite:")
			return &media.Info{ID: id, Title: "Video " + id, Formats: []*media.Format{
				{FormatID: "hd", URL: "https://cdn.example.com/" + id + ".mp4", Ext: "mp4", Height: 720},
			}}, nil
		},
	})
	ev := <-events
	fmt.Println(ev.Type, ev.Name)

	info, _ := client.Extract(ctx, "mysite:42")
	fmt.Println(info.Extractor, info.Title, info.Formats[0].FormatID)

	updates <- extractor.Remove("mysite")
	ev = <-events
	fmt.Println(ev.Type, ev.Name)

	_, err := client.Extract(ctx, "mysite:42")
	fmt.Println(err != nil)
	// Output:
	// added mysite
	// mysite Video 42 hd
	// removed mysite
	// true
}
