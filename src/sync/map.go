// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"sync/atomic"
	"unsafe"
)

// Map is like a Go map[interface{}]interface{} but is safe for concurrent use
// by multiple goroutines without additional locking or coordination.
// Loads, stores, and deletes run in amortized constant time.
//
// The Map type is specialized. Most code should use a plain Go map instead,
// with separate locking or coordination, for better type safety and to make it
// easier to maintain other invariants along with the map content.
//
// The Map type is optimized for two common use cases: (1) when the entry for a given
// key is only ever written once but read many times, as in caches that only grow,
// or (2) when multiple goroutines read, write, and overwrite entries for disjoint
// sets of keys. In these two cases, use of a Map may significantly reduce lock
// contention compared to a Go map paired with a separate Mutex or RWMutex.
//
// The zero Map is empty and ready for use. A Map must not be copied after first use.

// Map 是一个 map[interface{}]interface{}，并且它是线程安全的。无需
// 任何的锁或同步。Load，Store和Delete分摊运行时间。这个Map是应对特殊场景而
// 应用的，大部分场景应该使用普通的go map和一个额外的锁或同步器。能得到更好
// 类型安全和更好的维护其他的不变量和map的内容。
//
// Map 有两种常用的场景：1. 当某一个key的entry对象只被写一次，但是读取确
// 非常多次（读多写少场景），像值增长的缓存。2. 当多个协成对多个不相关的key
// 的集合，进行读，写，覆盖entries对象。这两种场景，使用此Map能减少锁的抢占，
// 相比于有额外一个读写锁或锁的普通map。
//
// 未初始化的Map是空的，并且已经可以被使用。一旦被使用之后的Map，禁止再次拷贝。
type Map struct {
	// 锁
	mu Mutex

	// read contains the portion of the map's contents that are safe for
	// concurrent access (with or without mu held).
	//
	// The read field itself is always safe to load, but must only be stored with
	// mu held.
	//
	// Entries stored in read may be updated concurrently without mu, but updating
	// a previously-expunged entry requires that the entry be copied to the dirty
	// map and unexpunged with mu held.

	// read 包含一部分map的内容。这部分内容是可以线程
	// 安全的访问的，这也是为什么性能更好的原因。此字段
	// 可以被线程安全加载，但是假如覆盖的话被拒获取 mu 锁。
	// 存储在read里面的Entries对象可以并行的被更新，而且无
	// 需获取 mu 锁，但是更新一个之前删除的entry对象时，需要
	// 获取锁拷贝entry到dirty，并取消删除。
	read atomic.Value // readOnly 这是个 readOnly 对象

	// dirty contains the portion of the map's contents that require mu to be
	// held. To ensure that the dirty map can be promoted to the read map quickly,
	// it also includes all of the non-expunged entries in the read map.
	//
	// Expunged entries are not stored in the dirty map. An expunged entry in the
	// clean map must be unexpunged and added to the dirty map before a new value
	// can be stored to it.
	//
	// If the dirty map is nil, the next write to the map will initialize it by
	// making a shallow copy of the clean map, omitting stale entries.

	// dirty 包含一部分map的内容，这部分内容是需要通过获
	// 取 mu 来操作的。确保 dirty map 能被快速的提升成
	// 为 read map。其也包含所有在 read map里未删除的
	// entries对象。
	// 删除的entries不会被保存在 dirty map。一个在clean
	// map删除的entry，必须取消删除，在一个新的数据存储进
	// 来之前添加到 dirty map。
	// 如果 dirty map 是nil, 那样写一个写操作会初始化它，
	// 通过拷贝一份clean map的副本，并去除已经不新鲜的数据。
	dirty map[interface{}]*entry

	// misses counts the number of loads since the read map was last updated that
	// needed to lock mu to determine whether the key was present.
	//
	// Once enough misses have occurred to cover the cost of copying the dirty
	// map, the dirty map will be promoted to the read map (in the unamended
	// state) and the next store to the map will make a new dirty copy.

	// misses 记录多少次加载，当 read map 上一次更新并需要
	// 锁住 mu 去确认是否操作的key是否存在。
	// 当足够多的 misses 发生之后，以至于覆盖掉了拷贝
	// dirty map的消耗成本。 dirty map 将被提升为 read
	// map， 并且下一个存储到map的操作，将生成一个新的
	// dirty 副本。
	misses int
}

