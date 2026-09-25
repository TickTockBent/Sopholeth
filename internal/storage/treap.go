package storage

// treapNode is a randomized balanced binary search tree keyed by the store's
// live keys. It keeps keys in lexicographic order with expected O(log n)
// insert, delete, split, and merge, so paged scans touch only the page window
// instead of scanning and sorting the whole keyspace under the read lock
// (#220).
type treapNode struct {
	key      string
	priority uint64
	left     *treapNode
	right    *treapNode
}

// treapPriority derives a deterministic node priority from the put sequence.
// A cheap splitmix-style avalanche keeps priorities well distributed across
// sequential counters; ties are impossible because the sequence is unique per
// put. Deterministic priorities make tree shapes reproducible in tests.
func treapPriority(seq uint64) uint64 {
	z := seq + 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// treapInsert adds key to the subtree rooted at t and returns the new root.
// Inserting an existing key refreshes its priority and re-heapifies.
func treapInsert(t *treapNode, key string, priority uint64) *treapNode {
	if t == nil {
		return &treapNode{key: key, priority: priority}
	}
	if key == t.key {
		t.priority = priority
		// Bubble the refreshed priority down so the max-heap ordering holds
		// even when an overwrite lowers a node's priority.
		for t.left != nil && t.left.priority > t.priority || t.right != nil && t.right.priority > t.priority {
			if t.right == nil || t.left != nil && t.left.priority > t.right.priority {
				t = rotateRight(t)
			} else {
				t = rotateLeft(t)
			}
		}
		return t
	}
	if key < t.key {
		t.left = treapInsert(t.left, key, priority)
		if t.left.priority > t.priority {
			t = rotateRight(t)
		}
		return t
	}
	t.right = treapInsert(t.right, key, priority)
	if t.right.priority > t.priority {
		t = rotateLeft(t)
	}
	return t
}

// treapDelete removes key from the subtree rooted at t and returns the new
// root. Missing keys leave the tree unchanged.
func treapDelete(t *treapNode, key string) *treapNode {
	if t == nil {
		return nil
	}
	switch {
	case key < t.key:
		t.left = treapDelete(t.left, key)
		return t
	case key > t.key:
		t.right = treapDelete(t.right, key)
		return t
	default:
		return treapMerge(t.left, t.right)
	}
}

// walkFrom performs an in-order walk over the keys strictly greater than
// after, calling fn for each key until fn returns false. It only reads the
// tree, so it is safe under the store's read lock alongside concurrent
// walkers (#220).
func (t *treapNode) walkFrom(after string, fn func(key string) bool) {
	// Descend to the first key > after, keeping the ancestors already known
	// to sort after it; everything else is <= after and skipped wholesale.
	var stack []*treapNode
	node := t
	for node != nil {
		if node.key > after {
			stack = append(stack, node)
			node = node.left
		} else {
			node = node.right
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		n := stack[i]
		if !fn(n.key) {
			return
		}
		cont := true
		n.right.walkWhile(func(key string) bool {
			cont = fn(key)
			return cont
		})
		if !cont {
			return
		}
	}
}

// treapMerge joins two subtrees where every key in a sorts before every key
// in b, restoring the heap ordering by priority.
func treapMerge(a, b *treapNode) *treapNode {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	if a.priority > b.priority {
		a.right = treapMerge(a.right, b)
		return a
	}
	b.left = treapMerge(a, b.left)
	return b
}

// walkWhile performs an in-order walk, calling fn for each key until fn
// returns false. Safe under the store's read lock: it mutates nothing.
func (t *treapNode) walkWhile(fn func(key string) bool) {
	if t == nil {
		return
	}
	var stack []*treapNode
	node := t
	for node != nil || len(stack) > 0 {
		for node != nil {
			stack = append(stack, node)
			node = node.left
		}
		node = stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !fn(node.key) {
			return
		}
		node = node.right
	}
}

// rotateRight rotates the tree right around t, restoring max-heap priority
// ordering after a left-subtree insertion.
func rotateRight(t *treapNode) *treapNode {
	l := t.left
	t.left = l.right
	l.right = t
	return l
}

// rotateLeft rotates the tree left around t, restoring max-heap priority
// ordering after a right-subtree insertion.
func rotateLeft(t *treapNode) *treapNode {
	r := t.right
	t.right = r.left
	r.left = t
	return r
}
