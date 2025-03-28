package sync

import (
	"sync"
	"sync/atomic"
)

// VMap is a concurrent map with type-safe values and an unconstrained key type.
// It is similar to KVMap, but it allows keys of any comparable type (as an
// interface{}). This mirrors the flexibility of sync.Map keys while still
// providing a generic type V for values.
//
// Use VMap when you need to store values of one type V but your keys may be
// of different types. If keys are all one type, KVMap is preferable for
// compile-time checking of the key type.
//
// The zero VMap is empty and ready for use. A VMap must not be copied after
// first use.
//
// Internally, VMap uses the same algorithm as sync.Map (with a read-only
// segment and a dirty segment) to minimize locking. Values are stored as
// *V pointers, and a nil *V is treated as no value.
type VMap[T any] struct {
	mu     sync.Mutex
	read   atomic.Pointer[readOnly[T]]
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

// Load returns the value stored in the map for a key, or nil if no value is present.
//
// For VMap[V]: 'key' is of type any.
// It returns a pointer to the value (*V) and a bool indicating whether the key
// was found.
//
// If the key exists, ok is true and the returned *V points to the stored value.
// If the key is not present, ok is false and a nil pointer is returned.
//
// Note: If the map stored a nil pointer for this key, it is treated as "not present",
// so Load would return ok == false in that case.
//
// This operation is safe for concurrent use. It does not block other readers
// (and in most cases does not involve locking at all, thanks to the internal
// read-optimized snapshot).
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

// Store sets the value for a key in the map.
//
// For VMap[V]: 'key' is of type any (interface{}), and 'value' is *V.
//
// Store inserts or updates the entry for the given key, associating it with
// the provided value. It overwrites any previous value for that key without
// returning the old value (contrast with Swap).
//
// The stored value must be a pointer of type *V. A nil pointer value will
// effectively remove the key from the map (as if Delete were called).
//
// Store is safe to call concurrently from multiple goroutines. It may block
// briefly if another operation is writing to the map’s internal structures.
func (m *VMap[T]) Store(key any, value *T) {
	_, _ = m.Swap(key, value)
}

// Clear removes all key-value entries from the map.
//
// After Clear, the map will be empty. Any concurrent readers may still see some keys briefly during the call,
// but once Clear() returns, no keys remain. Writers attempting to Store during a Clear may either happen before
// or after the Clear (Clear holds a lock during its operation).
//
// This is a convenience method not provided by sync.Map. It can be useful to reset a map to empty without allocating a new one.
// Under the hood, it locks the map, clears internal maps, and resets state, which is typically a O(n) operation where n is the number of entries.
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

// LoadOrStore returns the existing value for the key if present. Otherwise, it stores
// and returns the given value. The loaded result is true if the value was already
// present, false if the value was stored as a result of this call.
//
// For VMap[V]: 'key' is any and 'value' is *V.
// It returns a pointer to the value (*V) that is in the map after the call (either the
// pre-existing value or the newly stored one), along with a bool 'loaded' which is true
// if the key was already present.
//
// Example:
//
//	actualPtr, loaded := m.LoadOrStore(k, newValPtr)
//	// If loaded == true, actualPtr is the existing value and newValPtr was not used.
//	// If loaded == false, newValPtr was stored and actualPtr == newValPtr.
//
// If another goroutine concurrently stored a value for the key, this call may return
// that value (and not store the provided one). Only one of the concurrent calls will
// store and the rest will retrieve the stored value.
//
// This operation locks the map only briefly if the key is missing, to set up the new entry.
// It is safe for concurrent use by multiple goroutines.
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

// LoadAndDelete deletes the entry for a key, returning the value that was present and
// a boolean indicating if the key was found.
//
// For VMap[V]: 'key' is any.
// It returns (*V, bool) similar to Load. If the key was in the map, it is removed and
// its value pointer is returned with loaded == true. If the key was not in the map,
// loaded == false and the returned pointer is nil.
//
// This operation is atomic – it combines Load and Delete such that no other goroutine
// can intervene between the retrieval and the removal. It’s useful for scenarios where
// you want to retrieve-and-consume a value.
//
// Safe for concurrent use. It will lock the map briefly to perform the deletion.
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

// Delete removes the entry for a key from the map.
//
// For VMap[V]: 'key' is any.
//
// Delete is a convenience method that is equivalent to LoadAndDelete(key) and ignoring
// the returned value. It ensures the key is not present after the call. If the key is
// not in the map, Delete does nothing (no error).
//
// This method is safe to call concurrently. It will lock the map briefly if necessary
// to remove the item.
func (m *VMap[T]) Delete(key any) {
	m.LoadAndDelete(key)
}

// Swap swaps the existing value for a given key with a new value, and returns the previous value.
//
// For VMap[V]: 'key' is any, 'new' is *V.
// It returns (prev *V, loaded bool). If the key was present, 'prev' is a pointer to the old value
// and loaded == true. If the key was not present, 'prev' is nil and loaded == false (in this case,
// the new value has been stored).
//
// After Swap, the key will exist in the map with the new value (unless the new value is nil, see below).
// If you pass a nil pointer as the new value, the effect is to delete the key, and the returned 'prev'
// will be the old value (if any) with loaded set accordingly.
//
// Swap provides a way to get the old value while simultaneously setting a new value, all in one atomic operation.
// It is safe for concurrent use; it locks the map briefly to perform the swap.
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

// CompareAndSwap swaps the old and new values for a key if the current value matches old.
//
// For VMap[V]: types are (key any, old *V, new *V) -> (swapped bool).
//
// If the map contains an entry for 'key' and its value pointer is equal to 'old', then
// it will be atomically replaced with 'new' and the function returns true. If the current
// value is not equal to 'old' (including the case where the key is not present), the map
// remains unchanged and the function returns false.
//
// This is an atomic compare-and-set operation. It is often used to implement conditional updates
// in a lock-free manner. For example, to increment a counter only if it hasn’t changed from an
// expected value, or to ensure no one else modified a value before replacing it.
//
// The comparison is done on pointer equality (literally old == current pointer). Therefore, you
// should pass the same pointer that was previously retrieved from the map (for instance, via Load).
// Do not create a new pointer with the same value contents – that will not be considered equal
// because it’s a different pointer.
//
// This operation is safe for concurrent use. It may lock the map if it has to check a key in the
// dirty map, but in the common case it will just use atomic reads.
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

// CompareAndDelete deletes the entry for a key if its current value matches old.
//
// For VMap[V]: (key any, old *V) -> (deleted bool).
//
// If the map contains an entry for 'key' and its value pointer is equal to 'old', that entry is
// removed and the function returns true. If the value does not match 'old' or the key is absent,
// nothing is removed and the function returns false.
//
// Like CompareAndSwap, the comparison is pointer equality. Typically you provide the pointer
// retrieved from an earlier Load or other operation.
//
// This can be used to avoid deleting a value that has been changed by another goroutine between
// the time you read it and the time you attempt to delete it. In such a case, CompareAndDelete
// will fail (return false) because the value no longer matches, indicating your delete was not applied.
//
// Safe for concurrent use. It will acquire a lock if needed to synchronize the deletion.
func (m *VMap[T]) CompareAndDelete(key any, old *T) (deleted bool) {
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
		if p == nil || p == (*T)(expunged) || p != old {
			return false
		}

		if e.p.CompareAndSwap(p, nil) {
			return true
		}
	}

	return false
}