// readOnly is an immutable struct stored atomically in the Map.read field.
// readOnly 是一个不可改变的结构体来保存原子性的 Map.read 字段。
type readOnly struct {
	// 读map
	m       map[interface{}]*entry
	// true 当 dirty map 有一些key不在 m 里
	amended bool // true if the dirty map contains some key not in m.
}

// expunged is an arbitrary pointer that marks entries which have been deleted
// from the dirty map.
// expunged 是一个随意的指针，标记entries已经在dirty map被删除。
var expunged = unsafe.Pointer(new(interface{}))

// An entry is a slot in the map corresponding to a particular key.
// entry 是一个map里对于某个key的槽
type entry struct {
	// p points to the interface{} value stored for the entry.
	//
	// If p == nil, the entry has been deleted and m.dirty == nil.
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

	// p 指向存储的interface类型数据。
	// 如果 p == nil，则 entry 已经被删除了并且m.dirty == nil。
	// 如果 p == expunged, entry 已经被删除，m.dirty != nil，并且entry已经在m.dirty不存在了。
	// 否则，entry 是合法，并且保存在 m.read.m[key]。假如 m.dirty != nil, m.dirty[key]里也存在。
	//
	// 一个 entry 能被删除，原子性的用nil来替换掉原本数据。当m.dirty被创建，它将自动的用 expunged
	// 替换掉nil，并保持m.dirty[key]未设置。
	//
	// 一个 entry 的相关联值可以被原子操作替换进行更新，前提 p != expunged。如果 p == expunged，
	// 这个p只能在第一个设置 m.dirty[key] = e之后更新。因此通过dirty查找到相关entry。
	p unsafe.Pointer // *interface{}
}

// 创建entry对象
func newEntry(i interface{}) *entry {
	return &entry{p: unsafe.Pointer(&i)}
}

// Load returns the value stored in the map for a key, or nil if no
// value is present.
// The ok result indicates whether value was found in the map.

// Load 返回存在map里key所关联的数据，或nil当现在没有数据。ok来标识value是否在map里找到。
func (m *Map) Load(key interface{}) (value interface{}, ok bool) {
	// 加载 read map，并检查是否 entry 存在。
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	// 如果key不存在，并且m.dirty里面有read没有的key，则进行多一次查找
	if !ok && read.amended {
		m.mu.Lock()
		// Avoid reporting a spurious miss if m.dirty got promoted while we were
		// blocked on m.mu. (If further loads of the same key will not miss, it's
		// not worth copying the dirty map for this key.)

		// 这里又从新查了一次，因为怕在抢占锁之前，dirty刚被提升到到read。
		// 因此这里又查找了一次，确保没有发生。
		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			// 从dirty map加载，miss++，并查看是否需要进行dirty提升为read。
			e, ok = m.dirty[key]
			// Regardless of whether the entry was present, record a miss: this key
			// will take the slow path until the dirty map is promoted to the read
			// map.
			m.missLocked()
		}
		m.mu.Unlock()
	}
	// 如果key不存在
	if !ok {
		return nil, false
	}
	// 加载entry里的数据
	return e.load()
}

// load entry 里的数据，如果数据的指针为nil或expunged，则
// 返回未找到。否则返回指针和找到成功。
func (e *entry) load() (value interface{}, ok bool) {
	p := atomic.LoadPointer(&e.p)
	if p == nil || p == expunged {
		return nil, false
	}
	return *(*interface{})(p), true
}

