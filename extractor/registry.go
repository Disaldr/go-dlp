package extractor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Op — операция над реестром.
type Op int

const (
	// OpAdd добавляет экстрактор или заменяет существующий с тем же именем.
	OpAdd Op = iota
	// OpRemove удаляет экстрактор по имени.
	OpRemove
)

func (o Op) String() string {
	switch o {
	case OpAdd:
		return "add"
	case OpRemove:
		return "remove"
	}
	return fmt.Sprintf("op(%d)", int(o))
}

// Update — команда изменения реестра, приходящая через канал (см. Watch).
type Update struct {
	Op        Op
	Extractor Extractor // для OpAdd
	Name      string    // для OpRemove (для OpAdd берётся Extractor.Name())
}

// Add — конструктор Update для добавления/замены.
func Add(e Extractor) Update { return Update{Op: OpAdd, Extractor: e} }

// Remove — конструктор Update для удаления.
func Remove(name string) Update { return Update{Op: OpRemove, Name: name} }

// EventType — тип события реестра.
type EventType int

const (
	EventAdded EventType = iota
	EventReplaced
	EventRemoved
)

func (t EventType) String() string {
	switch t {
	case EventAdded:
		return "added"
	case EventReplaced:
		return "replaced"
	case EventRemoved:
		return "removed"
	}
	return "unknown"
}

// Event — уведомление подписчикам об изменении реестра.
type Event struct {
	Type    EventType
	Name    string
	Version uint64
}

type entry struct {
	ext Extractor
	seq uint64
}

// Registry — потокобезопасный реестр экстракторов.
//
// Изменять его можно напрямую (Register/Unregister) или реактивно —
// передав канал обновлений в Watch. Подписчики (Subscribe) получают события.
// Поиск (Find/List) работает по неизменяемому снимку и не блокирует запись.
type Registry struct {
	mu      sync.RWMutex
	items   map[string]entry
	sorted  []Extractor
	seq     uint64
	version uint64

	subMu  sync.Mutex
	subs   map[int]chan Event
	nextID int
}

// NewRegistry создаёт реестр с начальным набором экстракторов.
func NewRegistry(exts ...Extractor) *Registry {
	r := &Registry{items: map[string]entry{}, subs: map[int]chan Event{}}
	r.Register(exts...)
	return r
}

func key(name string) string { return strings.ToLower(name) }

// Register добавляет (или заменяет одноимённые) экстракторы.
func (r *Registry) Register(exts ...Extractor) {
	for _, e := range exts {
		_ = r.Apply(Add(e))
	}
}

// Unregister удаляет экстракторы; возвращает число удалённых.
func (r *Registry) Unregister(names ...string) int {
	n := 0
	for _, name := range names {
		if r.Apply(Remove(name)) == nil {
			n++
		}
	}
	return n
}

// Apply применяет одно обновление.
func (r *Registry) Apply(u Update) error {
	var ev Event
	r.mu.Lock()
	switch u.Op {
	case OpAdd:
		if u.Extractor == nil {
			r.mu.Unlock()
			return fmt.Errorf("registry: nil extractor")
		}
		k := key(u.Extractor.Name())
		if k == "" {
			r.mu.Unlock()
			return fmt.Errorf("registry: extractor with empty name")
		}
		old, exists := r.items[k]
		seq := old.seq
		ev.Type = EventReplaced
		if !exists {
			r.seq++
			seq = r.seq
			ev.Type = EventAdded
		}
		r.items[k] = entry{ext: u.Extractor, seq: seq}
		ev.Name = u.Extractor.Name()
	case OpRemove:
		k := key(u.Name)
		if _, ok := r.items[k]; !ok {
			r.mu.Unlock()
			return fmt.Errorf("registry: extractor %q not found", u.Name)
		}
		delete(r.items, k)
		ev = Event{Type: EventRemoved, Name: u.Name}
	default:
		r.mu.Unlock()
		return fmt.Errorf("registry: unknown op %v", u.Op)
	}
	r.rebuild()
	r.version++
	ev.Version = r.version
	r.mu.Unlock()

	r.publish(ev)
	return nil
}

// rebuild пересобирает отсортированный снимок (вызывать под r.mu).
func (r *Registry) rebuild() {
	list := make([]entry, 0, len(r.items))
	for _, e := range r.items {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool {
		pi, pj := PriorityOf(list[i].ext), PriorityOf(list[j].ext)
		if pi != pj {
			return pi > pj
		}
		return list[i].seq < list[j].seq
	})
	sorted := make([]Extractor, len(list))
	for i, e := range list {
		sorted[i] = e.ext
	}
	r.sorted = sorted
}

// Watch применяет обновления из канала, пока он не закрыт или не отменён ctx.
// Ошибки отдельных обновлений передаются в onErr (может быть nil).
// Возвращает nil при закрытии канала, иначе ctx.Err().
func (r *Registry) Watch(ctx context.Context, updates <-chan Update, onErr func(Update, error)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case u, ok := <-updates:
			if !ok {
				return nil
			}
			if err := r.Apply(u); err != nil && onErr != nil {
				onErr(u, err)
			}
		}
	}
}

// Subscribe возвращает канал событий и функцию отписки.
// Отправка неблокирующая: если буфер подписчика полон, событие теряется —
// медленный подписчик может пересинхронизироваться через Version()/List().
func (r *Registry) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 16
	}
	ch := make(chan Event, buffer)
	r.subMu.Lock()
	id := r.nextID
	r.nextID++
	r.subs[id] = ch
	r.subMu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			r.subMu.Lock()
			delete(r.subs, id)
			r.subMu.Unlock()
			close(ch)
		})
	}
}

func (r *Registry) publish(ev Event) {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	for _, ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Version — монотонный номер версии реестра.
func (r *Registry) Version() uint64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

// List возвращает снимок экстракторов в порядке проверки.
func (r *Registry) List() []Extractor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sorted
}

// Get возвращает экстрактор по имени.
func (r *Registry) Get(name string) (Extractor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.items[key(name)]
	return e.ext, ok
}

// Find возвращает первый подходящий экстрактор.
func (r *Registry) Find(rawURL string) (Extractor, error) {
	return r.FindExcept(rawURL)
}

// FindExcept — как Find, но пропускает перечисленные имена.
func (r *Registry) FindExcept(rawURL string, skip ...string) (Extractor, error) {
	for _, e := range r.List() {
		if skipped(e.Name(), skip) {
			continue
		}
		if e.Suitable(rawURL) {
			return e, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrUnsupportedURL, rawURL)
}

func skipped(name string, skip []string) bool {
	for _, s := range skip {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}
