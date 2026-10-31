package storage

import (
	"math/rand/v2"
)

// treapNode is a randomized balanced binary search tree keyed by the store's
// live keys. It keeps keys in lexicographic order with expected O(log n)
// insert, delete, and range scan, so paged scans touch only the page window
// instead of scanning and sorting the whole keyspace under the read lock (#220).
type treapNode struct {
	key      string
	priority uint64
	left     *treapNode
	right    *treapNode
}

// randomTreapPriority generates a random priority for a treap node.
// Using math/rand/v2 provides fast, thread-safe, unpredictable priorities,
// avoiding adversarial tree degeneration.
func randomTreapPriority() uint64 {
	return rand.Uint64()
}

// treapInsert adds key to the subtree rooted at t and returns the new root.
func treapInsert(t *treapNode, key string, priority uint64) *treapNode {
	if t == nil {
		return &treapNode{key: key, priority: priority}
	}
	if key == t.key {
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

// treapDelete removes key from the subtree rooted at t and returns the new root.
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

// treapMerge joins two subtrees where every key in a sorts before every key in b.
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

// walkWhile performs an in-order walk, calling fn for each key until fn returns false.
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

// walkFrom performs an in-order walk over keys strictly greater than after.
func (t *treapNode) walkFrom(after string, fn func(key string) bool) {
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

// walkFromOrEqual performs an in-order walk over keys greater than or equal to start.
// This allows jumping directly to a prefix in O(log n) time without visiting earlier keys (#220).
func (t *treapNode) walkFromOrEqual(start string, fn func(key string) bool) {
	var stack []*treapNode
	node := t
	for node != nil {
		if node.key >= start {
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

// rotateRight rotates the tree right around t.
func rotateRight(t *treapNode) *treapNode {
	l := t.left
	t.left = l.right
	l.right = t
	return l
}

// rotateLeft rotates the tree left around t.
func rotateLeft(t *treapNode) *treapNode {
	r := t.right
	t.right = r.left
	r.left = t
	return r
}
