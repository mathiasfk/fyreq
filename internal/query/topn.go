package query

import (
	"container/heap"
	"sort"

	"fyreq/internal/catalog"
)

// topN retains the first limit records in result order using memory
// proportional to the limit. The heap root is the worst retained record.
type topN struct {
	limit int
	less  func(a, b catalog.File) bool
	files []catalog.File
}

func (t *topN) Len() int           { return len(t.files) }
func (t *topN) Less(i, j int) bool { return t.less(t.files[j], t.files[i]) }
func (t *topN) Swap(i, j int)      { t.files[i], t.files[j] = t.files[j], t.files[i] }
func (t *topN) Push(x any)         { t.files = append(t.files, x.(catalog.File)) }
func (t *topN) Pop() any {
	last := t.files[len(t.files)-1]
	t.files = t.files[:len(t.files)-1]
	return last
}

func (t *topN) offer(file catalog.File) {
	if len(t.files) < t.limit {
		heap.Push(t, file)
		return
	}
	if t.less(file, t.files[0]) {
		t.files[0] = file
		heap.Fix(t, 0)
	}
}

func (t *topN) sorted() []catalog.File {
	sort.Slice(t.files, func(i, j int) bool { return t.less(t.files[i], t.files[j]) })
	return t.files
}
