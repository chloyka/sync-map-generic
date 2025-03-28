package sync

import (
	"sync"
	"sync/atomic"
)

type kvreadOnly[K comparable, V any] struct {
	m       map[K]*entry[V]
	amended bool
}

type KVMap[K comparable, V any] struct {
	mu sync.Mutex

	read atomic.Pointer[kvreadOnly[K, V]]

	dirty map[K]*entry[V]

	misses int
}

func (m *KVMap[K, V]) loadReadOnly() kvreadOnly[K, V] {
	if p := m.read.Load(); p != nil {
		return *p
	}

	return kvreadOnly[K, V]{}
}

func (m *KVMap[K, V]) Load(key any) (value any, ok bool) {
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

func (m *KVMap[K, V]) Store(key K, value *V) {
	_, _ = m.Swap(key, value)
}

func (m *KVMap[K, V]) Clear() {
	read := m.loadReadOnly()
	if len(read.m) == 0 && !read.amended {

		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	read = m.loadReadOnly()
	if len(read.m) > 0 || read.amended {
		m.read.Store(&kvreadOnly[K, V]{})
	}

	clear(m.dirty)

	m.misses = 0
}

func (m *KVMap[K, V]) LoadOrStore(key K, value *V) (actual any, loaded bool) {

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
			m.read.Store(&kvreadOnly[K, V]{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
		actual, loaded = value, false
	}
	m.mu.Unlock()

	return actual, loaded
}

func (m *KVMap[K, V]) LoadAndDelete(key K) (value *V, loaded bool) {
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

func (m *KVMap[K, V]) Delete(key any) {
	m.LoadAndDelete(key)
}

func (m *KVMap[K, V]) Swap(key K, value *V) (previous *V, loaded bool) {
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
			m.read.Store(&kvreadOnly[K, V]{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
	}
	m.mu.Unlock()
	return previous, loaded
}

func (m *KVMap[K, V]) CompareAndSwap(key K, old, new *V) (swapped bool) {
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

func (m *KVMap[K, V]) CompareAndDelete(key K, old *V) (deleted bool) {
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
	for ok {
		p := e.p.Load()
		if p == nil || p == (*V)(expunged) || p != old {
			return false
		}
		if e.p.CompareAndSwap(p, nil) {
			return true
		}
	}
	return false
}

func (m *KVMap[K, V]) Range(f func(key K, value *V) bool) {

	read := m.loadReadOnly()
	if read.amended {

		m.mu.Lock()
		read = m.loadReadOnly()
		if read.amended {
			read = kvreadOnly[K, V]{m: m.dirty}
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

func (m *KVMap[K, V]) missLocked() {
	m.misses++
	if m.misses < len(m.dirty) {
		return
	}
	m.read.Store(&kvreadOnly[K, V]{m: m.dirty})
	m.dirty = nil
	m.misses = 0
}

func (m *KVMap[K, V]) dirtyLocked() {
	if m.dirty != nil {
		return
	}

	read := m.loadReadOnly()
	m.dirty = make(map[K]*entry[V], len(read.m))
	for k, e := range read.m {
		if !e.tryExpungeLocked() {
			m.dirty[k] = e
		}
	}
}
