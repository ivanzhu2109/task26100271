package callsched

// comboHeap 是单个活动内候选组合的最小堆。
// 堆内所有元素属于同一活动，活动ID恒定，故堆内序为 (dialableAt, contactID)。
// 通过 combo.heapIdx 支持 O(log n) 的任意删除；heapIdx < 0 表示不在堆中。
type comboHeap struct {
	items []*combo
}

func (h *comboHeap) Len() int { return len(h.items) }

func (h *comboHeap) less(i, j int) bool {
	a, b := h.items[i], h.items[j]
	if a.dialableAt != b.dialableAt {
		return a.dialableAt < b.dialableAt
	}
	return a.contactID < b.contactID
}

func (h *comboHeap) swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].heapIdx = i
	h.items[j].heapIdx = j
}

func (h *comboHeap) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !h.less(i, p) {
			return
		}
		h.swap(i, p)
		i = p
	}
}

func (h *comboHeap) down(i int) {
	n := len(h.items)
	for {
		l, r, m := 2*i+1, 2*i+2, i
		if l < n && h.less(l, m) {
			m = l
		}
		if r < n && h.less(r, m) {
			m = r
		}
		if m == i {
			return
		}
		h.swap(i, m)
		i = m
	}
}

// push 将组合插入堆尾并上浮，O(log n)。调用方保证 c 当前不在堆中。
func (h *comboHeap) push(c *combo) {
	c.heapIdx = len(h.items)
	h.items = append(h.items, c)
	h.up(c.heapIdx)
}

// pop 删除并返回堆顶，O(log n)。
func (h *comboHeap) pop() *combo {
	n := len(h.items)
	top := h.items[0]
	h.swap(0, n-1)
	h.items = h.items[:n-1]
	top.heapIdx = -1
	if n-1 > 0 {
		h.down(0)
	}
	return top
}

// remove 按索引删除任意元素，O(log n)。调用方保证 c 在堆中。
func (h *comboHeap) remove(c *combo) {
	i := c.heapIdx
	n := len(h.items)
	h.swap(i, n-1)
	h.items = h.items[:n-1]
	c.heapIdx = -1
	if i < n-1 {
		h.down(i)
		h.up(i)
	}
}

// top 返回堆顶（不删除），O(1)。
func (h *comboHeap) top() *combo {
	if len(h.items) == 0 {
		return nil
	}
	return h.items[0]
}
