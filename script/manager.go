package script

import (
	"strings"
	"sync"

	ex "github.com/Disaldr/go-dlp/extractor"
)

// Manager устанавливает пакеты скриптов в реестр. Скрипт с именем
// встроенного экстрактора подменяет его, а встроенный остаётся запасным
// (Extractor.Fallback); при установке пакета без этого скрипта встроенный
// экстрактор возвращается в реестр.
type Manager struct {
	reg *ex.Registry

	mu       sync.Mutex
	version  int64
	loaded   bool
	active   map[string]*Extractor   // ключ — имя в нижнем регистре
	shadowed map[string]ex.Extractor // подменённые встроенные
}

// NewManager создаёт менеджер для реестра.
func NewManager(reg *ex.Registry) *Manager {
	return &Manager{reg: reg, active: map[string]*Extractor{}, shadowed: map[string]ex.Extractor{}}
}

// Version — версия установленного пакета (0 — встроенные скрипты или ничего).
func (m *Manager) Version() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version
}

// Install заменяет текущий набор скриптов пакетом p. Пакет не новее
// установленного игнорируется (защита от отката), кроме первой установки.
func (m *Manager) Install(p *Pack) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded && p.Version <= m.version {
		return false, nil
	}
	next := map[string]*Extractor{}
	for _, e := range p.Extractors {
		// Недоверенный (сетевой) пакет — скрипты в песочнице.
		e.sandboxed = !p.Trusted
		k := strings.ToLower(e.name)
		if _, isScript := m.active[k]; !isScript {
			if cur, ok := m.reg.Get(e.name); ok {
				m.shadowed[k] = cur
			}
		}
		e.fallback = m.shadowed[k]
		if err := m.reg.Apply(ex.Add(e)); err != nil {
			return false, err
		}
		next[k] = e
	}
	for k, old := range m.active {
		if _, keep := next[k]; keep {
			continue
		}
		if b := m.shadowed[k]; b != nil {
			m.reg.Apply(ex.Add(b))
			delete(m.shadowed, k)
		} else {
			m.reg.Apply(ex.Remove(old.name))
		}
	}
	m.active, m.version, m.loaded = next, p.Version, true
	return true, nil
}

// Active — установленные скриптовые экстракторы.
func (m *Manager) Active() []*Extractor {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Extractor, 0, len(m.active))
	for _, e := range m.active {
		out = append(out, e)
	}
	return out
}