// Range calls the given function sequentially for each key and value present in the map.
//
// The iteration order is undefined (it can vary). For each key/value pair in the map, Range
// invokes f(key, valuePtr). If f returns false, the iteration stops early.
//
// Note that the callback receives the actual key type (any for VMap) and a
// *V pointer to the value. You must dereference the pointer to get the value.
//
// Range does not necessarily correspond to a consistent snapshot of the map's content. In other
// words, the map may be concurrently modified during the iteration:
//   - An entry seen by Range may be updated or deleted by other goroutines while Range is still running.
//   - No key will be visited more than once. If a key is deleted during iteration (by f or another goroutine),
//     Range may or may not call f for it, depending on whether it was already visited. If a key is inserted
//     during iteration, Range may or may not visit it.
//   - The iteration stops when all keys that were present at the time Range started have been processed (each at most once).
//     However, if the map was completely cleared and repopulated during iteration, some of those new keys might be visited as well
//     because the internal iteration uses the map's structure at the time of calling Range.
//
// In practice, Range is thread-safe and can be used concurrently with other operations. The provided function f should not modify
// the map in ways that fundamentally disrupt the iteration (it’s okay to delete the current key or add new keys, but avoid patterns
// like recursively calling Range within Range).
//
// If f panics, the panic propagates out of Range and the map's state is safe (no partial holds on locks).
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
