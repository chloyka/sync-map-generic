package sync

import (
	"sync"
	"sync/atomic"
)

type VMap[T any] struct {
	mu sync.Mutex

	read atomic.Pointer[readOnly[T]]

	dirty  map[any]*entry[T]
	misses int
}

type readOnly[T any] struct {
	m       map[any]*entry[T]
	amended bool
}

func (m *VMap[T]) loadReadOnly() readOnly[T] {
	if p := m.read.Load(); p != nil {
		return *p
	}

	return readOnly[T]{}
}

func (m *VMap[T]) Load(key any) (value *T, ok bool) {
	read := m.loadReadOnly()
	e, ok := read.m[key]
	if !ok && read.amended {
		m.mu.Lock()

		read = m.loadReadOnly()
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]

			m.missLocked()
		}
		m.mu.Unlock()
	}
	if !ok {
		return nil, false
	}

	return e.load()
}

func (m *VMap[T]) Store(key any, value *T) {
	_, _ = m.Swap(key, value)
}

func (m *VMap[T]) Clear() {
	read := m.loadReadOnly()
	if len(read.m) == 0 && !read.amended {

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	read = m.loadReadOnly()
	if len(read.m) > 0 || read.amended {
		m.read.Store(&readOnly[T]{})
	}

	clear(m.dirty)

	m.misses = 0
}

func (m *VMap[T]) LoadOrStore(key any, value *T) (actual *T, loaded bool) {

	read := m.loadReadOnly()
	if e, ok := read.m[key]; ok {
		actual, loaded, ok := e.tryLoadOrStore(value)
		if ok {
			return actual, loaded
		}
	}

	m.mu.Lock()
	read = m.loadReadOnly()
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() {
			m.dirty[key] = e
		}
		actual, loaded, _ = e.tryLoadOrStore(value)
	} else if e, ok := m.dirty[key]; ok {
		actual, loaded, _ = e.tryLoadOrStore(value)
		m.missLocked()
	} else {
		if !read.amended {

			m.dirtyLocked()
			m.read.Store(&readOnly[T]{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
		actual, loaded = value, false
	}
	m.mu.Unlock()

	return actual, loaded
}

func (m *VMap[T]) LoadAndDelete(key any) (value *T, loaded bool) {
	read := m.loadReadOnly()
	e, ok := read.m[key]
	if !ok && read.amended {
		m.mu.Lock()
		read = m.loadReadOnly()
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]
			delete(m.dirty, key)

			m.missLocked()
		}
		m.mu.Unlock()
	}
	if ok {
		return e.delete()
	}
	return nil, false
}

func (m *VMap[T]) Delete(key any) {
	m.LoadAndDelete(key)
}

func (m *VMap[T]) Swap(key any, value *T) (previous *T, loaded bool) {
	read := m.loadReadOnly()
	if e, ok := read.m[key]; ok {
		if v, ok := e.trySwap(value); ok {
			if v == nil {
				return nil, false
			}
			return v, true
		}
	}

	m.mu.Lock()
	read = m.loadReadOnly()
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() {

			m.dirty[key] = e
		}
		if v := e.swapLocked(value); v != nil {
			loaded = true
			previous = v
		}
	} else if e, ok := m.dirty[key]; ok {
		if v := e.swapLocked(value); v != nil {
			loaded = true
			previous = v
		}
	} else {
		if !read.amended {

			m.dirtyLocked()
			m.read.Store(&readOnly[T]{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
	}
	m.mu.Unlock()
	return previous, loaded
}

func (m *VMap[T]) CompareAndSwap(key any, old, new *T) (swapped bool) {
	read := m.loadReadOnly()
	if e, ok := read.m[key]; ok {
		return e.tryCompareAndSwap(old, new)
	} else if !read.amended {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	read = m.loadReadOnly()
	swapped = false
	if e, ok := read.m[key]; ok {
		swapped = e.tryCompareAndSwap(old, new)
	} else if e, ok := m.dirty[key]; ok {
		swapped = e.tryCompareAndSwap(old, new)

		m.missLocked()
	}
	return swapped
}

//

func (m *VMap[T]) CompareAndDelete(key any, old *T) (deleted bool) {
	read := m.loadReadOnly()
	e, ok := read.m[key]
	if !ok && read.amended {
		m.mu.Lock()
		read = m.loadReadOnly()
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]

			//

			m.missLocked()
		}
		m.mu.Unlock()
	}
	for ok {
		p := e.p.Load()
		if p == nil || p == (*T)(expunged) || p != old {
			return false
		}
		if e.p.CompareAndSwap(p, nil) {
			return true
		}
	}
	return false
}

//

//

func (m *VMap[T]) Range(f func(key any, value *T) bool) {

	read := m.loadReadOnly()
	if read.amended {

		m.mu.Lock()
		read = m.loadReadOnly()
		if read.amended {
			read = readOnly[T]{m: m.dirty}
			copyRead := read
			m.read.Store(&copyRead)
			m.dirty = nil
			m.misses = 0
		}
		m.mu.Unlock()
	}

	for k, e := range read.m {
		v, ok := e.load()
		if !ok {
			continue
		}
		if !f(k, v) {
			break
		}
	}
}

func (m *VMap[T]) missLocked() {
	m.misses++
	if m.misses < len(m.dirty) {
		return
	}
	m.read.Store(&readOnly[T]{m: m.dirty})
	m.dirty = nil
	m.misses = 0
}

func (m *VMap[T]) dirtyLocked() {
	if m.dirty != nil {
		return
	}

	read := m.loadReadOnly()
	m.dirty = make(map[any]*entry[T], len(read.m))
	for k, e := range read.m {
		if !e.tryExpungeLocked() {
			m.dirty[k] = e
		}
	}
}
