## Generic sync.Map for Go

**sync-map-generic** is a Go package providing a type-safe, generic version of Go’s `sync.Map`. It offers the same concurrent map functionality as the standard `sync.Map` but with compile-time type checking for keys and values. This eliminates the need for type assertions and improves code safety and clarity.

## Installation

This package requires **Go 1.21+** (for generics and `atomic.Pointer` support). To install, use the standard Go tooling:

```bash
go get github.com/chloyka/sync-map-generic@latest
```

Import it in your Go code. Because the package name is `sync` (to align with the standard library's `sync` package), it’s recommended to use an alias to avoid confusion with `sync`:

```go
import "github.com/chloyka/sync-map-generic"
```

Now you can refer to the package as `sync` (or an alias of your choice) in your code.

## Overview

**sync-map-generic** provides two generic map types:

- **`KVMap[K comparable, V any]`** – A concurrent map with typed keys and values. Keys must fulfill Go’s `comparable` constraint (just like keys in a Go map). Both keys and values are type-checked at compile time.
- **`VMap[V any]`** – A concurrent map with typed values and unconstrained keys. The keys are of type `any` (interface{}), meaning you can use keys of any comparable type (same flexibility as `sync.Map` keys) while still having compile-time type safety for the values.

These types mirror the API of `sync.Map` in the standard library. They are safe for concurrent use by multiple goroutines without additional locking. Under the hood, they use the same algorithm as Go’s `sync.Map` (a split ordered list of read-mostly data plus a dirty map for writes) to provide efficient atomic load/store operations with minimal locking.

**Key benefits:**
- **Type Safety:** Eliminates the need for casting keys or values from `interface{}`. Misuse (storing or loading wrong types) is caught by the compiler.
- **Performance:** Avoids the overhead of interface conversion. For keys or values that are not pointers (e.g. primitives like int), operations can be faster than `sync.Map` since no interface boxing/unboxing is needed. (For pointer types, performance is on par with `sync.Map`.)

## Usage

Choose **`KVMap`** if all your keys are of one type. Choose **`VMap`** if you need to use heterogeneous key types (or simply want a drop-in replacement for `sync.Map` but with a specific value type). In general, using `KVMap` is preferable when possible, as it enforces key consistency.

#### Creating a Map
You can declare a map in its zero state (no need to explicitly initialize):

```go
var m syncmap.KVMap[int, string] // keys are int, values are string
// Or using VMap, for example:
var m2 syncmap.VMap[string]     // keys are any type, values are string
```

The zero value of `KVMap` or `VMap` is ready to use. You do **not** need to call any initializer. Just like with `sync.Map`, do not copy a map after first use.

#### Storing values

Use `Store` to set a value for a key, overwriting any existing value:

```go
name := "Alice"
m.Store(1, &name)  // store value "Alice" under key 1

// For VMap (keys of any type):
id := 123
m2.Store("user-id", &id)  // store under a string key
```

**Note:** This map stores **pointers to values**. In the above example, we passed `&name` (of type `*string`). The map will hold that pointer. On retrieval, you'll get the same pointer back. This design avoids copying large values and integrates with the atomic mechanism. It does mean you should take care that the value pointed to isn’t modified unexpectedly elsewhere. If you want to store a value directly, take its address when calling `Store`. 
For example, to store an integer literal, do:
```go
x := 42
m.Store(key, &x)
```
Storing a `nil` pointer as the value is effectively treated as deleting that key (the entry will be removed).

Compared to `sync.Map.Store`, which takes an `interface{}` value, `KVMap.Store` requires a `*V` (pointer to your value type). This ensures type correctness but requires taking the address of value variables.

#### Loading values

Use `Load` to retrieve a value by key:

```go
valPtr, ok := m.Load(1)
if ok {
    fmt.Println("Value:", *valPtr)  // dereference pointer to get the actual string
} else {
    fmt.Println("No value for key 1")
}
```

`Load` returns a pointer to the value (`*V`) and a boolean. `ok` is true if the key was present. If the key doesn’t exist, `valPtr` will be `nil` and `ok` will be false. (If the stored value itself was a pointer type and set to nil, it would also be treated as absent.)

**Comparison with `sync.Map.Load`:** In `sync.Map`, you get a value of type `any` and must cast it. Here, you get a `*V` and simply dereference it. The rest of the semantics (returning a second boolean, etc.) are the same.

#### Load or Store (atomic get-or-set)

Use `LoadOrStore` to either retrieve an existing value for a key or store a new value if the key wasn’t present:

```go
count := 1
actualPtr, loaded := m.LoadOrStore(2, &count)
if loaded {
    fmt.Println("Key 2 already existed with value:", *actualPtr)
} else {
    fmt.Println("Key 2 was missing, stored new value:", *actualPtr)
}
```

If the key 2 was already in the map, `actualPtr` will point to the existing value and `loaded` will be true (the provided `count` was not used). If the key was absent, the map stores the provided value (in this case `&count`), and returns it, with `loaded` false.

**Comparison with `sync.Map.LoadOrStore`:** Behavior is identical. The difference is type: `actualPtr` is `*V` instead of `interface{}`. Also, because this map treats a nil stored pointer as no value, if you call `LoadOrStore` with a nil pointer, it will always store it (since a nil pointer is considered "not present") and return it with `loaded == false`.

#### Load and Delete (retrieve and remove)

Use `LoadAndDelete` to atomically remove a key and retrieve its value in one operation:

```go
valPtr, loaded := m.LoadAndDelete(2)
if loaded {
    fmt.Println("Removed value:", *valPtr)
} else {
    fmt.Println("Nothing to remove for key 2")
}
```

If the key was present, it is deleted and its former value is returned (with `loaded = true`). If the key was absent, `loaded` is false and `valPtr` will be nil.

This is analogous to calling `Load` then `Delete`, but doing it atomically under the hood (preventing race conditions where another goroutine might insert the key between the load and delete).

**Comparison with `sync.Map.LoadAndDelete`:** Semantics match exactly, except for the typed return value.

#### Delete

Use `Delete` to remove a key (no return value):

```go
m.Delete(1)
```

After `Delete`, key `1` will no longer be in the map. Calling `Delete(key)` is equivalent to `m.LoadAndDelete(key)` and discarding the returned value. It will do nothing if the key is not present.

**Comparison with `sync.Map.Delete`:** Same behavior. (In this implementation, `Delete` simply calls `LoadAndDelete` internally.)

#### Swap (store and retrieve old value)

Use `Swap` to set a new value for a key and simultaneously retrieve the previous value in one atomic operation:

```go
newName := "Bob"
prevPtr, loaded := m.Swap(1, &newName)
if loaded {
    fmt.Println("Previous value for key 1 was:", *prevPtr)
} else {
    fmt.Println("Key 1 was not present before; now set to", *m.Load(1))
}
```

`Swap` stores the new value `&newName` for key 1 and returns the old value that was previously associated with key 1 (if any). The boolean `loaded` is true if an old value existed, false if the key was empty before the swap. If `loaded` is false, `prevPtr` will be nil (since there was no previous value).

If you call `Swap` on a key that does not exist, it simply inserts the new value (and returns `nil, false`). If you swap in a `nil` pointer as the new value, it will delete the key and return the old value (if any).

**Comparison with standard `sync.Map`:** Go’s `sync.Map` has a similar method `Swap` (added in Go 1.20) or an equivalent `LoadAndStore`. The functionality is the same here, with type-safe pointers.

#### Compare and Swap

Use `CompareAndSwap` to update the value for a key **only if** it currently matches an expected old value. This is an atomic check-and-set:

```go
oldPtr := valPtr       // suppose valPtr was obtained from a previous Load
newVal := "Charlie"
swapped := m.CompareAndSwap(1, oldPtr, &newVal)
if swapped {
    fmt.Println("Update succeeded, new value:", *m.Load(1))
} else {
    fmt.Println("Update failed, value was not", *oldPtr)
}
```

`CompareAndSwap(key, old, new)` will atomically check the current value for `key`. If the current value pointer equals `old`, it will replace it with `new` and return true. If the current value is different (or if the key is not present), it leaves the map unchanged and returns false.

This operation is useful to implement lock-free algorithms where you only want to update if no one else has changed the value in the meantime.

**Important:** The comparisons are done on the pointer values (`old` and the stored pointer). Typically you would pass in an `old` pointer that you got from a prior `Load` or `LoadOrStore`. If the value at that key has been modified or swapped by another goroutine, the compare-and-swap will fail.

**Comparison with `sync.Map.CompareAndSwap`:** Same semantics. The method in `sync.Map` works on `interface{}` values, whereas here you use typed pointers. Note that for non-pointer values, `sync.Map.CompareAndSwap` compares by value (via interface equality). In this generic map, you compare the actual pointer references. In practice, if you use it as intended (with the pointer obtained from this map), it achieves the same goal of checking that the value hasn’t changed.

#### Compare and Delete

Use `CompareAndDelete` to delete a key *only if* its current value matches an expected value:

```go
targetPtr := m.Load(1)  // assume this returns a pointer
deleted := m.CompareAndDelete(1, targetPtr)
if deleted {
    fmt.Println("Key 1 was deleted")
} else {
    fmt.Println("Key 1 was not deleted (value did not match)")
}
```

`CompareAndDelete(key, old)` checks if `key` exists and currently has value pointer equal to `old`. If so, it deletes the entry and returns true. If not (either the key has a different value or doesn’t exist), nothing is removed and it returns false.

**Comparison with `sync.Map.CompareAndDelete`:** Identical behavior, except using a typed pointer for the expected value.

#### Range (iterate over all entries)

Use `Range` to iterate over all key-value pairs in the map:

```go
m.Range(func(key int, valPtr *string) bool {
    fmt.Printf("Key=%d, Value=%s\n", key, *valPtr)
    return true  // continue iteration
})
```

The iteration order is undefined (it’s not sorted by key). The provided callback function `f` will be called sequentially for each key-value pair present in the map at the time of the iteration. If `f` returns false, the iteration stops early.

Within the callback, you can access or modify the map (including calling `Delete`, `Store`, etc.), but be aware that such modifications may or may not be reflected in the ongoing iteration:
- No key will be visited more than once during a single `Range` call.
- The iteration is *not* guaranteed to see a consistent snapshot of the map. For example, if a value is being concurrently changed, `Range` might see either the old or new value for that key (or reflect a deletion or insertion) nondeterministically. However, it will handle internal synchronization so that each key yields a valid value at the moment of the call.
- It is safe for the function `f` to call map methods on *different* keys (or even on the same key; for example, you could delete the current key from inside the callback). The `Range` will proceed correctly.

**Comparison with `sync.Map.Range`:** The behavior is the same. The function signature here uses the concrete types for key and value pointer, making it more convenient than casting inside the loop.

#### Clear

Use `Clear` to remove **all** entries from the map in one call:

```go
m.Clear()
```

After `Clear()`, the map will be empty. This method locks the map during the clearance operation. It effectively resets the internal state. Any concurrent operations during a `Clear` might see the map as either partly cleared or cleared depending on timing, but once `Clear` returns, no keys remain.

## Comparison with `sync.Map`

Each method in **sync-map-generic** corresponds to a method in the standard `sync.Map`, with analogous semantics. The table below summarizes the differences and improvements:

- **Type Safety:** All operations require the correct key/value types. Standard `sync.Map` uses `any` (interface{}) for keys and values, so you must cast values on retrieval. With `KVMap`/`VMap`, the compiler enforces type correctness and no casting is needed (you get a `*V` of the expected type).
- **Keys:** `KVMap` restricts keys to a single specified type `K` (which must be comparable), preventing mixing different types of keys in one map. `VMap` allows keys of any type (like `sync.Map`), for cases where you truly need heterogeneous keys.
- **Values as Pointers:** Values are stored as pointers (`*V`). This design choice means you should always pass pointers to the `Store`/`LoadOrStore`/etc. and you will receive pointers from `Load`/`Range`. It avoids copying values and aligns with the internal atomic mechanism. In contrast, `sync.Map` stores values as interfaces (which may internally copy the value or allocate). Using pointers here can yield performance benefits, especially for large values (since updates can swap the pointer without copying the entire value). However, it’s a difference to be mindful of: you need to dereference to get the actual value, and a nil pointer is treated as no value.
- **Performance:** In scenarios with keys/values that are not pointers (e.g., integers, structs), this package can improve performance by avoiding interface boxing. In scenarios where keys and values are already pointer types (e.g., pointers to structs), performance is similar to `sync.Map`. If keys or values are large struct types, storing pointers to them (which this package forces for values) avoids copying those large structures on each access. In the standard `sync.Map`, large values would be wrapped in an interface which may also avoid some copying by pointer, but large keys would be copied for interface equality checks; here a large key of type `K` will be copied for map lookups (just as it would in a normal Go map). See recommendations below for handling large types.

- **Memory model and concurrency:** The generic maps maintain the same concurrency guarantees as `sync.Map`. A write (Store, Delete, etc.) “happens before” a subsequent read (Load, Range, etc.) that returns the value that was written. The internal structure (read-only segment and dirty segment with a mutex) is identical, so you can expect similar performance characteristics under contention patterns.

In summary, **sync-map-generic** should behave the same as `sync.Map` from a semantics perspective, with the primary difference being the type enforcement and slight API adjustments (pointer values).

## Practical Usage Recommendations

Here are some guidelines and best practices for using `KVMap` or `VMap` effectively:

- **Use Cases:** Just like `sync.Map`, these types are optimized for specific scenarios. They perform best when either:
    1. Keys are mostly written once and then read many times (append-only cache pattern), **or**
    2. Multiple goroutines access disjoint sets of keys (reducing contention on any single key).

  If your usage pattern is a general read-write mix on overlapping keys, a simple map protected by a `sync.Mutex` might sometimes be simpler and even perform better. Don’t use a concurrent map blindly for all cases of shared maps—consider if a mutex or other strategy is sufficient.

- **Avoid Copying After Use:** Once a map is in use (after any Store/Load), do not copy it by value. Copying a `KVMap` or `VMap` (like assigning it to a new variable or passing by value) can lead to corruption because the internal state is not deep-copied. This is the same rule as all sync primitives in Go (e.g., you shouldn’t copy a `sync.Mutex` after use). If you need a snapshot of the data, consider using `Range` to collect it, or use the provided methods to reconstruct desired state.

- **Choosing KVMap vs VMap:** Prefer `KVMap[K, V]` if you know the key type upfront. It provides stronger guarantees (all keys must be the same type) and may prevent bugs (accidentally using two different types of keys will be a compile-time error). Use `VMap[V]` if you truly need to allow different types of keys in one map (which is relatively uncommon – an example might be a cache keyed by either string IDs or integer IDs in the same structure). `VMap` still ensures all values are of a single type `V`.

- **Large Keys or Values:** If your key or value types are large (for example, a struct with many fields), you should be mindful of performance:
    - Large **values**: This library already requires storing a pointer to the value, which is beneficial because it avoids copying the large value on each operation. Just ensure you are actually passing a pointer to a single instance of that large value. If you generate a new large object each time, you’ll still incur allocation/copy costs.
    - Large **keys**: Keys in a `KVMap` are stored in a Go map internally, so they are copied when used as map keys. If the key type is very large (not common, but possible), you might prefer to use a pointer or some hashed handle as the key instead. For instance, instead of using a large struct directly as the key, use a pointer to it or a unique identifier. Another approach is to use `VMap` and supply keys as type `any` where you wrap large keys in an interface; however, note that interface comparison on large underlying types may still copy or be expensive. Generally, large keys are rare – keys are typically simple types (ints, strings, small structs).

- **Pointer Considerations:** Because values are pointers, if you store a pointer to a variable that might go out of scope or be modified, you should ensure it remains valid. A common pattern is to allocate new values for the map:
  ```go
  m.Store(k, &MyStruct{Field: 10})
  ```
  This way, the pointer is to a heap-allocated object that only the map accesses. If you instead do:
  ```go
  val := MyStruct{Field: 10}
  m.Store(k, &val)
  ```
  and then modify `val` later in your code, the map’s stored value will also appear changed since it points to the same `val`. In concurrent contexts, that can be a subtle bug. So treat the pointers you put in the map as owned by the map (or at least not modify them without intentionally wanting to).

- **Concurrent Usage:** All methods are safe to call from multiple goroutines. It is possible to have writers and readers in parallel without additional locks. Keep in mind that `Range` provides weak ordering guarantees under concurrency (as described above), and `Clear` will acquire a full lock on the map (blocking other ops briefly). High-frequency calls to `Clear` in parallel with other ops might cause contention; use it sparingly (e.g., when you truly need to reset an entire map).

By following these recommendations, you can effectively use **sync-map-generic** to manage concurrent maps with improved type safety and maintain performance characteristics similar to or better than Go’s built-in `sync.Map`.