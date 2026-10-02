package dialsched

// intHeap 是支持任意删除的整数索引二叉最小堆。
// 元素为整数 ID,比较与位置回写由调用方提供,
// 因而每个元素的在堆位置可 O(1) 记录在其宿主结构里。
// push/pop/removeAt/fix 均为 O(log n)。
type intHeap struct {
	items  []int
	less   func(a, b int) bool
	setPos func(id, pos int)
}

func (h *intHeap) len() int { return len(h.items) }

// top 返回堆首元素,调用前需保证堆非空。
func (h *intHeap) top() int { return h.items[0] }

func (h *intHeap) swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.setPos(h.items[i], i)
	h.setPos(h.items[j], j)
}

func (h *intHeap) up(i int) {
	for i > 0 {
		p := (i - 1) / 2
		if !h.less(h.items[i], h.items[p]) {
			break
		}
		h.swap(i, p)
		i = p
	}
}

// down 返回元素是否发生了下移。
func (h *intHeap) down(i int) bool {
	moved := false
	n := len(h.items)
	for {
		l := 2*i + 1
		if l >= n {
			break
		}
		m := l
		if r := l + 1; r < n && h.less(h.items[r], h.items[l]) {
			m = r
		}
		if !h.less(h.items[m], h.items[i]) {
			break
		}
		h.swap(i, m)
		i = m
		moved = true
	}
	return moved
}

// fix 在元素键值变化后恢复堆序。
func (h *intHeap) fix(i int) {
	if !h.down(i) {
		h.up(i)
	}
}

func (h *intHeap) push(id int) {
	h.setPos(id, len(h.items))
	h.items = append(h.items, id)
	h.up(len(h.items) - 1)
}

// pop 删除并返回堆首元素。
func (h *intHeap) pop() int {
	top := h.items[0]
	h.removeAt(0)
	return top
}

// removeAt 删除指定位置的元素。
func (h *intHeap) removeAt(i int) {
	n := len(h.items) - 1
	removed := h.items[i]
	h.swap(i, n)
	h.items = h.items[:n]
	h.setPos(removed, -1)
	if i < n {
		h.fix(i)
	}
}

// heapify 就地建堆,O(n)。
func (h *intHeap) heapify() {
	for i := range h.items {
		h.setPos(h.items[i], i)
	}
	for i := len(h.items)/2 - 1; i >= 0; i-- {
		h.down(i)
	}
}
