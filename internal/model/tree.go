package model

import "sync"

// treeLock guards the whole document tree: every FSNode's fields, its Parent, Children and
// OrderedChildren. The file system reads the tree while the repository's background check for
// tablet changes and the file system's own changes (uploads, deletes, renames) update it.
//
// Rules:
//   - Change the tree only while holding LockTree, read it only while holding RLockTree.
//   - Hold it only for in-memory work, never during tablet or disk I/O, so listings stay fast.
//   - Lock order: the repository's own lock first, then the tree lock. So never call a
//     Repository method, or a go-fuse inode function (it may call OnAdd, which reads the
//     tree), while holding the tree lock.
//   - It isn't reentrant: a function holding it must not call one that takes it.
//   - Treat Metadata, Content and Pages as read-only once in the tree: replace them, don't
//     edit them in place, so copies taken under the lock stay valid after it is released.
var treeLock sync.RWMutex

// LockTree takes the tree lock for changing the tree.
func LockTree() { treeLock.Lock() }

// UnlockTree releases LockTree.
func UnlockTree() { treeLock.Unlock() }

// RLockTree takes the tree lock for reading the tree.
func RLockTree() { treeLock.RLock() }

// RUnlockTree releases RLockTree.
func RUnlockTree() { treeLock.RUnlock() }