// Store sets the value for a key.
// Store 值到某个key
func (m *Map) Store(key, value interface{}) {
	// 加载read，如果值已经存在，直接尝试替换entry的里面的数据指针。
	read, _ := m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok && e.tryStore(&value) {
		return
	}

	// 加锁来处理
	m.mu.Lock()
	read, _ = m.read.Load().(readOnly)
	// 如果key在read里面，
	if e, ok := read.m[key]; ok {
		// 替换entry value
		if e.unexpungeLocked() {
			// The entry was previously expunged, which implies that there is a
			// non-nil dirty map and this entry is not in it.

			// The entry 已经之前被标记为 expunged，暗示dirty map 部位nil。并且entry不再里面。
			m.dirty[key] = e
		}
		e.storeLocked(&value)
	} else if e, ok := m.dirty[key]; ok {
		// key不在read里，但是在dirty里有。替换dirty里面entry数据。
		e.storeLocked(&value)
	} else {
		// 如果没有脏数据在dirty里面，如果我们初始化dirty map，并设置amended true
		if !read.amended {
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.
			m.dirtyLocked()
			m.read.Store(readOnly{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
	}
	m.mu.Unlock()
}

// tryStore stores a value if the entry has not been expunged.
//
// If the entry is expunged, tryStore returns false and leaves the entry
// unchanged.

// tryStore  如果条目没有被删除，tryStore会存储一个值。
//
// 如果条目被删除，tryStore返回false，并使条目保持不变。
// 没有变化。
func (e *entry) tryStore(i *interface{}) bool {
	// 这里用的for，是否因为CompareAndSwapPointer可能返回为否？？
	for {
		p := atomic.LoadPointer(&e.p)
		if p == expunged {
			return false
		}
		if atomic.CompareAndSwapPointer(&e.p, p, unsafe.Pointer(i)) {
			return true
		}
	}
}

// unexpungeLocked ensures that the entry is not marked as expunged.
//
// If the entry was previously expunged, it must be added to the dirty map
// before m.mu is unlocked.

// unexpungeLocked 确保该条目不被标记为已删除。
//
// 如果该条目之前被删除，则必须在m.mu解锁之前将其添加到dirty map中。
// 在m.mu被解锁之前。
func (e *entry) unexpungeLocked() (wasExpunged bool) {
	return atomic.CompareAndSwapPointer(&e.p, expunged, nil)
}

// storeLocked unconditionally stores a value to the entry.
//
// The entry must be known not to be expunged.
func (e *entry) storeLocked(i *interface{}) {
	atomic.StorePointer(&e.p, unsafe.Pointer(i))
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.

// LoadOrStore 如果存在的话，将返回键的现有值。
// 否则，它存储并返回给定的值。
// 如果值被加载，加载的结果为真，如果被存储，则为假。
func (m *Map) LoadOrStore(key, value interface{}) (actual interface{}, loaded bool) {
	// Avoid locking if it's a clean hit.

	// 避免锁定，如果它是一个干净的加载。
	read, _ := m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		actual, loaded, ok := e.tryLoadOrStore(value)
		if ok {
			return actual, loaded
		}
	}

	m.mu.Lock()
	read, _ = m.read.Load().(readOnly)
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
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.

			// 我们正在向dirty map添加第一个新键。
			// 确保它被分配，并将只读地图标记为不完整。
			m.dirtyLocked()
			m.read.Store(readOnly{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
		actual, loaded = value, false
	}
	m.mu.Unlock()

	return actual, loaded
}

// tryLoadOrStore atomically loads or stores a value if the entry is not
// expunged.
//
// If the entry is expunged, tryLoadOrStore leaves the entry unchanged and
// returns with ok==false.

// tryLoadOrStore 在条目没有被删除的情况下原子化地加载或存储一个值。
// 被删除。
//
// 如果条目被删除，tryLoadOrStore将不对该条目进行修改，并返回ok=false。
// 并返回ok==false。
func (e *entry) tryLoadOrStore(i interface{}) (actual interface{}, loaded, ok bool) {
	p := atomic.LoadPointer(&e.p)
	if p == expunged {
		return nil, false, false
	}
	if p != nil {
		return *(*interface{})(p), true, true
	}

	// Copy the interface after the first load to make this method more amenable
	// to escape analysis: if we hit the "load" path or the entry is expunged, we
	// shouldn't bother heap-allocating.

	// 在第一次加载后复制接口，使这个方法更容易被接受
	// 逃避分析：如果我们碰到了 "加载 "路径或条目被删除，我们
	// 就不应该费力地进行堆分配。
	ic := i
	for {
		if atomic.CompareAndSwapPointer(&e.p, nil, unsafe.Pointer(&ic)) {
			return i, false, true
		}
		p = atomic.LoadPointer(&e.p)
		if p == expunged {
			return nil, false, false
		}
		if p != nil {
			return *(*interface{})(p), true, true
		}
	}
}

// LoadAndDelete deletes the value for a key, returning the previous value if any.
// The loaded result reports whether the key was present.

// LoadAndDelete 删除了一个键的值，如果有的话，返回之前的值。
//加载的结果报告该键是否存在。
func (m *Map) LoadAndDelete(key interface{}) (value interface{}, loaded bool) {
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	if !ok && read.amended {
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]
			delete(m.dirty, key)
			// Regardless of whether the entry was present, record a miss: this key
			// will take the slow path until the dirty map is promoted to the read
			// map.

			// 无论该条目是否存在，记录一个失误：该键
			// 将采取慢速路径，直到dirty map被提升到read
			// map。
			m.missLocked()
		}
		m.mu.Unlock()
	}
	if ok {
		return e.delete()
	}
	return nil, false
}

// Delete deletes the value for a key.
func (m *Map) Delete(key interface{}) {
	m.LoadAndDelete(key)
}

func (e *entry) delete() (value interface{}, ok bool) {
	for {
		p := atomic.LoadPointer(&e.p)
		if p == nil || p == expunged {
			return nil, false
		}
		if atomic.CompareAndSwapPointer(&e.p, p, nil) {
			return *(*interface{})(p), true
		}
	}
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
//
// Range does not necessarily correspond to any consistent snapshot of the Map's
// contents: no key will be visited more than once, but if the value for any key
// is stored or deleted concurrently, Range may reflect any mapping for that key
// from any point during the Range call.
//
// Range may be O(N) with the number of elements in the map even if f returns
// false after a constant number of calls.

// 对于地图中的每个键和值，Range依次调用f。
// 如果f返回false，range停止迭代。
//
// Range不一定对应于地图的任何一致的快照。
// 内容：没有一个键会被访问超过一次，但是如果任何键的值
// 的值被同时存储或删除，Range可能反映该键的任何映射。
// 在Range调用过程中的任何一点。
//
// Range可能是O(N)，即映射中的元素数，即使f返回了
// f在调用次数不变的情况下返回false。
func (m *Map) Range(f func(key, value interface{}) bool) {
	// We need to be able to iterate over all of the keys that were already
	// present at the start of the call to Range.
	// If read.amended is false, then read.m satisfies that property without
	// requiring us to hold m.mu for a long time.

	// 我们需要能够遍历所有在调用Range时已经存在的键。
	// 在调用Range时已经存在。
	// 如果read.修正为false，那么read.m就满足了这个属性，而不需要我们长期持有m.mu。
	// 要求我们长时间地保持m.mu。
	read, _ := m.read.Load().(readOnly)
	if read.amended {
		// m.dirty contains keys not in read.m. Fortunately, Range is already O(N)
		// (assuming the caller does not break out early), so a call to Range
		// amortizes an entire copy of the map: we can promote the dirty copy
		// immediately!

		// m.dirty包含read.m中没有的键。
		// (假设调用者没有提前中断)，所以调用Range
		// 摊销了整个地图的副本：我们可以立即提升脏的副本
		// 马上就可以了
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		if read.amended {
			read = readOnly{m: m.dirty}
			m.read.Store(read)
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

// m.misses++， 如果misses已经超过m.dirty的size。
// 则dirty提升为read，并dirty设置为nil, 清空misses。
func (m *Map) missLocked() {
	m.misses++
	if m.misses < len(m.dirty) {
		return
	}
	m.read.Store(readOnly{m: m.dirty})
	m.dirty = nil
	m.misses = 0
}

// 如果dirty 未初始化，从read加载数据，并初始化真个dirty map
func (m *Map) dirtyLocked() {
	if m.dirty != nil {
		return
	}

	read, _ := m.read.Load().(readOnly)
	m.dirty = make(map[interface{}]*entry, len(read.m))
	for k, e := range read.m {
		// 如果还未被删除，则拷贝到dirty。
		if !e.tryExpungeLocked() {
			m.dirty[k] = e
		}
	}
}

// 检查entry 是否为expunged。 如果现在为nil, 尝试从nil设置为expunged。
func (e *entry) tryExpungeLocked() (isExpunged bool) {
	p := atomic.LoadPointer(&e.p)
	for p == nil {
		if atomic.CompareAndSwapPointer(&e.p, nil, expunged) {
			return true
		}
		p = atomic.LoadPointer(&e.p)
	}
	return p == expunged
}
