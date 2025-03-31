package sync

import "sync"

// Map is an alias to the standard library's sync.Map (non-generic).
//
// This type is provided for compatibility. It is effectively the same as sync.Map from the Go standard library.
// Users are encouraged to use KVMap or VMap for type safety. Map exists in this package primarily to allow importing
// "github.com/chloyka/sync-map-generic" as a drop-in replacement for "sync" if needed.
//
// Since Map is just the original sync.Map, it does not use generics and its methods accept/return interface{} values.
type Map sync.Map
