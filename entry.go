package sync

import "sync/atomic"

// An entry is a slot in the map corresponding to a particular key.
type entry[T any] struct {
	// p points to the interface{} value stored for the entry.
	//
	// If p == nil, the entry has been deleted, and either m.dirty == nil or
	// m.dirty[key] is e.
	//
	// If p == expunged, the entry has been deleted, m.dirty != nil, and the entry
	// is missing from m.dirty.
	//
	// Otherwise, the entry is valid and recorded in m.read.m[key] and, if m.dirty
	// != nil, in m.dirty[key].
	//
	// An entry can be deleted by atomic replacement with nil: when m.dirty is
	// next created, it will atomically replace nil with expunged and leave
	// m.dirty[key] unset.
	//
	// An entry's associated value can be updated by atomic replacement, provided
	// p != expunged. If p == expunged, an entry's associated value can be updated
	// only after first setting m.dirty[key] = e so that lookups using the dirty
	// map find the entry.
	p atomic.Pointer[T]
}

func newEntry[T any](i *T) *entry[T] {
	e := &entry[T]{}
	e.p.Store(i)
	return e
}

func (e *entry[T]) load() (value *T, ok bool) {
	p := e.p.Load()
	if p == nil || p == (*T)(expunged) {
		return nil, false
	}
	return p, true
}

// tryCompareAndSwap compare the entry with the given old value and swaps
// it with a new value if the entry is equal to the old value, and the entry
// has not been expunged.
//
// If the entry is expunged, tryCompareAndSwap returns false and leaves
// the entry unchanged.
func (e *entry[T]) tryCompareAndSwap(old, new *T) bool {
	p := e.p.Load()
	if p == nil || p == (*T)(expunged) || p != old {
		return false
	}

	// Copy the interface after the first load to make this method more amenable
	// to escape analysis: if the comparison fails from the start, we shouldn't
	// bother heap-allocating an interface value to store.
	nc := new
	for {
		if e.p.CompareAndSwap(p, nc) {
			return true
		}
		p = e.p.Load()
		if p == nil || p == (*T)(expunged) || p != old {
			return false
		}
	}
}

// unexpungeLocked ensures that the entry is not marked as expunged.
//
// If the entry was previously expunged, it must be added to the dirty map
// before m.mu is unlocked.
func (e *entry[T]) unexpungeLocked() (wasExpunged bool) {
	return e.p.CompareAndSwap((*T)(expunged), nil)
}

// swapLocked unconditionally swaps a value into the entry.
//
// The entry must be known not to be expunged.
func (e *entry[T]) swapLocked(i *T) *T {
	return e.p.Swap(i)
}

// tryLoadOrStore atomically loads or stores a value if the entry is not
// expunged.
//
// If the entry is expunged, tryLoadOrStore leaves the entry unchanged and
// returns with ok==false.
func (e *entry[T]) tryLoadOrStore(i *T) (actual any, loaded, ok bool) {
	p := e.p.Load()
	if p == (*T)(expunged) {
		return nil, false, false
	}
	if p != nil {
		return *p, true, true
	}

	// Copy the interface after the first load to make this method more amenable
	// to escape analysis: if we hit the "load" path or the entry is expunged, we
	// shouldn't bother heap-allocating.
	ic := i
	for {
		if e.p.CompareAndSwap(nil, ic) {
			return i, false, true
		}
		p = e.p.Load()
		if p == (*T)(expunged) {
			return nil, false, false
		}
		if p != nil {
			return *p, true, true
		}
	}
}

func (e *entry[T]) delete() (value *T, ok bool) {
	for {
		p := e.p.Load()
		if p == nil || p == (*T)(expunged) {
			return nil, false
		}
		if e.p.CompareAndSwap(p, nil) {
			return p, true
		}
	}
}

// trySwap swaps a value if the entry has not been expunged.
//
// If the entry is expunged, trySwap returns false and leaves the entry
// unchanged.
func (e *entry[T]) trySwap(i *T) (*T, bool) {
	for {
		p := e.p.Load()
		if p == (*T)(expunged) {
			return nil, false
		}
		if e.p.CompareAndSwap(p, i) {
			return p, true
		}
	}
}

func (e *entry[T]) tryExpungeLocked() (isExpunged bool) {
	p := e.p.Load()
	for p == nil {
		if e.p.CompareAndSwap(nil, (*T)(expunged)) {
			return true
		}
		p = e.p.Load()
	}

	return p == (*T)(expunged)
}
